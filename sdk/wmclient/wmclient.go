// Package wmclient gives a program a window from the window manager,
// after Inferno's Wmclient module (wmclient(2)), with a title bar and
// border drawn here.
//
// The window manager (wm/wm) serves /mnt/wm. A Window is a connection
// to it: requests are written to its wmctl file and pointer, keyboard
// and control events come back on its event file, which stream(1)
// copies into a pipe so that Next can wait for it together with the
// program's own files: a Go guest is one thread, and purgatorio.wait
// cannot wait on a mounted file. With no window manager the window is
// the display's image, the whole screen or, in a rio window, the window
// (draw.Display.GetWindow), and Next reads /dev/pointer and
// /dev/keyboard; in a rio window, through stream(1) too.
//
//	w, err := wmclient.New(display, "Clock", wmclient.Appl)
//	w.Reshape(draw.Rect(0, 0, 100, 100))
//	w.Onscreen("place")
//	w.StartInput("kbd", "ptr")
//	for {
//		e, _ := w.Next(nil, 1000)
//		...
//	}
package wmclient

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Title bar buttons, as in wmclient.m.
const (
	Resize = 1 << iota
	Hide
	Help
	OK
	Popup
	Plain
	Appl = Resize | Hide
)

// Where the window manager serves its clients.
const Mntwm = "/mnt/wm"

// Event kinds.
const (
	Timeout = iota // Next's timeout passed
	Ptr            // a pointer event in the window's image
	Kbd            // a key
	Ctl            // a control message from the window manager
	Fd             // one of Next's fds is ready to read
	Reshape        // the window has a new image: redraw it
)

// Event is what Next returns.
type Event struct {
	Kind int
	Ptr  draw.Pointer
	Key  rune
	Ctl  string
	Fd   int
}

// Window is a window and its connection to the window manager.
type Window struct {
	Display *draw.Display
	// R is the whole window on the screen, title bar and border too.
	R draw.Rectangle
	// Image is the window's image; the program draws in ImageR.
	Image *draw.Image
	// Displayr is the screen's rectangle.
	Displayr draw.Rectangle
	Focused  bool
	Bd       int

	title    string
	buts     int
	tbh      int // title bar height
	tbr      draw.Rectangle
	wm       bool
	cdir     string // the window's directory in /mnt/wm
	ctlfd    int
	evfd     int
	ptrfd    int
	kbdfd    int
	buf      []byte
	queue    []Event
	iname    string
	ptrfocus int // 0 none, 1 image, 2 title bar
	buttons  int
	pressed  int // the title bar button held, or -1
	exiting  bool
	ownpgrp  bool  // in a rio window: made a process group of its own
	streams  []int // in a rio window: the streams copying input
}

// New connects to the window manager and makes a window titled title
// with the title bar buttons buts (Plain for no title bar). It has no
// image until Reshape and Onscreen.
func New(d *draw.Display, title string, buts int) (*Window, error) {
	w := &Window{Display: d, title: title, buts: buts, ctlfd: -1, evfd: -1, ptrfd: -1, kbdfd: -1, pressed: -1}
	w.Displayr = d.Image.R
	if fd, err := sys.Open(Mntwm+"/clone", sys.ORDWR); err == nil {
		if err := w.connect(fd); err != nil {
			syscall.Close(fd)
			return nil, err
		}
	} else {
		buts &^= Resize | Hide
		w.buts = buts
	}
	if buts&Plain == 0 {
		w.Bd = 1
		w.tbh = d.DefaultFont.Height + 4
	}
	return w, nil
}

// connect sets up the connection on an open clone file.
func (w *Window) connect(fd int) error {
	b := make([]byte, 32)
	n, err := syscall.Read(fd, b)
	if err != nil || n <= 0 {
		return fmt.Errorf("wmclient: cannot read window number: %v", err)
	}
	dir := Mntwm + "/" + strings.TrimSpace(string(b[:n]))
	p, err := sys.Pipe()
	if err != nil {
		return err
	}
	if _, err := sys.Spawn("/bin/stream.wasm", []string{"stream", dir + "/event"}, sys.NEWFD,
		[][2]int{{1, p[1]}, {2, 2}}); err != nil {
		syscall.Close(p[0])
		syscall.Close(p[1])
		return fmt.Errorf("wmclient: cannot start stream: %v", err)
	}
	syscall.Close(p[1])
	w.wm, w.ctlfd, w.evfd, w.cdir = true, fd, p[0], dir
	if rfd, err := sys.Open(Mntwm+"/wmrect", sys.OREAD); err == nil {
		n, _ := syscall.Read(rfd, b)
		syscall.Close(rfd)
		if r, ok := s2r(string(b[:max(n, 0)])); ok {
			w.Displayr = r
		}
	}
	return nil
}

func s2r(s string) (draw.Rectangle, bool) {
	f := strings.Fields(s)
	if len(f) < 4 {
		return draw.Rectangle{}, false
	}
	var v [4]int
	for i := range v {
		x, err := strconv.Atoi(f[i])
		if err != nil {
			return draw.Rectangle{}, false
		}
		v[i] = x
	}
	return draw.Rect(v[0], v[1], v[2], v[3]), true
}

func r2s(r draw.Rectangle) string {
	return fmt.Sprintf("%d %d %d %d", r.Min.X, r.Min.Y, r.Max.X, r.Max.Y)
}

// ScreenR returns the window rectangle that holds image rectangle r with
// the title bar and border added.
func (w *Window) ScreenR(r draw.Rectangle) draw.Rectangle {
	if w.tbh > 0 {
		r.Min.Y -= w.tbh
	}
	return r.Inset(-w.Bd)
}

// ImageR returns the part of window rectangle r left for the program.
func (w *Window) ImageR(r draw.Rectangle) draw.Rectangle {
	r = r.Inset(w.Bd)
	r.Min.Y += w.tbh
	if r.Dy() < 0 {
		r.Max.Y = r.Min.Y
	}
	if r.Dx() < 0 {
		r.Max.X = r.Min.X
	}
	return r
}

// Reshape asks for an image of rectangle r, not counting the title bar.
func (w *Window) Reshape(r draw.Rectangle) error {
	w.R = w.ScreenR(r)
	if w.Image == nil && w.wm {
		return nil
	}
	return w.Wmctl("!reshape . -1 " + r2s(w.R))
}

// Onscreen puts the window on the screen; how is place (the default),
// onscreen, exact or max.
func (w *Window) Onscreen(how string) error {
	if how == "" {
		how = "place"
	}
	return w.Wmctl("!reshape . -1 " + r2s(w.R) + " " + quote.Quote(how))
}

// StartInput starts the given inputs: ptr, kbd.
func (w *Window) StartInput(devs ...string) error {
	for _, d := range devs {
		if err := w.Wmctl("start " + quote.Quote(d)); err != nil {
			return err
		}
	}
	return nil
}

// SetTitle changes the title.
func (w *Window) SetTitle(t string) {
	w.title = t
	w.drawTitle()
}

// Wmctl handles a request or control message (Window.wmctl): exit ends
// the program, rect and haskbdfocus are noted, and the rest go to the
// window manager.
func (w *Window) Wmctl(req string) error {
	args := quote.Unquoted(req)
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "exit":
		Exit()
	case "rect":
		if r, ok := s2r(strings.Join(args[1:], " ")); ok {
			w.Displayr = r
		}
		return nil
	case "haskbdfocus":
		w.Focused = len(args) > 1 && args[1] != "0"
		w.drawTitle()
		return nil
	case "ok", "help":
		return nil
	}
	return w.wmreq(req)
}

// Exit ends the program and the progs it started. The prog's pid comes
// from pctl, as in Inferno: os.Getpid is a constant on wasip1.
func Exit() {
	pid, err := sys.Pctl(0, nil)
	if err != nil {
		sys.Exits("")
	}
	if fd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/ctl", sys.OWRITE); err == nil {
		_, _ = syscall.Write(fd, []byte("killgrp"))
	}
	sys.Exits("")
}

func (w *Window) wmreq(req string) error {
	if w.wm {
		if _, err := syscall.Write(w.ctlfd, []byte(req)); err != nil {
			return fmt.Errorf("%s", sys.Errstr())
		}
		if strings.HasPrefix(req, "!") {
			return w.attach()
		}
		return nil
	}
	// No window manager: the window is the screen.
	if strings.HasPrefix(req, "!") {
		w.putimage(w.Display.Image)
		return nil
	}
	if strings.HasPrefix(req, "start ") {
		return w.startlocal(strings.TrimSpace(req[6:]))
	}
	return nil
}

// startlocal opens the pointer or keyboard when there is no window
// manager.
func (w *Window) startlocal(dev string) error {
	var err error
	switch dev {
	case "ptr", "mouse":
		if w.ptrfd < 0 {
			w.ptrfd, err = w.input("/dev/pointer")
		}
	case "kbd":
		if w.kbdfd < 0 {
			w.kbdfd, err = w.input("/dev/keyboard")
			w.Focused = err == nil
		}
	}
	return err
}

// input opens a device for Next to wait on. In a rio window the
// pointer and keyboard are rio's files, through the mount driver, which
// purgatorio.wait cannot wait on, so stream(1) copies each into a pipe,
// as for the window manager's event file. The program then runs in a
// process group of its own, so that Exit's killgrp ends the streams and
// not the window's shell.
func (w *Window) input(file string) (int, error) {
	if !w.Display.Windowed() {
		return sys.Open(file, sys.OREAD)
	}
	if !w.ownpgrp {
		if _, err := sys.Pctl(sys.NEWPGRP, nil); err != nil {
			return -1, err
		}
		w.ownpgrp = true
	}
	// The file is opened here, so it is open when this returns; the
	// stream reads it as its fd 0.
	fd, err := sys.Open(file, sys.OREAD)
	if err != nil {
		return -1, err
	}
	defer syscall.Close(fd)
	p, err := sys.Pipe()
	if err != nil {
		return -1, err
	}
	pid, err := sys.Spawn("/bin/stream.wasm", []string{"stream", "#d/0"}, sys.NEWFD,
		[][2]int{{0, fd}, {1, p[1]}, {2, 2}})
	syscall.Close(p[1])
	if err != nil {
		syscall.Close(p[0])
		return -1, fmt.Errorf("wmclient: cannot start stream: %v", err)
	}
	w.streams = append(w.streams, pid)
	return p[0], nil
}

// attach takes the image the window manager has just made or moved.
func (w *Window) attach() error {
	b := make([]byte, 128)
	fd, err := sys.Open(w.cdir+"/winname", sys.OREAD)
	if err != nil {
		return err
	}
	n, err := syscall.Read(fd, b)
	syscall.Close(fd)
	if err != nil || n <= 0 {
		return errors.New("wmclient: cannot read winname")
	}
	name := string(b[:n])
	img, err := w.Display.NamedImage(name)
	if err != nil {
		return fmt.Errorf("wmclient: cannot get image %s: %v", name, err)
	}
	old := w.Image
	w.iname = name
	w.putimage(img)
	if old != nil && old != img {
		_ = old.Free()
	}
	return nil
}

// putimage installs a new image and draws the frame.
func (w *Window) putimage(i *draw.Image) {
	w.Image = i
	w.R = i.R
	w.tbr = draw.Rectangle{}
	if w.tbh > 0 {
		in := i.R.Inset(w.Bd)
		w.tbr = draw.Rect(in.Min.X, in.Min.Y, in.Max.X, in.Min.Y+w.tbh)
	}
	w.drawBorder()
	w.drawTitle()
	w.queue = append(w.queue, Event{Kind: Reshape})
}

// Title bar colours, from Inferno's titlebar and wmclient.
const (
	bdup      = 0xFFFFFFFF
	bddown    = 0xADADADFF
	tbfocus   = 0x0000CCFF
	tbunfocus = 0xAAAAAAFF
)

func (w *Window) colour(c uint32) *draw.Image {
	i, err := w.Display.Color(c)
	if err != nil {
		return w.Display.Black
	}
	return i
}

func (w *Window) drawBorder() {
	if w.Image == nil || w.Bd == 0 {
		return
	}
	i, r, b := w.Image, w.Image.R, w.Bd
	up, down := w.colour(bdup), w.colour(bddown)
	i.Draw(draw.Rect(r.Min.X, r.Min.Y, r.Min.X+b, r.Max.Y), up, nil, draw.ZP)
	i.Draw(draw.Rect(r.Min.X+b, r.Min.Y, r.Max.X, r.Min.Y+b), up, nil, draw.ZP)
	i.Draw(draw.Rect(r.Max.X-b, r.Min.Y+b, r.Max.X, r.Max.Y), down, nil, draw.ZP)
	i.Draw(draw.Rect(r.Min.X+b, r.Max.Y-b, r.Max.X-b, r.Max.Y), down, nil, draw.ZP)
	up.Free()
	down.Free()
}

// buttonsR returns the title bar buttons, rightmost first: exit, then
// size if the window has Resize.
func (w *Window) buttonsR() []draw.Rectangle {
	if w.tbr.Empty() {
		return nil
	}
	h := w.tbr.Dy() - 2
	var rs []draw.Rectangle
	x := w.tbr.Max.X - 1
	n := 1
	if w.buts&Resize != 0 {
		n = 2
	}
	for k := 0; k < n; k++ {
		rs = append(rs, draw.Rect(x-h, w.tbr.Min.Y+1, x, w.tbr.Min.Y+1+h))
		x -= h + 2
	}
	return rs
}

func (w *Window) drawTitle() {
	if w.Image == nil || w.tbr.Empty() {
		return
	}
	i := w.Image
	bg := w.colour(tbunfocus)
	if w.Focused {
		bg.Free()
		bg = w.colour(tbfocus)
	}
	i.Draw(w.tbr, bg, nil, draw.ZP)
	f := w.Display.DefaultFont
	_, _ = i.String(draw.Pt(w.tbr.Min.X+4, w.tbr.Min.Y+2), w.Display.White, draw.ZP, f, w.title)
	grey := w.colour(0xDDDDDDFF)
	for k, br := range w.buttonsR() {
		i.Draw(br, grey, nil, draw.ZP)
		i.Border(br, 1, w.Display.Black, draw.ZP)
		c := br.Min.Add(br.Max).Div(2)
		d := br.Dy()/2 - 2
		if k == 0 { // exit: a cross
			i.Line(c.Sub(draw.Pt(d, d)), c.Add(draw.Pt(d, d)), draw.Endsquare, draw.Endsquare, 0, w.Display.Black, draw.ZP)
			i.Line(c.Add(draw.Pt(-d, d)), c.Add(draw.Pt(d, -d)), draw.Endsquare, draw.Endsquare, 0, w.Display.Black, draw.ZP)
		} else { // size: a box
			i.Border(draw.Rect(c.X-d, c.Y-d, c.X+d+1, c.Y+d+1), 1, w.Display.Black, draw.ZP)
		}
	}
	grey.Free()
	bg.Free()
	_ = w.Display.Flush(true)
}

// Pointer handles a pointer event in the title bar (Window.pointer) and
// reports whether it did.
func (w *Window) Pointer(p draw.Pointer) bool {
	if w.Image == nil || w.tbr.Empty() {
		return false
	}
	if p.Buttons != 0 && (w.ptrfocus == 0 || w.buttons == 0) {
		w.ptrfocus = 1
		if p.XY.In(w.tbr) {
			w.ptrfocus = 2
		}
	}
	old := w.buttons
	w.buttons = p.Buttons
	if w.ptrfocus != 2 {
		return false
	}
	pressed := p.Buttons &^ old
	released := old &^ p.Buttons
	bs := w.buttonsR()
	if pressed&1 != 0 {
		w.pressed = -1
		for k, br := range bs {
			if p.XY.In(br) {
				w.pressed = k
			}
		}
		if w.pressed < 0 && w.wm {
			// Drag the window while the button is down.
			_ = w.Wmctl(fmt.Sprintf("!move . -1 %d %d", p.XY.X, p.XY.Y))
			w.buttons = 0
		}
	}
	if released&1 != 0 && w.pressed >= 0 {
		k := w.pressed
		w.pressed = -1
		if k < len(bs) && p.XY.In(bs[k]) {
			switch k {
			case 0:
				Exit()
			case 1:
				_ = w.Wmctl(fmt.Sprintf("!size . -1 %d %d", 50, w.tbh+2*w.Bd+10))
			}
		}
	}
	if p.Buttons == 0 {
		w.ptrfocus = 0
	}
	return true
}

// Next waits up to timeout milliseconds (forever if negative) for an
// event from the window manager or for one of fds to be readable, and
// returns it. Title bar events are handled here; control messages are
// handled by Wmctl and returned too.
func (w *Window) Next(fds []int, timeout int) (Event, error) {
	for {
		for len(w.queue) > 0 {
			e := w.queue[0]
			w.queue = w.queue[1:]
			switch e.Kind {
			case Ptr:
				if w.Pointer(e.Ptr) {
					continue
				}
			case Ctl:
				_ = w.Wmctl(e.Ctl)
			}
			return e, nil
		}
		var ops [][2]int
		var src []int
		for _, fd := range []int{w.evfd, w.ptrfd, w.kbdfd} {
			if fd >= 0 {
				ops = append(ops, [2]int{sys.WaitRead, fd})
				src = append(src, fd)
			}
		}
		for _, fd := range fds {
			ops = append(ops, [2]int{sys.WaitRead, fd})
		}
		i, err := sys.Wait(ops, timeout)
		if err != nil {
			return Event{}, err
		}
		if i < 0 {
			return Event{Kind: Timeout}, nil
		}
		if i >= len(src) {
			return Event{Kind: Fd, Fd: fds[i-len(src)]}, nil
		}
		switch src[i] {
		case w.evfd:
			if err := w.readEvents(); err != nil {
				return Event{}, err
			}
		case w.ptrfd:
			var b [draw.PointerSize]byte
			n, err := syscall.Read(w.ptrfd, b[:])
			if err == nil && n == len(b) && b[0] == 'r' {
				// A rio window has a new image.
				if ok, err := w.Display.GetWindow(); err != nil {
					return Event{}, err
				} else if ok {
					w.putimage(w.Display.Image)
				}
				b[0] = 'm'
			}
			if err == nil && n == len(b) {
				if p, ok := draw.ParsePointer(b[:]); ok {
					w.queue = append(w.queue, Event{Kind: Ptr, Ptr: p})
				}
			} else if w.Display.Windowed() && n <= 0 {
				// rio has gone.
				Exit()
			}
		case w.kbdfd:
			var b [64]byte
			n, err := syscall.Read(w.kbdfd, b[:])
			if err != nil || n <= 0 {
				return Event{}, errors.New("wmclient: keyboard: end of file")
			}
			w.buf = append(w.buf, b[:n]...)
			for len(w.buf) > 0 && utf8.FullRune(w.buf) {
				r, sz := utf8.DecodeRune(w.buf)
				w.buf = w.buf[sz:]
				w.queue = append(w.queue, Event{Kind: Kbd, Key: r})
			}
		}
	}
}

// readEvents reads lines from the event pipe.
func (w *Window) readEvents() error {
	b := make([]byte, 8192)
	n, err := syscall.Read(w.evfd, b)
	if err != nil || n <= 0 {
		// The window manager has gone.
		Exit()
	}
	w.buf = append(w.buf, b[:n]...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			return nil
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		switch {
		case strings.HasPrefix(line, "m"):
			if p, ok := draw.ParsePointer([]byte(line)); ok {
				w.queue = append(w.queue, Event{Kind: Ptr, Ptr: p})
			}
		case strings.HasPrefix(line, "k "):
			if k, err := strconv.Atoi(line[2:]); err == nil {
				w.queue = append(w.queue, Event{Kind: Kbd, Key: rune(k)})
			}
		case strings.HasPrefix(line, "c "):
			w.queue = append(w.queue, Event{Kind: Ctl, Ctl: line[2:]})
		}
	}
}

// Close takes the window away: with a window manager its connection is
// closed, which deletes the window; without one, the input it opened is
// closed.
func (w *Window) Close() {
	for _, fd := range []*int{&w.ctlfd, &w.evfd, &w.ptrfd, &w.kbdfd} {
		if *fd >= 0 {
			syscall.Close(*fd)
			*fd = -1
		}
	}
	for _, pid := range w.streams {
		if fd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/ctl", sys.OWRITE); err == nil {
			_, _ = syscall.Write(fd, []byte("kill"))
			syscall.Close(fd)
		}
	}
	w.streams = nil
	if w.Image != nil && w.wm {
		_ = w.Image.Free()
		w.Image = nil
	}
	w.wm = false
}
