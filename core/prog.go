package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	// Prof is #P's profiler, which the engine asks as modules start.
	Prof *Profile
}

// Progs is the prog table.
type Progs struct {
	mu      sync.Mutex
	progs   map[int]*Prog
	nextpid int
	dom     domainState
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

// KillGroupIn kills the progs of group g that are in domain id.
func (t *Progs) KillGroupIn(g, id int) {
	for _, pid := range t.Pids() {
		if p := t.Get(pid); p != nil && p.Group() == g && p.InDomain(id) {
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
	// posted is closed once Exit has taken the prog out of the table and
	// posted its wait message (Kill waits for it).
	posted chan struct{}
	// domains are the prog domains this prog is in, outermost first
	// (limits.go); inherited from the parent.
	domains []int
	// memlimit is the most wasm memory the prog may hold, in bytes, and
	// mem what it holds now.
	memlimit atomic.Uint64
	mem      atomic.Uint64
	dbg      progdbg
	// modpol is the module policy (modsig.go), inherited at spawn, and
	// modules what the prog has loaded.
	modpol  *ModPolicy
	modules []ModRecord
	// exflags are the exceptions ctl settings, Inferno's Ppropagate
	// and Pnotifyleader, inherited at spawn.
	exflags int
}

// The exceptions settings of a group leader's ctl file.
const (
	exPropagate = 1 << iota
	exNotifyLeader
)

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
	p := &Prog{Kernel: k, pgrp: pgrp, fgrp: NewFgrp(), egrp: NewEgrp(), user: user, state: "alt",
		posted: make(chan struct{})}
	p.memlimit.Store(Unlimited)
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

// SetPgrp gives the prog name space g, taking over the caller's
// reference, and releases the one it had.
func (p *Prog) SetPgrp(g *Pgrp) {
	p.mu.Lock()
	old := p.pgrp
	p.pgrp = g
	p.mu.Unlock()
	if old != nil && old != g {
		old.Release()
	}
}

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
// A broken prog exits before Kill returns, as Inferno's killprog
// deletes it, so its wait message is posted by then. The cancel also
// wakes the prog's own End, which may reach Exit first; Kill then waits
// for that Exit to post.
func (p *Prog) Kill() {
	p.Note.Kill()
	p.Cancel()
	if p.Broken() {
		p.Exit("")
		<-p.posted
	}
}

// Broken reports whether the prog is broken (End).
func (p *Prog) Broken() bool { p.dbg.mu.Lock(); defer p.dbg.mu.Unlock(); return p.dbg.broken }

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
		p.exflags = 0
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
	c := &Prog{Kernel: p.Kernel, pgrp: p.pgrp.IncRef(), fgrp: p.fgrp.Share(), egrp: p.egrp,
		user: p.user, group: p.group, name: p.name, state: "alt", parent: p, waitq: p.childq,
		domains: p.domains, modpol: p.modpol, exflags: p.exflags, posted: make(chan struct{})}
	p.mu.Unlock()
	c.memlimit.Store(p.Memlimit())
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
			c.pgrp.Release()
			return nil, err
		}
		c.pgrp.Release()
		c.pgrp = n
	} else if flags&abi.FORKNS != 0 {
		n := c.pgrp.Fork()
		c.pgrp.Release()
		c.pgrp = n
	}
	if flags&abi.NEWENV != 0 {
		c.egrp = NewEgrp()
	} else if flags&abi.FORKENV != 0 {
		c.egrp = c.egrp.Fork()
	}
	if flags&abi.NODEVS != 0 {
		c.pgrp.SetNodevs()
	}
	admitted, err := p.Kernel.Progs.admit(c.domains)
	if err != nil {
		c.fgrp.Release()
		c.pgrp.Release()
		return nil, err
	}
	p.Kernel.Progs.register(c)
	admitted()
	if flags&abi.NEWPGRP != 0 {
		c.group = c.Pid
		c.exflags = 0
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
	if p.Note.Killed() {
		if status == "" {
			status = "killed"
		}
		p.propex("killed")
	}
	p.dbgexit(false, status)
	msg := p.waitmsg(status)
	// The prog leaves the table before its parent hears of it, so a
	// prog the parent starts next does not find it still counted
	// against a domain's prog limit.
	p.Kernel.Progs.remove(p)
	p.mu.Lock()
	wq := p.waitq
	p.mu.Unlock()
	if wq != nil {
		wq.Produce([]byte(msg))
	}
	close(p.posted)
	p.mu.Lock()
	f := p.fgrp
	p.fgrp = NewFgrp()
	p.state = "exiting"
	g := p.pgrp
	p.mu.Unlock()
	f.Release()
	// The name space goes too, as in Inferno's delprog: mounts only
	// this prog held end now.
	p.SetPgrp(g.Exited(context.Background()))
	p.Cancel()
	p.dbg.mu.Lock()
	release := p.dbg.release
	p.dbg.release, p.dbg.mem, p.dbg.frames = nil, nil, nil
	p.dbg.mu.Unlock()
	if release != nil {
		release()
	}
}

func (p *Prog) waitmsg(status string) string {
	return fmt.Sprintf("%d \"%s\":%s", p.Pid, p.Name(), status)
}

// End is how a prog ends when its module returns, with its Inferno
// status, as Inferno's progexit decides. A prog whose guest trapped
// (Trapped) has raised an exception nothing handled: it breaks. So does
// any prog being debugged, after its debugger is told how it ended. A
// broken prog posts its wait message now and stays, in state broken
// with its memory and stack readable, until it is killed. Any other prog
// exits.
func (p *Prog) End(status string) {
	if p.Note.Killed() {
		p.Exit(status)
		return
	}
	p.dbg.mu.Lock()
	broken := p.dbg.trapped
	p.dbg.mu.Unlock()
	// A trap and a fail: status are what Inferno calls exceptions; the
	// group hears of them when its leader asked to.
	if broken || strings.HasPrefix(status, "fail:") {
		p.propex(status)
	}
	if p.dbgexit(broken, status) {
		broken = true
	}
	if !broken {
		p.Exit(status)
		return
	}
	msg := p.waitmsg(status)
	p.mu.Lock()
	wq := p.waitq
	p.state = "broken"
	p.mu.Unlock()
	p.dbg.mu.Lock()
	p.dbg.broken = true
	p.dbg.mu.Unlock()
	if wq != nil {
		wq.Produce([]byte(msg))
	}
	var r Rendez
	_ = ParkKillable(p.Ctx, &r, func() bool { return false })
	p.Exit("")
}

// propex passes an exception on to the prog's group, as Inferno's propex
// does when the group leader wrote exceptions to its ctl. Inferno raises
// the exception in each other member (propagate), or kills them and
// raises it in the leader (notifyleader). A guest cannot take an
// exception raised from outside, so here each of those progs is killed.
func (p *Prog) propex(estr string) {
	p.mu.Lock()
	fl, g := p.exflags, p.group
	p.mu.Unlock()
	if fl == 0 || estr == "" {
		return
	}
	t := p.Kernel.Progs
	leader := t.Get(g)
	if leader == nil || leader.Group() != g {
		leader = p
	}
	leader.mu.Lock()
	lf := leader.exflags
	leader.mu.Unlock()
	if lf == 0 {
		return
	}
	for _, pid := range t.Pids() {
		f := t.Get(pid)
		if f == nil || f == p || f == leader || f.Group() != g {
			continue
		}
		f.mu.Lock()
		f.exflags = 0 // no recursion
		f.mu.Unlock()
		f.Kill()
	}
	if leader != p {
		leader.Kill()
	}
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
