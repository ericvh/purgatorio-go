package memdraw

import "math"

const (
	arrow1 = 8
	arrow2 = 10
	arrow3 = 3
)

// sinus[d] is sin(d°) scaled by ICOSSCALE, rounded.
var sinus [91]int

// atansin[i] and atancos[i] are sin and cos of atan(i/100) scaled by
// ICOSSCALE, rounded.
var atansin, atancos [102]int

func init() {
	for d := range sinus {
		sinus[d] = int(math.Floor(math.Sin(math.Pi*float64(d)/180)*ICOSSCALE + 0.5))
	}
	for i := range atansin {
		a := math.Atan(float64(i) / 100)
		atansin[i] = int(math.Sin(a)*ICOSSCALE + 0.5)
		atancos[i] = int(math.Cos(a)*ICOSSCALE + 0.5)
	}
}

// Icossin returns ICOSSCALE times the cosine and sine of deg degrees.
func Icossin(deg int) (cos, sin int) {
	deg %= 360
	if deg < 0 {
		deg += 360
	}
	sinsign, cossign := 1, 1
	switch deg / 90 {
	case 2:
		sinsign, cossign = -1, -1
		deg -= 180
	case 3:
		sinsign, cossign = -1, -1
		deg -= 180
		fallthrough
	case 1:
		deg = 180 - deg
		cossign = -cossign
	}
	return cossign * sinus[90-deg], sinsign * sinus[deg]
}

// Icossin2 returns ICOSSCALE times the cosine and sine of the angle of
// the vector (x, y).
func Icossin2(x, y int) (cos, sin int) {
	if x == 0 {
		if y >= 0 {
			return 0, ICOSSCALE
		}
		return 0, -ICOSSCALE
	}
	sinsign, cossign := 1, 1
	if x < 0 {
		cossign = -1
		x = -x
	}
	if y < 0 {
		sinsign = -1
		y = -y
	}
	var tan int
	var st, ct []int
	if y > x {
		tan = 1000 * x / y
		st, ct = atancos[tan/10:], atansin[tan/10:]
	} else {
		tan = 1000 * y / x
		st, ct = atansin[tan/10:], atancos[tan/10:]
	}
	rem := tan - (tan/10)*10
	return cossign * (ct[0] + (ct[1]-ct[0])*rem/10), sinsign * (st[0] + (st[1]-st[0])*rem/10)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func membrush(radius int) *Image {
	brush, err := Alloc(Rect(0, 0, 2*radius+1, 2*radius+1), Opaque.Chan)
	if err != nil {
		return nil
	}
	Ellipse(brush, Pt(radius, radius), radius, radius, -1, Opaque, Pt(radius, radius), S)
	return brush
}

func discend(p Point, radius int, dst, src *Image, dsrc Point, op int) {
	disc := membrush(radius)
	if disc == nil {
		return
	}
	r := Rect(p.X-radius, p.Y-radius, p.X+radius+1, p.Y+radius+1)
	Draw(dst, r, src, r.Min.Add(dsrc), disc, Pt(0, 0), op)
}

func arrowend(tip Point, pp []Point, end, sin, cos, radius int) {
	var x1, x2, x3 int
	if end == Endarrow {
		x1, x2, x3 = arrow1, arrow2, arrow3
	} else {
		x1 = (end >> 5) & 0x1FF  // distance along line from end of line to tip
		x2 = (end >> 14) & 0x1FF // distance along line from barb to tip
		x3 = (end >> 23) & 0x1FF // distance perpendicular from edge of line to barb
	}
	pp[0] = Pt(tip.X+((2*radius+1)*sin/2-x1*cos), tip.Y-((2*radius+1)*cos/2+x1*sin))
	pp[1] = Pt(tip.X+((2*radius+2*x3+1)*sin/2-x2*cos), tip.Y-((2*radius+2*x3+1)*cos/2+x2*sin))
	pp[2] = tip
	pp[3] = Pt(tip.X+(-(2*radius+2*x3+1)*sin/2-x2*cos), tip.Y-(-(2*radius+2*x3+1)*cos/2+x2*sin))
	pp[4] = Pt(tip.X+(-(2*radius+1)*sin/2-x1*cos), tip.Y+((2*radius+1)*cos/2-x1*sin))
}

// imageLine draws a line of half-width radius from p0 to p1 on a plain
// image, touching only pixels in clipr (_memimageline).
func imageLine(dst *Image, p0, p1 Point, end0, end1, radius int, src *Image, sp Point, clipr Rectangle, op int) {
	if radius < 0 {
		return
	}
	if !RectClip(&clipr, dst.R) || !RectClip(&clipr, dst.Clipr) {
		return
	}
	d := sp.Sub(p0)
	if !RectClip(&clipr, src.Clipr.Sub(d)) {
		return
	}
	if src.Flags&Frepl == 0 && !RectClip(&clipr, src.R.Sub(d)) {
		return
	}
	// Only the vertical case handles degenerate lines (p0 == p1).
	hor := abs(p1.X-p0.X) > abs(p1.Y-p0.Y)
	if (hor && p0.X > p1.X) || (!hor && p0.Y > p1.Y) {
		p0, p1 = p1, p0
		end0, end1 = end1, end0
	}
	if (p0.X == p1.X || p0.Y == p1.Y) && end0&0x1F == Endsquare && end1&0x1F == Endsquare {
		r := Rpt(p0, p1)
		if p0.X == p1.X {
			r.Min.X -= radius
			r.Max.X += radius + 1
			r.Max.Y++
		} else {
			r.Min.Y -= radius
			r.Max.Y += radius + 1
			r.Max.X++
		}
		oclipr := dst.Clipr
		dst.Clipr = clipr
		sp = r.Min.Add(d)
		ImageDraw(dst, r, src, sp, Opaque, sp, op)
		dst.Clipr = oclipr
		return
	}
	// A thick line is a filled polygon.
	cos, sin := Icossin2(p1.X-p0.X, p1.Y-p0.Y)
	dx := (sin * (2*radius + 1)) / 2
	dy := (cos * (2*radius + 1)) / 2
	var pts [10]Point
	pp := 0
	oclipr := dst.Clipr
	dst.Clipr = clipr
	q := Pt(ICOSSCALE*p0.X+ICOSSCALE/2-cos/2, ICOSSCALE*p0.Y+ICOSSCALE/2-sin/2)
	switch end0 & 0x1F {
	case Endarrow:
		arrowend(q, pts[pp:], end0, -sin, -cos, radius)
		fillPolySc(dst, pts[:5], ^0, src, pts[0].Add(d.Mul(ICOSSCALE)), 1, 10, 1, op)
		pts[pp+1] = pts[pp+4]
		pp += 2
	case Enddisc:
		discend(p0, radius, dst, src, d, op)
		fallthrough
	default:
		pts[pp] = Pt(q.X-dx, q.Y+dy)
		pts[pp+1] = Pt(q.X+dx, q.Y-dy)
		pp += 2
	}
	q = Pt(ICOSSCALE*p1.X+ICOSSCALE/2+cos/2, ICOSSCALE*p1.Y+ICOSSCALE/2+sin/2)
	switch end1 & 0x1F {
	case Endarrow:
		arrowend(q, pts[pp:], end1, sin, cos, radius)
		fillPolySc(dst, pts[pp:pp+5], ^0, src, pts[0].Add(d.Mul(ICOSSCALE)), 1, 10, 1, op)
		pts[pp+1] = pts[pp+4]
		pp += 2
	case Enddisc:
		discend(p1, radius, dst, src, d, op)
		fallthrough
	default:
		pts[pp] = Pt(q.X+dx, q.Y-dy)
		pts[pp+1] = Pt(q.X-dx, q.Y+dy)
		pp += 2
	}
	fillPolySc(dst, pts[:pp], ^0, src, pts[0].Add(d.Mul(ICOSSCALE)), 0, 10, 1, op)
	dst.Clipr = oclipr
}

// ImageLine draws a line on a plain image (memimageline).
func ImageLine(dst *Image, p0, p1 Point, end0, end1, radius int, src *Image, sp Point, op int) {
	imageLine(dst, p0, p1, end0, end1, radius, src, sp, dst.Clipr, op)
}

// LineEndSize is how far an arrowhead end reaches beyond the line.
func LineEndSize(end int) int {
	if end&0x3F != Endarrow {
		return 0
	}
	if end == Endarrow {
		return arrow3
	}
	return (end >> 23) & 0x1FF
}

// LineBBox is a conservative bounding box of a line (memlinebbox).
func LineBBox(p0, p1 Point, end0, end1, radius int) Rectangle {
	r := Rect(10000000, 10000000, -10000000, -10000000)
	extra := max(LineEndSize(end0), LineEndSize(end1))
	r1 := Rpt(p0, p1).Canon().Inset(-(radius + extra))
	addbbox := func(p Point) {
		r.Min.X = min(r.Min.X, p.X)
		r.Min.Y = min(r.Min.Y, p.Y)
		r.Max.X = max(r.Max.X, p.X+1)
		r.Max.Y = max(r.Max.Y, p.Y+1)
	}
	addbbox(r1.Min)
	addbbox(r1.Max)
	return r
}

// Line draws a line on an image that may be a window (memline).
func Line(dst *Image, p0, p1 Point, end0, end1, radius int, src *Image, sp Point, op int) {
	line(dst, p0, p1, end0, end1, radius, src, sp, dst.Clipr, op)
}

func line(dst *Image, p0, p1 Point, end0, end1, radius int, src *Image, sp Point, clipr Rectangle, op int) {
	if radius < 0 || src.Layer != nil {
		return
	}
	srcclipped := false
	for {
		dl := dst.Layer
		if dl == nil {
			imageLine(dst, p0, p1, end0, end1, radius, src, sp, clipr, op)
			return
		}
		if !srcclipped {
			d := sp.Sub(p0)
			if !RectClip(&clipr, src.Clipr.Sub(d)) {
				return
			}
			if src.Flags&Frepl == 0 && !RectClip(&clipr, src.R.Sub(d)) {
				return
			}
			srcclipped = true
		}
		p0 = p0.Add(dl.Delta)
		p1 = p1.Add(dl.Delta)
		clipr = clipr.Add(dl.Delta)
		if dl.Clear {
			dst = dl.Screen.Image
			continue
		}
		r := LineBBox(p0, p1, end0, end1, radius)
		if !RectClip(&r, clipr) {
			return
		}
		delta := dl.Delta
		layerOp(func(d *Image, screenr, clipr Rectangle, insave bool) {
			if insave && dl.Save == nil {
				return
			}
			if !RectClip(&clipr, screenr) {
				return
			}
			q0, q1 := p0, p1
			if insave {
				q0, q1 = p0.Sub(delta), p1.Sub(delta)
				clipr = clipr.Sub(delta)
			}
			line(d, q0, q1, end0, end1, radius, src, sp, clipr, op)
		}, dst, r, r)
		return
	}
}

// Poly draws the connected lines through vert (mempoly).
func Poly(dst *Image, vert []Point, end0, end1, radius int, src *Image, sp Point, op int) {
	if len(vert) < 2 {
		return
	}
	d := sp.Sub(vert[0])
	for i := 1; i < len(vert); i++ {
		e0, e1 := Enddisc, Enddisc
		if i == 1 {
			e0 = end0
		}
		if i == len(vert)-1 {
			e1 = end1
		}
		Line(dst, vert[i-1], vert[i], e0, e1, radius, src, d.Add(vert[i-1]), op)
	}
}
