package main

import (
	"sort"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// A Column is a tag and a stack of windows (acme's col.b).
type Column struct {
	r   draw.Rectangle
	tag Text
	row *Row
	w   []*Window
}

const coltagtext = "New Cut Paste Snarf Sort Zerox Delcol "

func newColumn(row *Row, r draw.Rectangle) *Column {
	c := &Column{r: r, row: row}
	c.tag.what = Columntag
	c.tag.col = c
	c.tag.row = row
	r1 := r
	r1.Max.Y = r1.Min.Y + g.font.Height
	f := &File{}
	c.tag.init(f, r1, g.tagcols)
	f.b = []rune(coltagtext)
	c.tag.setselect(len(f.b), len(f.b))
	c.draw()
	return c
}

// draw paints the column's frame: its tag, the line under it and the
// background where no window is.
func (c *Column) draw() {
	g.screen.Draw(c.r, g.textcols[BACK], nil, draw.ZP)
	r1 := c.r
	r1.Max.Y = r1.Min.Y + g.font.Height
	c.tag.resize(r1)
	g.screen.Draw(draw.Rect(c.r.Min.X, r1.Max.Y, c.r.Max.X, r1.Max.Y+1), g.black, nil, draw.ZP)
}

// top is where windows start.
func (c *Column) top() int { return c.r.Min.Y + g.font.Height + 1 }

// minht is the least height a window can have: its tag and a line.
func minht() int { return 2*g.font.Height + 2 }

// add makes a window in the column, cloning clone if set, near y (or
// where there is most room when y < 0).
func (c *Column) add(w *Window, clone *Window, y int) *Window {
	r := c.r
	r.Min.Y = c.top()
	var at int // index for the new window
	if len(c.w) == 0 {
		at = 0
	} else {
		// Split the window at y, or the tallest.
		k := len(c.w) - 1
		if y >= 0 {
			for i, v := range c.w {
				if y >= v.r.Min.Y && y < v.r.Max.Y {
					k = i
				}
			}
		} else {
			for i, v := range c.w {
				if v.r.Dy() > c.w[k].r.Dy() {
					k = i
				}
			}
		}
		v := c.w[k]
		if y < v.r.Min.Y+minht() || y > v.r.Max.Y-minht() {
			y = v.r.Min.Y + v.r.Dy()/2
		}
		if v.r.Dy() < 2*minht() {
			// No room to split: share the column out.
			c.w = append(c.w, nil)
			copy(c.w[k+2:], c.w[k+1:])
			at = k + 1
			if w == nil {
				w = newWindow(clone, draw.Rect(r.Min.X, r.Max.Y, r.Max.X, r.Max.Y), nil)
			}
			w.col = c
			w.tag.col, w.body.col = c, c
			w.tag.row, w.body.row = c.row, c.row
			c.w[at] = w
			c.share()
			return w
		}
		v.resize(draw.Rect(v.r.Min.X, v.r.Min.Y, v.r.Max.X, y))
		r = draw.Rect(c.r.Min.X, y, c.r.Max.X, v.r.Max.Y)
		// Fix the old window's height to what it took.
		r.Max.Y = c.bottomOf(k)
		at = k + 1
	}
	if w == nil {
		w = newWindow(clone, r, nil)
	}
	w.col = c
	w.tag.col, w.body.col = c, c
	w.tag.row, w.body.row = c.row, c.row
	c.w = append(c.w, nil)
	copy(c.w[at+1:], c.w[at:])
	c.w[at] = w
	if at > 0 {
		r.Min.Y = c.w[at-1].r.Max.Y
	}
	w.resize(r)
	return w
}

// bottomOf is where the window after window k ends: the next window's
// top, or the column's bottom.
func (c *Column) bottomOf(k int) int {
	if k+1 < len(c.w) {
		return c.w[k+1].r.Min.Y
	}
	return c.r.Max.Y
}

// share lays the windows out with equal heights.
func (c *Column) share() {
	n := len(c.w)
	if n == 0 {
		return
	}
	y := c.top()
	h := (c.r.Max.Y - y) / n
	for i, w := range c.w {
		y1 := y + h
		if i == n-1 {
			y1 = c.r.Max.Y
		}
		w.resize(draw.Rect(c.r.Min.X, y, c.r.Max.X, y1))
		y = y1
	}
}

// close takes w out of the column; with dofree it is closed too.
func (c *Column) close(w *Window, dofree bool) {
	i := -1
	for k, v := range c.w {
		if v == w {
			i = k
		}
	}
	if i < 0 {
		return
	}
	r := w.r
	if dofree {
		w.close()
	}
	c.w = append(c.w[:i], c.w[i+1:]...)
	if len(c.w) == 0 {
		g.screen.Draw(r, g.textcols[BACK], nil, draw.ZP)
		return
	}
	// The window above (or below) takes the room.
	if i > 0 {
		v := c.w[i-1]
		v.resize(draw.Rect(v.r.Min.X, v.r.Min.Y, v.r.Max.X, r.Max.Y))
	} else {
		v := c.w[0]
		v.resize(draw.Rect(v.r.Min.X, r.Min.Y, v.r.Max.X, v.r.Max.Y))
	}
}

// closeall closes every window.
func (c *Column) closeall() {
	for _, w := range c.w {
		w.close()
	}
	c.w = nil
	c.tag.close()
}

// resize lays the column out in r, keeping the windows' proportions.
func (c *Column) resize(r draw.Rectangle) {
	old := c.r
	c.r = r
	c.draw()
	if len(c.w) == 0 {
		return
	}
	oh := max(1, old.Max.Y-(old.Min.Y+g.font.Height+1))
	nh := r.Max.Y - c.top()
	y := c.top()
	for i, w := range c.w {
		h := (w.r.Dy() * nh) / oh
		y1 := y + max(h, 0)
		if i == len(c.w)-1 {
			y1 = r.Max.Y
		}
		w.resize(draw.Rect(r.Min.X, y, r.Max.X, y1))
		y = y1
	}
}

// grow makes w bigger: a little (but 1), as big as can be with the
// others' tags showing (2), or the whole column (3).
func (c *Column) grow(w *Window, but int) {
	i := -1
	for k, v := range c.w {
		if v == w {
			i = k
		}
	}
	if i < 0 {
		return
	}
	n := len(c.w)
	tagh := g.font.Height + 1
	y := c.top()
	switch but {
	case 3:
		// Only w shows; the others keep their tags off the screen.
		for k, v := range c.w {
			if k == i {
				v.resize(draw.Rect(c.r.Min.X, y, c.r.Max.X, c.r.Max.Y))
			} else {
				v.r = draw.Rect(c.r.Min.X, c.r.Max.Y, c.r.Max.X, c.r.Max.Y)
				v.tag.all, v.body.all = v.r, v.r
				v.tag.fr.r, v.body.fr.r = v.r, v.r
			}
		}
		return
	case 2:
		// The others get a tag's height each.
		for k, v := range c.w {
			h := tagh
			if k == i {
				h = c.r.Max.Y - c.top() - (n-1)*tagh
			}
			y1 := y + h
			if k == n-1 {
				y1 = c.r.Max.Y
			}
			v.resize(draw.Rect(c.r.Min.X, y, c.r.Max.X, y1))
			y = y1
		}
		return
	}
	// Take a share from the neighbours.
	want := max(w.r.Dy()+(c.r.Max.Y-c.top())/4, minht())
	hs := make([]int, n)
	for k, v := range c.w {
		hs[k] = v.r.Dy()
	}
	need := want - hs[i]
	for k := 0; k < n && need > 0; k++ {
		if k == i {
			continue
		}
		give := min(need, max(0, hs[k]-tagh))
		hs[k] -= give
		need -= give
	}
	hs[i] = want - need
	for k, v := range c.w {
		y1 := y + hs[k]
		if k == n-1 {
			y1 = c.r.Max.Y
		}
		v.resize(draw.Rect(c.r.Min.X, y, c.r.Max.X, y1))
		y = y1
	}
}

// sort orders the windows by file name.
func (c *Column) sort() {
	sort.SliceStable(c.w, func(i, j int) bool { return c.w[i].body.file.name < c.w[j].body.file.name })
	c.share()
}

// which returns the text at p, if any.
func (c *Column) which(p draw.Point) *Text {
	if !p.In(c.r) {
		return nil
	}
	if p.In(c.tag.all) {
		return &c.tag
	}
	for _, w := range c.w {
		if p.In(w.r) {
			if p.Y < w.tag.all.Max.Y {
				return &w.tag
			}
			return &w.body
		}
	}
	return nil
}

// clean reports whether every window may be deleted.
func (c *Column) clean() bool {
	ok := true
	for _, w := range c.w {
		ok = w.clean(true) && ok
	}
	return ok
}
