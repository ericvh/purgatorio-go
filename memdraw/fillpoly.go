package memdraw

import "sort"

// seg is one edge of a polygon being scan converted.
type seg struct {
	p0, p1                       Point
	num, den, dz, dzrem, z, zerr int
	d                            int
}

func fillline(dst *Image, left, right, y int, src *Image, p Point, op int) {
	r := Rect(left, y, right, y+1)
	p.X += left
	p.Y += y
	Draw(dst, r, src, p, Opaque, p, op)
}

func fillpoint(dst *Image, x, y int, src *Image, p Point, op int) {
	r := Rect(x, y, x+1, y+1)
	p.X += x
	p.Y += y
	Draw(dst, r, src, p, Opaque, p, op)
}

// FillPoly fills the polygon vert with src, using winding rule w: ^0
// for nonzero winding, 1 for odd/even (memfillpoly).
func FillPoly(dst *Image, vert []Point, w int, src *Image, sp Point, op int) {
	fillPolySc(dst, vert, w, src, sp, 0, 0, 0, op)
}

// fillPolySc fills a polygon whose vertices are fixed point with
// fixshift fraction bits; detail also fills thin parts by scanning in y.
func fillPolySc(dst *Image, vert []Point, w int, src *Image, sp Point, detail, fixshift, clipped, op int) {
	nvert := len(vert)
	if nvert == 0 {
		return
	}
	segtab := make([]seg, nvert+1)
	sp.X = (sp.X - vert[0].X) >> fixshift
	sp.Y = (sp.Y - vert[0].Y) >> fixshift
	p0 := vert[nvert-1]
	if fixshift == 0 {
		p0.X <<= 1
		p0.Y <<= 1
	}
	for i := 0; i < nvert; i++ {
		segtab[i].p0 = p0
		p0 = vert[i]
		if fixshift == 0 {
			p0.X <<= 1
			p0.Y <<= 1
		}
		segtab[i].p1 = p0
		segtab[i].d = 1
	}
	if fixshift == 0 {
		fixshift = 1
	}
	xscan(dst, segtab[:nvert], w, src, sp, detail, fixshift, op)
	if detail != 0 {
		yscan(dst, segtab[:nvert], w, src, sp, fixshift, op)
	}
}

func mod(x, y int) int {
	z := x % y
	if (z^y) > 0 || z == 0 {
		return z
	}
	return z + y
}

func sdiv(x, y int) int {
	if (x^y) >= 0 || x == 0 {
		return x / y
	}
	return (x+((y>>30)|1))/y - 1
}

func smuldivmod(x, y, z int) (int, int) {
	if x == 0 || y == 0 {
		return 0, 0
	}
	vx := int64(x) * int64(y)
	m := vx % int64(z)
	if m < 0 {
		m += int64(z)
	}
	if (vx < 0) == (z < 0) {
		return int(vx / int64(z)), int(m)
	}
	return int(-((-vx) / int64(z))), int(m)
}

// zsort sorts the active edges by z; they are usually almost sorted.
func zsort(segs []*seg) {
	if len(segs) < 20 {
		for q := len(segs) - 1; ; q-- {
			done := true
			for p := 0; p < q; p++ {
				if segs[p].z > segs[p+1].z {
					segs[p], segs[p+1] = segs[p+1], segs[p]
					done = false
				}
			}
			if done {
				break
			}
		}
		return
	}
	for p := 0; p < len(segs)-1; p++ {
		if segs[p].z > segs[p+1].z {
			sort.SliceStable(segs, func(i, j int) bool { return segs[i].z < segs[j].z })
			break
		}
	}
}

func xscan(dst *Image, segtab []seg, wind int, src *Image, sp Point, detail, fixshift, op int) {
	var all []*seg
	for i := range segtab {
		s := &segtab[i]
		if s.p0.Y == s.p1.Y {
			continue
		}
		if s.p0.Y > s.p1.Y {
			s.p0, s.p1 = s.p1, s.p0
			s.d = -s.d
		}
		s.num = s.p1.X - s.p0.X
		s.den = s.p1.Y - s.p0.Y
		s.dz = sdiv(s.num, s.den) << fixshift
		s.dzrem = mod(s.num, s.den) << fixshift
		s.dz += sdiv(s.dzrem, s.den)
		s.dzrem = mod(s.dzrem, s.den)
		all = append(all, s)
	}
	if len(all) == 0 {
		return
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].p0.Y < all[j].p0.Y })

	onehalf := 0
	if fixshift != 0 {
		onehalf = 1 << (fixshift - 1)
	}
	minx, maxx := dst.Clipr.Min.X, dst.Clipr.Max.X
	y := all[0].p0.Y
	if y < dst.Clipr.Min.Y<<fixshift {
		y = dst.Clipr.Min.Y << fixshift
	}
	iy := (y + onehalf) >> fixshift
	y = iy<<fixshift + onehalf
	maxy := dst.Clipr.Max.Y << fixshift

	var active []*seg
	next := 0
	for y < maxy {
		q := active[:0]
		for _, s := range active {
			if s.p1.Y < y {
				continue
			}
			s.z += s.dz
			s.zerr += s.dzrem
			if s.zerr >= s.den {
				s.z++
				s.zerr -= s.den
			}
			q = append(q, s)
		}
		p := next
		for ; p < len(all); p++ {
			s := all[p]
			if s.p0.Y >= y {
				break
			}
			if s.p1.Y < y {
				continue
			}
			z, zerr := smuldivmod(y-s.p0.Y, s.num, s.den)
			s.z = s.p0.X + z
			s.zerr = zerr
			q = append(q, s)
		}
		active = q
		next = p
		if len(active) == 0 {
			if next >= len(all) {
				break
			}
			iy = (all[next].p0.Y + onehalf) >> fixshift
			y = iy<<fixshift + onehalf
			continue
		}
		zsort(active)
		for p := 0; p < len(active); p++ {
			cnt := 0
			x := active[p].z
			xerr := active[p].zerr
			xden := active[p].den
			ix := (x + onehalf) >> fixshift
			if ix >= maxx {
				break
			}
			if ix < minx {
				ix = minx
			}
			cnt += active[p].d
			p++
			for {
				if p == len(active) {
					return // fill to infinity
				}
				cnt += active[p].d
				if cnt&wind == 0 {
					break
				}
				p++
			}
			x2 := active[p].z
			ix2 := (x2 + onehalf) >> fixshift
			if ix2 <= minx {
				continue
			}
			if ix2 > maxx {
				ix2 = maxx
			}
			if ix == ix2 && detail != 0 {
				if xerr*active[p].den+active[p].zerr*xden > active[p].den*xden {
					x++
				}
				ix = (x + x2) >> (fixshift + 1)
				ix2 = ix + 1
			}
			fillline(dst, ix, ix2, iy, src, sp, op)
		}
		y += 1 << fixshift
		iy++
	}
}

func yscan(dst *Image, segtab []seg, wind int, src *Image, sp Point, fixshift, op int) {
	var all []*seg
	for i := range segtab {
		s := &segtab[i]
		if s.p0.X == s.p1.X {
			continue
		}
		if s.p0.X > s.p1.X {
			s.p0, s.p1 = s.p1, s.p0
			s.d = -s.d
		}
		s.num = s.p1.Y - s.p0.Y
		s.den = s.p1.X - s.p0.X
		s.dz = sdiv(s.num, s.den) << fixshift
		s.dzrem = mod(s.num, s.den) << fixshift
		s.dz += sdiv(s.dzrem, s.den)
		s.dzrem = mod(s.dzrem, s.den)
		all = append(all, s)
	}
	if len(all) == 0 {
		return
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].p0.X < all[j].p0.X })

	onehalf := 0
	if fixshift != 0 {
		onehalf = 1 << (fixshift - 1)
	}
	miny, maxy := dst.Clipr.Min.Y, dst.Clipr.Max.Y
	x := all[0].p0.X
	if x < dst.Clipr.Min.X<<fixshift {
		x = dst.Clipr.Min.X << fixshift
	}
	ix := (x + onehalf) >> fixshift
	x = ix<<fixshift + onehalf
	maxx := dst.Clipr.Max.X << fixshift

	var active []*seg
	next := 0
	for x < maxx {
		q := active[:0]
		for _, s := range active {
			if s.p1.X < x {
				continue
			}
			s.z += s.dz
			s.zerr += s.dzrem
			if s.zerr >= s.den {
				s.z++
				s.zerr -= s.den
			}
			q = append(q, s)
		}
		p := next
		for ; p < len(all); p++ {
			s := all[p]
			if s.p0.X >= x {
				break
			}
			if s.p1.X < x {
				continue
			}
			z, zerr := smuldivmod(x-s.p0.X, s.num, s.den)
			s.z = s.p0.Y + z
			s.zerr = zerr
			q = append(q, s)
		}
		active = q
		next = p
		if len(active) == 0 {
			if next >= len(all) {
				break
			}
			ix = (all[next].p0.X + onehalf) >> fixshift
			x = ix<<fixshift + onehalf
			continue
		}
		zsort(active)
		for p := 0; p < len(active); p++ {
			cnt := 0
			y := active[p].z
			yerr := active[p].zerr
			yden := active[p].den
			iy := (y + onehalf) >> fixshift
			if iy >= maxy {
				break
			}
			if iy < miny {
				iy = miny
			}
			cnt += active[p].d
			p++
			for {
				if p == len(active) {
					return
				}
				cnt += active[p].d
				if cnt&wind == 0 {
					break
				}
				p++
			}
			y2 := active[p].z
			iy2 := (y2 + onehalf) >> fixshift
			if iy2 <= miny {
				continue
			}
			if iy2 > maxy {
				iy2 = maxy
			}
			if iy == iy2 {
				if yerr*active[p].den+active[p].zerr*yden > active[p].den*yden {
					y++
				}
				iy = (y + y2) >> (fixshift + 1)
				fillpoint(dst, ix, iy, src, sp, op)
			}
		}
		x += 1 << fixshift
		ix++
	}
}
