package main

import (
	"fmt"
	"path"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// A Window is a tag and a body (acme's wind.b).
type Window struct {
	tag, body  Text
	r          draw.Rectangle
	id         int
	col        *Column
	isdir      bool
	isscratch  bool
	tagchanged bool
	dirty      bool // as shown in the tag
	nopen      [nqid]int
	addr       [2]int // the addr file's range
	limit      [2]int
	nomark     bool
	noscroll   bool
	ctlfid     int
	dumpstr    string
	dumpdir    string
	events     []byte // queued for the event file
	eventx     int    // readers of the event file waiting
	deleted    bool
	filemenu   bool
	echomode   bool
	utflastqid int
}

var nextid = 1

// newWindow makes a window in r, with its body's file f (or a new one).
func newWindow(clone *Window, r draw.Rectangle, f *File) *Window {
	w := &Window{id: nextid, filemenu: true}
	nextid++
	tf := &File{}
	if f == nil {
		f = &File{}
	}
	w.tag.what, w.body.what = Tag, Body
	w.tag.w, w.body.w = w, w
	r1 := r
	r1.Max.Y = r1.Min.Y + g.font.Height
	w.tag.init(tf, r1, g.tagcols)
	r1 = r
	r1.Min.Y += g.font.Height + 1
	w.body.init(f, r1, g.textcols)
	w.r = r
	if clone != nil {
		w.body.org, w.body.q0, w.body.q1 = clone.body.org, clone.body.q0, clone.body.q1
		w.isdir = clone.isdir
	}
	w.settag()
	return w
}

// setname names the window's file and sets the tag.
func (w *Window) setname(name string) {
	w.body.file.name = name
	for _, t := range w.body.file.text {
		if t.w != nil {
			t.w.isscratch = strings.HasSuffix(name, "/+Errors") || name == "+Errors" ||
				strings.HasSuffix(name, "/guide")
			t.w.settag()
		}
	}
}

// dir is the directory the window's commands run in.
func (w *Window) dir() string {
	n := w.body.file.name
	if n == "" {
		return g.wdir
	}
	if w.isdir {
		return strings.TrimSuffix(n, "/")
	}
	d := path.Dir(n)
	if !strings.HasPrefix(n, "/") {
		d = path.Join(g.wdir, d)
	}
	return d
}

// tagname returns the file name at the start of the tag.
func (w *Window) tagname() string {
	rs := w.tag.file.b
	i := 0
	for i < len(rs) && rs[i] != ' ' && rs[i] != '\t' {
		i++
	}
	return string(rs[:i])
}

// settag rebuilds the tag: the name, the commands that apply, then the
// user's text after the bar (wsettag).
func (w *Window) settag() {
	f := w.body.file
	old := string(w.tag.file.b)
	user := ""
	if i := strings.IndexByte(old, '|'); i >= 0 {
		user = old[i+1:]
	} else {
		user = " Look "
	}
	var b strings.Builder
	b.WriteString(f.name)
	b.WriteString(" Del Snarf")
	if len(f.undo) > 0 {
		b.WriteString(" Undo")
	}
	if len(f.redo) > 0 {
		b.WriteString(" Redo")
	}
	if f.Dirty() && f.name != "" && !w.isscratch {
		b.WriteString(" Put")
	}
	if w.isdir {
		b.WriteString(" Get")
	}
	b.WriteString(" |")
	b.WriteString(user)
	nt := b.String()
	if nt != old {
		q0, q1 := w.tag.q0, w.tag.q1
		w.tag.file.b = []rune(nt)
		w.tag.q0 = min(q0, len(w.tag.file.b))
		w.tag.q1 = min(q1, len(w.tag.file.b))
		// A selection in the old text after the bar stays with it.
		if i := strings.IndexByte(old, '|'); i >= 0 && q0 > runelen(old[:i]) {
			d := runelen(nt) - runelen(old)
			w.tag.q0, w.tag.q1 = max(0, q0+d), max(0, q1+d)
		}
		w.tag.dirty()
	}
	w.dirty = f.Dirty()
	w.tagchanged = false
	w.drawbutton()
}

// changed is called when the body changes.
func (w *Window) changed() {
	if w.dirty != w.body.file.Dirty() || !w.tagchanged {
		w.tagchanged = true
		g.retag[w] = true
	}
}

// commit takes a typed tag's new name (wcommit).
func (w *Window) commit(t *Text) {
	if t.what != Tag {
		return
	}
	name := w.tagname()
	if name != w.body.file.name {
		g.seq++
		w.body.file.Mark()
		w.body.file.mod = true
		w.dirty = true
		w.setname(name)
		w.settag()
	}
}

// drawbutton draws the box at the left of the tag: filled when the file
// is dirty.
func (w *Window) drawbutton() {
	r := w.tag.scrollr
	r.Max.Y = r.Min.Y + g.font.Height
	if r.Dx() <= 0 {
		return
	}
	g.screen.Draw(r, g.tagcols[BACK], nil, draw.ZP)
	b := r.Inset(1)
	b.Max.X = b.Min.X + min(b.Dx(), b.Dy())
	b.Max.Y = b.Min.Y + b.Dx()
	if w.body.file.Dirty() {
		g.screen.Draw(b, g.modbutton, nil, draw.ZP)
	} else {
		g.screen.Draw(b, g.tagcols[BORD], nil, draw.ZP)
		g.screen.Draw(b.Inset(2), g.tagcols[BACK], nil, draw.ZP)
	}
}

// resize puts w in r.
func (w *Window) resize(r draw.Rectangle) int {
	w.r = r
	r1 := r
	r1.Max.Y = min(r.Max.Y, r1.Min.Y+g.font.Height)
	y := w.tag.resize(r1)
	w.drawbutton()
	r1 = r
	r1.Min.Y = y
	if y+1 < r.Max.Y {
		g.screen.Draw(draw.Rect(r.Min.X, y, r.Max.X, y+1), g.black, nil, draw.ZP)
		r1.Min.Y = y + 1
		y = w.body.resize(r1)
	} else {
		r1.Min.Y, r1.Max.Y = y, y
		w.body.resize(r1)
	}
	return max(y, r.Min.Y)
}

// close takes the window down.
func (w *Window) close() {
	w.deleted = true
	w.tag.close()
	w.body.close()
	w.event("M", 'x', 0, 0, 0, nil)
	fsysWindowGone(w)
}

// clean reports whether w may be deleted, complaining once if not.
func (w *Window) clean(conservative bool) bool {
	if w.isscratch || w.isdir {
		return true
	}
	f := w.body.file
	if !conservative && w.nopen[qwEvent] > 0 {
		return true
	}
	if f.Dirty() {
		if f.name != "" {
			warning(nil, f.name+" modified\n")
		} else {
			if f.Nc() < 100 {
				return true
			}
			warning(nil, "unnamed file modified\n")
		}
		f.Clean()
		w.settag()
		return false
	}
	return true
}

// event queues an event for the event file: origin c1 (E, F, K or M),
// type c2, the range, a flag and text.
func (w *Window) event(c1 string, c2 rune, q0, q1, flag int, r []rune) {
	if w.nopen[qwEvent] == 0 {
		return
	}
	if len(r) > 256 {
		r = nil
	}
	s := fmt.Sprintf("%s%c%d %d %d %d %s\n", c1, c2, q0, q1, flag, len(r), string(r))
	w.events = append(w.events, s...)
	fsysEvents(w)
}

// undo undoes or redoes a change to the body.
func (w *Window) undo(isundo bool) {
	f := w.body.file
	q0, q1, ok := f.UndoRedo(isundo)
	if ok {
		w.body.show(q0, q1, true)
	}
	for _, t := range f.text {
		if t.w != nil {
			t.w.settag()
		}
	}
}
