package main

import (
	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// Frame colours, as libframe numbers them.
const (
	BACK  = iota // background
	HIGH         // selection background
	BORD         // border and scroll bar
	TEXT         // text
	HTEXT        // selected text
	NCOL
)

// A Frame shows runes in a rectangle in one font, wrapping long lines
// and expanding tabs, with a selection or a tick (libframe, frame.b).
// It is laid out afresh each time its text changes; Text keeps the
// runes, the frame only what is visible.
type Frame struct {
	r      draw.Rectangle
	font   *draw.Font
	b      *draw.Image
	cols   [NCOL]*draw.Image
	maxtab int // tab width in pixels

	rs      []rune // what it shows
	lines   []fline
	nchars  int // runes that fit
	full    bool
	p0, p1  int // selection, in runes from the frame's start
	ticked  bool
	tickcol *draw.Image
}

// fline is one line of the frame: runes c0 to c1 and their x positions.
type fline struct {
	c0, c1 int
	xs     []int // len c1-c0+1
}

func (f *Frame) init(r draw.Rectangle, font *draw.Font, b *draw.Image, cols [NCOL]*draw.Image) {
	f.r, f.font, f.b, f.cols = r, font, b, cols
	f.maxtab = 8 * max(1, font.Width("0"))
}

func (f *Frame) maxlines() int { return max(0, f.r.Dy()/f.font.Height) }

// layout places rs in the frame and returns how many fit.
func (f *Frame) layout(rs []rune) int {
	f.lines = f.lines[:0]
	maxl := f.maxlines()
	x0, x1 := f.r.Min.X, f.r.Max.X
	i := 0
	for len(f.lines) < maxl {
		l := fline{c0: i}
		x := x0
		l.xs = []int{x}
		for i < len(rs) {
			c := rs[i]
			if c == '\n' {
				i++
				x = x1
				l.xs = append(l.xs, x)
				break
			}
			var w int
			if c == '\t' {
				w = f.maxtab - (x-x0)%max(1, f.maxtab)
				if x+w > x1 && x > x0 {
					break
				}
				w = min(w, x1-x)
			} else {
				w = f.font.Width(string(c))
				if x+w > x1 && x > x0 {
					break
				}
			}
			x += w
			i++
			l.xs = append(l.xs, x)
		}
		l.c1 = i
		f.lines = append(f.lines, l)
		if i >= len(rs) {
			break
		}
	}
	f.nchars = i
	f.full = len(f.lines) == maxl && (i < len(rs) || len(rs) > 0 && rs[len(rs)-1] == '\n')
	return i
}

// lastLineFull reports whether the frame has no room for more lines.
func (f *Frame) lastLineFull() bool { return len(f.lines) >= f.maxlines() }

// ptofchar returns the point where rune n of the frame starts.
func (f *Frame) ptofchar(n int) draw.Point {
	h := f.font.Height
	for k, l := range f.lines {
		if n >= l.c0 && n < l.c1 {
			return draw.Pt(l.xs[n-l.c0], f.r.Min.Y+k*h)
		}
	}
	if len(f.lines) == 0 {
		return f.r.Min
	}
	k := len(f.lines) - 1
	l := f.lines[k]
	if l.c1 > l.c0 && l.c1-1 < len(f.rs) && f.rs[l.c1-1] == '\n' {
		return draw.Pt(f.r.Min.X, f.r.Min.Y+(k+1)*h)
	}
	return draw.Pt(l.xs[len(l.xs)-1], f.r.Min.Y+k*h)
}

// charofpt returns the rune nearest point p.
func (f *Frame) charofpt(p draw.Point) int {
	if len(f.lines) == 0 {
		return 0
	}
	k := (p.Y - f.r.Min.Y) / f.font.Height
	if p.Y < f.r.Min.Y {
		k = 0
	}
	if k >= len(f.lines) {
		return f.nchars
	}
	l := f.lines[k]
	for c := l.c0; c < l.c1; c++ {
		x0, x1 := l.xs[c-l.c0], l.xs[c-l.c0+1]
		if c < len(f.rs) && f.rs[c] == '\n' {
			return c
		}
		if p.X < (x0+x1)/2 || p.X < x1 && x1-x0 > 2*f.font.Width("0") && p.X < x0+f.font.Width("0") {
			return c
		}
	}
	return l.c1
}

// redraw lays out rs and draws everything: the background, the
// selection p0 to p1 in high, and a tick if p0 == p1 and tick is set.
func (f *Frame) redraw(rs []rune, p0, p1 int, tick bool, high, htext *draw.Image) {
	f.rs = rs
	f.layout(rs)
	f.p0, f.p1 = p0, p1
	b := f.b
	b.Draw(f.r, f.cols[BACK], nil, draw.ZP)
	h := f.font.Height
	for k, l := range f.lines {
		y := f.r.Min.Y + k*h
		// Selection background.
		if p0 < p1 {
			s0, s1 := max(p0, l.c0), min(p1, l.c1)
			if s0 < s1 {
				x1 := l.xs[s1-l.c0]
				if s1 == l.c1 && s1 > l.c0 && rs[s1-1] == '\n' {
					x1 = f.r.Max.X
				}
				b.Draw(draw.Rect(l.xs[s0-l.c0], y, x1, y+h), high, nil, draw.ZP)
			}
		}
		// Text, in runs of the same colour.
		for c := l.c0; c < l.c1; {
			sel := c >= p0 && c < p1
			e := c
			for e < l.c1 && (e >= p0 && e < p1) == sel && rs[e] != '\t' && rs[e] != '\n' {
				e++
			}
			if e == c {
				c++
				continue
			}
			col := f.cols[TEXT]
			if sel {
				col = htext
			}
			_, _ = b.String(draw.Pt(l.xs[c-l.c0], y), col, draw.ZP, f.font, string(rs[c:e]))
			c = e
		}
	}
	f.ticked = false
	if tick && p0 == p1 && p0 <= f.nchars {
		f.drawtick(true)
	}
}

// drawtick draws or clears the tick at p0.
func (f *Frame) drawtick(on bool) {
	if f.tickcol == nil {
		return
	}
	p := f.ptofchar(f.p0)
	r := draw.Rect(p.X, p.Y, p.X+1, p.Y+f.font.Height)
	if !r.In(f.r) && !(r.Min.X == f.r.Max.X) {
		return
	}
	if on {
		f.b.Draw(r, f.tickcol, nil, draw.ZP)
		f.b.Draw(draw.Rect(p.X-1, p.Y, p.X+2, p.Y+2), f.tickcol, nil, draw.ZP)
		f.b.Draw(draw.Rect(p.X-1, p.Y+f.font.Height-2, p.X+2, p.Y+f.font.Height), f.tickcol, nil, draw.ZP)
	}
	f.ticked = on
}
