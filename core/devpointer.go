package core

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/styx"
)

// The pointer device #m, after Inferno's emu/port/devpointer.c: reads of
// pointer return the next pointer event, with button changes queued so
// that no click is lost.

const (
	qptrdir = iota
	qpointer
	qcursor
)

var pointertab = []Tab{
	{"pointer", qpointer, 0o666, 0},
	{"cursor", qcursor, 0o222, 0},
}

const nevent = 16

type pointerState struct {
	X, Y, B int
	Msec    uint32
}

// DevPointer is #m.
type DevPointer struct {
	DevBase
	k *Kernel

	once sync.Once
	mu   sync.Mutex
	v    pointerState
	ref  int // opens of pointer
	// ptrq: button changes, and a count of all events.
	clicks   [nevent]pointerState
	rd, wr   int
	full     bool
	put, get int
	r        Rendez
}

func NewDevPointer(k *Kernel) *DevPointer { return &DevPointer{k: k} }

func (d *DevPointer) DC() rune     { return 'm' }
func (d *DevPointer) Name() string { return "pointer" }

func (d *DevPointer) screen() platform.Screen {
	if dp, ok := d.k.Platform.(platform.Displayer); ok {
		return dp.Display()
	}
	return nil
}

// start runs a kproc that feeds the platform's pointer events to Track.
func (d *DevPointer) start() {
	d.once.Do(func() {
		p, ok := d.screen().(platform.Pointerer)
		if !ok {
			return
		}
		ch := p.Pointer()
		if ch == nil {
			return
		}
		go func() {
			for e := range ch {
				d.Track(e.Buttons, e.X, e.Y, false)
			}
		}()
	})
}

// Track records a pointer event (mousetrack).
func (d *DevPointer) Track(b, x, y int, isdelta bool) {
	d.mu.Lock()
	if isdelta {
		x += d.v.X
		y += d.v.Y
	}
	lastb := d.v.B
	d.v = pointerState{X: x, Y: y, B: b, Msec: uint32(d.k.Platform.Now() / 1e6)}
	if !d.full && lastb != b {
		d.clicks[d.wr] = d.v
		d.wr = (d.wr + 1) % nevent
		if d.wr == d.rd {
			d.full = true
		}
	}
	d.put++
	d.mu.Unlock()
	d.r.Wakeup()
}

func (d *DevPointer) Attach(context.Context, string) (Walked, error) {
	d.start()
	return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: qptrdir}}, nil
}

func (d *DevPointer) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	if c.Qid.Path != qptrdir {
		return Walked{}, Error(abi.Enotdir)
	}
	return TabWalk(pointertab, name)
}

func (d *DevPointer) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	if c.Qid.Path == qptrdir {
		return TabDir('m', Tab{".", qptrdir, styx.DMDIR | 0o555, 0}, d.k.Owner), nil
	}
	for _, t := range pointertab {
		if t.Path == c.Qid.Path {
			return TabDir('m', t, d.k.Owner), nil
		}
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevPointer) DirRead(context.Context, *Chan) ([]styx.Dir, error) {
	var out []styx.Dir
	for _, t := range pointertab {
		out = append(out, TabDir('m', t, d.k.Owner))
	}
	return out, nil
}

func (d *DevPointer) Open(_ context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		return c, nil
	}
	for _, t := range pointertab {
		if t.Path == c.Qid.Path {
			if err := OpenPerm(t.Perm, mode); err != nil {
				return nil, err
			}
		}
	}
	if c.Qid.Path == qpointer {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.ref != 0 {
			return nil, Error(abi.Einuse)
		}
		d.ref++
	}
	return c, nil
}

func (d *DevPointer) Close(c *Chan) {
	if c.Qid.Path == qpointer {
		d.mu.Lock()
		d.ref--
		d.mu.Unlock()
	}
}

func (d *DevPointer) Read(ctx context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	if c.Qid.Path != qpointer {
		return 0, nil
	}
	// Wait for an event (mouseconsume).
	err := Park(ctx, &d.r, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.full || d.put != d.get
	})
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	d.full = false
	d.get = d.put
	e := d.v
	if d.rd != d.wr {
		e = d.clicks[d.rd]
		d.rd = (d.rd + 1) % nevent
	}
	d.mu.Unlock()
	s := fmt.Sprintf("m%11d %11d %11d %11d ", e.X, e.Y, e.B, e.Msec)
	return copy(buf, s), nil
}

// strtoul parses a number as C's strtoul with base 0 does and returns
// the rest of s.
func strtoul(s string) (int, string) {
	s = strings.TrimLeft(s, " \t\n")
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == 'x' || s[i] == 'X' ||
		s[i] >= 'a' && s[i] <= 'f' || s[i] >= 'A' && s[i] <= 'F') {
		i++
	}
	v, err := strconv.ParseInt(s[:i], 0, 64)
	if err != nil {
		return 0, s
	}
	return int(v), s[i:]
}

func (d *DevPointer) Write(_ context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	n := len(buf)
	switch c.Qid.Path {
	case qpointer:
		if n > 127 {
			n = 127
		}
		s := string(buf[:n])
		if len(s) < 1 {
			return 0, Error(abi.Eshort)
		}
		x, rest := strtoul(s[1:])
		if rest == "" {
			return 0, Error(abi.Eshort)
		}
		y, _ := strtoul(rest)
		if ps, ok := d.screen().(platform.PointerSetter); ok {
			ps.SetPointer(x, y)
		}
		return n, nil
	case qcursor:
		ps, _ := d.screen().(platform.PointerSetter)
		if n == 0 {
			if ps != nil {
				ps.SetCursor(0, 0, 0, 0, nil, nil)
			}
			return 0, nil
		}
		if n < 8 {
			return 0, Error(abi.Eshort)
		}
		if n < 16 {
			return 0, Error(abi.Ebadarg)
		}
		hotx := int(int32(binary.LittleEndian.Uint32(buf)))
		hoty := int(int32(binary.LittleEndian.Uint32(buf[4:])))
		dx := int(int32(binary.LittleEndian.Uint32(buf[8:])))
		dy := int(int32(binary.LittleEndian.Uint32(buf[12:])))
		if dx%8 != 0 || dy%2 != 0 || dx < 0 || dy < 0 || n-16 != dx/8*dy {
			return 0, Error(abi.Ebadarg)
		}
		half := dx / 8 * dy / 2
		if ps != nil {
			ps.SetCursor(hotx, hoty, dx, dy, buf[16:16+half], buf[16+half:])
		}
		return n, nil
	}
	return 0, Error(abi.Ebadusefd)
}
