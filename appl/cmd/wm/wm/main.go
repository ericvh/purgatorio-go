// Command wm is the window manager (Inferno wm(1)): it shares the screen
// among client programs, each with windows on a screen of its own on
// the display, and gives them the pointer and keyboard.
//
//	wm/wm [command [arg ...]]
//
// It runs command (wm/sh by default) and serves its clients a file tree
// on /mnt/wm, after Inferno's wmexport:
//
//	/mnt/wm/clone      opening it makes a client; the first read gives
//	                   its number n, writes are requests (wm(1))
//	/mnt/wm/wmrect     the screen's rectangle
//	/mnt/wm/n/wmctl    requests, and reads of control messages
//	/mnt/wm/n/pointer  pointer events, as /dev/pointer
//	/mnt/wm/n/keyboard keys, as /dev/keyboard
//	/mnt/wm/n/winname  the name of the window image after a ! request,
//	                   for Display.NamedImage
//	/mnt/wm/n/event    all of the above as lines: "m" and a pointer
//	                   event, "k" and a key's code, "c" and a control
//	                   message
//
// A Go guest cannot wait for several mounted files at once, so sdk/wmclient
// copies the event file into a pipe with stream(1) and waits on that.
// The requests are wm(1)'s: start, key, ptr, cursor, !reshape, delete,
// raise, lower, !move, !size, fixedorigin, kbdfocus and the
// controller's request, ctl and endcontrol; and exit, which closes the
// window manager.
package main

import (
	"fmt"
	"os"
	"syscall"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	mntwm      = "/mnt/wm"
	background = 0x777777FF
	srvfd      = 5 // the pipe's end in the mounting helper
)

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "wm: %s\n", s)
	sys.Exits("fail:error")
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "-M" {
		mounter(args[1:])
		return
	}
	if len(args) == 0 {
		args = []string{"wm/sh"}
	}
	// As wm.b: wm and its clients are a process group of their own, so
	// a client's killgrp on exit stays within it.
	sys.Pctl(sys.NEWPGRP|sys.FORKNS, nil)
	w, err := newWM()
	if err != nil {
		fatal(err.Error())
	}
	// A helper prog mounts our end of the pipe, which needs us to
	// answer, and then runs the command; we share its name space.
	_ = os.MkdirAll(mntwm, 0o755)
	p, err := sys.Pipe()
	if err != nil {
		fatal(fmt.Sprintf("pipe: %v", err))
	}
	if _, err := sys.Spawn("/bin/wm/wm.wasm", append([]string{"wm", "-M"}, args...), sys.NEWFD,
		[][2]int{{0, 0}, {1, 1}, {2, 2}, {srvfd, p[0]}}); err != nil {
		fatal(fmt.Sprintf("cannot run command: %v", err))
	}
	syscall.Close(p[0])
	w.serve(p[1])
}

// mounter mounts the window manager's file tree and runs the command.
func mounter(args []string) {
	if _, err := sys.Mount(srvfd, -1, mntwm, sys.MREPL, ""); err != nil {
		fatal(fmt.Sprintf("cannot mount %s: %v", mntwm, err))
	}
	syscall.Close(srvfd)
	if len(args) == 0 {
		return
	}
	err := sys.Exec("/bin/sh.wasm", append([]string{"sh", "-c", "$*"}, args...))
	fatal(fmt.Sprintf("cannot run command: %v", err))
}

// wm is the window manager's state.
type wm struct {
	d        *draw.Display
	screen   *draw.Screen
	ptrfd    int
	kbdfd    int
	reffd    int // the draw connection's refresh file, or -1
	kbdbuf   []byte
	clients  []*client // in z order, top first
	nextid   int
	ptrfocus *client
	kbdfocus *client
	control  *client
	allow    bool // the next client may become the controller
	buttons  int
	last     draw.Pointer
	lastrect draw.Rectangle
	imgseq   int
	srv      *server
	quit     bool
	menu     *wmmenu // the menu, while it is up
}

func newWM() (*wm, error) {
	d, err := draw.Init("")
	if err != nil {
		return nil, err
	}
	w := &wm{d: d, allow: true, nextid: 1}
	fill, err := d.Color(background)
	if err != nil {
		return nil, err
	}
	if w.screen, err = d.Image.AllocScreen(fill, true); err != nil {
		return nil, fmt.Errorf("cannot allocate screen: %v", err)
	}
	d.Image.Draw(d.Image.R, fill, nil, draw.ZP)
	if err := d.Flush(true); err != nil {
		return nil, err
	}
	if w.ptrfd, err = sys.Open("/dev/pointer", sys.OREAD); err != nil {
		return nil, fmt.Errorf("cannot open /dev/pointer: %v", err)
	}
	if w.kbdfd, err = sys.Open("/dev/keyboard", sys.OREAD); err != nil {
		return nil, fmt.Errorf("cannot open /dev/keyboard: %v", err)
	}
	// The draw device reports a change of the screen's size (a host
	// window resized) on the refresh file.
	if w.reffd, err = sys.Open(fmt.Sprintf("/dev/draw/%d/refresh", d.Dirno), sys.OREAD); err != nil {
		w.reffd = -1
	}
	return w, nil
}

// serve runs the window manager until it is told to exit or its
// clients' file tree goes.
func (w *wm) serve(fd int) {
	w.srv = newServer(w, fd)
	for !w.quit {
		ops := [][2]int{{sys.WaitRead, fd}, {sys.WaitRead, w.ptrfd}, {sys.WaitRead, w.kbdfd}}
		if w.reffd >= 0 {
			ops = append(ops, [2]int{sys.WaitRead, w.reffd})
		}
		i, err := sys.Wait(ops, -1)
		if err != nil {
			fatal(fmt.Sprintf("wait: %v", err))
		}
		switch i {
		case 0:
			if !w.srv.one() {
				return
			}
		case 1:
			if p, ok := w.readPointer(); ok {
				w.pointer(p)
			}
		case 2:
			w.keyboard()
		case 3:
			w.refresh()
		}
		w.srv.deliver()
		_ = w.d.Flush(true)
	}
}

// refresh reads the refresh file. The device fills what a larger
// screen exposes with the screen's fill and keeps the windows, so a
// resize only needs the screen's new rectangle, which wm's requests and
// menu use from then on.
func (w *wm) refresh() {
	var b [20 * 16]byte
	n, err := syscall.Read(w.reffd, b[:])
	if err != nil {
		syscall.Close(w.reffd)
		w.reffd = -1
		return
	}
	for i := 0; i+20 <= n; i += 20 {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 0 {
			_, _ = w.d.CheckResize()
			return
		}
	}
}

// readPointer reads the next event from /dev/pointer.
func (w *wm) readPointer() (draw.Pointer, bool) {
	var b [draw.PointerSize]byte
	n, err := syscall.Read(w.ptrfd, b[:])
	if err != nil || n != len(b) {
		return draw.Pointer{}, false
	}
	p, ok := draw.ParsePointer(b[:])
	if ok {
		w.last = p
	}
	return p, ok
}

// pointer routes a pointer event, as wm.b does: a press picks the
// client under it, which is raised and given the keyboard; events go to
// that client while it has started pointer input.
func (w *wm) pointer(p draw.Pointer) {
	if w.menu != nil {
		w.menuPointer(p)
		return
	}
	if p.Buttons != 0 && (w.ptrfocus == nil || w.buttons == 0) {
		if c := w.find(p.XY); c != nil {
			w.ptrfocus = c
			w.top(c)
			w.setfocus(c)
		} else {
			// The background: the menu.
			w.ptrfocus = nil
			w.buttons = 0
			w.showMenu(p.XY)
			return
		}
	}
	if c := w.ptrfocus; c != nil && c.flags&ptrStarted != 0 {
		w.buttons = p.Buttons
		c.putptr(p)
		return
	}
	w.buttons = 0
}

// keyboard reads keys and gives them to the client with the focus.
func (w *wm) keyboard() {
	var b [64]byte
	n, err := syscall.Read(w.kbdfd, b[:])
	if err != nil || n <= 0 {
		return
	}
	w.kbdbuf = append(w.kbdbuf, b[:n]...)
	for len(w.kbdbuf) > 0 && utf8.FullRune(w.kbdbuf) {
		r, sz := utf8.DecodeRune(w.kbdbuf)
		w.kbdbuf = w.kbdbuf[sz:]
		w.key(r)
	}
}

func (w *wm) key(r rune) {
	if c := w.kbdfocus; c != nil {
		c.putkey(r)
	}
}

// find returns the topmost client with a window at p.
func (w *wm) find(p draw.Point) *client {
	for _, c := range w.clients {
		for _, win := range c.wins {
			if p.In(win.r) {
				return c
			}
		}
	}
	return nil
}

// setfocus gives the keyboard to c (wm.b's setfocus).
func (w *wm) setfocus(c *client) {
	old := w.kbdfocus
	if old == c {
		return
	}
	if c != nil && c.flags&kbdStarted == 0 {
		return
	}
	if old != nil {
		old.putctl("haskbdfocus 0")
	}
	w.kbdfocus = c
	if c != nil {
		c.putctl("haskbdfocus 1")
	}
}

// top raises c's windows above the others.
func (w *wm) top(c *client) {
	w.unlink(c)
	w.clients = append([]*client{c}, w.clients...)
	if imgs := c.images(); len(imgs) > 0 {
		_ = w.screen.Top(imgs...)
	}
}

func (w *wm) bottom(c *client) {
	w.unlink(c)
	w.clients = append(w.clients, c)
	if imgs := c.images(); len(imgs) > 0 {
		_ = w.screen.Bottom(imgs...)
	}
}

func (w *wm) unlink(c *client) {
	for i, x := range w.clients {
		if x == c {
			w.clients = append(w.clients[:i], w.clients[i+1:]...)
			return
		}
	}
}

// controlevent tells the controller, if it asked, about other clients.
func (w *wm) controlevent(e string) {
	if c := w.control; c != nil && c.flags&controlStarted != 0 {
		c.putctl(e)
	}
}

// exit closes the window manager: every client is told to exit.
func (w *wm) exit() {
	for _, c := range w.clients {
		c.putctl("exit")
	}
	w.quit = true
}

func r2s(r draw.Rectangle) string {
	return fmt.Sprintf("%d %d %d %d", r.Min.X, r.Min.Y, r.Max.X, r.Max.Y)
}
