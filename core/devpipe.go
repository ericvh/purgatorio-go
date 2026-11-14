package core

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	qpipedir = iota
	qdata0
	qdata1
)

// pipeqsize is each direction's queue size, as Inferno's pipeqsize.
const pipeqsize = 32 * 1024

func (p *pipe) queues() [2]*Queue {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.q
}

type pipe struct {
	path uint64
	user string
	q    [2]*Queue
	mu   sync.Mutex
	qref [2]int
}

// DevPipe is #|: each attach makes a new pipe whose two files, data and
// data1, are its ends (devpipe.c). Data written on one is read on the
// other; when every open of an end is closed, the other end's reads see
// end of file and its writes fail.
type DevPipe struct {
	DevBase
	k    *Kernel
	next atomic.Uint64
}

func NewDevPipe(k *Kernel) *DevPipe { return &DevPipe{k: k} }

func (d *DevPipe) DC() rune     { return '|' }
func (d *DevPipe) Name() string { return "pipe" }

func netqid(id uint64, t uint64) uint64 { return id<<5 | t }
func nettype(path uint64) uint64        { return path & 0x1f }

func (d *DevPipe) Attach(ctx context.Context, _ string) (Walked, error) {
	user := d.k.Owner
	if p := ProgFrom(ctx); p != nil {
		user = p.User()
	}
	p := &pipe{path: d.next.Add(1), user: user, q: [2]*Queue{NewQueue(pipeqsize), NewQueue(pipeqsize)}}
	return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: netqid(2*p.path, qpipedir)}, Aux: p}, nil
}

var pipetab = []Tab{{"data", qdata0, 0o660, 0}, {"data1", qdata1, 0o660, 0}}

func (d *DevPipe) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	p := c.Aux.(*pipe)
	if nettype(c.Qid.Path) != qpipedir {
		return Walked{}, Error(abi.Enotdir)
	}
	w, err := TabWalk(pipetab, name)
	if err != nil {
		return w, err
	}
	w.Qid.Path = netqid(2*p.path, w.Qid.Path)
	w.Aux = p
	return w, nil
}

func (d *DevPipe) dir(p *pipe, t Tab) styx.Dir {
	switch t.Path {
	case qdata0:
		t.Length = uint64(p.queues()[0].Len())
	case qdata1:
		t.Length = uint64(p.queues()[1].Len())
	}
	dir := TabDir('|', t, p.user)
	dir.Qid.Path = netqid(2*p.path, t.Path)
	return dir
}

func (d *DevPipe) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	p := c.Aux.(*pipe)
	switch nettype(c.Qid.Path) {
	case qpipedir:
		return d.dir(p, Tab{".", qpipedir, styx.DMDIR | 0o500, 0}), nil
	case qdata0:
		return d.dir(p, pipetab[0]), nil
	case qdata1:
		return d.dir(p, pipetab[1]), nil
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevPipe) DirRead(_ context.Context, c *Chan) ([]styx.Dir, error) {
	p := c.Aux.(*pipe)
	return []styx.Dir{d.dir(p, pipetab[0]), d.dir(p, pipetab[1])}, nil
}

func (d *DevPipe) Open(_ context.Context, c *Chan, mode int) (*Chan, error) {
	p := c.Aux.(*pipe)
	t := nettype(c.Qid.Path)
	if t == qpipedir {
		if mode != abi.OREAD {
			return nil, Error(abi.Eperm)
		}
		return c, nil
	}
	p.mu.Lock()
	p.qref[t-qdata0]++
	p.mu.Unlock()
	return c, nil
}

// Close hangs up both directions once an end has no opens left, so the
// peer sees end of file on read and a hangup on write.
func (d *DevPipe) Close(c *Chan) {
	p := c.Aux.(*pipe)
	t := nettype(c.Qid.Path)
	if t == qpipedir {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.qref[t-qdata0]--
	if p.qref[t-qdata0] == 0 {
		// Closing either side hangs up the stream: data reads q[0]
		// and writes q[1], data1 the reverse.
		p.q[0].Hangup()
		p.q[1].Hangup()
	}
	// With both sides closed the pipe can be opened again.
	if p.qref[0] == 0 && p.qref[1] == 0 {
		p.q = [2]*Queue{NewQueue(pipeqsize), NewQueue(pipeqsize)}
	}
}

func (d *DevPipe) Read(ctx context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	p := c.Aux.(*pipe)
	switch nettype(c.Qid.Path) {
	case qdata0:
		return p.queues()[0].Read(ctx, buf)
	case qdata1:
		return p.queues()[1].Read(ctx, buf)
	}
	return 0, Error(abi.Eisdir)
}

func (d *DevPipe) Write(ctx context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	p := c.Aux.(*pipe)
	switch nettype(c.Qid.Path) {
	case qdata0:
		return p.queues()[1].Write(ctx, buf)
	case qdata1:
		return p.queues()[0].Write(ctx, buf)
	}
	return 0, Error(abi.Eperm)
}

func (d *DevPipe) Iounit(*Chan) uint32 { return 0 }

func (d *DevPipe) Ready(c *Chan, write bool) bool {
	p := c.Aux.(*pipe)
	q := p.queues()
	r, w := q[0], q[1]
	if nettype(c.Qid.Path) == qdata1 {
		r, w = w, r
	}
	if write {
		return w.Writable()
	}
	return r.Readable()
}

func (d *DevPipe) Watch(c *Chan, _ bool, r *Rendez) {
	q := c.Aux.(*pipe).queues()
	q[0].Watch(r)
	q[1].Watch(r)
}

func (d *DevPipe) Unwatch(c *Chan, r *Rendez) {
	q := c.Aux.(*pipe).queues()
	q[0].Unwatch(r)
	q[1].Unwatch(r)
}
