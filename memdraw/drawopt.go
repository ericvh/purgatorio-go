package memdraw

// setpix stores the raw pixel value v at x in the row at offset o.
func (i *Image) setpix(o, x int, v uint32) {
	b := i.Data.Bdata
	d := i.Depth
	if d < 8 {
		npack := 8 / d
		p := o + i.xoff(x)
		sh := 8 - d*(x&(npack-1)+1)
		m := byte(1<<d-1) << sh
		b[p] = b[p]&^m | byte(v<<sh)&m
		return
	}
	p := o + x*(d/8)
	for j := 0; j < d/8; j++ {
		b[p+j] = byte(v >> (8 * j))
	}
}

// memoptdraw handles fills and scrolls with memset and memmove, and
// aligned boolean images with bit operations.
func memoptdraw(par *drawParam) bool {
	dx, dy := par.r.Dx(), par.r.Dy()
	src, dst, op := par.src, par.dst, par.op

	// An opaque mask and one opaque source pixel: replicate the pixel
	// converted to the destination's format.
	m := simplesrc | simplemask | fullmask
	if par.state&m == m && par.srgba&0xFF == 0xFF && (op == S || op == SoverD) {
		v := par.sdval
		if dst.Depth < 8 {
			v &= 1<<dst.Depth - 1
		}
		for y := par.r.Min.Y; y < par.r.Max.Y; y++ {
			o := dst.rowbase(y)
			for x := par.r.Min.X; x < par.r.Max.X; x++ {
				dst.setpix(o, x, v)
			}
		}
		return true
	}

	// No source alpha and an opaque mask: copy the source. With the same
	// channels and no replication, memmove suffices.
	m = simplemask | fullmask
	if par.state&(m|replsrc) == m && src.Depth >= 8 && src.Chan == dst.Chan &&
		(op == S || (op == SoverD && src.Flags&Falpha == 0)) {
		swid, dwid := 4*src.Width, 4*dst.Width
		sp, dp := src.ByteAddr(par.sr.Min), dst.ByteAddr(par.r.Min)
		if src.Data == dst.Data && dp > sp {
			sp += (dy - 1) * swid
			dp += (dy - 1) * dwid
			swid, dwid = -swid, -dwid
		}
		nb := dx * src.Depth / 8
		sb, db := src.Data.Bdata, dst.Data.Bdata
		for y := 0; y < dy; y++ {
			copy(db[dp:dp+nb], sb[sp:sp+nb])
			sp += swid
			dp += dwid
		}
		return true
	}

	// 1-bit mask, source and destination, all bit aligned: whatever the
	// operator, copy source bits where the mask is set.
	if par.state&(simplemask|simplesrc|replmask|replsrc) == 0 &&
		dst.Chan == GREY1 && src.Chan == GREY1 && par.mask.Chan == GREY1 &&
		par.r.Min.X&7 == par.sr.Min.X&7 && par.r.Min.X&7 == par.mr.Min.X&7 {
		mask := par.mask
		y0, y1, step := 0, dy, 1
		if src.Data == dst.Data && dst.ByteAddr(par.r.Min) > src.ByteAddr(par.sr.Min) {
			y0, y1, step = dy-1, -1, -1
		}
		bits := make([]uint32, 0, dx)
		for y := y0; y != y1; y += step {
			so := src.rowbase(par.sr.Min.Y + y)
			mo := mask.rowbase(par.mr.Min.Y + y)
			do := dst.rowbase(par.r.Min.Y + y)
			bits = bits[:0]
			for x := 0; x < dx; x++ {
				bits = append(bits, src.pixel(so, par.sr.Min.X+x))
			}
			for x := 0; x < dx; x++ {
				if mask.pixel(mo, par.mr.Min.X+x) != 0 {
					dst.setpix(do, par.r.Min.X+x, bits[x])
				}
			}
		}
		return true
	}
	return false
}

// chardraw paints one opaque source pixel through a 1-bit mask onto a
// destination of depth 8 or more.
func chardraw(par *drawParam) bool {
	mask, src, dst := par.mask, par.src, par.dst
	if par.state&(replsrc|simplesrc|replmask) != replsrc|simplesrc ||
		mask.Depth != 1 || src.Flags&Falpha != 0 || dst.Depth < 8 || dst.Data == src.Data ||
		par.op != SoverD {
		return false
	}
	r, mr := par.r, par.mr
	v := par.sdval
	for y := 0; y < r.Dy(); y++ {
		mo := mask.rowbase(mr.Min.Y + y)
		do := dst.rowbase(r.Min.Y + y)
		for x := 0; x < r.Dx(); x++ {
			if mask.pixel(mo, mr.Min.X+x) != 0 {
				dst.setpix(do, r.Min.X+x, v)
			}
		}
	}
	return true
}
