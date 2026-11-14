package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	qconsdir = iota
	qcons
	qconsctl
	qdrivers
	qhostowner
	qjit
	qkeyboard
	qkprint
	qmemory
	qmsec
	qnotquiterandom
	qnull
	qrandom
	qsysctl
	qsysname
	qtime
	quser
)

// The files of #c, in Inferno's order.
var contab = []Tab{
	{"cons", qcons, 0o666, 0},
	{"consctl", qconsctl, 0o222, 0},
	{"drivers", qdrivers, 0o444, 0},
	{"hostowner", qhostowner, 0o644, 0},
	{"jit", qjit, 0o666, 0},
	{"keyboard", qkeyboard, 0o666, 0},
	{"kprint", qkprint, 0o444, 0},
	{"memory", qmemory, 0o444, 0},
	{"msec", qmsec, 0o444, 12},
	{"notquiterandom", qnotquiterandom, 0o444, 0},
	{"null", qnull, 0o666, 0},
	{"random", qrandom, 0o444, 0},
	{"sysctl", qsysctl, 0o644, 0},
	{"sysname", qsysname, 0o644, 0},
	{"time", qtime, 0o644, 0},
	{"user", quser, 0o644, 0},
}

// Version is what #c/sysctl reads.
const Version = "Purgatorio-Go 0.1 (Go)\n"

// DevCons is #c, the console device (Inferno's devcons.c). Input from the
// platform console is read by a kproc into a queue and cooked into lines
// here, as Inferno's kbd queue is: backspace, ^U (kill line) and ^D (send
// the line as it is) are handled unless rawon has been written to
// consctl. The keyboard file is Inferno's gkbdq, a separate queue: the
// screen's keys (a platform.Keyboarder) and writes to keyboard go there,
// and the console's input does not.
type DevCons struct {
	DevBase
	k       *Kernel
	start   time.Time
	drivers string

	once   sync.Once
	kbdq   *Queue
	gonce  sync.Once
	gkbdq  *Queue
	lineq  *Queue
	rd     sync.Mutex
	raw    bool
	line   []byte
	kprint *Queue

	mu         sync.Mutex
	sysname    string
	jit        int
	timeoffset int64
}

func NewDevCons(k *Kernel) *DevCons {
	return &DevCons{k: k, start: time.Now(), kbdq: NewQueue(4096), gkbdq: NewQueue(4096),
		lineq: NewMsgQueue(64 * 1024)}
}

func (d *DevCons) DC() rune     { return 'c' }
func (d *DevCons) Name() string { return "cons" }

func (d *DevCons) Init(t *DevTab) { d.drivers = t.Drivers() }

func (d *DevCons) plat() platform.Platform { return d.k.Platform }

// startKbd starts the kproc that reads the host console into kbdq.
func (d *DevCons) startKbd() {
	d.once.Do(func() {
		go func() {
			buf := make([]byte, 512)
			for {
				n, err := d.plat().Console().Read(buf)
				if n > 0 {
					_, _ = d.kbdq.Write(context.Background(), buf[:n])
				}
				if err != nil || n == 0 {
					d.kbdq.Hangup()
					return
				}
			}
		}()
	})
}

// startGkbd starts the kproc that copies the screen's keys, if it has
// a keyboard, into gkbdq as UTF-8.
func (d *DevCons) startGkbd() {
	d.gonce.Do(func() {
		dp, ok := d.plat().(platform.Displayer)
		if !ok {
			return
		}
		kb, ok := dp.Display().(platform.Keyboarder)
		if !ok {
			return
		}
		ch := kb.Keyboard()
		if ch == nil {
			return
		}
		go func() {
			for r := range ch {
				_, _ = d.gkbdq.Write(context.Background(), []byte(string(r)))
			}
		}()
	})
}

func (d *DevCons) Attach(context.Context, string) (Walked, error) {
	return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: qconsdir}}, nil
}

func (d *DevCons) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	if c.Qid.Path != qconsdir {
		return Walked{}, Error(abi.Enotdir)
	}
	return TabWalk(contab, name)
}

func (d *DevCons) owner() string { return d.k.Owner }

func (d *DevCons) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	if c.Qid.Path == qconsdir {
		return TabDir('c', Tab{".", qconsdir, styx.DMDIR | 0o555, 0}, d.owner()), nil
	}
	for _, t := range contab {
		if t.Path == c.Qid.Path {
			return TabDir('c', t, d.owner()), nil
		}
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevCons) DirRead(context.Context, *Chan) ([]styx.Dir, error) {
	out := make([]styx.Dir, len(contab))
	for i, t := range contab {
		out[i] = TabDir('c', t, d.owner())
	}
	return out, nil
}

func (d *DevCons) Open(_ context.Context, c *Chan, mode int) (*Chan, error) {
	if c.Qid.Path == qconsdir {
		return c, nil
	}
	for _, t := range contab {
		if t.Path == c.Qid.Path {
			if err := OpenPerm(t.Perm, mode); err != nil {
				return nil, err
			}
		}
	}
	if c.Qid.Path == qkprint {
		d.mu.Lock()
		if d.kprint == nil {
			d.kprint = NewQueue(64 * 1024)
		}
		d.mu.Unlock()
	}
	return c, nil
}

func (d *DevCons) Close(c *Chan) {
	if c.Qid.Path == qkprint {
		d.mu.Lock()
		if d.kprint != nil {
			d.kprint.Hangup()
			d.kprint = nil
		}
		d.mu.Unlock()
	}
}

func (d *DevCons) user(ctx context.Context) string {
	if p := ProgFrom(ctx); p != nil {
		return p.User()
	}
	return d.owner()
}

func (d *DevCons) Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	str := func(s string) (int, error) { return ReadStr([]byte(s), buf, off), nil }
	switch c.Qid.Path {
	case qsysctl:
		return str(Version)
	case qsysname:
		d.mu.Lock()
		s := d.sysname
		d.mu.Unlock()
		if s == "" {
			s = d.plat().Sysname()
		}
		return str(s)
	case qrandom, qnotquiterandom:
		d.plat().Entropy(buf)
		return len(buf), nil
	case qhostowner:
		return str(d.owner())
	case quser:
		return str(d.user(ctx))
	case qjit:
		d.mu.Lock()
		j := d.jit
		d.mu.Unlock()
		return str(strconv.Itoa(j))
	case qtime:
		d.mu.Lock()
		o := d.timeoffset
		d.mu.Unlock()
		return str(strconv.FormatInt(d.plat().Now()/1000+o, 10))
	case qdrivers:
		return str(d.drivers)
	case qmemory:
		return str(memoryText())
	case qnull:
		return 0, nil
	case qmsec:
		return str(fmt.Sprintf("%11d ", time.Since(d.start).Milliseconds()))
	case qcons:
		return d.readCons(ctx, buf)
	case qkeyboard:
		d.startGkbd()
		return d.gkbdq.Read(ctx, buf)
	case qkprint:
		d.mu.Lock()
		q := d.kprint
		d.mu.Unlock()
		if q == nil {
			return 0, nil
		}
		return q.Read(ctx, buf)
	}
	return 0, Error(abi.Eperm)
}

// readCons cooks keyboard input into lines, as Inferno's consread does.
func (d *DevCons) readCons(ctx context.Context, buf []byte) (int, error) {
	d.startKbd()
	d.rd.Lock()
	defer d.rd.Unlock()
	var ch [1]byte
	for !d.lineq.Readable() || d.lineq.Len() == 0 {
		n, err := d.kbdq.Read(ctx, ch[:])
		if err != nil {
			return 0, err
		}
		if n == 0 { // host end of file
			if len(d.line) > 0 {
				_, _ = d.lineq.Write(ctx, d.line)
				d.line = d.line[:0]
				break
			}
			return 0, nil
		}
		send := false
		c := ch[0]
		switch {
		case c == 0:
			send = len(d.line) > 0 && !d.kbdq.Readable()
		case d.raw:
			d.line = append(d.line, c)
			send = d.kbdq.Len() == 0
		case c == '\b':
			if len(d.line) > 0 {
				d.line = d.line[:len(d.line)-1]
			}
		case c == 0x15:
			d.line = d.line[:0]
		case c == 0x04:
			send = true
		case c == '\n':
			send = true
			d.line = append(d.line, c)
		default:
			d.line = append(d.line, c)
		}
		if send || len(d.line) >= 1024 {
			_, _ = d.lineq.Write(ctx, d.line)
			d.line = d.line[:0]
		}
	}
	return d.lineq.Read(ctx, buf)
}

func (d *DevCons) Write(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	arg := func() string { return strings.TrimSuffix(string(buf), "\n") }
	switch c.Qid.Path {
	case qcons:
		d.mu.Lock()
		q := d.kprint
		d.mu.Unlock()
		if q != nil {
			return q.Write(ctx, buf)
		}
		return d.plat().Console().Write(buf)
	case qsysctl:
		return len(buf), nil
	case qconsctl:
		for _, a := range strings.Fields(string(buf)) {
			switch {
			case strings.HasPrefix(a, "rawon"):
				d.raw = true
				_ = d.plat().Console().SetRaw(true)
				_, _ = d.kbdq.Write(ctx, []byte{0})
			case strings.HasPrefix(a, "rawoff"):
				d.raw = false
				_ = d.plat().Console().SetRaw(false)
			}
		}
		return len(buf), nil
	case qkeyboard:
		_, err := d.gkbdq.Write(ctx, buf)
		return len(buf), err
	case qnull:
		return len(buf), nil
	case qtime:
		v, err := strconv.ParseInt(strings.TrimSpace(string(buf)), 0, 64)
		if err != nil {
			return 0, Error(abi.Ebadarg)
		}
		d.mu.Lock()
		d.timeoffset = v - d.plat().Now()/1000
		d.mu.Unlock()
		return len(buf), nil
	case qhostowner, quser:
		p := ProgFrom(ctx)
		if p == nil || p.User() != d.owner() {
			return 0, Error(abi.Eperm)
		}
		if off != 0 || len(buf) == 0 || len(buf) >= 128 {
			return 0, Error(abi.Ebadarg)
		}
		name := arg()
		if name == "" {
			return 0, Error(abi.Ebadarg)
		}
		if c.Qid.Path == qhostowner {
			d.k.Owner = name
		}
		p.SetUser(name)
		return len(buf), nil
	case qjit:
		x, err := strconv.Atoi(strings.TrimSpace(string(buf)))
		if err != nil || x < 0 || x > 9 {
			return 0, Error(abi.Ebadarg)
		}
		d.mu.Lock()
		d.jit = x
		d.mu.Unlock()
		return len(buf), nil
	case qsysname:
		if off != 0 || len(buf) == 0 || len(buf) >= 128 {
			return 0, Error(abi.Ebadarg)
		}
		d.mu.Lock()
		d.sysname = arg()
		d.mu.Unlock()
		return len(buf), nil
	}
	return 0, Error(abi.Eperm)
}

func (d *DevCons) Ready(c *Chan, write bool) bool {
	if write {
		return true
	}
	switch c.Qid.Path {
	case qcons:
		return d.lineq.Len() > 0 || d.kbdq.Readable()
	case qkeyboard:
		return d.gkbdq.Readable()
	}
	return true
}

func (d *DevCons) Watch(c *Chan, write bool, r *Rendez) {
	switch c.Qid.Path {
	case qcons:
		d.startKbd()
		d.kbdq.Watch(r)
		d.lineq.Watch(r)
	case qkeyboard:
		d.startGkbd()
		d.gkbdq.Watch(r)
	}
}

func (d *DevCons) Unwatch(c *Chan, r *Rendez) {
	d.kbdq.Unwatch(r)
	d.lineq.Unwatch(r)
	d.gkbdq.Unwatch(r)
}
