package core

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// #k, device subsystems (devds.c, from Plan 9): mirrors, catenations,
// interleavings and partitions of other files, configured by writing
// lines like "mirror name file1 file2" to /ds/ctl.
const (
	dsMirror = iota
	dsCat
	dsInter
	dsPart

	dsBlksize = 8 * 1024
	dsNdevs   = 8
	dsNfs     = 64
	dsQtop    = 0
	dsQdir    = 1
	dsQctl    = 2
	dsQfirst  = 3
	dsCfgstr  = "fsdev:\n"
)

type fsdev struct {
	typ   int
	name  string
	start int64
	size  int64
	idev  []*Chan
	isize []int64
}

// DevDS is #k.
type DevDS struct {
	DevBase
	k       *Kernel
	mu      sync.Mutex
	devs    [dsNfs]*fsdev
	conf    string
	configd bool
}

func NewDevDS(k *Kernel) *DevDS { return &DevDS{k: k} }

func (d *DevDS) DC() rune     { return 'k' }
func (d *DevDS) Name() string { return "ds" }

func (d *DevDS) Attach(context.Context, string) (Walked, error) {
	return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: dsQtop}}, nil
}

func (d *DevDS) gen(q styx.Qid) []styx.Dir {
	mk := func(path uint64, dir bool, name string, size int64) styx.Dir {
		x := styx.Dir{Type: 'k', Qid: styx.Qid{Path: path}, Name: name, UID: d.k.Owner, GID: d.k.Owner,
			Mode: 0o664, Length: uint64(size)}
		if dir {
			x.Qid.Type, x.Mode = styx.QTDIR, styx.DMDIR|0o775
		}
		return x
	}
	switch q.Path {
	case dsQtop:
		return []styx.Dir{mk(dsQdir, true, "ds", 0)}
	case dsQdir:
		out := []styx.Dir{mk(dsQctl, false, "ctl", 0)}
		d.mu.Lock()
		for i, f := range d.devs {
			if f != nil {
				out = append(out, mk(uint64(dsQfirst+i), false, f.name, f.size))
			}
		}
		d.mu.Unlock()
		return out
	}
	return nil
}

func (d *DevDS) Walk(ctx context.Context, c *Chan, name string) (Walked, error) {
	d.rdconf(ctx)
	if !c.IsDir() {
		return Walked{}, Error(abi.Enotdir)
	}
	for _, e := range d.gen(c.Qid) {
		if e.Name == name {
			return Walked{Qid: e.Qid}, nil
		}
	}
	return Walked{}, Error(abi.Enonexist)
}

func (d *DevDS) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	switch c.Qid.Path {
	case dsQtop:
		return styx.Dir{Type: 'k', Qid: c.Qid, Name: "#k", UID: d.k.Owner, GID: d.k.Owner, Mode: styx.DMDIR | 0o775}, nil
	case dsQdir:
		return d.gen(styx.Qid{Path: dsQtop})[0], nil
	}
	for _, e := range d.gen(styx.Qid{Path: dsQdir}) {
		if e.Qid.Path == c.Qid.Path {
			return e, nil
		}
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevDS) DirRead(_ context.Context, c *Chan) ([]styx.Dir, error) { return d.gen(c.Qid), nil }

func (d *DevDS) Open(_ context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() && mode != abi.OREAD {
		return nil, Error(abi.Eperm)
	}
	return c, nil
}

// rdconf reads /dev/sdC0/fscfg once, as devds does, quietly.
func (d *DevDS) rdconf(ctx context.Context) {
	d.mu.Lock()
	if d.configd {
		d.mu.Unlock()
		return
	}
	d.configd = true
	d.mu.Unlock()
	p := ProgFrom(ctx)
	if p == nil {
		return
	}
	c, err := p.OpenChan("/dev/sdC0/fscfg", abi.OREAD)
	if err != nil {
		return
	}
	defer c.DecRef()
	buf := make([]byte, 1024)
	n, _ := c.Dev.Read(ctx, c, buf, 0)
	s := string(buf[:n])
	if !strings.HasPrefix(s, dsCfgstr) {
		return
	}
	for _, line := range strings.Split(s[len(dsCfgstr):], "\n") {
		if line != "" {
			_ = d.config(ctx, line)
		}
	}
}

func (d *DevDS) config(ctx context.Context, line string) error {
	f := strings.Fields(line)
	if len(f) == 0 {
		return Error(abi.Ecmdargs)
	}
	typ := map[string]int{"mirror": dsMirror, "cat": dsCat, "inter": dsInter, "part": dsPart}
	t, ok := typ[f[0]]
	if !ok {
		return Error(abi.Enoctl)
	}
	args := f[1:]
	var start, size int64
	if t == dsPart {
		if len(f) != 5 {
			return Error(abi.Ecmdargs)
		}
		start, _ = strconv.ParseInt(args[2], 10, 64)
		size, _ = strconv.ParseInt(args[3], 10, 64)
		args = args[:2]
	}
	if len(args) < 1 {
		return Error(abi.Ecmdargs)
	}
	if len(args)-1 > dsNdevs {
		return Error("too many devices; fix me")
	}
	p := ProgFrom(ctx)
	if p == nil {
		return Error(abi.Eperm)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	slot := -1
	for i, x := range d.devs {
		if x != nil && x.name == args[0] {
			return Error(abi.Eexist)
		}
		if x == nil && slot < 0 {
			slot = i
		}
	}
	if slot < 0 {
		return Error(abi.Enodev)
	}
	m := &fsdev{typ: t, name: args[0], start: start, size: size}
	for _, n := range args[1:] {
		c, err := p.OpenChan(n, abi.ORDWR)
		if err != nil {
			for _, x := range m.idev {
				x.DecRef()
			}
			return err
		}
		m.idev = append(m.idev, c)
	}
	if t != dsPart {
		m.start, m.size = 0, 0
	}
	for _, c := range m.idev {
		dir, err := c.Dev.Stat(ctx, c)
		if err != nil {
			return err
		}
		l := int64(dir.Length)
		switch t {
		case dsMirror:
			if m.size == 0 || m.size > l {
				m.size = l
			}
		case dsCat:
			m.size += l
		case dsInter:
			l &^= dsBlksize - 1
			m.size += l
		case dsPart:
			if m.start > l {
				m.start = l
			}
			if l < m.start+m.size {
				m.size = l - m.start
			}
		}
		m.isize = append(m.isize, l)
	}
	if d.conf == "" {
		d.conf = dsCfgstr
	}
	d.conf += strings.Join(f, " ") + "\n"
	d.devs[slot] = m
	return nil
}

func (d *DevDS) dev(c *Chan) (*fsdev, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := int(c.Qid.Path) - dsQfirst
	if i < 0 || i >= dsNfs || d.devs[i] == nil {
		return nil, Error(abi.Enonexist)
	}
	return d.devs[i], nil
}

func dsrw(ctx context.Context, c *Chan, b []byte, off int64, read bool) (int, error) {
	if read {
		return c.Dev.Read(ctx, c, b, off)
	}
	return c.Dev.Write(ctx, c, b, off)
}

func (m *fsdev) catio(ctx context.Context, read bool, a []byte, off int64) (int, error) {
	n := len(a)
	done := 0
	for i := 0; done < n && i < len(m.idev); i++ {
		if off > m.isize[i] {
			off -= m.isize[i]
			continue
		}
		l := min(int64(n-done), m.isize[i]-off)
		w, err := dsrw(ctx, m.idev[i], a[done:done+int(l)], off, read)
		if err != nil {
			return done, err
		}
		if int64(w) != l {
			return done, Error("#k: write failed")
		}
		done += int(l)
		off = 0
	}
	return done, nil
}

func (m *fsdev) interio(ctx context.Context, read bool, a []byte, off int64) (int, error) {
	blk, boff := off/dsBlksize, off%dsBlksize
	wsz := dsBlksize - boff
	done := 0
	for done < len(a) {
		i := int(blk % int64(len(m.idev)))
		woff := (blk/int64(len(m.idev)))*dsBlksize + boff
		l := min(int64(len(a)-done), wsz)
		w, err := dsrw(ctx, m.idev[i], a[done:done+int(l)], woff, read)
		if err != nil || int64(w) != l || l == 0 {
			return done, Error(abi.Eio)
		}
		done += int(l)
		blk++
		boff, wsz = 0, dsBlksize
	}
	return done, nil
}

func (d *DevDS) Read(ctx context.Context, c *Chan, a []byte, off int64) (int, error) {
	if c.Qid.Path == dsQctl {
		d.mu.Lock()
		conf := strings.TrimPrefix(d.conf, dsCfgstr)
		d.mu.Unlock()
		return ReadStr([]byte(conf), a, off), nil
	}
	m, err := d.dev(c)
	if err != nil {
		return 0, err
	}
	if off >= m.size {
		return 0, nil
	}
	if off+int64(len(a)) > m.size {
		a = a[:m.size-off]
	}
	switch m.typ {
	case dsMirror:
		for _, x := range m.idev {
			if n, err := x.Dev.Read(ctx, x, a, off); err == nil {
				return n, nil
			}
		}
		return 0, Error(abi.Eio)
	case dsCat:
		return m.catio(ctx, true, a, off)
	case dsInter:
		return m.interio(ctx, true, a, off)
	}
	x := m.idev[0]
	return x.Dev.Read(ctx, x, a, off+m.start)
}

func (d *DevDS) Write(ctx context.Context, c *Chan, a []byte, off int64) (int, error) {
	if c.IsDir() {
		return 0, Error(abi.Eperm)
	}
	if c.Qid.Path == dsQctl {
		if err := d.config(ctx, string(a)); err != nil {
			return 0, err
		}
		return len(a), nil
	}
	m, err := d.dev(c)
	if err != nil {
		return 0, err
	}
	if off >= m.size {
		return 0, nil
	}
	if off+int64(len(a)) > m.size {
		a = a[:m.size-off]
	}
	switch m.typ {
	case dsMirror:
		res := len(a)
		for i := len(m.idev) - 1; i >= 0; i-- {
			x := m.idev[i]
			l, err := x.Dev.Write(ctx, x, a, off)
			if err != nil {
				return 0, err
			}
			res = min(res, l)
		}
		return res, nil
	case dsCat:
		return m.catio(ctx, false, a, off)
	case dsInter:
		return m.interio(ctx, false, a, off)
	}
	x := m.idev[0]
	return x.Dev.Write(ctx, x, a, off+m.start)
}
