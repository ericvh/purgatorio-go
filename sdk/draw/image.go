package draw

import (
	"errors"
	"io"
)

// Image is an image held by the draw device.
type Image struct {
	Display *Display
	ID      int
	Chan    Chan
	Depth   int
	Repl    bool
	R       Rectangle
	Clipr   Rectangle
	Screen  *Screen // the screen, if the image is a window
}

// Free releases the image in the device.
func (i *Image) Free() error {
	if i == nil || i.Display == nil {
		return nil
	}
	b, err := i.Display.bufimage(1 + 4)
	if err != nil {
		return err
	}
	b[0] = 'f'
	putl(b[1:], i.ID)
	i.Display = nil
	return nil
}

// ReplClipr sets the image's replication and clipping rectangle.
func (i *Image) ReplClipr(repl bool, clipr Rectangle) error {
	b, err := i.Display.bufimage(1 + 4 + 1 + 4*4)
	if err != nil {
		return err
	}
	b[0] = 'c'
	putl(b[1:], i.ID)
	if repl {
		b[5] = 1
	}
	putr(b[6:], clipr)
	i.Repl, i.Clipr = repl, clipr
	return nil
}

// Draw draws src through mask onto r of i with SoverD; p in src and mask
// corresponds to r.Min. A nil mask is opaque.
func (i *Image) Draw(r Rectangle, src, mask *Image, p Point) error {
	return i.GenDrawOp(r, src, p, mask, p, SoverD)
}

// DrawOp is Draw with an operator.
func (i *Image) DrawOp(r Rectangle, src, mask *Image, p Point, op Op) error {
	return i.GenDrawOp(r, src, p, mask, p, op)
}

// GenDraw is Draw with separate alignment points for src and mask.
func (i *Image) GenDraw(r Rectangle, src *Image, p0 Point, mask *Image, p1 Point) error {
	return i.GenDrawOp(r, src, p0, mask, p1, SoverD)
}

// GenDrawOp is GenDraw with an operator.
func (i *Image) GenDrawOp(r Rectangle, src *Image, p0 Point, mask *Image, p1 Point, op Op) error {
	d := i.Display
	if mask == nil {
		mask = d.Opaque
	}
	if err := d.setop(op); err != nil {
		return err
	}
	b, err := d.bufimage(1 + 4 + 4 + 4 + 4*4 + 2*4 + 2*4)
	if err != nil {
		return err
	}
	b[0] = 'd'
	putl(b[1:], i.ID)
	putl(b[5:], src.ID)
	putl(b[9:], mask.ID)
	putr(b[13:], r)
	putp(b[29:], p0)
	putp(b[37:], p1)
	return nil
}

// Line draws a line of half-width radius from p0 to p1 with the given
// ends; sp in src corresponds to p0.
func (i *Image) Line(p0, p1 Point, end0, end1, radius int, src *Image, sp Point) error {
	return i.LineOp(p0, p1, end0, end1, radius, src, sp, SoverD)
}

// LineOp is Line with an operator.
func (i *Image) LineOp(p0, p1 Point, end0, end1, radius int, src *Image, sp Point, op Op) error {
	d := i.Display
	if err := d.setop(op); err != nil {
		return err
	}
	b, err := d.bufimage(1 + 4 + 2*4 + 2*4 + 4 + 4 + 4 + 4 + 2*4)
	if err != nil {
		return err
	}
	b[0] = 'L'
	putl(b[1:], i.ID)
	putp(b[5:], p0)
	putp(b[13:], p1)
	putl(b[21:], end0)
	putl(b[25:], end1)
	putl(b[29:], radius)
	putl(b[33:], src.ID)
	putp(b[37:], sp)
	return nil
}

func (i *Image) doellipse(cmd byte, c Point, a, b, thick int, src *Image, sp Point, alpha, phi int, arc bool, op Op) error {
	d := i.Display
	if err := d.setop(op); err != nil {
		return err
	}
	m, err := d.bufimage(1 + 4 + 4 + 2*4 + 4 + 4 + 4 + 2*4 + 2*4)
	if err != nil {
		return err
	}
	m[0] = cmd
	putl(m[1:], i.ID)
	putl(m[5:], src.ID)
	putp(m[9:], c)
	putl(m[17:], a)
	putl(m[21:], b)
	putl(m[25:], thick)
	putp(m[29:], sp)
	if arc {
		putu(m[37:], uint32(alpha)|1<<31)
		putl(m[41:], phi)
	}
	return nil
}

// Ellipse draws an ellipse centred on c with semiaxes a and b and
// half-thickness thick.
func (i *Image) Ellipse(c Point, a, b, thick int, src *Image, sp Point) error {
	return i.doellipse('e', c, a, b, thick, src, sp, 0, 0, false, SoverD)
}

// FillEllipse draws a filled ellipse.
func (i *Image) FillEllipse(c Point, a, b int, src *Image, sp Point) error {
	return i.doellipse('E', c, a, b, 0, src, sp, 0, 0, false, SoverD)
}

// Arc draws the part of an ellipse from alpha through alpha+phi degrees,
// counterclockwise from the x axis.
func (i *Image) Arc(c Point, a, b, thick int, src *Image, sp Point, alpha, phi int) error {
	return i.doellipse('e', c, a, b, thick, src, sp, alpha, phi, true, SoverD)
}

// FillArc draws a filled wedge of an ellipse.
func (i *Image) FillArc(c Point, a, b int, src *Image, sp Point, alpha, phi int) error {
	return i.doellipse('E', c, a, b, 0, src, sp, alpha, phi, true, SoverD)
}

func addcoord(b []byte, oldx, newx int) []byte {
	dx := newx - oldx
	if uint(dx+0x40) <= 0x7F {
		return append(b, byte(dx&0x7F))
	}
	return append(b, 0x80|byte(newx&0x7F), byte(newx>>7), byte(newx>>15))
}

func (i *Image) dopoly(cmd byte, p []Point, end0, end1, radius int, src *Image, sp Point, op Op) error {
	if len(p) == 0 {
		return nil
	}
	var t []byte
	ox, oy := 0, 0
	for _, q := range p {
		t = addcoord(t, ox, q.X)
		ox = q.X
		t = addcoord(t, oy, q.Y)
		oy = q.Y
	}
	d := i.Display
	if err := d.setop(op); err != nil {
		return err
	}
	b, err := d.bufimage(1 + 4 + 2 + 4 + 4 + 4 + 4 + 2*4 + len(t))
	if err != nil {
		return err
	}
	b[0] = cmd
	putl(b[1:], i.ID)
	puts(b[5:], len(p)-1)
	putl(b[7:], end0)
	putl(b[11:], end1)
	putl(b[15:], radius)
	putl(b[19:], src.ID)
	putp(b[23:], sp)
	copy(b[31:], t)
	return nil
}

// Poly draws lines through the points p.
func (i *Image) Poly(p []Point, end0, end1, radius int, src *Image, sp Point) error {
	return i.dopoly('p', p, end0, end1, radius, src, sp, SoverD)
}

// FillPoly fills the polygon p; wind is ^0 for the nonzero winding rule,
// 1 for odd/even.
func (i *Image) FillPoly(p []Point, wind int, src *Image, sp Point) error {
	return i.dopoly('P', p, wind, 0, 0, src, sp, SoverD)
}

// Border draws a border of width n inside r (outside if n < 0).
func (i *Image) Border(r Rectangle, n int, color *Image, sp Point) error {
	if n < 0 {
		r = r.Inset(n)
		sp = sp.Add(Pt(n, n))
		n = -n
	}
	for _, s := range []Rectangle{
		Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+n),
		Rect(r.Min.X, r.Max.Y-n, r.Max.X, r.Max.Y),
		Rect(r.Min.X, r.Min.Y+n, r.Min.X+n, r.Max.Y-n),
		Rect(r.Max.X-n, r.Min.Y+n, r.Max.X, r.Max.Y-n),
	} {
		if err := i.Draw(s, color, nil, sp.Add(s.Min.Sub(r.Min))); err != nil {
			return err
		}
	}
	return nil
}

// Load replaces r of the image with data in the image's format, rows of
// BytesPerLine bytes (loadimage).
func (i *Image) Load(r Rectangle, data []byte) (int, error) {
	if !r.In(i.R) {
		return 0, errors.New("loadimage: bad rectangle")
	}
	bpl := BytesPerLine(r, i.Depth)
	n := bpl * r.Dy()
	if n > len(data) {
		return 0, errors.New("loadimage: insufficient data")
	}
	d := i.Display
	chunk := bufsize - 64
	for done := 0; r.Dy() > 0; {
		dy := chunk / bpl
		if dy <= 0 {
			return 0, errors.New("loadimage: image too wide for buffer")
		}
		dy = min(dy, r.Dy())
		b, err := d.bufimage(21 + dy*bpl)
		if err != nil {
			return 0, err
		}
		b[0] = 'y'
		putl(b[1:], i.ID)
		putr(b[5:], Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+dy))
		copy(b[21:], data[done:done+dy*bpl])
		done += dy * bpl
		r.Min.Y += dy
	}
	return n, nil
}

// Unload returns the pixels of r (unloadimage).
func (i *Image) Unload(r Rectangle) ([]byte, error) {
	if !r.In(i.R) {
		return nil, errors.New("unloadimage: bad rectangle")
	}
	d := i.Display
	bpl := BytesPerLine(r, i.Depth)
	out := make([]byte, 0, bpl*r.Dy())
	for r.Dy() > 0 {
		dy := max(1, min(r.Dy(), 8000/max(bpl, 1)))
		b, err := d.bufimage(1 + 4 + 4*4)
		if err != nil {
			return nil, err
		}
		b[0] = 'r'
		putl(b[1:], i.ID)
		putr(b[5:], Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+dy))
		if err := d.doflush(); err != nil {
			return nil, err
		}
		chunk := make([]byte, bpl*dy)
		n, err := d.data.Read(chunk)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if n != len(chunk) {
			return nil, errors.New("unloadimage: short read")
		}
		out = append(out, chunk...)
		r.Min.Y += dy
	}
	return out, nil
}
