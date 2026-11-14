package main

import (
	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// The Row is acme's whole screen: a tag and columns (acme's row.b).
type Row struct {
	r   draw.Rectangle
	tag Text
	col []*Column
}

const rowtagtext = "Newcol Kill Putall Dump Exit "

func newRow(r draw.Rectangle) *Row {
	row := &Row{r: r}
	g.screen.Draw(r, g.textcols[BACK], nil, draw.ZP)
	row.tag.what = Rowtag
	row.tag.row = row
	r1 := r
	r1.Max.Y = r1.Min.Y + g.font.Height
	f := &File{}
	row.tag.init(f, r1, g.tagcols)
	f.b = []rune(rowtagtext)
	row.tag.setselect(len(f.b), len(f.b))
	row.tag.redraw()
	g.screen.Draw(draw.Rect(r.Min.X, r1.Max.Y, r.Max.X, r1.Max.Y+1), g.black, nil, draw.ZP)
	return row
}

// top is where columns start.
func (row *Row) top() int { return row.r.Min.Y + g.font.Height + 1 }

// add makes a column at x, or where there is most room when x < 0.
func (row *Row) add(x int) *Column {
	r := row.r
	r.Min.Y = row.top()
	var at int
	if len(row.col) > 0 {
		k := len(row.col) - 1
		if x >= 0 {
			for i, c := range row.col {
				if x >= c.r.Min.X && x < c.r.Max.X {
					k = i
				}
			}
		} else {
			for i, c := range row.col {
				if c.r.Dx() > row.col[k].r.Dx() {
					k = i
				}
			}
		}
		c := row.col[k]
		if x < c.r.Min.X+50 || x > c.r.Max.X-50 {
			x = c.r.Min.X + c.r.Dx()/2
		}
		r = draw.Rect(x, r.Min.Y, c.r.Max.X, r.Max.Y)
		c.resize(draw.Rect(c.r.Min.X, c.r.Min.Y, x-1, c.r.Max.Y))
		g.screen.Draw(draw.Rect(x-1, r.Min.Y, x, r.Max.Y), g.black, nil, draw.ZP)
		at = k + 1
	}
	c := newColumn(row, r)
	row.col = append(row.col, nil)
	copy(row.col[at+1:], row.col[at:])
	row.col[at] = c
	return c
}

// close takes column c away; the column to its left takes its room.
func (row *Row) close(c *Column, dofree bool) {
	i := -1
	for k, x := range row.col {
		if x == c {
			i = k
		}
	}
	if i < 0 {
		return
	}
	r := c.r
	if dofree {
		c.closeall()
	}
	row.col = append(row.col[:i], row.col[i+1:]...)
	if len(row.col) == 0 {
		g.screen.Draw(r, g.textcols[BACK], nil, draw.ZP)
		return
	}
	if i > 0 {
		v := row.col[i-1]
		v.resize(draw.Rect(v.r.Min.X, v.r.Min.Y, r.Max.X, v.r.Max.Y))
	} else {
		v := row.col[0]
		v.resize(draw.Rect(r.Min.X, v.r.Min.Y, v.r.Max.X, v.r.Max.Y))
	}
}

// resize lays the row out in r.
func (row *Row) resize(r draw.Rectangle) {
	or := row.r
	row.r = r
	g.screen.Draw(r, g.textcols[BACK], nil, draw.ZP)
	r1 := r
	r1.Max.Y = r1.Min.Y + g.font.Height
	row.tag.resize(r1)
	g.screen.Draw(draw.Rect(r.Min.X, r1.Max.Y, r.Max.X, r1.Max.Y+1), g.black, nil, draw.ZP)
	ow := max(1, or.Dx())
	x := r.Min.X
	for i, c := range row.col {
		x1 := r.Min.X + (c.r.Max.X-or.Min.X)*r.Dx()/ow
		if i == len(row.col)-1 {
			x1 = r.Max.X
		}
		c.resize(draw.Rect(x, row.top(), x1, r.Max.Y))
		if i < len(row.col)-1 {
			g.screen.Draw(draw.Rect(x1-1, row.top(), x1, r.Max.Y), g.black, nil, draw.ZP)
		}
		x = x1
	}
}

// which returns the text at p.
func (row *Row) which(p draw.Point) *Text {
	if p.In(row.tag.all) {
		return &row.tag
	}
	for _, c := range row.col {
		if t := c.which(p); t != nil {
			return t
		}
	}
	return nil
}

// whichcol returns the column at p.
func (row *Row) whichcol(p draw.Point) *Column {
	for _, c := range row.col {
		if p.In(c.r) {
			return c
		}
	}
	return nil
}

// windows returns every window.
func (row *Row) windows() []*Window {
	var ws []*Window
	for _, c := range row.col {
		ws = append(ws, c.w...)
	}
	return ws
}

// lookid finds a window by number.
func (row *Row) lookid(id int) *Window {
	for _, w := range row.windows() {
		if w.id == id {
			return w
		}
	}
	return nil
}

// lookfile finds a window by file name.
func (row *Row) lookfile(name string) *Window {
	for _, w := range row.windows() {
		n := w.body.file.name
		if n == name || w.isdir && n == name+"/" {
			return w
		}
	}
	return nil
}

// clean reports whether all windows may go.
func (row *Row) clean() bool {
	ok := true
	for _, c := range row.col {
		ok = c.clean() && ok
	}
	return ok
}
