package main

import (
	"os"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/tk/tktest"
)

// setup makes acme on a host display: a row of two columns, without
// the file server or a window manager.
func setup(t *testing.T) {
	t.Helper()
	g.d = tktest.Display(t, 640, 480)
	g.font = g.d.DefaultFont
	g.screen = g.d.Image
	g.tabstop = 4
	g.wdir = t.TempDir()
	g.redraw = map[*Text]bool{}
	g.retag = map[*Window]bool{}
	g.mouseq = []draw.Pointer{}
	g.snarf = nil
	g.clicktext = nil
	g.exiting = false
	nextid = 1
	colinit()
	g.row = newRow(g.screen.R)
	g.row.add(-1)
	g.row.add(-1)
}

// at is a point in rune q of t.
func at(t *Text, q int) draw.Point {
	flush()
	p := t.fr.ptofchar(q - t.org)
	return p.Add(draw.Pt(1, g.font.Height/2))
}

var msec = 1000

// sweep presses buttons b at p0, moves to p1 and runs the given
// changes of buttons there, then releases.
func sweep(b int, p0, p1 draw.Point, chords ...int) {
	msec += 1000
	for _, c := range chords {
		g.mouseq = append(g.mouseq, draw.Pointer{Buttons: c, XY: p1, Msec: msec})
	}
	g.mouseq = append(append([]draw.Pointer{{Buttons: b, XY: p1, Msec: msec}}, g.mouseq...),
		draw.Pointer{XY: p1, Msec: msec})
	mousethread(draw.Pointer{Buttons: b, XY: p0, Msec: msec})
	flush()
}

func click(b int, p draw.Point, ms int) {
	g.mouseq = append(g.mouseq, draw.Pointer{XY: p, Msec: ms})
	mousethread(draw.Pointer{Buttons: b, XY: p, Msec: ms})
	flush()
}

func body(w *Window) string { return string(w.body.file.b) }

func newwin(text string) *Window {
	w := g.row.col[1].add(nil, nil, -1)
	w.body.insert(0, []rune(text), true)
	w.body.file.Clean()
	flush()
	return w
}

func TestSelectAndChords(t *testing.T) {
	setup(t)
	w := newwin("hello big world\nsecond line\n")
	b := &w.body
	// Button 1 sweeps a selection.
	sweep(1, at(b, 6), at(b, 9))
	if b.q0 != 6 || b.q1 != 9 {
		t.Errorf("sweep %d,%d", b.q0, b.q1)
	}
	// A double click selects a word; at a line's start, the line.
	click(1, at(b, 11), 5000)
	click(1, at(b, 11), 5100)
	if string(b.file.Read(b.q0, b.q1)) != "world" {
		t.Errorf("double click %q", b.file.Read(b.q0, b.q1))
	}
	click(1, at(b, 16), 7000)
	click(1, at(b, 16), 7100)
	if string(b.file.Read(b.q0, b.q1)) != "second line\n" {
		t.Errorf("double click at line start %q", b.file.Read(b.q0, b.q1))
	}
	// Button 1 then 2: cut; then 3: the cut is undone.
	sweep(1, at(b, 0), at(b, 6), 3, 1)
	if body(w) != "big world\nsecond line\n" || string(g.snarf) != "hello " {
		t.Errorf("after 1-2 chord %q snarf %q", body(w), string(g.snarf))
	}
	if !strings.Contains(string(w.tag.file.b), "Undo") {
		t.Errorf("tag after cut %q", string(w.tag.file.b))
	}
	// Button 1 then 3 pastes over the selection.
	sweep(1, at(b, 4), at(b, 9), 5, 1)
	if body(w) != "big hello \nsecond line\n" {
		t.Errorf("after 1-3 chord %q", body(w))
	}
	// Button 2 on Undo in the tag takes the paste back.
	tag := &w.tag
	u := strings.Index(string(tag.file.b), "Undo")
	click(2, at(tag, u+1), 20000)
	if body(w) != "big world\nsecond line\n" {
		t.Errorf("after Undo %q", body(w))
	}
	click(2, at(tag, strings.Index(string(tag.file.b), "Undo")+1), 21000)
	if body(w) != "hello big world\nsecond line\n" {
		t.Errorf("after second Undo %q", body(w))
	}
	if !strings.Contains(string(tag.file.b), "Redo") {
		t.Errorf("tag after undos %q", string(tag.file.b))
	}
}

func TestLookAndCommands(t *testing.T) {
	setup(t)
	w := newwin("find me then me again\n")
	b := &w.body
	// Button 3 on a word that is not a file searches for it.
	click(4, at(b, 6), 1000)
	if b.q0 != 13 || b.q1 != 15 {
		t.Errorf("look selected %d,%d", b.q0, b.q1)
	}
	// Typing goes to the text under the mouse.
	g.mouse.XY = at(b, 0)
	b.setselect(0, 0)
	for _, r := range "X" {
		keyboard(r)
	}
	if body(w) != "Xfind me then me again\n" {
		t.Errorf("typed %q", body(w))
	}
	// New in a column's tag makes a window; Del deletes it.
	c := g.row.col[0]
	n := len(c.w)
	click(2, at(&c.tag, 1), 3000)
	if len(c.w) != n+1 {
		t.Fatalf("New made %d windows", len(c.w)-n)
	}
	nw := c.w[len(c.w)-1]
	click(2, at(&nw.tag, strings.Index(string(nw.tag.file.b), "Del")+1), 4000)
	if len(c.w) != n {
		t.Errorf("Del left %d windows", len(c.w))
	}
	// A typed name in the tag, then Put, writes the file.
	name := g.wdir + "/out.txt"
	w.tag.insert(0, []rune(name), true)
	w.commit(&w.tag)
	if w.body.file.name != name {
		t.Errorf("name %q", w.body.file.name)
	}
	// The tag is one line, too short to show Put after the long name:
	// execute it as button 2 would.
	q := runelen(string(w.tag.file.b)[:strings.Index(string(w.tag.file.b), "Put")])
	execute(&w.tag, q+1, q+1, false, nil)
	if w.body.file.Dirty() || strings.Contains(string(w.tag.file.b), "Put") {
		t.Errorf("still dirty after Put: %q", string(w.tag.file.b))
	}
	if b, err := os.ReadFile(name); err != nil || string(b) != body(w) {
		t.Errorf("Put wrote %q %v", b, err)
	}
	// Button 3 on the file's name opens it in a window of its own,
	// or shows the one there is.
	g.row.col[0].add(nil, nil, -1).body.insert(0, []rune("out.txt:1"), true)
	var ow *Window
	for _, x := range g.row.col[0].w {
		if strings.HasPrefix(body(x), "out.txt") {
			ow = x
		}
	}
	ow.body.file.name = g.wdir + "/scratch"
	before := len(g.row.windows())
	click(4, at(&ow.body, 2), 6000)
	if len(g.row.windows()) != before {
		t.Errorf("look opened another window on a file already open")
	}
	if w.body.q0 != 0 || w.body.q1 != len("Xfind me then me again\n") {
		t.Errorf("look at :1 selected %d,%d", w.body.q0, w.body.q1)
	}
}

func TestScroll(t *testing.T) {
	setup(t)
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("line\n")
	}
	w := newwin(sb.String())
	b := &w.body
	p := draw.Pt(b.scrollr.Min.X+2, b.scrollr.Min.Y+5*g.font.Height+1)
	click(4, p, 1000)
	if b.org != 5*5 {
		t.Errorf("button 3 scrolled to %d", b.org)
	}
	click(1, p, 2000)
	if b.org != 0 {
		t.Errorf("button 1 scrolled back to %d", b.org)
	}
	// Button 2 jumps in proportion.
	mid := draw.Pt(p.X, b.scrollr.Min.Y+b.scrollr.Dy()/2)
	click(2, mid, 3000)
	if b.org < 400 || b.org > 600 {
		t.Errorf("button 2 jumped to %d", b.org)
	}
}
