package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// The pointer: focus, the menus, sweeping, dragging and selection.

// queueMouse queues a pointer(3) record for window w's program; kind is
// m, or r for a new image.
func (r *rio) queueMouse(w *window, p draw.Pointer, kind byte) {
	if len(w.mouseq) >= maxmouseq {
		w.mouseq = w.mouseq[1:]
	}
	b := p.Bytes()
	b[0] = kind
	w.mouseq = append(w.mouseq, b)
}

// release waits for the buttons to be released.
func (r *rio) release() {
	for r.m.Buttons != 0 {
		r.readPointer()
	}
}

// pointer routes a pointer event.
func (r *rio) pointer(p draw.Pointer) {
	prev := r.prevButtons
	r.prevButtons = p.Buttons
	pressed := p.Buttons &^ prev
	if g := r.grab; g != nil {
		r.queueMouse(g, p, 'm')
		if p.Buttons == 0 {
			r.grab = nil
		}
		return
	}
	w, inside := r.at(p.XY)
	drawn := w != nil && w.mouseOpens > 0
	switch {
	case pressed&(1|4) != 0 && w != nil && !inside:
		// Buttons 1 and 3 on a border drag its edge or corner.
		if r.current != w {
			r.focusWin(w)
		}
		r.borderDrag(w, p.Buttons)
	case pressed&4 != 0 && !drawn:
		r.menu()
	case pressed&1 != 0 && w != nil && r.current != w:
		r.focusWin(w)
		r.release()
	case w != nil && inside && r.current == w && drawn:
		r.queueMouse(w, p, 'm')
		if pressed != 0 {
			r.grab = w
		}
	case w != nil && inside && r.current == w && pressed&1 != 0:
		r.selectText(w)
	case w != nil && inside && r.current == w && pressed&2 != 0:
		r.menu2(w)
	case pressed&(8|16) != 0 && w != nil && !drawn:
		n := 3
		if pressed&8 != 0 {
			n = -3
		}
		_, _, width := r.lines(w)
		w.text.scroll(n, width, r.wd)
		w.dirty = true
	}
	r.prevButtons = r.m.Buttons
}

// selectText selects text in window w with button 1, from a press: it
// sweeps from the rune pressed on to the rune released on, or after a
// second click on the same rune selects the word or line there.
func (r *rio) selectText(w *window) {
	q := r.runeAt(w, r.m.XY)
	msec := r.m.Msec
	double := r.click != nil && r.click.w == w && r.click.q == q && msec-r.click.msec < doubleMsec
	r.click = nil
	if double {
		w.text.sel(w.text.double(q))
		r.show(w)
	} else {
		w.text.sel(q, q)
		r.show(w)
		for r.m.Buttons == 1 {
			// Held above or below the text, the text scrolls a line
			// each scrollMsec and the selection follows, as in rio.
			if dir := r.beyond(w, r.m.XY); dir != 0 {
				i, err := sys.Wait([][2]int{{sys.WaitRead, r.ptrfd}}, scrollMsec)
				if err == nil && i < 0 {
					_, n, width := r.lines(w)
					if dir < 0 || !w.text.endShows(n, width, r.wd) {
						w.text.scroll(dir, width, r.wd)
					}
					w.text.sel(q, r.runeAt(w, r.m.XY))
					r.show(w)
					continue
				}
			}
			p, _ := r.readPointer()
			if p.Buttons != 1 {
				break
			}
			q1 := r.runeAt(w, p.XY)
			if w.text.q0 != min(q, q1) || w.text.q1 != max(q, q1) {
				w.text.sel(q, q1)
				r.show(w)
			}
		}
		if w.text.q0 == w.text.q1 {
			r.click = &click{w, q, msec}
		}
	}
	r.chords(w)
}

// beyond is -1 when p is above window w's text, 1 when it is below, and
// 0 otherwise.
func (r *rio) beyond(w *window, p draw.Point) int {
	area := textArea(w.r)
	switch {
	case p.Y < area.Min.Y:
		return -1
	case p.Y >= area.Max.Y:
		return 1
	}
	return 0
}

// chords: while button 1 is held after a selection, button 2 cuts and
// button 3 pastes, as in rio. It returns when every button is up.
func (r *rio) chords(w *window) {
	last := 1
	for r.m.Buttons != 0 {
		b := r.m.Buttons
		pressed := b &^ last
		if b&1 != 0 && pressed&2 != 0 {
			r.snarf(w, true)
		} else if b&1 != 0 && pressed&4 != 0 {
			r.paste(w)
		}
		if pressed&6 != 0 {
			r.click = nil
			r.show(w)
		}
		last = b
		r.readPointer()
	}
}

// snarf puts window w's selection in the snarf buffer, and with del
// takes it out of the text.
func (r *rio) snarf(w *window, del bool) {
	s := w.text.selected()
	if s == "" {
		return
	}
	if del {
		w.text.delete(w.text.q0, w.text.q1)
		w.dirty = true
	}
	snarfPut(s)
}

// paste puts the snarf buffer in place of window w's selection.
func (r *rio) paste(w *window) {
	w.text.paste(snarfGet())
	w.dirty = true
}

// menu2 is button 2's menu in text window w, from a press.
func (r *rio) menu2(w *window) {
	var items []string
	for _, s := range menu2 {
		if s == "plumb" {
			if _, err := os.Stat(plumbinput); err != nil {
				continue
			}
		}
		items = append(items, s)
	}
	if w.scroll {
		items = append(items, "noscroll")
	} else {
		items = append(items, "scroll")
	}
	i := r.menuhit(items, 2, r.lasthit2)
	if i < 0 {
		return
	}
	r.lasthit2 = i
	r.click = nil
	switch items[i] {
	case "cut":
		r.snarf(w, true)
	case "paste":
		r.paste(w)
	case "snarf":
		r.snarf(w, false)
	case "plumb":
		r.plumb(w)
	case "send":
		r.snarf(w, false)
		if s := snarfGet(); s != "" {
			w.text.send(s)
			w.follow = true
		}
	default:
		w.scroll = items[i] == "scroll"
		w.follow = w.follow || w.scroll
	}
	w.dirty = true
}

// borderDrag drags the edge or corner of window w's border nearest the
// pointer while button is held, showing the outline, and gives the
// window the rectangle it ends on.
func (r *rio) borderDrag(w *window, button int) {
	r0, p0 := w.r, r.m.XY
	near := func(v, lo, hi int) (bool, bool) {
		c := min(corner, (hi-lo)/2)
		return v < lo+c, v >= hi-c
	}
	left, right := near(p0.X, r0.Min.X, r0.Max.X)
	top, bottom := near(p0.Y, r0.Min.Y, r0.Max.Y)
	var boxes []*draw.Image
	nr := r0
	for r.m.Buttons == button {
		p, _ := r.readPointer()
		d := p.XY.Sub(p0)
		n := r0
		if left {
			n.Min.X += d.X
		}
		if right {
			n.Max.X += d.X
		}
		if top {
			n.Min.Y += d.Y
		}
		if bottom {
			n.Max.Y += d.Y
		}
		nr = canon(n.Min, n.Max)
		boxes = r.outline(boxes, &nr)
	}
	ok := r.m.Buttons == 0
	r.outline(boxes, nil)
	r.release()
	if ok && nr != r0 && nr.Dx() >= minsize && nr.Dy() >= minsize {
		r.reshape(w, nr, false, false)
	}
}

func canon(a, b draw.Point) draw.Rectangle {
	return draw.Rect(min(a.X, b.X), min(a.Y, b.Y), max(a.X, b.X), max(a.Y, b.Y))
}

// menu is the menu, from a button 3 press: it shows it until the button
// is released and does what was chosen.
func (r *rio) menu() {
	items := append([]string(nil), menu3...)
	var hidden []*window
	for _, w := range r.wins {
		if w.hidden {
			hidden = append(hidden, w)
			items = append(items, w.label)
		}
	}
	i := r.menuhit(items, 4, r.lasthit)
	if i < 0 {
		return
	}
	r.lasthit = i
	if i >= len(menu3) {
		r.focusWin(hidden[i-len(menu3)])
		return
	}
	switch items[i] {
	case "New":
		if nr, ok := r.sweepRect(); ok {
			if _, err := r.newWindow(newopts{r: nr, scroll: true}, nil); err != nil {
				fmt.Fprintf(os.Stderr, "rio: new window: %v\n", err)
			}
		}
	case "Resize":
		if w := r.pick(); w != nil {
			if nr, ok := r.sweepRect(); ok {
				r.reshape(w, nr, false, false)
			}
		}
	case "Move":
		r.drag()
	case "Delete":
		if w := r.pick(); w != nil {
			r.delete(w)
		}
	case "Hide":
		if w := r.pick(); w != nil {
			r.hide(w)
		}
	case "Exit":
		for _, w := range r.wins {
			progctl(w.pid, "killgrp")
		}
		r.exit = true
	}
}

// itemRect is where item i of a menu r is.
func itemRect(r draw.Rectangle, ih, i int) draw.Rectangle {
	y := r.Min.Y + 1 + i*ih
	return draw.Rect(r.Min.X+1, y, r.Max.X-1, y+ih)
}

// menuhit shows items in a menu with item last under the pointer, and
// returns the item the pointer is on when button is released, or -1.
func (r *rio) menuhit(items []string, button, last int) int {
	ih := r.font.Height + 2*menupad
	wid := 0
	for _, s := range items {
		wid = max(wid, r.font.Width(s))
	}
	wid += 16
	last = min(last, len(items)-1)
	p := r.m.XY
	ht := ih*len(items) + 2
	s := r.screen.R
	x := min(max(p.X-wid/2, s.Min.X), max(s.Max.X-wid, s.Min.X))
	y := min(max(p.Y-1-last*ih-ih/2, s.Min.Y), max(s.Max.Y-ht, s.Min.Y))
	mr := draw.Rect(x, y, x+wid, y+ht)
	mw, err := r.scr.NewWindow(mr, draw.Refbackup, menubgcol)
	if err != nil {
		r.release()
		return -1
	}
	hitp := func(p draw.Point) int {
		for i := range items {
			if p.In(itemRect(mr, ih, i)) {
				return i
			}
		}
		return -1
	}
	hit, shown := hitp(p), -2
	for {
		if shown != hit {
			mw.Draw(mr, r.menuhi, nil, draw.ZP)
			mw.Draw(mr.Inset(1), r.menubg, nil, draw.ZP)
			for i, t := range items {
				ir := itemRect(mr, ih, i)
				fg := r.black
				if i == hit {
					mw.Draw(ir, r.menuhi, nil, draw.ZP)
					fg = r.menubg
				}
				tp := draw.Pt(ir.Min.X+(ir.Dx()-r.font.Width(t))/2, ir.Min.Y+menupad)
				_, _ = mw.String(tp, fg, draw.ZP, r.font, t)
			}
			_ = r.d.Flush(true)
			shown = hit
		}
		if r.m.Buttons&button == 0 {
			break
		}
		np, _ := r.readPointer()
		hit = hitp(np.XY)
	}
	_ = mw.Free()
	_ = r.d.Flush(true)
	r.release()
	return hit
}

// outline draws a box round nr in thin windows, replacing old; nil
// takes it away.
func (r *rio) outline(old []*draw.Image, nr *draw.Rectangle) []*draw.Image {
	for _, i := range old {
		_ = i.Free()
	}
	var out []*draw.Image
	if nr != nil {
		b, t := *nr, 2
		for _, side := range []draw.Rectangle{
			draw.Rect(b.Min.X, b.Min.Y, b.Max.X, b.Min.Y+t),
			draw.Rect(b.Min.X, b.Max.Y-t, b.Max.X, b.Max.Y),
			draw.Rect(b.Min.X, b.Min.Y, b.Min.X+t, b.Max.Y),
			draw.Rect(b.Max.X-t, b.Min.Y, b.Max.X, b.Max.Y),
		} {
			if side.Dx() > 0 && side.Dy() > 0 {
				if i, err := r.scr.NewWindow(side, draw.Refbackup, sweepcol); err == nil {
					out = append(out, i)
				}
			}
		}
	}
	_ = r.d.Flush(true)
	return out
}

// press3 waits for a press and reports whether it is button 3 alone.
func (r *rio) press3() bool {
	for r.m.Buttons == 0 {
		r.readPointer()
	}
	if r.m.Buttons != 4 {
		r.release()
		return false
	}
	return true
}

// sweepRect is a rectangle swept with button 3.
func (r *rio) sweepRect() (draw.Rectangle, bool) {
	if !r.press3() {
		return draw.Rectangle{}, false
	}
	p0 := r.m.XY
	var boxes []*draw.Image
	nr := canon(p0, p0)
	for r.m.Buttons == 4 {
		p, _ := r.readPointer()
		nr = canon(p0, p.XY)
		boxes = r.outline(boxes, &nr)
	}
	ok := r.m.Buttons == 0
	r.outline(boxes, nil)
	r.release()
	if !ok || nr.Dx() < minsize || nr.Dy() < minsize {
		return draw.Rectangle{}, false
	}
	return nr, true
}

// pick is a window chosen with button 3.
func (r *rio) pick() *window {
	if !r.press3() {
		return nil
	}
	w, _ := r.at(r.m.XY)
	r.release()
	return w
}

// drag moves a window dragged with button 3.
func (r *rio) drag() {
	if !r.press3() {
		return
	}
	p0 := r.m.XY
	w, _ := r.at(p0)
	if w == nil {
		r.release()
		return
	}
	r0 := w.r
	var boxes []*draw.Image
	nr := r0
	for r.m.Buttons == 4 {
		p, _ := r.readPointer()
		nr = r0.Add(p.XY.Sub(p0))
		boxes = r.outline(boxes, &nr)
	}
	ok := r.m.Buttons == 0
	r.outline(boxes, nil)
	r.release()
	if ok && nr != r0 {
		r.reshape(w, nr, true, false)
	}
}
