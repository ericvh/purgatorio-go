package core

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// The file2chan request and reply formats on kernel channels (docs/abi.md):
//
//	read request:  off[8] count[4] fid[4], cap: reply channel
//	write request: off[8] fid[4] data..., cap: reply channel
//	reply:         0 then the data read (or count[4] written), or 1 then
//	               an error string
//
// A request without a reply channel tells the server that fid was closed,
// as Inferno's srvunblock sends a nil reply channel.
const (
	F2CReadHdr  = 16
	F2CWriteHdr = 12
)

type srvfile struct {
	name   string
	user   string
	perm   uint32
	qid    styx.Qid
	opens  int
	orc    bool
	read   *KChan
	write  *KChan
	dir    *srvdir
	length uint64
}

type srvdir struct {
	// domain is the innermost prog domain of the prog that made it (0
	// for none): each domain has its own registry of specs.
	domain  int
	spec    string
	name    string
	user    string
	qid     styx.Qid
	entries []*srvfile
}

// DevSrv is #s (devsrv.c): each attach makes a directory, unless the spec
// names an existing one, in which file2chan puts files whose reads and
// writes become messages to the serving prog. Each prog domain
// (#pdomain) has its own specs: an attach finds only directories made by
// progs whose innermost domain is the attaching prog's.
type DevSrv struct {
	DevBase
	k       *Kernel
	mu      sync.Mutex
	pathgen uint64
	dirs    []*srvdir
	fidgen  uint32
}

func NewDevSrv(k *Kernel) *DevSrv { return &DevSrv{k: k, pathgen: 1} }

func (d *DevSrv) DC() rune     { return 's' }
func (d *DevSrv) Name() string { return "srv" }

func (d *DevSrv) Attach(ctx context.Context, spec string) (Walked, error) {
	user, pid, domain := d.k.Owner, 0, 0
	if p := ProgFrom(ctx); p != nil {
		user, pid, domain = p.User(), p.Pid, p.Domain()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if spec != "" {
		for _, s := range d.dirs {
			if s.spec == spec && s.domain == domain {
				return Walked{Qid: s.qid, Aux: s}, nil
			}
		}
	}
	s := &srvdir{domain: domain, spec: spec, name: fmt.Sprintf("srv%d", pid), user: user,
		qid: styx.Qid{Type: styx.QTDIR, Path: d.pathgen}}
	d.pathgen++
	d.dirs = append(d.dirs, s)
	return Walked{Qid: s.qid, Aux: s}, nil
}

func (d *DevSrv) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	s, ok := c.Aux.(*srvdir)
	if !ok {
		return Walked{}, Error(abi.Enotdir)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range s.entries {
		if f.name == name {
			return Walked{Qid: f.qid, Aux: f}, nil
		}
	}
	return Walked{}, Error(abi.Enonexist)
}

func (f *srvfile) dirent() styx.Dir {
	return styx.Dir{Type: 's', Qid: f.qid, Mode: f.perm, Length: f.length, Name: f.name, UID: f.user, GID: f.user}
}

func (d *DevSrv) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch a := c.Aux.(type) {
	case *srvdir:
		return styx.Dir{Type: 's', Qid: a.qid, Mode: styx.DMDIR | 0o770, Name: a.name, UID: a.user, GID: a.user}, nil
	case *srvfile:
		return a.dirent(), nil
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevSrv) DirRead(_ context.Context, c *Chan) ([]styx.Dir, error) {
	s, ok := c.Aux.(*srvdir)
	if !ok {
		return nil, Error(abi.Enotdir)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []styx.Dir
	for _, f := range s.entries {
		out = append(out, f.dirent())
	}
	return out, nil
}

type srvopen struct{ fid uint32 }

func (d *DevSrv) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, Error(abi.Eisdir)
		}
		return c, nil
	}
	f := c.Aux.(*srvfile)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := OpenPerm(f.perm, mode); err != nil {
		return nil, err
	}
	user := d.k.Owner
	if p := ProgFrom(ctx); p != nil {
		user = p.User()
	}
	if mode&abi.ORCLOSE != 0 && f.user != user {
		return nil, Error(abi.Eperm)
	}
	if f.perm&styx.DMEXCL != 0 && f.opens != 0 {
		return nil, Error(abi.Einuse)
	}
	f.opens++
	if mode&abi.ORCLOSE != 0 {
		f.orc = true
	}
	d.fidgen++
	c.State = &srvopen{fid: d.fidgen}
	return c, nil
}

func (d *DevSrv) Close(c *Chan) {
	f, ok := c.Aux.(*srvfile)
	if !ok {
		return
	}
	o := c.State.(*srvopen)
	d.mu.Lock()
	f.opens--
	remove := f.opens == 0 && f.orc
	rd, wr := f.read, f.write
	d.mu.Unlock()
	// Tell the server the fid has gone, as srvunblock does: a request
	// with no reply channel on each channel, sent only if it would not
	// block.
	if rd != nil {
		var b [F2CReadHdr]byte
		binary.LittleEndian.PutUint32(b[12:], o.fid)
		rd.TrySend(KMsg{Bytes: b[:]})
	}
	if wr != nil {
		var b [F2CWriteHdr]byte
		binary.LittleEndian.PutUint32(b[8:], o.fid)
		wr.TrySend(KMsg{Bytes: b[:]})
	}
	if remove {
		d.delete(f)
	}
}

func (d *DevSrv) delete(f *srvfile) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := f.dir
	for i, e := range s.entries {
		if e == f {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			break
		}
	}
	f.read, f.write = nil, nil
}

func (d *DevSrv) Remove(ctx context.Context, c *Chan) error {
	f, ok := c.Aux.(*srvfile)
	if !ok {
		return Error(abi.Eperm)
	}
	user := d.k.Owner
	if p := ProgFrom(ctx); p != nil {
		user = p.User()
	}
	if f.dir.user != user {
		return Error(abi.Eperm)
	}
	d.delete(f)
	return nil
}

func (d *DevSrv) Wstat(ctx context.Context, c *Chan, dir *styx.Dir) error {
	f, ok := c.Aux.(*srvfile)
	if !ok {
		return Error(abi.Eperm)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !NullMode(dir) {
		f.perm = dir.Mode&0o777 | f.perm&^0o777
	}
	if !NullLength(dir) {
		f.length = dir.Length
	}
	if dir.Name != "" && dir.Name != f.name {
		for _, e := range f.dir.entries {
			if e.name == dir.Name {
				return Error(abi.Eexist)
			}
		}
		f.name = dir.Name
	}
	return nil
}

// reply decodes a file2chan reply.
func f2cReply(m KMsg) ([]byte, error) {
	if len(m.Bytes) == 0 {
		return nil, Error(abi.Ehungup)
	}
	if m.Bytes[0] == 1 {
		return nil, Error(string(m.Bytes[1:]))
	}
	return m.Bytes[1:], nil
}

func (d *DevSrv) Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	f := c.Aux.(*srvfile)
	d.mu.Lock()
	rd := f.read
	d.mu.Unlock()
	if rd == nil {
		return 0, Error(abi.Ehungup)
	}
	rc := NewKChan(1)
	var b [F2CReadHdr]byte
	binary.LittleEndian.PutUint64(b[0:], uint64(off))
	binary.LittleEndian.PutUint32(b[8:], uint32(len(buf)))
	binary.LittleEndian.PutUint32(b[12:], c.State.(*srvopen).fid)
	if err := rd.Send(ctx, KMsg{Bytes: b[:], Caps: []Cap{{Chan: rc}}}); err != nil {
		return 0, err
	}
	m, ok, err := rc.Recv(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, Error(abi.Ehungup)
	}
	data, err := f2cReply(m)
	if err != nil {
		return 0, err
	}
	return copy(buf, data), nil
}

func (d *DevSrv) Write(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	f := c.Aux.(*srvfile)
	d.mu.Lock()
	wr := f.write
	d.mu.Unlock()
	if wr == nil {
		return 0, Error(abi.Ehungup)
	}
	rc := NewKChan(1)
	msg := make([]byte, F2CWriteHdr, F2CWriteHdr+len(buf))
	binary.LittleEndian.PutUint64(msg[0:], uint64(off))
	binary.LittleEndian.PutUint32(msg[8:], c.State.(*srvopen).fid)
	msg = append(msg, buf...)
	if err := wr.Send(ctx, KMsg{Bytes: msg, Caps: []Cap{{Chan: rc}}}); err != nil {
		return 0, err
	}
	m, ok, err := rc.Recv(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, Error(abi.Ehungup)
	}
	data, err := f2cReply(m)
	if err != nil {
		return 0, err
	}
	if len(data) < 4 {
		return 0, nil
	}
	return int(int32(binary.LittleEndian.Uint32(data))), nil
}

// File2chan makes file in the #s directory dir, whose reads and writes are
// sent on read and write (srvf2c).
func (d *DevSrv) File2chan(ctx context.Context, dir *Chan, file string, read, write *KChan) error {
	if strings.Contains(file, "/") || len(file) >= 64 || file == "." || file == ".." {
		return Error(abi.Efilename)
	}
	s, ok := dir.Aux.(*srvdir)
	if !ok || dir.Dev.DC() != 's' {
		return Error("directory not a srv device")
	}
	user := d.k.Owner
	if p := ProgFrom(ctx); p != nil {
		user = p.User()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range s.entries {
		if f.name == file {
			return Error(abi.Eexist)
		}
	}
	f := &srvfile{name: file, user: user, perm: 0o666, read: read, write: write, dir: s,
		qid: styx.Qid{Path: d.pathgen}}
	d.pathgen++
	s.entries = append([]*srvfile{f}, s.entries...)
	return nil
}
