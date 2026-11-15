package core

import (
	"context"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/styx"
)

// Export errors, from Inferno's exportfs.c.
const (
	enofid   = "no such fid"
	eseekdir = "can't seek on a directory"
	eopen    = "walk of open fid"
	emode    = "open/create -- unknown mode"
	edupfid  = "fid in use"
	eaccess  = "read/write -- not open in suitable mode"
	ecount   = "read/write -- count too big"
	erdonly  = "file system read only"
)

// maxrpcmax is the largest msize export agrees to.
const maxrpcmax = 128*1024 + styx.IOHDRSZ

type exfid struct {
	c      *Chan
	open   bool
	offset int64
}

// exq is one request being served.
type exq struct {
	note    *Note
	flushed bool
	ftag    int // the Tflush's tag, when flushed
}

// Exporter serves a name space subtree over a 9P conversation, Inferno's
// exportfs: each request runs in its own goroutine, a Tflush interrupts
// the request it names, and qids are made unique across the devices the
// tree spans (uqid.c).
type Exporter struct {
	pgrp     *Pgrp
	root     *Chan
	io       *Chan
	user     string
	readonly bool

	wmu   sync.Mutex
	mu    sync.Mutex
	msize uint32
	fids  map[uint32]*exfid
	reqs  map[uint16]*exq
	uqids map[Key]uint64
	paths map[uint64]bool
	gen   uint64
	wg    sync.WaitGroup

	auth *exAuth // nil: no authentication, as Inferno's export
}

// NewExporter serves root, in name space pgrp, on the conversation io.
// The exporter holds the name space and root until Serve returns, as
// Inferno's exportfs holds the exporting prog's pgrp: an export made in
// the background outlives the prog that started it.
func NewExporter(pgrp *Pgrp, root, io *Chan, user string, readonly bool) *Exporter {
	pgrp.IncRef()
	root.hold()
	return &Exporter{pgrp: pgrp, root: root, io: io, user: user, readonly: readonly,
		fids: map[uint32]*exfid{}, reqs: map[uint16]*exq{}, uqids: map[Key]uint64{}, paths: map[uint64]bool{}}
}

// Export is Sys->export: serve dir on fd until the client hangs up, or in
// the background when async is set.
func (p *Prog) Export(fd int, dir string, async bool) error {
	flags := uint32(0)
	if async {
		flags = abi.EXPASYNC
	}
	return p.ExportKey(fd, dir, flags, nil)
}

// ExportKey is export_key: Export with the export flags of abi, and the
// authinfo (or seed) the server proves itself with when key is not nil.
// With EXPHYBRID or EXPCLASSICAL the export authenticates its clients
// (docs/auth.md). EXPRDONLY refuses every request that would change a
// file.
func (p *Prog) ExportKey(fd int, dir string, flags uint32, key []byte) error {
	async := flags&abi.EXPASYNC != 0
	var auth *ExportAuth
	if acc := pgauth.Accept(0); flags&(abi.EXPHYBRID|abi.EXPCLASSICAL) != 0 {
		if flags&abi.EXPHYBRID != 0 {
			acc |= pgauth.AcceptHybrid
		}
		if flags&abi.EXPCLASSICAL != 0 {
			acc |= pgauth.AcceptClassical
		}
		auth = &ExportAuth{Accept: acc, Encrypt: flags&abi.EXPENCRYPT != 0}
		if key != nil {
			k, c, err := ParseKeyInfo(key)
			if err != nil {
				return Error(abi.Ebadarg)
			}
			auth.Key, auth.Cert = k, c
		}
	} else if flags&abi.EXPENCRYPT != 0 || key != nil {
		return Error(abi.Ebadarg)
	}
	c, err := fdtochan(p.Fgrp(), fd, abi.ORDWR)
	if err != nil {
		return err
	}
	root, err := p.Namec(dir)
	if err != nil {
		return err
	}
	if !root.IsDir() {
		return Error(abi.Enotdir)
	}
	root.Name = Absolute(dir, p.Dot())
	c.IncRef()
	e := NewExporter(p.Pgrp(), root, c, p.User(), flags&abi.EXPRDONLY != 0)
	if auth != nil {
		k := p.Kernel
		e.auth = &exAuth{cfg: auth, now: func() uint64 { return nowSecs(k) }, afids: map[uint32]*exAfid{}}
	}
	if async {
		go func() { _ = e.Serve(context.Background()); c.DecRef() }()
		return nil
	}
	defer c.DecRef()
	return e.Serve(p.Ctx)
}

func (e *Exporter) uqid(c *Chan) styx.Qid {
	q := c.Qid
	e.mu.Lock()
	defer e.mu.Unlock()
	k := c.Key()
	if np, ok := e.uqids[k]; ok {
		q.Path = np
		return q
	}
	np := c.Qid.Path
	for e.paths[np] {
		e.gen++
		if e.gen >= 1<<16 {
			e.gen = 1
		}
		np = e.gen<<48 | c.Qid.Path&(1<<48-1)
	}
	e.uqids[k] = np
	e.paths[np] = true
	q.Path = np
	return q
}

func (e *Exporter) readMsg(ctx context.Context, buf *[]byte) (*styx.Fcall, error) {
	tmp := make([]byte, 8192)
	for {
		if e.auth != nil && e.auth.open != nil {
			if len(*buf) >= pgauth.RecordHeader {
				n, err := pgauth.RecordSize(*buf, int(e.msizeOr()))
				if err != nil {
					return nil, err
				}
				if n <= len(*buf) {
					rec := append([]byte(nil), (*buf)[:n]...)
					*buf = append([]byte(nil), (*buf)[n:]...)
					msg, err := e.auth.open.Open(rec)
					if err != nil {
						return nil, err
					}
					return styx.Unmarshal(msg, 0)
				}
			}
		} else if n := styx.Size(*buf); n != 0 && int(n) <= len(*buf) {
			msg := (*buf)[:n]
			*buf = append([]byte(nil), (*buf)[n:]...)
			return styx.Unmarshal(msg, 0)
		}
		k, err := e.io.Dev.Read(ctx, e.io, tmp, e.io.Offset())
		if err != nil {
			return nil, err
		}
		if k == 0 {
			return nil, Error(abi.Ehungup)
		}
		*buf = append(*buf, tmp[:k]...)
	}
}

func (e *Exporter) reply(f *styx.Fcall) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	e.writeLocked(f)
}

// writeLocked sends a reply, sealed once the conversation has switched to
// records. The caller holds wmu.
func (e *Exporter) writeLocked(f *styx.Fcall) {
	b, err := styx.Marshal(nil, f)
	if err != nil {
		return
	}
	if e.auth != nil && e.auth.seal != nil {
		if b, err = e.auth.seal.Seal(nil, b); err != nil {
			return
		}
	}
	for len(b) > 0 {
		n, err := e.io.Dev.Write(context.Background(), e.io, b, e.io.Offset())
		if err != nil || n <= 0 {
			return
		}
		b = b[n:]
	}
}

// Serve reads requests until the conversation ends.
func (e *Exporter) Serve(ctx context.Context) error {
	var buf []byte
	defer e.shutdown()
	for {
		t, err := e.readMsg(ctx, &buf)
		if err != nil {
			if Errstr(err) == abi.Ehungup {
				return nil
			}
			return err
		}
		if t.Type < styx.Tversion || t.Type%2 == 1 || t.Type == styx.Terror {
			return Error("invalid T-message type")
		}
		if e.isAuthMsg(t) {
			e.serveAuth(ctx, t)
			continue
		}
		if e.readonly {
			switch t.Type {
			case styx.Topen:
				if t.Mode&(abi.ORCLOSE|abi.OTRUNC|3) == abi.OREAD {
					break
				}
				fallthrough
			case styx.Tcreate, styx.Twrite, styx.Tremove, styx.Twstat:
				e.reply(&styx.Fcall{Type: styx.Rerror, Tag: t.Tag, Ename: erdonly})
				continue
			}
		}
		if t.Type == styx.Tflush {
			e.flush(t)
			continue
		}
		if t.Type == styx.Tversion {
			e.reply(e.version(t))
			continue
		}
		q := &exq{note: NewNote()}
		e.mu.Lock()
		e.reqs[t.Tag] = q
		e.mu.Unlock()
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			r := e.serve(WithNote(context.Background(), q.note), t)
			e.mu.Lock()
			delete(e.reqs, t.Tag)
			flushed := q.flushed
			e.mu.Unlock()
			if !flushed {
				e.reply(r)
			}
			if flushed {
				e.reply(&styx.Fcall{Type: styx.Rflush, Tag: q.flushTag()})
			}
		}()
	}
}

// flushTag is stored in the request's note-independent field.
func (q *exq) flushTag() uint16 { return uint16(q.ftag) }

func (e *Exporter) flush(t *styx.Fcall) {
	e.mu.Lock()
	q, ok := e.reqs[t.Oldtag]
	if ok && !q.flushed {
		q.flushed = true
		q.ftag = int(t.Tag)
		e.mu.Unlock()
		q.note.Interrupt()
		return
	}
	e.mu.Unlock()
	e.reply(&styx.Fcall{Type: styx.Rflush, Tag: t.Tag})
}

func (e *Exporter) shutdown() {
	e.mu.Lock()
	for _, q := range e.reqs {
		q.note.Kill()
	}
	e.mu.Unlock()
	e.wg.Wait()
	e.mu.Lock()
	for id, f := range e.fids {
		if f.open {
			f.c.DecRef()
		}
		delete(e.fids, id)
	}
	e.mu.Unlock()
	e.root.unhold()
	e.pgrp.Release()
}

func (e *Exporter) version(t *styx.Fcall) *styx.Fcall {
	r := &styx.Fcall{Type: styx.Rversion, Tag: t.Tag, Msize: min(t.Msize, maxrpcmax)}
	if r.Msize < 64 {
		return &styx.Fcall{Type: styx.Rerror, Tag: t.Tag, Ename: "message size too small"}
	}
	v := t.Version
	if i := strings.IndexByte(v, '.'); i >= 0 {
		v = v[:i]
	}
	if strings.HasPrefix(v, "9P") && styx.Version <= v {
		r.Version = styx.Version
		e.mu.Lock()
		e.msize = r.Msize
		// A new version aborts every fid.
		for id, f := range e.fids {
			if f.open {
				f.c.DecRef()
			}
			delete(e.fids, id)
		}
		e.mu.Unlock()
	} else {
		r.Version = "unknown"
	}
	return r
}

func (e *Exporter) getfid(id uint32) *exfid {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fids[id]
}

func (e *Exporter) setfid(id uint32, f *exfid) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, used := e.fids[id]; used {
		return false
	}
	e.fids[id] = f
	return true
}

func (e *Exporter) isroot(c *Chan) bool { return c.Key() == e.root.Key() }

func rerr(t *styx.Fcall, s string) *styx.Fcall {
	return &styx.Fcall{Type: styx.Rerror, Tag: t.Tag, Ename: s}
}

func rerrf(t *styx.Fcall, err error) *styx.Fcall { return rerr(t, Errstr(err)) }

func (e *Exporter) serve(ctx context.Context, t *styx.Fcall) *styx.Fcall {
	r := &styx.Fcall{Type: t.Type + 1, Tag: t.Tag}
	switch t.Type {
	case styx.Tauth:
		return rerr(t, "authentication not required")
	case styx.Tattach:
		if err := e.checkAttach(ctx, t); err != nil {
			return rerrf(t, err)
		}
		c := e.root.CloneUnopened()
		if !e.setfid(t.Fid, &exfid{c: c}) {
			return rerr(t, edupfid)
		}
		r.Qid = e.uqid(c)
	case styx.Twalk:
		f := e.getfid(t.Fid)
		if f == nil {
			return rerr(t, enofid)
		}
		if f.open {
			return rerr(t, eopen)
		}
		c := f.c.CloneUnopened()
		for i, name := range t.Wname {
			var nc *Chan
			var err error
			switch {
			case name == ".." && e.isroot(c):
				nc = c
			case name == "..":
				nc, err = e.pgrp.Namec(ctx, Cleanname(c.Name+"/.."), "/")
			default:
				nc, err = e.pgrp.Walk1(ctx, c, name)
			}
			if err != nil {
				if i == 0 {
					return rerrf(t, err)
				}
				return r // a short walk: no new fid
			}
			c = nc
			r.Wqid = append(r.Wqid, e.uqid(c))
		}
		if t.Newfid != t.Fid {
			if !e.setfid(t.Newfid, &exfid{c: c}) {
				return rerr(t, edupfid)
			}
		} else {
			e.mu.Lock()
			f.c = c
			e.mu.Unlock()
		}
	case styx.Topen:
		f := e.getfid(t.Fid)
		if f == nil {
			return rerr(t, enofid)
		}
		if f.open {
			return rerr(t, emode)
		}
		c, err := OpenC(ctx, f.c.CloneUnopened(), int(t.Mode))
		if err != nil {
			return rerrf(t, err)
		}
		e.mu.Lock()
		f.c, f.open, f.offset = c, true, 0
		e.mu.Unlock()
		r.Qid = e.uqid(c)
		r.Iounit = e.iounit(c)
	case styx.Tcreate:
		f := e.getfid(t.Fid)
		if f == nil {
			return rerr(t, enofid)
		}
		if f.open {
			return rerr(t, emode)
		}
		if t.Name == "." || t.Name == ".." || t.Name == "" || strings.Contains(t.Name, "/") {
			return rerr(t, abi.Efilename)
		}
		dir := f.c
		if dir.Union != nil {
			dir = nil
			for _, m := range f.c.Union {
				if m.CanCreate() {
					dir = m.Chan.CloneUnopened()
					dir.Union = nil
					break
				}
			}
			if dir == nil {
				return rerr(t, abi.Enocreate)
			}
		}
		w, err := dir.Dev.Create(ctx, dir, t.Name, t.Perm)
		if err != nil {
			return rerrf(t, err)
		}
		c := NewChan(dir.Dev, dir.Devno, w.Qid, f.c.Name+"/"+t.Name, w.Aux)
		if c, err = OpenC(ctx, c, int(t.Mode)&^abi.OTRUNC); err != nil {
			return rerrf(t, err)
		}
		e.mu.Lock()
		f.c, f.open, f.offset = c, true, 0
		e.mu.Unlock()
		r.Qid = e.uqid(c)
		r.Iounit = e.iounit(c)
	case styx.Tread:
		f := e.getfid(t.Fid)
		if f == nil {
			return rerr(t, enofid)
		}
		c := f.c
		if !f.open {
			return rerr(t, emode)
		}
		if a := c.Mode & 3; a != abi.OREAD && a != abi.ORDWR && a != abi.OEXEC {
			return rerr(t, eaccess)
		}
		if t.Count > e.msizeOr()-styx.IOHDRSZ {
			return rerr(t, ecount)
		}
		buf := make([]byte, t.Count)
		var n int
		var err error
		if c.IsDir() {
			if int64(t.Offset) != f.offset && t.Offset != 0 {
				return rerr(t, eseekdir)
			}
			n, err = dirRead(ctx, c, buf, int64(t.Offset))
			if err == nil {
				f.offset = int64(t.Offset) + int64(n)
			}
		} else {
			n, err = c.Dev.Read(ctx, c, buf, int64(t.Offset))
		}
		if err != nil {
			return rerrf(t, err)
		}
		r.Data = buf[:n]
	case styx.Twrite:
		f := e.getfid(t.Fid)
		if f == nil {
			return rerr(t, enofid)
		}
		c := f.c
		if !f.open {
			return rerr(t, emode)
		}
		if a := c.Mode & 3; a != abi.OWRITE && a != abi.ORDWR {
			return rerr(t, eaccess)
		}
		if c.IsDir() {
			return rerr(t, abi.Eisdir)
		}
		n, err := c.Dev.Write(ctx, c, t.Data, int64(t.Offset))
		if err != nil {
			return rerrf(t, err)
		}
		r.Count = uint32(n)
	case styx.Tclunk, styx.Tremove:
		e.mu.Lock()
		f := e.fids[t.Fid]
		delete(e.fids, t.Fid)
		e.mu.Unlock()
		if f == nil {
			return rerr(t, enofid)
		}
		var err error
		if t.Type == styx.Tremove {
			err = f.c.Dev.Remove(ctx, f.c)
		}
		if f.open {
			f.c.DecRef()
		}
		if err != nil {
			return rerrf(t, err)
		}
	case styx.Tstat:
		f := e.getfid(t.Fid)
		if f == nil {
			return rerr(t, enofid)
		}
		d, err := statNamed(ctx, f.c)
		if err != nil {
			return rerrf(t, err)
		}
		d.Qid = e.uqid(f.c)
		if e.isroot(f.c) && d.Name == "/" {
			d.Name = "/"
		}
		r.Stat = styx.MarshalDir(nil, &d)
	case styx.Twstat:
		f := e.getfid(t.Fid)
		if f == nil {
			return rerr(t, enofid)
		}
		d, err := styx.UnmarshalDir(t.Stat)
		if err != nil {
			return rerr(t, abi.Ebadstat)
		}
		if d.Name != "" && (d.Name == "." || d.Name == ".." || strings.Contains(d.Name, "/")) {
			return rerr(t, abi.Efilename)
		}
		if err := f.c.Dev.Wstat(ctx, f.c, d); err != nil {
			return rerrf(t, err)
		}
	default:
		return rerr(t, "invalid T-message type")
	}
	return r
}

func (e *Exporter) msizeOr() uint32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.msize == 0 {
		return maxrpc
	}
	return e.msize
}

func (e *Exporter) iounit(c *Chan) uint32 {
	n := e.msizeOr() - styx.IOHDRSZ
	if iu, ok := c.Dev.(Iouniter); ok {
		if k := iu.Iounit(c); k != 0 && k < n {
			n = k
		}
	}
	return n
}
