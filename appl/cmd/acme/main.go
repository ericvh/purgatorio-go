// Command acme is Inferno's acme(1): a text editor and window system in
// columns of windows, each a tag and a body, driven by the mouse.
//
//	acme [-c ncol] [-t tabstop] [file ...]
//
// Button 1 selects, button 2 executes, button 3 looks; with button 1
// held, button 2 cuts and button 3 pastes. Text in tags is commands:
// Cut, Del, Delcol, Delete, Exit, Get, Kill, Look, New, Newcol, Paste,
// Put, Putall, Redo, Snarf, Sort, Tab, Undo and Zerox are built in, and
// anything else runs as a command, whose output goes to the +Errors
// window of its directory.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wmclient"
)

// g is acme's state.
var g struct {
	d        *draw.Display
	win      *wmclient.Window
	screen   *draw.Image
	font     *draw.Font
	textcols [NCOL]*draw.Image
	tagcols  [NCOL]*draw.Image
	but2col  *draw.Image
	but3col  *draw.Image
	black    *draw.Image
	white    *draw.Image

	modbutton *draw.Image
	row       *Row
	seq       int
	wdir      string
	tabstop   int
	redraw    map[*Text]bool
	retag     map[*Window]bool
	mouse     draw.Pointer
	snarf     []rune
	clicktext *Text
	clickmsec int
	argtext   *Text // the text of the last button 1 selection, for 2-1 chords
	pending   []wmclient.Event
	procs     []*proc
	srv       *fsys
	exiting   bool
	// mouseq, when set, is where getmouse takes pointer events from,
	// for tests.
	mouseq []draw.Pointer
}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "acme: %s\n", s)
	sys.Exits("fail:" + s)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-M" {
		mounter()
		return
	}
	a := arg.New(os.Args)
	a.SetUsage("acme [-c ncol] [-t tabstop] [file ...]")
	ncol := -1
	g.tabstop = 4
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'c':
			ncol, _ = strconv.Atoi(a.EArg())
		case 't':
			g.tabstop, _ = strconv.Atoi(a.EArg())
		case 'f', 'F', 'l':
			a.EArg() // fonts and dumps are not supported
		case 'b':
		default:
			a.Usage()
		}
	}
	files := a.Argv()
	if ts := os.Getenv("tabstop"); ts != "" {
		if n, err := strconv.Atoi(ts); err == nil && n > 0 {
			g.tabstop = n
		}
	}
	if g.tabstop <= 0 {
		g.tabstop = 4
	}
	sys.Pctl(sys.NEWPGRP, nil)
	if wd, err := os.Getwd(); err == nil {
		g.wdir = wd
	} else {
		g.wdir = "/"
	}
	var err error
	if g.d, err = draw.Init(""); err != nil {
		fatal(err.Error())
	}
	if g.font, err = g.d.GetDefaultFont(); err != nil {
		fatal(err.Error())
	}
	if g.win, err = wmclient.New(g.d, "Acme", wmclient.Appl); err != nil {
		fatal(err.Error())
	}
	r := g.d.Image.R
	if g.win.Displayr.Dx() > 0 {
		r = g.win.Displayr
	}
	if err := g.win.Reshape(r); err != nil {
		fatal(err.Error())
	}
	if err := g.win.Onscreen("max"); err != nil {
		fatal(err.Error())
	}
	if err := g.win.StartInput("kbd", "ptr"); err != nil {
		fatal(err.Error())
	}
	g.screen = g.win.Image
	g.redraw = map[*Text]bool{}
	g.retag = map[*Window]bool{}
	colinit()
	startfsys()
	g.row = newRow(g.win.ImageR(g.screen.R))
	if ncol < 0 {
		ncol = 2
		if len(files) == 0 {
			ncol = 2
		}
	}
	for i := 0; i < ncol; i++ {
		g.row.add(-1)
	}
	for _, f := range files {
		c := g.row.col[len(g.row.col)-1]
		openfile(c, f, "")
	}
	flush()
	loop()
}

// colinit allocates acme's colours.
func colinit() {
	c := func(v uint32) *draw.Image {
		i, err := g.d.Color(v)
		if err != nil {
			fatal(err.Error())
		}
		return i
	}
	g.black, g.white = c(0x000000FF), c(0xFFFFFFFF)
	g.textcols = [NCOL]*draw.Image{BACK: c(0xFFFFEAFF), HIGH: c(0xEEEE9EFF), BORD: c(0x99994CFF),
		TEXT: g.black, HTEXT: g.black}
	g.tagcols = [NCOL]*draw.Image{BACK: c(0xEAFFFFFF), HIGH: c(0x9EEEEEFF), BORD: c(0x8888CCFF),
		TEXT: g.black, HTEXT: g.black}
	g.but2col = c(0xAA0000FF)
	g.but3col = c(0x006600FF)
	g.modbutton = c(0x000099FF)
}

// loop is acme's main loop: input, the file server and commands'
// output, one at a time.
func loop() {
	for !g.exiting {
		var e wmclient.Event
		if len(g.pending) > 0 {
			e = g.pending[0]
			g.pending = g.pending[1:]
		} else {
			var fds []int
			if g.srv != nil {
				fds = append(fds, g.srv.fd)
			}
			for _, p := range g.procs {
				fds = append(fds, p.fd)
			}
			var err error
			e, err = g.win.Next(fds, -1)
			if err != nil {
				fatal(err.Error())
			}
		}
		switch e.Kind {
		case wmclient.Ptr:
			g.mouse = e.Ptr
			mousethread(e.Ptr)
		case wmclient.Kbd:
			keyboard(e.Key)
		case wmclient.Reshape:
			g.screen = g.win.Image
			for _, t := range alltexts() {
				t.fr.b = g.screen
			}
			g.row.resize(g.win.ImageR(g.screen.R))
		case wmclient.Fd:
			if g.srv != nil && e.Fd == g.srv.fd {
				g.srv.one()
			} else {
				procoutput(e.Fd)
			}
		}
		flush()
	}
	wmclient.Exit()
}

// alltexts returns every text.
func alltexts() []*Text {
	ts := []*Text{&g.row.tag}
	for _, c := range g.row.col {
		ts = append(ts, &c.tag)
		for _, w := range c.w {
			ts = append(ts, &w.tag, &w.body)
		}
	}
	return ts
}

// flush draws what has changed.
func flush() {
	for w := range g.retag {
		if !w.deleted {
			w.settag()
		}
		delete(g.retag, w)
	}
	for t := range g.redraw {
		if t.w == nil || !t.w.deleted {
			t.redraw()
		}
		delete(g.redraw, t)
	}
	_ = g.d.Flush(true)
}

// getmouse waits for the next pointer event, keeping others for later.
func getmouse() draw.Pointer {
	flush()
	if g.mouseq != nil {
		m := draw.Pointer{XY: g.mouse.XY, Msec: g.mouse.Msec}
		if len(g.mouseq) > 0 {
			m = g.mouseq[0]
			g.mouseq = g.mouseq[1:]
		}
		g.mouse = m
		return m
	}
	for {
		e, err := g.win.Next(nil, -1)
		if err != nil {
			fatal(err.Error())
		}
		if e.Kind == wmclient.Ptr {
			g.mouse = e.Ptr
			return e.Ptr
		}
		g.pending = append(g.pending, e)
	}
}

// keyboard sends a key to the text under the mouse (or the last one
// typed into).
func keyboard(r rune) {
	t := g.row.which(g.mouse.XY)
	if t == nil {
		return
	}
	if t.what == Body || t.what == Tag {
		if t.w != nil && t.w.nopen[qwEvent] > 0 && t.what == Body {
			// Typing is still done by acme, with events sent.
		}
	}
	t.typex(r)
	if t.w != nil {
		g.retag[t.w] = true
	}
}

// mousethread handles a pointer event.
func mousethread(m draw.Pointer) {
	if m.Buttons == 0 {
		return
	}
	t := g.row.which(m.XY)
	if t == nil {
		return
	}
	if t.what == Body && m.XY.In(t.scrollr) {
		scroll(t, m)
		return
	}
	if t.what == Tag && m.XY.In(t.scrollr) {
		movewin(t.w, m)
		return
	}
	switch {
	case m.Buttons&1 != 0:
		g.argtext = t
		t.select1(m)
		if t.w != nil {
			g.retag[t.w] = true
		}
	case m.Buttons&2 != 0:
		q0, q1, cancel, argt := t.select2(m)
		if !cancel {
			execute(t, q0, q1, false, argt)
		}
	case m.Buttons&4 != 0:
		q0, q1, cancel := t.select3(m)
		if !cancel {
			look3(t, q0, q1, false)
		}
	}
}

// select1 is button 1: select by sweeping or double clicking, and the
// chords: button 2 cuts, button 3 pastes, and the other undoes.
func (t *Text) select1(m draw.Pointer) {
	q := t.org + t.fr.charofpt(m.XY)
	b := m.Buttons
	q0, q1 := q, q
	if g.clicktext == t && m.Msec-g.clickmsec < 500 && t.q0 == t.q1 && q == t.q0 {
		q0, q1 = t.doubleclick(q)
		g.clicktext = nil
		t.setselect(q0, q1)
		for m.Buttons == b {
			m = getmouse()
		}
	} else {
		g.clicktext, g.clickmsec = t, m.Msec
		anchor := q
		t.setselect(q, q)
		for m.Buttons == b {
			m = getmouse()
			if m.Buttons != b {
				break
			}
			// Scroll when the pointer leaves the frame.
			if t.what == Body && m.XY.Y >= t.fr.r.Max.Y && t.org+t.fr.nchars < t.file.Nc() {
				t.scrollLines(1)
				t.redrawIfNeeded()
			} else if t.what == Body && m.XY.Y < t.fr.r.Min.Y && t.org > 0 {
				t.scrollLines(-1)
				t.redrawIfNeeded()
			}
			p := m.XY
			p.Y = max(t.fr.r.Min.Y, min(p.Y, t.fr.r.Max.Y-1))
			q = t.org + t.fr.charofpt(p)
			q0, q1 = min(anchor, q), max(anchor, q)
			t.setselect(q0, q1)
			if q0 != q1 {
				g.clicktext = nil
			}
		}
	}
	// Chords.
	state := 0 // +1 after a cut, -1 after a paste
	for m.Buttons != 0 {
		b = m.Buttons
		if b&6 != 0 {
			if state == 0 && t.what == Body {
				g.seq++
				t.file.Mark()
			}
			if b&2 != 0 {
				if state == -1 && t.what == Body {
					t.w.undo(true)
					t.setselect(q0, t.q0)
					state = 0
				} else if state != 1 {
					cut(t, t, true, true)
					state = 1
				}
			} else {
				if state == 1 && t.what == Body {
					t.w.undo(true)
					t.setselect(q0, t.q1)
					state = 0
				} else if state != -1 {
					paste(t, t, true, false)
					state = -1
				}
			}
			g.clicktext = nil
		}
		for m.Buttons == b {
			m = getmouse()
		}
	}
}

// select23 sweeps with button 2 or 3, showing the sweep in high; it
// returns the range and whether another button cancelled it, and for
// button 2 whether button 1 chorded (2-1, an argument).
func (t *Text) select23(m draw.Pointer, high *draw.Image, mask int) (q0, q1 int, buts int) {
	b := m.Buttons
	anchor := t.org + t.fr.charofpt(m.XY)
	q0, q1 = anchor, anchor
	show := func() {
		rs := t.visible()
		p0, p1 := max(0, min(q0-t.org, len(rs))), max(0, min(q1-t.org, len(rs)))
		t.fr.redraw(rs, p0, p1, false, high, g.white)
		_ = g.d.Flush(true)
	}
	show()
	for m.Buttons == b {
		m = getmouse()
		p := m.XY
		p.Y = max(t.fr.r.Min.Y, min(p.Y, t.fr.r.Max.Y-1))
		q := t.org + t.fr.charofpt(p)
		q0, q1 = min(anchor, q), max(anchor, q)
		show()
	}
	buts = m.Buttons
	for m.Buttons != 0 {
		buts |= m.Buttons
		m = getmouse()
	}
	t.dirty()
	return q0, q1, buts &^ mask
}

func (t *Text) select2(m draw.Pointer) (int, int, bool, *Text) {
	q0, q1, buts := t.select23(m, g.but2col, 2)
	if buts&4 != 0 {
		return 0, 0, true, nil
	}
	if buts&1 != 0 {
		return q0, q1, false, g.argtext
	}
	return q0, q1, false, nil
}

func (t *Text) select3(m draw.Pointer) (int, int, bool) {
	q0, q1, buts := t.select23(m, g.but3col, 4)
	return q0, q1, buts != 0
}

// scroll handles a press in a body's scroll bar (scrl.b): button 1
// moves back, button 3 forward, by the pointer's depth in lines, and
// button 2 jumps to the place in proportion.
func scroll(t *Text, m draw.Pointer) {
	b := m.Buttons
	for {
		y := m.XY.Y - t.scrollr.Min.Y
		lines := max(1, y/g.font.Height)
		switch {
		case b&1 != 0:
			t.scrollLines(-lines)
		case b&4 != 0:
			t.scrollLines(lines)
		case b&2 != 0:
			h := max(1, t.scrollr.Dy())
			q := int(int64(t.file.Nc()) * int64(max(0, min(y, h))) / int64(h))
			t.setorigin(q, false)
		}
		t.redrawIfNeeded()
		_ = g.d.Flush(true)
		m = getmouse()
		if m.Buttons == 0 {
			return
		}
		if b&2 == 0 {
			// Buttons 1 and 3 act once per press.
			for m.Buttons != 0 {
				m = getmouse()
			}
			return
		}
	}
}

// movewin handles a press in a window's tag box: button 1 drags the
// window to another place or column, button 2 makes it as big as it
// can be with the others' tags showing, and button 3 gives it the
// whole column.
func movewin(w *Window, m draw.Pointer) {
	b := m.Buttons
	for m.Buttons != 0 {
		m = getmouse()
	}
	switch {
	case b&2 != 0:
		w.col.grow(w, 2)
		return
	case b&4 != 0:
		w.col.grow(w, 3)
		return
	}
	c := g.row.whichcol(m.XY)
	if c == nil {
		return
	}
	if c == w.col && m.XY.Y >= w.r.Min.Y && m.XY.Y < w.r.Min.Y+g.font.Height {
		w.col.grow(w, 1)
		return
	}
	w.col.close(w, false)
	c.add(w, nil, m.XY.Y)
}

// warning puts s in the +Errors window of dir (or acme's directory).
func warning(dir *string, s string) {
	d := g.wdir
	if dir != nil {
		d = *dir
	}
	w := errorwin(d)
	t := &w.body
	q := t.file.Nc()
	t.insert(q, []rune(s), true)
	t.file.Clean()
	w.settag()
	t.show(t.file.Nc(), t.file.Nc(), true)
}

// errorwin returns the +Errors window of dir, making it if need be.
func errorwin(dir string) *Window {
	name := strings.TrimSuffix(dir, "/") + "/+Errors"
	if dir == "/" {
		name = "/+Errors"
	}
	if w := g.row.lookfile(name); w != nil {
		return w
	}
	c := g.row.col[len(g.row.col)-1]
	w := c.add(nil, nil, -1)
	w.setname(name)
	return w
}

// mounter mounts acme's file tree, given on fd 5, and exits.
func mounter() {
	if err := sys.Mount(srvfd, -1, "/mnt/acme", sys.MREPL, ""); err != nil {
		fatal(fmt.Sprintf("cannot mount /mnt/acme: %v", err))
	}
	syscall.Close(srvfd)
}
