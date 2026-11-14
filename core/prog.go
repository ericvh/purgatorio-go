package core

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
)

// Kernel is the state every prog shares: the device table, the prog
// table and the platform.
type Kernel struct {
	Platform platform.Platform
	DevTab   *DevTab
	Progs    *Progs
	// Owner is the host user who started the system (eve).
	Owner string
	// Mnt is the mount driver, which mount and fauth reach directly.
	Mnt *DevMnt
	// Srv is #s, where file2chan makes files.
	Srv *DevSrv
}

// Progs is the prog table.
type Progs struct {
	mu      sync.Mutex
	progs   map[int]*Prog
	nextpid int
	private map[int]bool // groups made private by ctl
}

func NewProgs() *Progs { return &Progs{progs: map[int]*Prog{}, nextpid: 1} }

func (t *Progs) Get(pid int) *Prog {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.progs[pid]
}

// Pids lists the live progs, lowest first.
func (t *Progs) Pids() []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]int, 0, len(t.progs))
	for pid := range t.progs {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

// Kill kills one prog.
func (t *Progs) Kill(pid int) bool {
	if p := t.Get(pid); p != nil {
		p.Kill()
		return true
	}
	return false
}

// KillGroup kills every prog in group g.
func (t *Progs) KillGroup(g int) {
	for _, pid := range t.Pids() {
		if p := t.Get(pid); p != nil && p.Group() == g {
			p.Kill()
		}
	}
}

// InterruptGroup posts an interrupt to every prog in group g.
func (t *Progs) InterruptGroup(g int) {
	for _, pid := range t.Pids() {
		if p := t.Get(pid); p != nil && p.Group() == g {
			p.Note.Interrupt()
		}
	}
}

func (t *Progs) register(p *Prog) {
	t.mu.Lock()
	p.Pid = t.nextpid
	t.nextpid++
	t.progs[p.Pid] = p
	t.mu.Unlock()
}

func (t *Progs) remove(p *Prog) {
	t.mu.Lock()
	delete(t.progs, p.Pid)
	t.mu.Unlock()
}

// Prog is a process: Inferno's Prog with its Osenv. It runs one guest
// instance on its own goroutine.
type Prog struct {
	Kernel *Kernel
	Pid    int
	Note   *Note
	// Ctx carries the prog and its note to every blocking call; Cancel
	// ends guest execution on kill.
	Ctx    context.Context
	Cancel context.CancelFunc

	mu     sync.Mutex
	group  int
	pgrp   *Pgrp
	fgrp   *Fgrp
	egrp   *Egrp
	user   string
	name   string
	state  string
	errstr string
	parent *Prog
	// childq is the queue of a /prog/n/wait file open on this prog;
	// children spawned while it is open post their exit messages there.
	// waitq is the parent's childq when this prog was spawned (Inferno's
	// Osenv waitq).
	kgrp   *Kgrp
	childq *Queue
	waitq  *Queue
	start  time.Time
	exited atomic.Bool
	dbg    progdbg
}

type progKey struct{}

// ProgFrom returns the prog behind ctx, or nil.
func ProgFrom(ctx context.Context) *Prog {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(progKey{}).(*Prog)
	return p
}

// NewProg makes the first prog of a system: a new group, the given name
// space, a fresh fd and env group, and user.
func NewProg(k *Kernel, pgrp *Pgrp, user string) *Prog {
	p := &Prog{Kernel: k, pgrp: pgrp, fgrp: NewFgrp(), egrp: NewEgrp(), user: user, state: "alt"}
	k.Progs.register(p)
	p.group = p.Pid
	p.init(context.Background())
	return p
}

func (p *Prog) init(parent context.Context) {
	p.Note = NewNote()
	p.kgrp = NewKgrp()
	p.start = time.Now()
	ctx, cancel := context.WithCancel(parent)
	ctx = WithNote(context.WithValue(ctx, progKey{}, p), p.Note)
	p.Ctx, p.Cancel = ctx, cancel
}

func (p *Prog) Pgrp() *Pgrp   { p.mu.Lock(); defer p.mu.Unlock(); return p.pgrp }
func (p *Prog) Fgrp() *Fgrp   { p.mu.Lock(); defer p.mu.Unlock(); return p.fgrp }
func (p *Prog) Egrp() *Egrp   { p.mu.Lock(); defer p.mu.Unlock(); return p.egrp }
func (p *Prog) User() string  { p.mu.Lock(); defer p.mu.Unlock(); return p.user }
func (p *Prog) Name() string  { p.mu.Lock(); defer p.mu.Unlock(); return p.name }
func (p *Prog) Group() int    { p.mu.Lock(); defer p.mu.Unlock(); return p.group }
func (p *Prog) State() string { p.mu.Lock(); s := p.state; p.mu.Unlock(); return p.dbgstate(s) }

func (p *Prog) SetName(n string)  { p.mu.Lock(); p.name = n; p.mu.Unlock() }
func (p *Prog) SetState(s string) { p.mu.Lock(); p.state = s; p.mu.Unlock() }
func (p *Prog) SetUser(u string)  { p.mu.Lock(); p.user = u; p.mu.Unlock() }
func (p *Prog) SetPgrp(g *Pgrp)   { p.mu.Lock(); p.pgrp = g; p.mu.Unlock() }

func (p *Prog) SetFgrp(f *Fgrp) {
	p.mu.Lock()
	old := p.fgrp
	p.fgrp = f
	p.mu.Unlock()
	if old != nil && old != f {
		old.Release()
	}
}

// Errstr and SetErrstr give the prog's error string.
func (p *Prog) Errstr() string { p.mu.Lock(); defer p.mu.Unlock(); return p.errstr }
func (p *Prog) SetErrstr(s string) {
	if len(s) > ERRMAX {
		s = s[:ERRMAX]
	}
	p.mu.Lock()
	p.errstr = s
	p.mu.Unlock()
}

// SetError records err as the errstr.
func (p *Prog) SetError(err error) { p.SetErrstr(Errstr(err)) }

// ERRMAX is Inferno's longest error string.
const ERRMAX = 128

// Dot is the current directory's name.
func (p *Prog) Dot() string { return p.Pgrp().Dot() }

// Kill ends the prog: blocking calls return Eintr and guest code stops.
func (p *Prog) Kill() {
	p.Note.Kill()
	p.Cancel()
}

// OpenWait makes a fresh queue for child exit messages, as opening
// /prog/n/wait does, and returns it.
func (p *Prog) OpenWait() *Queue {
	q := NewMsgQueue(1024 * 64)
	p.mu.Lock()
	p.childq = q
	p.mu.Unlock()
	return q
}

// CloseWait drops the wait queue q if it is still the prog's.
func (p *Prog) CloseWait(q *Queue) {
	p.mu.Lock()
	if p.childq == q {
		p.childq = nil
	}
	p.mu.Unlock()
}

// Kgrp is the prog's kernel-channel handles.
func (p *Prog) Kgrp() *Kgrp { return p.kgrp }

// Elapsed is how long the prog has run.
func (p *Prog) Elapsed() time.Duration { return time.Since(p.start) }

// Pctl changes which groups the prog shares, as Sys->pctl does, and
// returns its pid. movefd lists the fds NEWFD keeps, or FORKFD closes.
func (p *Prog) Pctl(flags uint32, movefd []int) (int, error) {
	ctx := p.Ctx
	if flags&abi.NEWFD != 0 {
		p.SetFgrp(p.Fgrp().Keep(movefd))
	} else if flags&abi.FORKFD != 0 {
		f := p.Fgrp().Fork()
		for _, fd := range movefd {
			_ = f.Close(fd)
		}
		p.SetFgrp(f)
	}
	if flags&abi.NEWNS != 0 {
		n, err := p.Pgrp().NewNS(ctx)
		if err != nil {
			return -1, err
		}
		p.SetPgrp(n)
	} else if flags&abi.FORKNS != 0 {
		p.SetPgrp(p.Pgrp().Fork())
	}
	if flags&abi.NEWENV != 0 {
		p.mu.Lock()
		p.egrp = NewEgrp()
		p.mu.Unlock()
	} else if flags&abi.FORKENV != 0 {
		p.mu.Lock()
		p.egrp = p.egrp.Fork()
		p.mu.Unlock()
	}
	if flags&abi.NEWPGRP != 0 {
		p.mu.Lock()
		p.group = p.Pid
		p.mu.Unlock()
	}
	if flags&abi.NODEVS != 0 {
		p.Pgrp().SetNodevs()
	}
	return p.Pid, nil
}

// Spawn makes a child prog. By default it shares the parent's name space,
// fd group and env group, as Limbo's spawn does; flags fork or replace
// them as pctl would. fdmap moves fds (child = parent) into a new fd group
// when NEWFD is set.
func (p *Prog) Spawn(flags uint32, fdmap [][2]int) (*Prog, error) {
	p.mu.Lock()
	c := &Prog{Kernel: p.Kernel, pgrp: p.pgrp, fgrp: p.fgrp.Share(), egrp: p.egrp,
		user: p.user, group: p.group, name: p.name, state: "alt", parent: p, waitq: p.childq}
	p.mu.Unlock()
	switch {
	case flags&abi.NEWFD != 0:
		nf := NewFgrp()
		for _, m := range fdmap {
			if ch, err := c.fgrp.Get(m[1]); err == nil {
				_ = nf.Install(m[0], ch.IncRef())
			}
		}
		c.fgrp.Release()
		c.fgrp = nf
	case flags&abi.FORKFD != 0:
		f := c.fgrp.Fork()
		c.fgrp.Release()
		c.fgrp = f
	}
	if flags&abi.NEWNS != 0 {
		n, err := c.pgrp.NewNS(p.Ctx)
		if err != nil {
			c.fgrp.Release()
			return nil, err
		}
		c.pgrp = n
	} else if flags&abi.FORKNS != 0 {
		c.pgrp = c.pgrp.Fork()
	}
	if flags&abi.NEWENV != 0 {
		c.egrp = NewEgrp()
	} else if flags&abi.FORKENV != 0 {
		c.egrp = c.egrp.Fork()
	}
	if flags&abi.NODEVS != 0 {
		c.pgrp.SetNodevs()
	}
	p.Kernel.Progs.register(c)
	if flags&abi.NEWPGRP != 0 {
		c.group = c.Pid
	}
	c.dbg.restrict = p.Restricted()
	c.init(context.Background())
	p.dbgspawn(c.Pid)
	return c, nil
}

// Exit ends the prog with an Inferno status (empty for success), posts
// the wait message to its parent and releases its groups.
func (p *Prog) Exit(status string) {
	if !p.exited.CompareAndSwap(false, true) {
		return
	}
	if status == "" && p.Note.Killed() {
		status = "killed"
	}
	p.dbgexit(status)
	msg := fmt.Sprintf("%d \"%s\":%s", p.Pid, p.Name(), status)
	p.mu.Lock()
	wq := p.waitq
	p.mu.Unlock()
	if wq != nil {
		wq.Produce([]byte(msg))
	}
	p.Kernel.Progs.remove(p)
	p.mu.Lock()
	f := p.fgrp
	p.fgrp = NewFgrp()
	p.state = "exiting"
	p.mu.Unlock()
	f.Release()
	p.Cancel()
}

// Detach ends the prog as far as its parent can tell, posting its wait
// message with an empty status, while the running code carries on as a
// new prog in the same groups. It is what a Limbo command does when init
// returns with threads it spawned still running, as ramfile and memfs do.
// It returns the new pid.
func (p *Prog) Detach() int {
	msg := fmt.Sprintf("%d \"%s\":", p.Pid, p.Name())
	p.mu.Lock()
	wq := p.waitq
	p.waitq = nil
	p.parent = nil
	p.mu.Unlock()
	t := p.Kernel.Progs
	t.mu.Lock()
	delete(t.progs, p.Pid)
	p.Pid = t.nextpid
	t.nextpid++
	t.progs[p.Pid] = p
	t.mu.Unlock()
	if wq != nil {
		wq.Produce([]byte(msg))
	}
	return p.Pid
}
