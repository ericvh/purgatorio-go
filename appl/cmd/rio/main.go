// Command rio is a window system after Plan 9's rio(1) and rio(4), on
// the draw device with no toolkit.
//
//	rio [-f font] [-i cmd]
//
// rio fills its screen with a grey background and serves windows on it.
// Button 3 on the background, or in a window no program draws in,
// brings up the menu: New, Resize, Move, Delete, Hide, Exit, then the
// labels of hidden windows. New and Resize ask for a rectangle swept
// with button 3; Resize, Move, Delete and Hide ask for a window, chosen
// with button 3 (Move drags it); a hidden window's label shows it
// again; Exit kills every window's processes and ends rio. Button 1 on
// a window that is not current makes it current and raises it. Button
// 1 or 3 on a window's border drags the nearest edge or corner. -i cmd
// runs cmd in a first window.
//
// Each window runs sh -i with the window's files (rio(4), see fsys.go)
// before /dev. Without a program drawing in it, a window shows its
// text: what is written to /dev/cons goes in at the output point,
// typing goes at the selection or the end and is sent a line at a time,
// ^D sends what is typed without a newline, Delete throws away what is
// typed and not yet read, and the wheel scrolls. Button 1 selects text
// (a double click selects a word or a line; button 2 or 3 pressed while
// it is held cuts or pastes), and button 2 brings up cut, paste, snarf,
// plumb (when the plumber runs), send and scroll or noscroll, on the
// snarf buffer. Escape starts and ends hold mode, as do holdon and
// holdoff on consctl: the text and the border turn dark blue, and
// newlines typed send nothing until hold mode ends. What is written to
// a window's kbdin is typed at it.
//
// rio serves its files on /chan/rio.user.pid too (fsys.go), which
// stands in for Plan 9's /srv/rio.user.pid, and puts its name in $wsys
// for its windows. window(1) makes windows through it from outside
// rio's windows. rio runs in a rio window as well: there stream(1)
// copies its pointer and keyboard into pipes, as for sdk/wmclient,
// since purgatorio.wait cannot wait on a mounted file.
//
// A program that opens /dev/draw in a window draws in the window: the
// SDK looks up the image /dev/winname names and makes a screen and a
// window of its own on it (Plan 9's getwindow). While it has
// /dev/pointer open, pointer events in the window go to it and rio
// does not draw the text. A resize or move gives the window a new image
// and a new name, and the program hears of it as a pointer event marked
// r. When rio's own screen changes size, the windows are scaled to it.
//
// rio -W [-cd dir] [cmd] is the window side of a new window, which rio
// runs in a name space, environment and process group of its own: it
// mounts the window's files (fd 0) before /dev, opens /dev/cons as fds
// 0, 1 and 2 and runs cmd with sh -c, or sh -i, in dir.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/plumbmsg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	background = 0x777777FF
	focuscol   = 0x2E6E6EFF // the border of the current window
	unfocuscol = 0x9ECECEFF // the border of the others
	menubgcol  = 0xEAFFEAFF
	menuhicol  = 0x448844FF
	sweepcol   = 0xDD2222FF
	highcol    = 0xEEEE9EFF // the selection's background
	holdcol    = 0x000099FF // text, and the current window's border, in hold mode
	holdbdcol  = 0x7777CCFF // the border of another window in hold mode
	minsize    = 50         // the smallest window a sweep makes
	corner     = 20         // how near a corner a border press drags it
	doubleMsec = 500        // the longest gap in a double click
	menupad    = 2          // pixels above and below a menu item's text
	maxmouseq  = 64         // pointer events kept for a slow reader
	plumbinput = "/chan/plumb.input"
)

var (
	menu3 = []string{"New", "Resize", "Move", "Delete", "Hide", "Exit"}
	menu2 = []string{"cut", "paste", "snarf", "plumb", "send"}
)

type window struct {
	id     int
	r      draw.Rectangle // on the screen, border included
	img    *draw.Image
	name   string // img's name, for winname
	serial int    // images the window has had, for new names
	label  string
	hidden bool
	pid    int      // rio -W, the leader of the window's process group; 0 for none
	sess   *session // the window's pipe
	conn   *session // for a window made by an attach, its conversation
	text   text
	follow bool // scroll to show the end before the next drawing
	scroll bool // output scrolls the text (scroll, noscroll)
	dirty  bool // the text needs drawing

	mouseq       [][]byte // pointer(3) records waiting to be read
	mouseOpens   int
	kbdq         []byte
	kbdOpens     int
	rawq         []byte
	raw          bool
	consctlOpens int
	consHold     bool // consctl's holdon started hold mode
	opens        int  // files of the window open, in every session
	z            int  // stacking order: higher is nearer the top
}

type click struct {
	w    *window
	q    int
	msec int
}

type rio struct {
	d      *draw.Display
	screen *draw.Image
	scr    *draw.Screen
	font   *draw.Font
	wd     *widths

	bg, black, white, high, focus, unfocus, menubg, menuhi, sweep, hold, holdbd *draw.Image

	ptrfd, kbdfd, reffd, waitfd int
	kbdbuf                      []byte
	m                           draw.Pointer // the pointer as last read
	prevButtons                 int          // the buttons before the event being routed

	sess     []*session  // the windows' pipes and the opens of the service file
	srvio    *sys.FileIO // the service file, /chan/rio.user.pid
	srvname  string      // and its name
	streams  []int       // in a rio window: stream(1)s copying the pointer and keyboard
	wins     []*window
	current  *window
	nextid   int
	z        int
	lasthit  int // the last item chosen from the menu
	lasthit2 int // and from button 2's menu
	click    *click
	grab     *window // gets the pointer while the buttons pressed in it are held
	user     string
	pid      int
	exit     bool
	plumbing bool // connected to the plumber
}

// cleanup is what rio undoes when it ends.
var cleanup func()

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "rio: %s\n", s)
	if cleanup != nil {
		cleanup()
	}
	sys.Exits("fail:" + s)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rio [-f font] [-i cmd]")
	sys.Exits("fail:usage")
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "-W" {
		windowSide(args[1:])
		return
	}
	var font, init string
	for len(args) > 0 {
		if len(args) < 2 {
			usage()
		}
		switch args[0] {
		case "-f":
			font = args[1]
		case "-i":
			init = args[1]
		default:
			usage()
		}
		args = args[2:]
	}
	r, err := newRio(font)
	if err != nil {
		fatal(err.Error())
	}
	cleanup = r.cleanup
	if init != "" {
		if _, err := r.newWindow(newopts{r: r.defaultRect(), cmd: init, scroll: true}, nil); err != nil {
			fatal(err.Error())
		}
	}
	r.run()
	r.cleanup()
}

// cleanup removes the service file and stops the streams.
func (r *rio) cleanup() {
	if r.srvname != "" {
		_ = os.Remove(r.srvname)
		r.srvname = ""
	}
	// The keyboard's stream goes first, and each is waited for, so that
	// an outer rio has the keyboard back before the pointer, whose close
	// shows its text again.
	for i := len(r.streams) - 1; i >= 0; i-- {
		progctl(r.streams[i], "kill")
		r.awaitExit(r.streams[i])
	}
	r.streams = nil
}

// awaitExit reads rio's wait file until prog pid has ended, or for two
// seconds at most.
func (r *rio) awaitExit(pid int) {
	for r.waitfd >= 0 {
		i, err := sys.Wait([][2]int{{sys.WaitRead, r.waitfd}}, 2000)
		if err != nil || i < 0 {
			return
		}
		var b [256]byte
		n, err := syscall.Read(r.waitfd, b[:])
		if err != nil || n <= 0 {
			return
		}
		if f := strings.Fields(string(b[:n])); len(f) > 0 && f[0] == strconv.Itoa(pid) {
			return
		}
	}
}

// windowSide is rio -W [-cd dir] [cmd]: it mounts the window's files
// on fd 0 before /dev and runs the command on the window's cons in
// dir. Its pid stays the command's, so rio sees the command end on its
// wait file.
func windowSide(args []string) {
	if _, err := sys.Mount(0, -1, "/dev", sys.MBEFORE, ""); err != nil {
		fatal(fmt.Sprintf("cannot mount window: %v", err))
	}
	if len(args) >= 2 && args[0] == "-cd" {
		if err := os.Chdir(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "rio: cd %s: %v\n", args[1], err)
		}
		args = args[2:]
	}
	syscall.Close(0)
	fd, err := sys.Open("/dev/cons", sys.ORDWR)
	if err != nil {
		fatal(fmt.Sprintf("/dev/cons: %v", err))
	}
	for i := 0; i < 3; i++ {
		if fd != i {
			_, _ = sys.Dup(fd, i)
		}
	}
	if fd > 2 {
		syscall.Close(fd)
	}
	argv := []string{"sh", "-i"}
	if len(args) > 0 {
		argv = []string{"sh", "-c", strings.Join(args, " ")}
	}
	err = sys.Exec("/bin/sh.wasm", argv)
	fatal(fmt.Sprintf("cannot run sh: %v", err))
}

func newRio(fontname string) (*rio, error) {
	d, err := draw.Init("")
	if err != nil {
		return nil, err
	}
	r := &rio{d: d, screen: d.Image, nextid: 1, reffd: -1, waitfd: -1}
	if fontname != "" {
		r.font, err = d.OpenFont(fontname)
	} else {
		r.font, err = d.GetDefaultFont()
	}
	if err != nil {
		return nil, err
	}
	r.wd = newWidths(r.font)
	for _, c := range []struct {
		p   **draw.Image
		col uint32
	}{{&r.bg, background}, {&r.black, 0x000000FF}, {&r.white, draw.White}, {&r.high, highcol},
		{&r.focus, focuscol}, {&r.unfocus, unfocuscol}, {&r.menubg, menubgcol},
		{&r.menuhi, menuhicol}, {&r.sweep, sweepcol}, {&r.hold, holdcol}, {&r.holdbd, holdbdcol}} {
		if *c.p, err = d.Color(c.col); err != nil {
			return nil, err
		}
	}
	if r.scr, err = r.screen.AllocScreen(r.bg, true); err != nil {
		return nil, fmt.Errorf("cannot allocate screen: %v", err)
	}
	r.screen.Draw(r.screen.R, r.bg, nil, draw.ZP)
	if err := d.Flush(true); err != nil {
		return nil, err
	}
	if r.ptrfd, err = r.openInput("/dev/pointer"); err != nil {
		return nil, fmt.Errorf("cannot open /dev/pointer: %v", err)
	}
	if r.kbdfd, err = r.openInput("/dev/keyboard"); err != nil {
		r.cleanup()
		return nil, fmt.Errorf("cannot open /dev/keyboard: %v", err)
	}
	if !d.Windowed() {
		// The draw device reports a change of the screen's size on the
		// refresh file.
		if fd, err := sys.Open(fmt.Sprintf("/dev/draw/%d/refresh", d.Dirno), sys.OREAD); err == nil {
			r.reffd = fd
		}
	}
	r.pid, _ = sys.Pctl(0, nil)
	// A window's prog reports its end here.
	if fd, err := sys.Open("/prog/"+strconv.Itoa(r.pid)+"/wait", sys.OREAD); err == nil {
		r.waitfd = fd
	}
	r.user = "rio"
	if b, err := os.ReadFile("/dev/user"); err == nil && len(b) > 0 {
		r.user = string(b)
	}
	r.post()
	return r, nil
}

// openInput opens the pointer or the keyboard for rio's loop to wait on.
// In a rio window they are the outer rio's files, through the mount
// driver, which purgatorio.wait cannot wait on, so stream(1) copies
// each into a pipe, as sdk/wmclient does.
func (r *rio) openInput(file string) (int, error) {
	if !r.d.Windowed() {
		return sys.Open(file, sys.OREAD)
	}
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
		return -1, fmt.Errorf("cannot start stream: %v", err)
	}
	r.streams = append(r.streams, pid)
	return p[0], nil
}

// post makes the service file, /chan/rio.user.pid, binding #s on /chan
// first if /chan is not a directory of it, and sets $wsys to its name
// in an environment of rio's own, which the windows copy.
func (r *rio) post() {
	name := fmt.Sprintf("rio.%s.%d", r.user, r.pid)
	f, err := sys.File2chan("/chan", name)
	if err != nil {
		if _, berr := sys.Bind("#s", "/chan", sys.MBEFORE); berr == nil {
			f, err = sys.File2chan("/chan", name)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "rio: cannot make /chan/%s: %v\n", name, err)
		return
	}
	r.srvio, r.srvname = f, "/chan/"+name
	_, _ = sys.Pctl(sys.FORKENV, nil)
	_ = os.WriteFile("/env/wsys", []byte(r.srvname), 0o666)
}

// run is rio's loop: the pointer, the keyboard, the screen's size,
// windows' progs ending and the windows' files, one at a time.
func (r *rio) run() {
	for {
		r.serveReads()
		for _, w := range r.wins {
			if w.dirty {
				r.paint(w)
			}
		}
		_ = r.d.Flush(true)
		if r.exit {
			return
		}
		if r.keyPending() {
			continue
		}
		ops := [][2]int{{sys.WaitRead, r.ptrfd}, {sys.WaitRead, r.kbdfd}}
		srcs := []int{0, 1}
		if r.reffd >= 0 {
			ops = append(ops, [2]int{sys.WaitRead, r.reffd})
			srcs = append(srcs, 2)
		}
		if r.waitfd >= 0 {
			ops = append(ops, [2]int{sys.WaitRead, r.waitfd})
			srcs = append(srcs, 3)
		}
		if r.srvio != nil {
			ops = append(ops, [2]int{sys.WaitChanRecv, int(r.srvio.Read)}, [2]int{sys.WaitChanRecv, int(r.srvio.Write)})
			srcs = append(srcs, 4, 4)
		}
		var pipes []*session
		for _, s := range r.sess {
			if s.fd >= 0 {
				pipes = append(pipes, s)
				ops = append(ops, [2]int{sys.WaitRead, s.fd})
			}
		}
		i, err := sys.Wait(ops, -1)
		if err != nil {
			fatal(fmt.Sprintf("wait: %v", err))
		}
		if i < 0 {
			continue
		}
		if i >= len(srcs) {
			s := pipes[i-len(srcs)]
			if w := s.win; r.win(w.id) != nil && !r.input(s) {
				r.delete(w)
			}
			continue
		}
		switch srcs[i] {
		case 0:
			if p, ok := r.readPointer(); ok {
				r.pointer(p)
			}
		case 1:
			var b [64]byte
			n, err := syscall.Read(r.kbdfd, b[:])
			if err != nil || n <= 0 {
				fatal("keyboard: end of file")
			}
			r.kbdbuf = append(r.kbdbuf, b[:n]...)
		case 2:
			r.refresh()
		case 3:
			r.waited()
		case 4:
			r.service()
		}
	}
}

// keyPending handles one whole key read earlier, if there is one.
func (r *rio) keyPending() bool {
	if len(r.kbdbuf) == 0 || !utf8.FullRune(r.kbdbuf) {
		return false
	}
	c, n := utf8.DecodeRune(r.kbdbuf)
	r.kbdbuf = r.kbdbuf[n:]
	r.key(c)
	return true
}

// readPointer reads the next event from /dev/pointer.
func (r *rio) readPointer() (draw.Pointer, bool) {
	var b [draw.PointerSize]byte
	n, err := syscall.Read(r.ptrfd, b[:])
	if err != nil || n <= 0 {
		fatal("pointer: end of file")
	}
	if n == len(b) && b[0] == 'r' {
		// rio runs in a rio window that has a new image.
		b[0] = 'm'
		if ok, _ := r.d.GetWindow(); ok {
			r.resized(r.screen.R)
		}
	}
	p, ok := draw.ParsePointer(b[:n])
	if ok {
		r.m = p
	}
	return p, ok
}

// refresh reads the refresh file; a record for image 0 is a change of
// the screen's size.
func (r *rio) refresh() {
	var b [20 * 16]byte
	n, err := syscall.Read(r.reffd, b[:])
	if err != nil || n <= 0 {
		syscall.Close(r.reffd)
		r.reffd = -1
		return
	}
	for i := 0; i+20 <= n; i += 20 {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 0 {
			old := r.screen.R
			if ok, _ := r.d.CheckResize(); ok {
				r.resized(old)
			}
			return
		}
	}
}

// waited reads a wait record: a window's prog has ended, and its
// window goes.
func (r *rio) waited() {
	var b [256]byte
	n, err := syscall.Read(r.waitfd, b[:])
	if err != nil || n <= 0 {
		syscall.Close(r.waitfd)
		r.waitfd = -1
		return
	}
	f := strings.Fields(string(b[:n]))
	if len(f) == 0 {
		return
	}
	pid, _ := strconv.Atoi(f[0])
	for _, w := range r.wins {
		if w.pid == pid {
			r.remove(w)
			return
		}
	}
}

func (r *rio) win(id int) *window {
	for _, w := range r.wins {
		if w.id == id {
			return w
		}
	}
	return nil
}

// at returns the topmost visible window at p and whether p is inside
// its border.
func (r *rio) at(p draw.Point) (*window, bool) {
	var hit *window
	for _, w := range r.wins {
		if !w.hidden && p.In(w.r) && (hit == nil || w.z > hit.z) {
			hit = w
		}
	}
	if hit == nil {
		return nil, false
	}
	return hit, p.In(hit.r.Inset(draw.Borderwidth))
}

// Drawing.

func (r *rio) border(w *window) {
	col := r.unfocus
	switch {
	case w == r.current && w.text.hold:
		col = r.hold
	case w == r.current:
		col = r.focus
	case w.text.hold:
		col = r.holdbd
	}
	img, wr, b := w.img, w.r, draw.Borderwidth
	img.Draw(draw.Rect(wr.Min.X, wr.Min.Y, wr.Max.X, wr.Min.Y+b), col, nil, draw.ZP)
	img.Draw(draw.Rect(wr.Min.X, wr.Max.Y-b, wr.Max.X, wr.Max.Y), col, nil, draw.ZP)
	img.Draw(draw.Rect(wr.Min.X, wr.Min.Y, wr.Min.X+b, wr.Max.Y), col, nil, draw.ZP)
	img.Draw(draw.Rect(wr.Max.X-b, wr.Min.Y, wr.Max.X, wr.Max.Y), col, nil, draw.ZP)
}

// lines is how many lines of text window w shows, and how wide.
func (r *rio) lines(w *window) (draw.Rectangle, int, int) {
	area := textArea(w.r)
	fh := max(r.font.Height, 1)
	return area, max(area.Dy()/fh, 1), max(area.Dx(), 1)
}

// paint draws window w's text, unless a program draws in it: the
// selection on its background, or the tick at the point.
func (r *rio) paint(w *window) {
	w.dirty = false
	r.border(w)
	if w.mouseOpens > 0 {
		return
	}
	area, n, width := r.lines(w)
	fh := max(r.font.Height, 1)
	if w.follow {
		w.follow = false
		if !w.text.endShows(n, width, r.wd) {
			w.text.org = w.text.orgForEnd(n, width, r.wd)
		}
	}
	t := &w.text
	fg := r.black
	if t.hold {
		fg = r.hold
	}
	lines := t.layout(t.org, n, width, r.wd)
	w.img.Draw(w.r.Inset(draw.Borderwidth), r.white, nil, draw.ZP)
	for i, l := range lines {
		y := area.Min.Y + i*fh
		if a, b := max(t.q0, l.s), min(t.q1, l.e); a < b {
			x0 := t.xOf(l.s, a, r.wd)
			x1 := t.xOf(l.s, b, r.wd)
			if t.s[b-1] == '\n' {
				x1 = area.Dx()
			}
			w.img.Draw(draw.Rect(area.Min.X+x0, y, area.Min.X+x1, y+fh), r.high, nil, draw.ZP)
		}
		for _, p := range t.pieces(l, r.wd) {
			_, _ = w.img.String(draw.Pt(area.Min.X+p.x, y), fg, draw.ZP, r.font, p.s)
		}
	}
	if t.q0 == t.q1 {
		if p, ok := t.pointOf(t.q0, lines, fh, r.wd); ok {
			p = p.Add(area.Min)
			w.img.Draw(draw.Rect(p.X, p.Y, p.X+1, p.Y+fh), fg, nil, draw.ZP)
			w.img.Draw(draw.Rect(p.X-1, p.Y, p.X+2, p.Y+1), fg, nil, draw.ZP)
			w.img.Draw(draw.Rect(p.X-1, p.Y+fh-1, p.X+2, p.Y+fh), fg, nil, draw.ZP)
		}
	}
}

// show draws window w's text now.
func (r *rio) show(w *window) {
	r.paint(w)
	_ = r.d.Flush(true)
}

// runeAt is the rune of window w's text drawn at p, or nearest it.
func (r *rio) runeAt(w *window, p draw.Point) int {
	area, n, width := r.lines(w)
	lines := w.text.layout(w.text.org, n, width, r.wd)
	if len(lines) == 0 {
		return w.text.org
	}
	fh := max(r.font.Height, 1)
	i := min(max((p.Y-area.Min.Y)/fh, 0), len(lines)-1)
	return w.text.atX(lines[i], p.X-area.Min.X, r.wd)
}

// Windows.

// newopts are what a new window is made with: its rectangle, the
// command it runs (sh -i when empty) in directory dir, and whether it
// starts hidden and scrolls with output.
type newopts struct {
	r              draw.Rectangle
	cmd, dir       string
	hidden, scroll bool
}

// newWindow makes a window. With conn it runs nothing: it is a window
// made by an attach on the service file, whose programs mount it, and
// it lasts while its files are open and the conversation goes on.
// Else rio -W runs the command in a name space, environment and
// process group of its own, on the window's pipe.
func (r *rio) newWindow(o newopts, conn *session) (*window, error) {
	id := r.nextid
	img, err := r.scr.NewWindow(o.r, draw.Refbackup, draw.White)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("rio.%d.%d.1", r.pid, id)
	if err := img.Name(name, true); err != nil {
		_ = img.Free()
		return nil, err
	}
	pid, fd := 0, -1
	if conn == nil {
		p, err := sys.Pipe()
		if err != nil {
			_ = img.Name(name, false)
			_ = img.Free()
			return nil, err
		}
		argv := []string{"rio", "-W"}
		if o.dir != "" {
			argv = append(argv, "-cd", o.dir)
		}
		if o.cmd != "" {
			argv = append(argv, o.cmd)
		}
		pid, err = sys.Spawn("/bin/rio.wasm", argv, sys.NEWFD|sys.FORKNS|sys.NEWPGRP|sys.FORKENV,
			[][2]int{{0, p[1]}, {2, 2}})
		syscall.Close(p[1])
		if err != nil {
			syscall.Close(p[0])
			_ = img.Name(name, false)
			_ = img.Free()
			return nil, err
		}
		fd = p[0]
	}
	r.nextid++
	r.z++
	w := &window{id: id, r: o.r, img: img, name: name, serial: 1, label: "window " + strconv.Itoa(id),
		pid: pid, conn: conn, follow: true, scroll: o.scroll, dirty: true, z: r.z}
	if fd >= 0 {
		w.sess = r.newSession(fd, w)
	}
	r.wins = append(r.wins, w)
	r.focusWin(w)
	if o.hidden {
		r.hide(w)
	}
	return w, nil
}

// focusWin makes w current and raises it, showing it if it is hidden.
func (r *rio) focusWin(w *window) {
	if w.hidden {
		r.reshape(w, w.r, true, false)
		return
	}
	old := r.current
	r.current = w
	r.z++
	w.z = r.z
	_ = r.scr.Top(w.img)
	if old != nil && old != w && r.win(old.id) != nil && !old.hidden {
		r.border(old)
	}
	r.border(w)
}

// focusTop makes the topmost visible window current, if any.
func (r *rio) focusTop() {
	r.current = nil
	var top *window
	for _, w := range r.wins {
		if !w.hidden && (top == nil || w.z > top.z) {
			top = w
		}
	}
	if top != nil {
		r.focusWin(top)
	}
}

// reshape gives window w a new image on nr with a new name; with keep
// the old pixels go with it. With hidden the image is an ordinary one,
// on no screen, so the window shows nowhere; else it is a window on
// rio's screen, and current. A program drawing in it hears of it.
func (r *rio) reshape(w *window, nr draw.Rectangle, keep, hidden bool) {
	var img *draw.Image
	var err error
	if hidden {
		img, err = r.d.AllocImage(nr, r.screen.Chan, false, draw.White)
	} else {
		img, err = r.scr.NewWindow(nr, draw.Refbackup, draw.White)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "rio: reshape: %v\n", err)
		return
	}
	w.serial++
	name := fmt.Sprintf("rio.%d.%d.%d", r.pid, w.id, w.serial)
	if keep {
		img.Draw(nr, w.img, nil, w.img.R.Min)
	}
	_ = img.Name(name, true)
	_ = w.img.Name(w.name, false)
	_ = w.img.Free()
	w.img, w.r, w.name, w.hidden = img, nr, name, hidden
	w.dirty, w.follow = true, true
	if w.mouseOpens > 0 {
		r.queueMouse(w, r.m, 'r')
	}
	if !hidden {
		r.focusWin(w)
	} else if r.current == w {
		r.focusTop()
	}
}

func (r *rio) hide(w *window) {
	if !w.hidden {
		r.reshape(w, w.r, true, true)
	}
}

// scale puts rectangle wr on screen from in the same place, scaled, on
// screen to, as rio does when its screen changes size: at least minsize
// each way where the screen allows, and on it.
func scale(wr, from, to draw.Rectangle) draw.Rectangle {
	along := func(v, f0, fd, t0, td int) int { return t0 + (v-f0)*td/max(fd, 1) }
	fit := func(lo, hi, t0, t1 int) (int, int) {
		size := min(max(hi-lo, min(minsize, t1-t0)), t1-t0)
		lo = min(max(lo, t0), max(t1-size, t0))
		return lo, lo + size
	}
	x0, x1 := fit(along(wr.Min.X, from.Min.X, from.Dx(), to.Min.X, to.Dx()),
		along(wr.Max.X, from.Min.X, from.Dx(), to.Min.X, to.Dx()), to.Min.X, to.Max.X)
	y0, y1 := fit(along(wr.Min.Y, from.Min.Y, from.Dy(), to.Min.Y, to.Dy()),
		along(wr.Max.Y, from.Min.Y, from.Dy(), to.Min.Y, to.Dy()), to.Min.Y, to.Max.Y)
	return draw.Rect(x0, y0, x1, y1)
}

// resized scales every window from rio's old screen rectangle to its
// new one. On the display the device keeps rio's screen and fills
// what it exposes; in a rio window rio's image is new, so it makes a
// new screen on it.
func (r *rio) resized(old draw.Rectangle) {
	var oldscr *draw.Screen
	if r.d.Image != r.screen {
		r.screen = r.d.Image
		r.screen.Draw(r.screen.R, r.bg, nil, draw.ZP)
		scr, err := r.screen.AllocScreen(r.bg, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rio: cannot allocate screen: %v\n", err)
			return
		}
		oldscr, r.scr = r.scr, scr
	}
	cur := r.current
	order := append([]*window(nil), r.wins...)
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && order[j].z < order[j-1].z; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	for _, w := range order {
		r.reshape(w, scale(w.r, old, r.screen.R), false, w.hidden)
	}
	if cur != nil && r.win(cur.id) != nil && !cur.hidden {
		r.focusWin(cur)
	} else {
		r.focusTop()
	}
	if oldscr != nil {
		_ = oldscr.Free()
	}
	_ = r.d.Flush(true)
}

func progctl(pid int, msg string) {
	if fd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/ctl", sys.OWRITE); err == nil {
		_, _ = syscall.Write(fd, []byte(msg))
		syscall.Close(fd)
	}
}

// delete kills window w's processes and takes it away. A window made
// by an attach has none of rio's: its files hang up.
func (r *rio) delete(w *window) {
	if w.pid != 0 {
		progctl(w.pid, "killgrp")
	}
	r.remove(w)
}

// remove takes window w away; its files hang up.
func (r *rio) remove(w *window) {
	i := -1
	for k, x := range r.wins {
		if x == w {
			i = k
		}
	}
	if i < 0 {
		return
	}
	r.wins = append(r.wins[:i], r.wins[i+1:]...)
	if w.sess != nil {
		r.dropSession(w.sess)
	}
	if r.grab == w {
		r.grab = nil
	}
	if r.click != nil && r.click.w == w {
		r.click = nil
	}
	_ = w.img.Name(w.name, false)
	_ = w.img.Free()
	if r.current == w {
		r.focusTop()
	}
}

// defaultRect is a rectangle for a window made without one.
func (r *rio) defaultRect() draw.Rectangle {
	s := r.screen.R
	wd, ht := max(s.Dx()*2/3, minsize), max(s.Dy()*2/3, minsize)
	off := 20 * (len(r.wins) % 5)
	p := draw.Pt(s.Min.X+s.Dx()/6+off, s.Min.Y+s.Dy()/6+off)
	return draw.Rect(p.X, p.Y, p.X+wd, p.Y+ht)
}

// wctl carries out a wctl request written by window w's programs.
func (r *rio) wctl(w *window, c wctlreq) string {
	switch c.verb {
	case "new":
		_, e := r.wctlNew(c, nil)
		return e
	case "resize", "move":
		nr := c.rect(w.r)
		if c.verb == "move" {
			nr = w.r.Add(nr.Min.Sub(w.r.Min))
		}
		if !goodRect(nr) {
			return ebadrect
		}
		r.reshape(w, nr, nr.Size() == w.r.Size(), false)
	case "top", "current", "unhide":
		r.focusWin(w)
	case "bottom":
		w.z = 0
		if !w.hidden {
			_ = r.scr.Bottom(w.img)
		}
	case "hide":
		r.hide(w)
	case "delete":
		r.delete(w)
	case "scroll", "noscroll":
		w.scroll = c.verb == "scroll"
		w.follow = w.follow || w.scroll
		w.dirty = true
	default:
		return ebadwctl
	}
	return ""
}

// goodRect reports whether nr can be a window.
func goodRect(nr draw.Rectangle) bool {
	return nr.Dx() >= 2*draw.Borderwidth+4 && nr.Dy() >= 2*draw.Borderwidth+4
}

// wctlNew makes a window as wctl's new request c says; with conn, for
// an attach on the service file.
func (r *rio) wctlNew(c wctlreq, conn *session) (*window, string) {
	o := newopts{r: c.rect(r.defaultRect()), cmd: c.cmd, dir: c.dir, hidden: c.hide, scroll: true}
	if c.scroll != nil {
		o.scroll = *c.scroll
	}
	if !goodRect(o.r) {
		return nil, ebadrect
	}
	w, err := r.newWindow(o, conn)
	if err != nil {
		return nil, err.Error()
	}
	return w, ""
}

// setHold starts or ends window w's hold mode.
func (r *rio) setHold(w *window, on bool) {
	w.text.setHold(on)
	if !on {
		w.consHold = false
	}
	w.dirty = true
}

// The keyboard.

// key is a key typed at the current window.
func (r *rio) key(c rune) {
	r.click = nil
	if r.current != nil {
		r.keyTo(r.current, c)
	}
}

// keyTo is a key typed at window w, or written to its kbdin.
func (r *rio) keyTo(w *window, c rune) {
	switch {
	case w.kbdOpens > 0:
		w.kbdq = utf8.AppendRune(w.kbdq, c)
	case w.raw:
		w.rawq = utf8.AppendRune(w.rawq, c)
	case c == 0x1B:
		// Escape: hold mode starts or ends, as in Plan 9's rio.
		r.setHold(w, !w.text.hold)

	case c == 0x7F:
		// Delete: what is typed and not yet read goes. Inferno has no
		// notes, so nothing is interrupted.
		w.text.flushInput()
		w.dirty = true
	case c >= 0xE000 && c <= 0xF8FF:
		// A key with no character.
	default:
		w.text.typed(c)
		w.follow, w.dirty = true, true
	}
}

// The snarf buffer.

func snarfOpen(mode int) (int, error) {
	fd, err := sys.Open("/dev/snarf", mode)
	if err != nil {
		fd, err = sys.Open("#^/snarf", mode)
	}
	return fd, err
}

func snarfGet() string {
	fd, err := snarfOpen(sys.OREAD)
	if err != nil {
		return ""
	}
	defer syscall.Close(fd)
	var out []byte
	b := make([]byte, 8192)
	for {
		n, err := syscall.Read(fd, b)
		if err != nil || n <= 0 {
			return string(out)
		}
		out = append(out, b[:n]...)
	}
}

func snarfPut(s string) {
	fd, err := snarfOpen(sys.OWRITE | sys.OTRUNC)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rio: snarf: %v\n", err)
		return
	}
	_, _ = syscall.Write(fd, []byte(s))
	syscall.Close(fd)
}

// plumb sends window w's selection, or the word at its point, to the
// plumber.
func (r *rio) plumb(w *window) {
	data := w.text.selected()
	if data == "" {
		a, b := w.text.double(w.text.q0)
		data = strings.TrimSpace(string(w.text.s[a:b]))
	}
	if data == "" {
		return
	}
	if !r.plumbing {
		if err := plumbmsg.Init(true, "", 0); err != nil {
			fmt.Fprintf(os.Stderr, "rio: plumb: %v\n", err)
			return
		}
		r.plumbing = true
	}
	dir, _ := os.Getwd()
	m := &plumbmsg.Msg{Src: "rio", Dir: dir, Kind: "text", Data: []byte(data)}
	if err := m.Send(); err != nil {
		fmt.Fprintf(os.Stderr, "rio: plumb: %v\n", err)
	}
}
