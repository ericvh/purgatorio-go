package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/styx"
)

// #t, the serial ports (Inferno's deveia-posix.c and deveia-bsd.c): for
// port n, eian (data), eianctl and eianstatus, over the host's serial
// devices through termios. The host devices are Host.Eia, or the
// system's own (DefaultEiaPorts).

const (
	eiaData = 1 + iota
	eiaCtl
	eiaStat
)

// eiaport is one serial port. Its settings persist between a close and
// the next open, as in Inferno.
type eiaport struct {
	dev     string
	mu      sync.Mutex
	ref     int
	f       *os.File
	frame   int
	overrun int
	restore bool
	ts      unix.Termios
	dtr     int // 1 set, -1 cleared, 0 untouched
	rts     int
	cts     int
}

// DevEia is #t.
type DevEia struct {
	core.DevBase
	k     *core.Kernel
	mu    sync.Mutex
	ports []*eiaport
	perm  []uint32 // by file: 3 per port
}

// NewDevEia makes #t over the host devices devs; the ports are those
// up to the first that does not exist, as Inferno's eiainit finds them.
func NewDevEia(k *core.Kernel, devs []string) *DevEia {
	d := &DevEia{k: k}
	for _, dev := range devs {
		if _, err := os.Stat(dev); err != nil {
			break
		}
		d.ports = append(d.ports, &eiaport{dev: dev})
		d.perm = append(d.perm, 0o660, 0o660, 0o660)
	}
	return d
}

// EiaPorts is the port list from $EMU_EIA (host devices separated by
// commas or spaces) when it is set, otherwise the system's.
func EiaPorts() []string {
	if v, ok := os.LookupEnv("EMU_EIA"); ok {
		return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
	}
	return DefaultEiaPorts()
}

func (d *DevEia) DC() rune     { return 't' }
func (d *DevEia) Name() string { return "eia" }

func eiaqid(port, t int) uint64 { return uint64(port)<<4 | uint64(t) }
func eiaportno(q uint64) int    { return int(q >> 4) }
func eiatype(q uint64) int      { return int(q & 0xf) }

func (d *DevEia) Attach(context.Context, string) (core.Walked, error) {
	if len(d.ports) == 0 {
		return core.Walked{}, core.Error(abi.Enodev)
	}
	return core.Walked{Qid: styx.Qid{Type: styx.QTDIR}}, nil
}

func (d *DevEia) dirs() []styx.Dir {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []styx.Dir
	for i := range d.ports {
		for t, suf := range []string{"", "ctl", "status"} {
			out = append(out, styx.Dir{Type: 't', Qid: styx.Qid{Path: eiaqid(i, eiaData+t)},
				Mode: d.perm[3*i+t], Name: fmt.Sprintf("eia%d%s", i, suf), UID: d.k.Owner, GID: d.k.Owner})
		}
	}
	return out
}

func (d *DevEia) Walk(_ context.Context, c *core.Chan, name string) (core.Walked, error) {
	if !c.IsDir() {
		return core.Walked{}, core.Error(abi.Enotdir)
	}
	if name == ".." {
		return core.Walked{Qid: c.Qid}, nil
	}
	for _, dir := range d.dirs() {
		if dir.Name == name {
			return core.Walked{Qid: dir.Qid}, nil
		}
	}
	return core.Walked{}, core.Error(abi.Enonexist)
}

func (d *DevEia) Stat(_ context.Context, c *core.Chan) (styx.Dir, error) {
	if c.IsDir() {
		return styx.Dir{Type: 't', Qid: c.Qid, Mode: styx.DMDIR | 0o555, Name: "#t",
			UID: d.k.Owner, GID: d.k.Owner}, nil
	}
	for _, dir := range d.dirs() {
		if dir.Qid.Path == c.Qid.Path {
			return dir, nil
		}
	}
	return styx.Dir{}, core.Error(abi.Enonexist)
}

func (d *DevEia) DirRead(context.Context, *core.Chan) ([]styx.Dir, error) { return d.dirs(), nil }

func (d *DevEia) Wstat(ctx context.Context, c *core.Chan, dir *styx.Dir) error {
	if p := core.ProgFrom(ctx); p != nil && p.User() != d.k.Owner {
		return core.Error(abi.Eperm)
	}
	if c.IsDir() {
		return core.Error(abi.Eperm)
	}
	if !core.NullMode(dir) {
		q := c.Qid.Path
		d.mu.Lock()
		d.perm[3*eiaportno(q)+eiatype(q)-eiaData] = dir.Mode & 0o666
		d.mu.Unlock()
	}
	return nil
}

func (d *DevEia) Open(ctx context.Context, c *core.Chan, mode int) (*core.Chan, error) {
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, core.Error(abi.Eperm)
		}
		return c, nil
	}
	dir, err := d.Stat(ctx, c)
	if err != nil {
		return nil, err
	}
	if err := core.OpenPerm(dir.Mode, mode); err != nil {
		return nil, err
	}
	if err := d.ports[eiaportno(c.Qid.Path)].open(); err != nil {
		return nil, err
	}
	c.State = d.ports[eiaportno(c.Qid.Path)]
	return c, nil
}

// open counts an open of one of the port's files; the first opens the
// host device and makes its settings sane, or puts back the saved ones.
func (e *eiaport) open() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ref++
	if e.ref != 1 {
		return nil
	}
	f, err := os.OpenFile(e.dev, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		e.ref--
		return oserror(err)
	}
	err = withfd(f, func(fd int) error {
		ts, err := tcget(fd)
		if err != nil {
			return err
		}
		ts.Iflag, ts.Oflag, ts.Lflag = 0, 0, 0
		if e.restore {
			*ts = e.ts
		} else {
			setspeed(ts, 9600)
			ts.Iflag |= unix.IGNPAR
			ts.Cflag &^= unix.CSIZE
			ts.Cflag |= unix.CS8 | unix.CREAD
			ts.Cflag &^= unix.PARENB | unix.PARODD
			ts.Cc[unix.VMIN] = 1
			ts.Cc[unix.VTIME] = 0
		}
		if err := tcset(fd, ts, false); err != nil {
			return err
		}
		if e.restore {
			for _, l := range []struct {
				v   int
				bit int
			}{{e.dtr, unix.TIOCM_DTR}, {e.rts, unix.TIOCM_RTS}, {e.cts, unix.TIOCM_CTS}} {
				if l.v != 0 {
					_ = modemline(fd, l.bit, l.v > 0)
				}
			}
		}
		return nil
	})
	if err != nil {
		f.Close()
		e.ref--
		return oserror(err)
	}
	e.f = f
	return nil
}

func (d *DevEia) Close(c *core.Chan) {
	e, ok := c.State.(*eiaport)
	if !ok {
		return
	}
	e.mu.Lock()
	e.ref--
	if e.ref == 0 && e.f != nil {
		e.f.Close()
		e.f = nil
	}
	e.mu.Unlock()
}

func (e *eiaport) file() *os.File { e.mu.Lock(); defer e.mu.Unlock(); return e.f }

func (d *DevEia) Read(ctx context.Context, c *core.Chan, buf []byte, off int64) (int, error) {
	if c.IsDir() {
		return 0, core.Error(abi.Eperm)
	}
	port := eiaportno(c.Qid.Path)
	e := d.ports[port]
	switch eiatype(c.Qid.Path) {
	case eiaData:
		return e.read(ctx, buf)
	case eiaCtl:
		return core.ReadStr([]byte(fmt.Sprintf("%11d ", port)), buf, off), nil
	case eiaStat:
		s, err := e.status()
		if err != nil {
			return 0, err
		}
		return core.ReadStr([]byte(s), buf, off), nil
	}
	return 0, nil
}

// read reads the device. It blocks until a byte comes, but a kill or
// an interrupt ends it.
func (e *eiaport) read(ctx context.Context, buf []byte) (int, error) {
	f := e.file()
	if f == nil {
		return 0, core.Error(abi.Ehungup)
	}
	var (
		r    core.Rendez
		mu   sync.Mutex
		done bool
		n    int
		rerr error
	)
	go func() {
		m, err := f.Read(buf)
		mu.Lock()
		n, rerr, done = m, err, true
		mu.Unlock()
		r.Wakeup()
	}()
	isdone := func() bool { mu.Lock(); defer mu.Unlock(); return done }
	if err := core.Park(ctx, &r, isdone); err != nil {
		if f.SetReadDeadline(time.Now()) == nil {
			_ = core.ParkKillable(context.Background(), &r, isdone)
			_ = f.SetReadDeadline(time.Time{})
			mu.Lock()
			defer mu.Unlock()
			if n > 0 {
				return n, nil
			}
		}
		return 0, err
	}
	if rerr != nil && n == 0 {
		if errors.Is(rerr, os.ErrDeadlineExceeded) {
			return 0, core.Error(abi.Eintr)
		}
		return 0, oserror(rerr)
	}
	return n, nil
}

// status is the text of eiaNstatus.
func (e *eiaport) status() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.f == nil {
		return "", core.Error(abi.Ehungup)
	}
	var ts *unix.Termios
	if err := withfd(e.f, func(fd int) (err error) { ts, err = tcget(fd); return err }); err != nil {
		return "", oserror(err)
	}
	baud := "unknown"
	if b, ok := getspeed(ts); ok {
		baud = strconv.Itoa(b)
	}
	return fmt.Sprintf("opens %d ferr %d oerr %d baud %s\n", e.ref-1, e.frame, e.overrun, baud), nil
}

func (d *DevEia) Write(ctx context.Context, c *core.Chan, buf []byte, _ int64) (int, error) {
	if c.IsDir() {
		return 0, core.Error(abi.Eperm)
	}
	e := d.ports[eiaportno(c.Qid.Path)]
	switch eiatype(c.Qid.Path) {
	case eiaData:
		f := e.file()
		if f == nil {
			return 0, core.Error(abi.Ehungup)
		}
		n, err := f.Write(buf)
		if err != nil && n == 0 {
			return 0, oserror(err)
		}
		return n, nil
	case eiaCtl:
		n := len(buf)
		if n > 127 {
			n = 127
		}
		if err := e.ctl(string(buf[:n])); err != nil {
			return 0, err
		}
		return n, nil
	}
	return 0, nil
}

// ctl carries out the commands of a write to eiaNctl, as Inferno's
// wrctl does: each field is a letter and a number, and the settings are
// put to the device once all are read.
func (e *eiaport) ctl(cmd string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.f == nil {
		return core.Error(abi.Ehungup)
	}
	var cerr error
	if err := withfd(e.f, func(fd int) error { cerr = e.ctlfd(fd, cmd); return nil }); err != nil {
		return oserror(err)
	}
	return cerr
}

// ctlfd is ctl on the device's fd. Called with e.mu held.
func (e *eiaport) ctlfd(fd int, cmd string) error {
	ts, err := tcget(fd)
	if err != nil {
		return oserror(err)
	}
	for _, f := range strings.Fields(cmd) {
		if strings.HasPrefix(f, "break") {
			_ = sendbreak(fd)
			continue
		}
		n := atoi(f[1:])
		switch f[0] {
		case 'F', 'f':
			if err := flushout(fd); err != nil {
				return oserror(err)
			}
		case 'K', 'k':
			_ = sendbreak(fd)
		case 'H', 'h':
			setspeed(ts, 0)
		case 'B', 'b':
			if !setspeed(ts, n) {
				return core.Error(abi.Ebadarg)
			}
		case 'L', 'l':
			if n < 5 || n > 8 {
				return core.Error(abi.Ebadarg)
			}
			ts.Cflag &^= unix.CSIZE
			switch n {
			case 5:
				ts.Cflag |= unix.CS5
			case 6:
				ts.Cflag |= unix.CS6
			case 7:
				ts.Cflag |= unix.CS7
			case 8:
				ts.Cflag |= unix.CS8
			}
		case 'S', 's':
			switch n {
			case 1:
				ts.Cflag &^= unix.CSTOPB
			case 2:
				ts.Cflag |= unix.CSTOPB
			default:
				return core.Error(abi.Ebadarg)
			}
		case 'P', 'p':
			switch {
			case len(f) > 1 && f[1] == 'o':
				ts.Cflag |= unix.PARENB | unix.PARODD
			case len(f) > 1 && f[1] == 'e':
				ts.Cflag |= unix.PARENB
				ts.Cflag &^= unix.PARODD
			default:
				ts.Cflag &^= unix.PARENB
			}
		case 'X', 'x':
			if n == 0 {
				ts.Iflag &^= unix.IXON | unix.IXOFF
			} else {
				ts.Iflag |= unix.IXON | unix.IXOFF
			}
		case 'I', 'i':
			// enable the fifo: nothing to do
		case 'D', 'd', 'R', 'r', 'M', 'm':
			bit, l := unix.TIOCM_DTR, &e.dtr
			switch f[0] {
			case 'R', 'r':
				bit, l = unix.TIOCM_RTS, &e.rts
			case 'M', 'm':
				bit, l = unix.TIOCM_CTS, &e.cts
			}
			if err := modemline(fd, bit, n != 0); err != nil {
				return oserror(err)
			}
			e.restore = true
			*l = 1
			if n == 0 {
				*l = -1
			}
		}
		// Other letters (c, e, n, q among them) are ignored, as
		// Inferno's wrctl ignores them.
	}
	if err := tcset(fd, ts, true); err != nil {
		return oserror(err)
	}
	e.restore = true
	e.ts = *ts
	return nil
}

// withfd runs fn on f's descriptor without taking f out of the
// poller, as Fd would, so that a read stays interruptible.
func withfd(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

// modemline sets or clears a modem control line.
func modemline(fd, bit int, on bool) error {
	if on {
		return unix.IoctlSetPointerInt(fd, unix.TIOCMBIS, bit)
	}
	return unix.IoctlSetPointerInt(fd, unix.TIOCMBIC, bit)
}

// atoi is C's atoi: leading digits, 0 if none.
func atoi(s string) int {
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}
