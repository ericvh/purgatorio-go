package memdraw

// The ellipse code follows Inferno's: the residual error of (x, y) is
// e(x,y) = b²x² + a²y² - a²b², zero on the ellipse, negative inside.

type estate struct {
	a, x                                               int
	a2, b2, b2x, a2y, c1, c2, ee, dxe, dye, d2xe, d2ye int64
}

func newstate(s *estate, a, b int) *estate {
	s.x = 0
	s.a = a
	s.a2 = int64(a * a)
	s.b2 = int64(b * b)
	s.b2x = 0
	s.a2y = s.a2 * int64(b)
	s.c1 = -((s.a2 >> 2) + int64(a&1) + s.b2)
	s.c2 = -((s.b2 >> 2) + int64(b&1))
	s.ee = -s.a2y
	s.dxe = 0
	s.dye = s.ee << 1
	s.d2xe = s.b2 << 1
	s.d2ye = s.a2 << 1
	return s
}

// step returns the x coordinate of the rightmost pixel on the next scan
// line.
func (s *estate) step() int {
	for s.x < s.a {
		if s.ee+s.b2x <= s.c1 || s.ee+s.a2y <= s.c2 {
			s.dxe += s.d2xe
			s.ee += s.dxe
			s.b2x += s.b2
			s.x++
			continue
		}
		s.dye += s.d2ye
		s.ee += s.dye
		s.a2y -= s.a2
		if s.ee-s.a2y <= s.c2 {
			s.dxe += s.d2xe
			s.ee += s.dxe
			s.b2x += s.b2
			x := s.x
			s.x++
			return x
		}
		break
	}
	return s.x
}

type eparam struct {
	dst, src *Image
	c        Point
	t        int
	sp       Point
	disc     *Image
	op       int
}

// Ellipse draws an ellipse centred on c with semiaxes a and b and
// semithickness t, or filled if t < 0; sp in src maps to c (memellipse).
func Ellipse(dst *Image, c Point, a, b, t int, src *Image, sp Point, op int) {
	a, b = abs(a), abs(b)
	p := &eparam{dst: dst, src: src, c: c, t: t, sp: sp.Sub(c), op: op}
	u := (t << 1) * (a - b)
	if b < a && u > b*b || a < b && -u > a*a {
		var in estate
		bellipse(b, newstate(&in, a, b), p)
		return
	}
	var in, out estate
	var inb, y int
	if t < 0 {
		inb = -1
		y = b
		newstate(&out, a, y)
	} else {
		inb = b - t
		y = b + t
		newstate(&out, a+t, y)
	}
	if t > 0 {
		newstate(&in, a-t, inb)
	}
	inx := 0
	for ; y >= 0; y-- {
		outx := out.step()
		if y > inb {
			erect(-outx, y, outx, y, p)
			if y != 0 {
				erect(-outx, -y, outx, -y, p)
			}
			continue
		}
		if t > 0 {
			inx = in.step()
			if y == inb {
				inx = 0
			}
		} else if inx > outx {
			inx = outx
		}
		erect(inx, y, outx, y, p)
		if y != 0 {
			erect(inx, -y, outx, -y, p)
		}
		erect(-outx, y, -inx, y, p)
		if y != 0 {
			erect(-outx, -y, -inx, -y, p)
		}
		inx = outx + 1
	}
}

// bellipse brushes a very thick, skinny ellipse with a disc.
func bellipse(y int, s *estate, p *eparam) {
	t := p.t
	var err error
	p.disc, err = Alloc(Rect(-t, -t, t+1, t+1), GREY1)
	if err != nil {
		return
	}
	Ellipse(p.disc, ZP, t, t, -1, Opaque, ZP, p.op)
	oy := y
	ox := 0
	x := s.step()
	nx := x
	for {
		for nx == x {
			y--
			if y < 0 {
				break
			}
			nx = s.step()
		}
		y++
		eline(-x, -oy, -ox, -y, p)
		eline(ox, -oy, x, -y, p)
		eline(-x, y, -ox, oy, p)
		eline(ox, y, x, oy, p)
		ox = x + 1
		x = nx
		y--
		oy = y
		if oy <= 0 {
			break
		}
	}
}

// erect draws a rectangle with closed coordinates relative to the centre.
func erect(x0, y0, x1, y1 int, p *eparam) {
	r := Rect(p.c.X+x0, p.c.Y+y0, p.c.X+x1+1, p.c.Y+y1+1)
	Draw(p.dst, r, p.src, p.sp.Add(r.Min), Opaque, ZP, p.op)
}

// epoint draws a brushed point.
func epoint(x, y int, p *eparam) {
	p0 := Pt(p.c.X+x, p.c.Y+y)
	r := Rpt(p0.Add(p.disc.R.Min), p0.Add(p.disc.R.Max))
	Draw(p.dst, r, p.src, p.sp.Add(r.Min), p.disc, p.disc.R.Min, p.op)
}

// eline draws a brushed horizontal or vertical line.
func eline(x0, y0, x1, y1 int, p *eparam) {
	if x1 > x0+1 {
		erect(x0+1, y0-p.t, x1-1, y1+p.t, p)
	} else if y1 > y0+1 {
		erect(x0-p.t, y0+1, x1+p.t, y1-1, p)
	}
	epoint(x0, y0, p)
	if x1-x0 != 0 || y1-y0 != 0 {
		epoint(x1, y1, p)
	}
}

var corners = [4]Point{{1, 1}, {-1, 1}, {-1, -1}, {1, -1}}

// Arc draws the part of an ellipse between the rays at angles alpha and
// alpha+phi degrees, counterclockwise from the positive x axis (memarc).
// It masks a full ellipse with a wedge.
func Arc(dst *Image, c Point, a, b, t int, src *Image, sp Point, alpha, phi, op int) {
	a, b = abs(a), abs(b)
	w := max(t, 0)
	alpha = -alpha // compensate for upside-down coordinates
	phi = -phi
	beta := alpha + phi
	if phi < 0 {
		alpha, beta = beta, alpha
		phi = -phi
	}
	if phi >= 360 {
		Ellipse(dst, c, a, b, t, src, sp, op)
		return
	}
	for alpha < 0 {
		alpha += 360
	}
	for beta < 0 {
		beta += 360
	}
	c1 := alpha / 90 & 3 // number of nearest corner
	c2 := beta / 90 & 3
	rect := Rect(-a-w, -b-w, a+w+1, b+w+1)
	m := max(rect.Max.X, rect.Max.Y) // inradius of bounding square
	m1 := (m + ICOSSCALE - 1) >> 10
	m = m1 << 10 // assure m1*cossin is inside
	var bnd []Point
	bnd = append(bnd, Pt(0, 0))
	cx, sy := Icossin(alpha)
	bnd = append(bnd, Pt(cx, sy).Mul(m1))
	for {
		bnd = append(bnd, corners[c1].Mul(m))
		if c1 == c2 && phi < 180 {
			break
		}
		c1 = (c1 + 1) & 3
		phi -= 90
	}
	cx, sy = Icossin(beta)
	bnd = append(bnd, Pt(cx, sy).Mul(m1))

	wedge, err := Alloc(rect, GREY1)
	if err != nil {
		return
	}
	FillPoly(wedge, bnd, ^0, Opaque, ZP, S)
	figure, err := Alloc(rect, GREY1)
	if err != nil {
		return
	}
	Ellipse(figure, ZP, a, b, t, Opaque, ZP, S)
	mask, err := Alloc(rect, GREY1)
	if err != nil {
		return
	}
	ImageDraw(mask, rect, figure, rect.Min, wedge, rect.Min, S)
	c = c.Sub(dst.R.Min)
	Draw(dst, dst.R, src, sp.Sub(c), mask, ZP.Sub(c), op)
}
