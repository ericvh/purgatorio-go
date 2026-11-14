package core

import (
	"context"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/styx"
)

const maxsnarf = 100 * 1024

// DevSnarf is #^, the host's snarf buffer (devsnarf.c). A write opens a
// new buffer that is handed to the host when the file is closed; a read
// at offset 0 fetches the host's buffer. Without a host clipboard the
// buffer is kept here.
type DevSnarf struct {
	DevBase
	k    *Kernel
	mu   sync.Mutex
	vers uint32
	own  []byte
}

func NewDevSnarf(k *Kernel) *DevSnarf { return &DevSnarf{k: k} }

func (d *DevSnarf) DC() rune     { return '^' }
func (d *DevSnarf) Name() string { return "snarf" }

var snarftab = []Tab{{"snarf", 1, 0o666, 0}}

func (d *DevSnarf) Attach(context.Context, string) (Walked, error) {
	return Walked{Qid: styx.Qid{Type: styx.QTDIR}}, nil
}

func (d *DevSnarf) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	if !c.IsDir() {
		return Walked{}, Error(abi.Enotdir)
	}
	return TabWalk(snarftab, name)
}

func (d *DevSnarf) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	if c.IsDir() {
		return TabDir('^', Tab{".", 0, styx.DMDIR | 0o555, 0}, d.k.Owner), nil
	}
	dir := TabDir('^', snarftab[0], d.k.Owner)
	d.mu.Lock()
	dir.Qid.Vers = d.vers
	d.mu.Unlock()
	return dir, nil
}

func (d *DevSnarf) DirRead(context.Context, *Chan) ([]styx.Dir, error) {
	return []styx.Dir{TabDir('^', snarftab[0], d.k.Owner)}, nil
}

type snarfbuf struct{ b []byte }

func (d *DevSnarf) Open(_ context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, Error(abi.Eperm)
		}
		return c, nil
	}
	if err := OpenPerm(0o666, mode); err != nil {
		return nil, err
	}
	c.State = &snarfbuf{}
	return c, nil
}

func (d *DevSnarf) clipread() []byte {
	if cb, ok := d.k.Platform.(platform.Clipboard); ok {
		return cb.ClipRead()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]byte(nil), d.own...)
}

func (d *DevSnarf) clipwrite(b []byte) {
	if cb, ok := d.k.Platform.(platform.Clipboard); ok {
		cb.ClipWrite(b)
		return
	}
	d.mu.Lock()
	d.own = append([]byte(nil), b...)
	d.mu.Unlock()
}

func (d *DevSnarf) Close(c *Chan) {
	s, ok := c.State.(*snarfbuf)
	if !ok || c.IsDir() {
		return
	}
	if a := c.Mode & 3; a == abi.OWRITE || a == abi.ORDWR {
		d.clipwrite(s.b)
	}
}

func (d *DevSnarf) Read(_ context.Context, c *Chan, buf []byte, off int64) (int, error) {
	s := c.State.(*snarfbuf)
	if off == 0 {
		s.b = d.clipread()
	}
	return ReadStr(s.b, buf, off), nil
}

// Write appends, whatever the offset, as Inferno's does.
func (d *DevSnarf) Write(_ context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	s := c.State.(*snarfbuf)
	if len(s.b)+len(buf) > maxsnarf {
		return 0, Error(abi.Etoobig)
	}
	s.b = append(s.b, buf...)
	d.mu.Lock()
	d.vers++
	d.mu.Unlock()
	return len(buf), nil
}
