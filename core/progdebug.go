package core

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
)

// The debugging side of a prog: what devprog's stack, text, heap,
// exception and dbgctl files show and do. A wasm guest has no Dis
// interpreter to single-step, so the engine reports the points it can
// see: every call the guest makes into a host function that may block.
// At such a call the engine hands over the guest's call stack and its
// linear memory, and a debugger can hold the prog there.

// Frame is one wasm call frame, top first.
type Frame struct {
	Func   uint32 // function index in its module (imports first)
	Offset uint64 // code section offset of the frame's pc, or 0
	Path   string // module path, or $module for a host function
}

// GuestMem is a guest's linear memory as the debugger sees it.
type GuestMem interface {
	Read(off, n uint32) ([]byte, bool)
	Size() uint32
	Max() (uint32, bool)
}

// progdbg is a prog's debugging state: Inferno's exstr, its debugger
// (Osenv's debug) and what the engine last reported.
type progdbg struct {
	mu       sync.Mutex
	text     string  // path of the running module
	frames   []Frame // the stack at the current host call
	inhost   int     // host calls in progress
	calls    uint64  // host calls reported so far
	halted   bool    // held by a debugger
	mem      GuestMem
	exstr    string
	restrict bool
	debug    *DbgCtl // the attached debugger
	r        Rendez  // the halted prog sleeps here
	// trapped is set when the guest of the prog's own module trapped;
	// broken once the prog is left broken (End). release returns the
	// memory a trapped guest still holds, when the prog exits.
	trapped bool
	broken  bool
	release func()
}

// DbgCtl is one open /prog/n/dbgctl, Inferno's Progctl.
type DbgCtl struct {
	q        *Queue
	stop     bool
	debugger bool   // a cont or step is waiting for the prog
	step     bool   // it is a step, which holds the prog at
	until    uint64 // host call number until
	msg      string // its reply
	r        Rendez // where it waits
}

// SetText records the path of the module the prog runs.
func (p *Prog) SetText(path string) {
	p.dbg.mu.Lock()
	p.dbg.text = path
	p.dbg.mu.Unlock()
}

// Text is the path of the module the prog runs.
func (p *Prog) Text() string { p.dbg.mu.Lock(); defer p.dbg.mu.Unlock(); return p.dbg.text }

// SetException records the last exception, in Inferno's form: the pc,
// the module and the error.
func (p *Prog) SetException(pc uint32, module, err string) {
	s := fmt.Sprintf("%d %s %s", pc, module, err)
	p.dbg.mu.Lock()
	p.dbg.exstr = s
	p.dbg.mu.Unlock()
}

// Exception is the last exception, or "".
func (p *Prog) Exception() string { p.dbg.mu.Lock(); defer p.dbg.mu.Unlock(); return p.dbg.exstr }

// Trapped records the state of a guest whose module trapped, for the
// stack and heap files of the broken prog it leaves: its memory, the
// frames of the trap, top first, and what returns the memory when the
// prog exits (or nil).
func (p *Prog) Trapped(mem GuestMem, frames []Frame, release func()) {
	d := &p.dbg
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trapped = true
	if mem != nil {
		d.mem = mem
	}
	if frames != nil {
		d.frames = append(d.frames[:0], frames...)
	}
	if d.release != nil {
		d.release()
	}
	d.release = release
}

// Restricted reports whether ctl's restricted message was written.
func (p *Prog) Restricted() bool { p.dbg.mu.Lock(); defer p.dbg.mu.Unlock(); return p.dbg.restrict }

// EnterHost is called by the engine as the guest calls a host function
// that may block. fill appends the guest's call stack, top first, to
// its argument; text is the path of the prog's module. A debugger may hold the prog here; EnterHost returns
// when it lets go or the prog is killed.
func (p *Prog) EnterHost(mem GuestMem, fill func(fs []Frame, text string) []Frame) {
	d := &p.dbg
	d.mu.Lock()
	d.inhost++
	d.calls++
	d.mem = mem
	d.frames = fill(d.frames[:0], d.text)
	for d.debug != nil && !p.Note.Killed() {
		c := d.debug
		if c.debugger && c.stop {
			c.tell("stopped")
		} else if c.debugger && c.step && d.calls >= c.until {
			c.tell("")
		} else if c.debugger {
			break
		}
		d.halted = true
		w := d.r.waitch()
		d.mu.Unlock()
		select {
		case <-w:
		case <-p.Note.Dead():
		}
		d.mu.Lock()
	}
	d.halted = false
	d.mu.Unlock()
}

// LeaveHost ends a host call begun with EnterHost.
func (p *Prog) LeaveHost() {
	p.dbg.mu.Lock()
	if p.dbg.inhost > 0 {
		p.dbg.inhost--
	}
	p.dbg.mu.Unlock()
}

// dbgstate is the state devprog shows for a prog the engine knows
// about: debug when held, release while in a host call.
func (p *Prog) dbgstate(s string) string {
	p.dbg.mu.Lock()
	defer p.dbg.mu.Unlock()
	switch {
	case p.dbg.halted:
		return "debug"
	case p.dbg.inhost > 0 && s == "ready":
		return "release"
	}
	return s
}

// Stack is the text of /prog/n/stack: one line per frame, top first.
// The fields are those of Inferno's stack file, for a wasm guest: the
// frame number counted from the bottom, the function index, the code
// section offset of the frame's pc, 0 for the code pointer, 1 when the
// module is compiled to native code (always so here: the engine hides
// the difference), and the module path.
func (p *Prog) Stack() (string, bool) {
	p.dbg.mu.Lock()
	defer p.dbg.mu.Unlock()
	if p.dbg.inhost == 0 && !p.dbg.halted && !p.dbg.broken {
		return "", false
	}
	var b strings.Builder
	n := len(p.dbg.frames)
	for i, f := range p.dbg.frames {
		fmt.Fprintf(&b, "%.8x %.8x %.8x %.8x %d %s\n", n-1-i, f.Func, f.Offset, 0, 1, f.Path)
	}
	return b.String(), true
}

// guestmem is the prog's memory, once the engine has reported it.
func (p *Prog) guestmem() GuestMem { p.dbg.mu.Lock(); defer p.dbg.mu.Unlock(); return p.dbg.mem }

// dbgexit tells an attached debugger that the prog has ended, broken
// or exited, and lets it go, as Inferno's dbgexit does. It reports
// whether a debugger was attached.
func (p *Prog) dbgexit(broken bool, estr string) bool {
	d := &p.dbg
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.debug
	if c == nil {
		return false
	}
	d.debug = nil
	msg := "exited"
	if broken {
		msg = "broken: " + estr
	}
	if c.debugger {
		c.tell(msg)
	}
	c.q.Produce([]byte(msg))
	return true
}

// dbgspawn tells an attached debugger about a new child.
func (p *Prog) dbgspawn(pid int) {
	p.dbg.mu.Lock()
	defer p.dbg.mu.Unlock()
	if c := p.dbg.debug; c != nil {
		c.q.Produce(fmt.Appendf(nil, "new %d", pid))
	}
}

// tell answers the waiting debugger. Called with the prog's dbg.mu held.
func (c *DbgCtl) tell(msg string) {
	c.msg = msg
	c.debugger = false
	c.r.Wakeup()
}

func newDbgCtl() *DbgCtl { return &DbgCtl{q: NewMsgQueue(1024)} }

// dbgcont runs the prog under c until it stops or exits, as cont does
// with no breakpoints set. Its reply is always an error, as Inferno's is.
// With step > 0 it is a step: the prog runs until it has made step more
// host calls, where it is held, and the reply is nil, as Inferno's step
// counts instructions.
func (p *Prog) dbgcont(ctx context.Context, c *DbgCtl, step uint64) error {
	if ProgFrom(ctx) == p {
		return Error("cannot step yourself")
	}
	d := &p.dbg
	d.mu.Lock()
	if d.broken {
		d.mu.Unlock()
		return Error("prog broken")
	}
	if c.debugger || (d.debug != nil && d.debug != c) {
		d.mu.Unlock()
		return Error("prog already debugged")
	}
	d.debug = c
	if c.stop {
		d.mu.Unlock()
		return Error("child: stopped")
	}
	c.debugger, c.msg = true, ""
	c.step, c.until = step > 0, d.calls+step
	d.r.Wakeup()
	d.mu.Unlock()
	err := ParkKillable(ctx, &c.r, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !c.debugger
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		c.debugger, c.msg = false, ""
		if d.debug == c {
			d.debug = nil
			d.r.Wakeup()
		}
		return err
	}
	if c.msg != "" {
		return Error("child: " + c.msg)
	}
	return nil
}

// dbgstart lets the prog run free of c, as start does.
func (p *Prog) dbgstart() error {
	d := &p.dbg
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.debug != nil && d.debug.debugger {
		return Error("prog debugged")
	}
	d.debug = nil
	d.r.Wakeup()
	return nil
}

func (p *Prog) dbgsetstop(c *DbgCtl, stop bool) {
	p.dbg.mu.Lock()
	c.stop = stop
	p.dbg.mu.Unlock()
}

// dbgclose drops c, letting the prog go if c held it.
func (p *Prog) dbgclose(c *DbgCtl) {
	p.dbg.mu.Lock()
	if p.dbg.debug == c {
		p.dbg.debug = nil
		p.dbg.r.Wakeup()
	}
	p.dbg.mu.Unlock()
	c.q.Hangup()
}

// SetPrivate marks the prog's group private, as ctl's private message
// does: its progs' heap and dbgctl files no longer open.
func (t *Progs) SetPrivate(g int) {
	t.mu.Lock()
	if t.private == nil {
		t.private = map[int]bool{}
	}
	t.private[g] = true
	t.mu.Unlock()
}

// Private reports whether group g is private.
func (t *Progs) Private(g int) bool { t.mu.Lock(); defer t.mu.Unlock(); return t.private[g] }

const (
	cdstep = iota
	cdtoret
	cdcont
	cdstart
	cdstop
	cdunstop
	cdmaim
	cdbpt
)

var progdbgcmd = []Cmdtab{
	{cdstep, "step", 0},
	{cdtoret, "toret", 1},
	{cdcont, "cont", 1},
	{cdstart, "start", 1},
	{cdstop, "stop", 1},
	{cdunstop, "unstop", 1},
	{cdmaim, "maim", 1},
	{cdbpt, "bpt", 4},
}

// dbgctl carries out a message written to /prog/n/dbgctl. The engine
// can hold a prog only where it calls the host, not between wasm
// instructions, so step n runs the prog to its nth next host call, and
// toret and breakpoints are refused.
func (p *Prog) dbgctl(ctx context.Context, c *DbgCtl, f []string) error {
	var ct Cmdtab
	if len(f) == 1 && strings.HasPrefix(f[0], "step") {
		ct = progdbgcmd[cdstep]
	} else {
		var err error
		if ct, err = LookupCmd(f, progdbgcmd); err != nil {
			return err
		}
	}
	switch ct.Index {
	case cdstep:
		arg := strings.TrimPrefix(f[0], "step")
		if len(f) > 1 {
			arg = f[1]
		}
		n, _ := cstrtoul(arg)
		return p.dbgcont(ctx, c, max(n, 1))
	case cdtoret:
		return Error(abi.Ebadctl)
	case cdbpt:
		if f[1] != "set" && f[1] != "del" {
			return Error(abi.Ebadctl)
		}
		return Error(abi.Ebadctl)
	case cdcont:
		return p.dbgcont(ctx, c, 0)
	case cdstart:
		return p.dbgstart()
	case cdstop:
		p.dbgsetstop(c, true)
	case cdunstop:
		p.dbgsetstop(c, false)
	case cdmaim:
		p.Kill()
	}
	return nil
}

// heapqry is the query last written to /prog/n/heap.
type heapqry struct {
	set   bool
	fmt   byte
	addr  uint64
	count uint64
}

// parse reads a heap query, addr.Fcount, as Inferno's progwrite does.
// The formats that read wasm linear memory are kept (W, B, V, R and P);
// those that follow Dis heap objects (L, A, C, M, c and the
// instruction query I) have nothing to follow and are refused.
func (h *heapqry) parse(b []byte) error {
	s := string(b)
	if len(s) > 127 {
		s = s[:127]
	}
	addr, rest := cstrtoul(s)
	if strings.HasPrefix(rest, "+") {
		_, rest = cstrtoul(rest[1:])
	}
	if !strings.HasPrefix(rest, ".") || len(rest) < 2 {
		return Error(abi.Ebadctl)
	}
	f := rest[1]
	if !strings.ContainsRune("WBVRP", rune(f)) {
		return Error(abi.Ebadctl)
	}
	count, _ := cstrtoul(rest[2:])
	*h = heapqry{set: true, fmt: f, addr: addr, count: count}
	return nil
}

// cstrtoul is C's strtoul with base 0.
func cstrtoul(s string) (uint64, string) {
	base := uint64(10)
	i := 0
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		base, i = 16, 2
	} else if strings.HasPrefix(s, "0") {
		base = 8
	}
	var v uint64
	for ; i < len(s); i++ {
		c := s[i]
		var d uint64
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint64(c-'A') + 10
		default:
			d = 99
		}
		if d >= base {
			break
		}
		v = v*base + d
	}
	return v, s[i:]
}

// read answers the query from the prog's linear memory. With no query
// written it reports the memory itself: its size in bytes, its limit
// in bytes (0 when the module sets none) and the wasm page size.
func (h *heapqry) read(p *Prog) (string, error) {
	m := p.guestmem()
	if !h.set {
		if m == nil {
			return "", nil
		}
		max, ok := m.Max()
		lim := uint64(0)
		if ok {
			lim = uint64(max) * 65536
		}
		return fmt.Sprintf("%11d %11d %11d memory\n", m.Size(), lim, 65536), nil
	}
	if m == nil {
		return "", Error(abi.Ebadaddr)
	}
	var b strings.Builder
	addr := h.addr
	for i := uint64(0); i < h.count; i++ {
		size := uint64(4)
		switch h.fmt {
		case 'B':
			size = 1
		case 'V', 'R':
			size = 8
		}
		if h.fmt != 'B' && addr&3 != 0 {
			return "", Error("misaligned address")
		}
		if addr+size > 1<<32 {
			return "", Error(abi.Ebadaddr)
		}
		v, ok := m.Read(uint32(addr), uint32(size))
		if !ok {
			return "", Error(abi.Ebadaddr)
		}
		switch h.fmt {
		case 'W':
			fmt.Fprintf(&b, "%d\n", int32(le32(v)))
		case 'B':
			fmt.Fprintf(&b, "%d\n", v[0])
		case 'V':
			fmt.Fprintf(&b, "%d\n", int64(le64(v)))
		case 'R':
			fmt.Fprintf(&b, "%g\n", math.Float64frombits(le64(v)))
		case 'P':
			if w := le32(v); w == 0 {
				b.WriteString("nil\n")
			} else {
				fmt.Fprintf(&b, "%x\n", w)
			}
		}
		addr += size
	}
	return b.String(), nil
}

func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }
