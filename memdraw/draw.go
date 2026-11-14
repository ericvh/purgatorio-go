package memdraw

// The compositing engine, after Inferno's libmemdraw/draw.c. Draw first
// tries the special cases that use memmove and memset, then character
// drawing, then the general case, which expands each scan line of source,
// mask and destination into 8-bit channels, composites, and writes the
// result back. The special cases are not merely faster: where Inferno's
// give different pixels from the general case, so do these.

// Draw parameter state bits.
const (
	simplesrc  = 1 << 0
	simplemask = 1 << 1
	replsrc    = 1 << 2
	replmask   = 1 << 3
	fullmask   = 1 << 4
)

type drawParam struct {
	dst   *Image
	r     Rectangle
	src   *Image
	sr    Rectangle
	mask  *Image
	mr    Rectangle
	op    int
	state int
	mval  uint32 // if simplemask, the mask pixel in mask format
	mrgba uint32
	sval  uint32 // if simplesrc, the source pixel in src format
	srgba uint32
	sdval uint32 // sval in dst format
}

// ImageDraw composites src through mask onto r of dst with operator op;
// sp and mp in src and mask correspond to r.Min (memimagedraw). A nil
// mask is Opaque. It knows nothing of layers; see Draw.
func ImageDraw(dst *Image, r Rectangle, src *Image, p0 Point, mask *Image, p1 Point, op int) {
	if mask == nil {
		mask = Opaque
	}
	var par drawParam
	if !DrawClip(dst, &r, src, &p0, mask, &p1, &par.sr, &par.mr) {
		return
	}
	if op < Clear || op > SoverD {
		return
	}
	par.op, par.dst, par.r, par.src, par.mask = op, dst, r, src, mask
	if src.Flags&Frepl != 0 {
		par.state |= replsrc
		if src.R.Dx() == 1 && src.R.Dy() == 1 {
			par.sval = pixelbits(src, src.R.Min)
			par.state |= simplesrc
			par.srgba = imgtorgba(src, par.sval)
			par.sdval = rgbatoimg(dst, par.srgba)
			if par.srgba&0xFF == 0 && op&DoutS != 0 {
				return // fill with transparent source
			}
		}
	}
	if mask.Flags&Frepl != 0 {
		par.state |= replmask
		if mask.R.Dx() == 1 && mask.R.Dy() == 1 {
			par.mval = pixelbits(mask, mask.R.Min)
			if par.mval == 0 && op&DoutS != 0 {
				return // fill with zero mask
			}
			par.state |= simplemask
			if par.mval == ^uint32(0) {
				par.state |= fullmask
			}
			par.mrgba = imgtorgba(mask, par.mval)
		}
	}
	if memoptdraw(&par) || chardraw(&par) {
		return
	}
	alphadraw(&par)
}

// DrawClip clips r to dst and the corresponding rectangles in src and
// mask, then moves sp and mp into replicated images' rectangles. It
// reports whether anything is left to draw.
func DrawClip(dst *Image, r *Rectangle, src *Image, p0 *Point, mask *Image, p1 *Point, sr, mr *Rectangle) bool {
	if r.Min.X >= r.Max.X || r.Min.Y >= r.Max.Y {
		return false
	}
	splitcoords := *p0 != *p1
	rmin := r.Min
	// An inverted clipping rectangle can make Inferno's clip produce an
	// inverted result, on which it would loop; refuse it instead.
	if !RectClip(r, dst.R) || !RectClip(r, dst.Clipr) || r.Empty() {
		return false
	}
	p1.X += r.Min.X - rmin.X
	p1.Y += r.Min.Y - rmin.Y
	p0.X += r.Min.X - rmin.X
	p0.Y += r.Min.Y - rmin.Y
	sr.Min = *p0
	sr.Max = Pt(p0.X+r.Dx(), p0.Y+r.Dy())
	if src.Flags&Frepl == 0 && !RectClip(sr, src.R) {
		return false
	}
	if !RectClip(sr, src.Clipr) || sr.Empty() {
		return false
	}
	if splitcoords {
		p1.X += sr.Min.X - p0.X
		p1.Y += sr.Min.Y - p0.Y
		mr.Min = *p1
		mr.Max = Pt(p1.X+sr.Dx(), p1.Y+sr.Dy())
		omr := *mr
		if mask.Flags&Frepl == 0 && !RectClip(mr, mask.R) {
			return false
		}
		if !RectClip(mr, mask.Clipr) || mr.Empty() {
			return false
		}
		sr.Min.X += mr.Min.X - omr.Min.X
		sr.Min.Y += mr.Min.Y - omr.Min.Y
		sr.Max.X += mr.Max.X - omr.Max.X
		sr.Max.Y += mr.Max.Y - omr.Max.Y
		*p1 = mr.Min
	} else {
		if mask.Flags&Frepl == 0 && !RectClip(sr, mask.R) {
			return false
		}
		if !RectClip(sr, mask.Clipr) || sr.Empty() {
			return false
		}
		*p1 = sr.Min
	}
	delta := r.Min.Sub(*p0)
	r.Min = sr.Min.Add(delta)
	r.Max = sr.Max.Add(delta)
	if src.Flags&Frepl != 0 {
		delta.X = ReplXY(src.R.Min.X, src.R.Max.X, sr.Min.X) - sr.Min.X
		delta.Y = ReplXY(src.R.Min.Y, src.R.Max.Y, sr.Min.Y) - sr.Min.Y
		sr.Min = sr.Min.Add(delta)
		sr.Max = sr.Max.Add(delta)
	}
	*p0 = sr.Min
	*p1 = Repl(mask.R, *p1)
	mr.Min = *p1
	mr.Max = Pt(p1.X+sr.Dx(), p1.Y+sr.Dy())
	return true
}

// replbit[n][v] replicates the n-bit value v to 8 bits.
var replbit [9][256]byte

func init() {
	for n := 1; n <= 8; n++ {
		mul := 0
		for i := 0; i < 16; i++ {
			if (i+1)%n == 0 {
				mul |= 1 << (15 - i)
			}
		}
		for v := 0; v < 256; v++ {
			small := v & (1<<n - 1)
			replbit[n][v] = byte(small * mul >> 8)
		}
	}
}

// mul is Inferno's MUL: x*y/255, rounded, exact for bytes.
func mul(x, y int) int {
	t := x*y + 128
	return (t + t>>8) >> 8
}

// row holds one scan line expanded to 8-bit channels. In grey mode k is
// used, otherwise r, g and b. alpha false means every pixel is opaque.
type row struct {
	r, g, b, k, a []byte
	alpha         bool
}

func (w *row) size(n int) {
	if cap(w.r) < n {
		w.r, w.g, w.b, w.k, w.a = make([]byte, n), make([]byte, n), make([]byte, n), make([]byte, n), make([]byte, n)
	}
	w.r, w.g, w.b, w.k, w.a = w.r[:n], w.g[:n], w.b[:n], w.k[:n], w.a[:n]
}

// pixel returns the raw pixel value at x in the row starting at byte o.
func (i *Image) pixel(o, x int) uint32 {
	b := i.Data.Bdata
	switch d := i.Depth; d {
	case 1, 2, 4:
		npack := 8 / d
		v := b[o+i.xoff(x)]
		sh := 8 - d*(x&(npack-1)+1)
		return uint32(v>>sh) & (1<<d - 1)
	case 8:
		return uint32(b[o+x])
	case 16:
		p := o + 2*x
		return uint32(b[p]) | uint32(b[p+1])<<8
	case 24:
		p := o + 3*x
		return uint32(b[p]) | uint32(b[p+1])<<8 | uint32(b[p+2])<<16
	default:
		p := o + 4*x
		return uint32(b[p]) | uint32(b[p+1])<<8 | uint32(b[p+2])<<16 | uint32(b[p+3])<<24
	}
}

// xoff is the offset of the byte holding x from the start of its row.
func (i *Image) xoff(x int) int { return i.ByteAddr(Pt(x, 0)) - i.Zero }

// rowbase is the offset of x = 0 in row y.
func (i *Image) rowbase(y int) int { return i.Zero + 4*y*i.Width }

// readrow expands dx pixels of row y of img from x0, wrapping at the
// image's right edge, into w (readnbit, readcmap and readbyte).
func readrow(img *Image, x0, dx, y int, grey bool, w *row) {
	w.size(dx)
	o := img.rowbase(y)
	x := x0
	next := func() int {
		cx := x
		x++
		if x == img.R.Max.X {
			x = img.R.Min.X
		}
		return cx
	}
	switch {
	case img.Depth < 8:
		w.alpha = false
		repl := &replbit[img.Depth]
		for i := 0; i < dx; i++ {
			v := repl[img.pixel(o, next())]
			w.k[i], w.r[i], w.g[i], w.b[i] = v, v, v, v
		}
	case img.nbits[CMap] == 8:
		cm := img.Cmap.Cmap2rgb[:]
		w.alpha = img.Flags&Falpha != 0
		for i := 0; i < dx; i++ {
			u := img.pixel(o, next())
			m := (u >> img.shift[CMap]) & 0xFF
			if w.alpha {
				w.a[i] = byte(u >> img.shift[CAlpha])
			}
			q := cm[3*m:]
			if grey {
				w.k[i] = byte(rgb2k(uint32(q[0]), uint32(q[1]), uint32(q[2])))
			} else {
				w.r[i], w.g[i], w.b[i] = q[0], q[1], q[2]
			}
		}
	default:
		w.alpha = img.Flags&Falpha != 0
		isgrey := img.Flags&Fgrey != 0
		rr, gr, br := &replbit[img.nbits[CRed]], &replbit[img.nbits[CGreen]], &replbit[img.nbits[CBlue]]
		ar, kr := &replbit[img.nbits[CAlpha]], &replbit[img.nbits[CGrey]]
		for i := 0; i < dx; i++ {
			u := img.pixel(o, next())
			if w.alpha {
				w.a[i] = ar[(u>>img.shift[CAlpha])&uint32(img.mask[CAlpha])]
			}
			if isgrey {
				v := kr[(u>>img.shift[CGrey])&uint32(img.mask[CGrey])]
				w.k[i], w.r[i], w.g[i], w.b[i] = v, v, v, v
				continue
			}
			r := rr[(u>>img.shift[CRed])&uint32(img.mask[CRed])]
			g := gr[(u>>img.shift[CGreen])&uint32(img.mask[CGreen])]
			b := br[(u>>img.shift[CBlue])&uint32(img.mask[CBlue])]
			if grey {
				w.k[i] = byte(rgb2k(uint32(r), uint32(g), uint32(b)))
			} else {
				w.r[i], w.g[i], w.b[i] = r, g, b
			}
		}
	}
}

// readmask returns the mask alpha of dx pixels of row y from x0: the
// alpha channel if there is one, else the grey value.
func readmask(img *Image, x0, dx, y int, w *row) []byte {
	if img.Flags&Falpha == 0 {
		readrow(img, x0, dx, y, true, w)
		return w.k
	}
	w.size(dx)
	o := img.rowbase(y)
	ar := &replbit[img.nbits[CAlpha]]
	x := x0
	if img.Flags&Frepl == 0 && img.Flags&Fbytes != 0 {
		for i := 0; i < dx; i++ {
			w.a[i] = ar[(img.pixel(o, x)>>img.shift[CAlpha])&uint32(img.mask[CAlpha])]
			x++
		}
		return w.a
	}
	// Inferno's readbyte stores grey and alpha interleaved for a grey
	// image read for its alpha alone, but steps one byte per pixel.
	// Reproduce that.
	isgrey := img.Flags&Fgrey != 0
	buf := w.k[:0]
	for i := 0; i < dx; i++ {
		u := img.pixel(o, x)
		x++
		if x == img.R.Max.X {
			x = img.R.Min.X
		}
		buf = append(buf, ar[(u>>img.shift[CAlpha])&uint32(img.mask[CAlpha])])
		if isgrey {
			buf = append(buf, replbit[img.nbits[CGrey]][(u>>img.shift[CGrey])&uint32(img.mask[CGrey])])
		}
	}
	copy(w.a, buf[:dx])
	return w.a
}

// writerow stores dx pixels of w in row y of img from x0 (writenbit,
// writecmap and writebyte).
func writerow(img *Image, x0, dx, y int, w *row, grey bool) {
	b := img.Data.Bdata
	o := img.rowbase(y)
	switch {
	case img.Depth < 8:
		d := img.Depth
		npack := 8 / d
		src := w.k
		if !grey {
			src = w.r
		}
		for i := 0; i < dx; i++ {
			x := x0 + i
			p := o + img.xoff(x)
			sh := 8 - d*(x&(npack-1)+1)
			m := byte(1<<d-1) << sh
			b[p] = b[p]&^m | (src[i]>>(8-d))<<sh&m
		}
	case img.Chan == CMAP8:
		cm := &img.Cmap.Rgb2cmap
		for i := 0; i < dx; i++ {
			b[o+x0+i] = cm[int(w.r[i]>>4)*256+int(w.g[i]>>4)*16+int(w.b[i]>>4)]
		}
	default:
		nb := img.Depth / 8
		isgrey := img.Flags&Fgrey != 0
		isalpha := img.Flags&Falpha != 0
		p := o + x0*nb
		for i := 0; i < dx; i++ {
			var u uint32
			if isgrey {
				u |= (uint32(w.k[i]>>(8-img.nbits[CGrey])) & uint32(img.mask[CGrey])) << img.shift[CGrey]
			} else {
				u |= (uint32(w.r[i]>>(8-img.nbits[CRed])) & uint32(img.mask[CRed])) << img.shift[CRed]
				u |= (uint32(w.g[i]>>(8-img.nbits[CGreen])) & uint32(img.mask[CGreen])) << img.shift[CGreen]
				u |= (uint32(w.b[i]>>(8-img.nbits[CBlue])) & uint32(img.mask[CBlue])) << img.shift[CBlue]
			}
			if isalpha {
				a := byte(0xFF)
				if w.alpha {
					a = w.a[i]
				}
				u |= (uint32(a>>(8-img.nbits[CAlpha])) & uint32(img.mask[CAlpha])) << img.shift[CAlpha]
			}
			for j := 0; j < nb; j++ {
				b[p+j] = byte(u >> (8 * j))
			}
			p += nb
		}
	}
}

// lanes gives, for a 32-bit pixel with alpha as Inferno's draw buffers
// hold it, the byte holding each of red, green, blue and alpha.
type lanes [4]int

// bufferLanes are those of a pixel expanded into a buffer: alpha, blue,
// green, red.
var bufferLanes = lanes{3, 2, 1, 0}

// imageLanes are those of a pixel in the image's own memory.
func imageLanes(i *Image) lanes {
	return lanes{i.shift[CRed] / 8, i.shift[CGreen] / 8, i.shift[CBlue] / 8, i.shift[CAlpha] / 8}
}

// calc composites src through mask alpha ma onto dst with op, in place.
// Inferno composites whole 32-bit words when source and destination
// have the same channels and four bytes a pixel in their buffers, even
// when one is the image's memory and the other a buffer with the bytes
// in another order; then sl and dl give the two byte orders, and the
// channels are mixed as Inferno mixes them.
func calc(dst, src *row, ma []byte, dx int, grey bool, op int, sl, dl *lanes) {
	if op == D {
		return
	}
	if op == Clear {
		for i := 0; i < dx; i++ {
			dst.r[i], dst.g[i], dst.b[i], dst.k[i], dst.a[i] = 0, 0, 0, 0, 0
		}
		return
	}
	for i := 0; i < dx; i++ {
		sa, da := 255, 255
		if src.alpha {
			sa = int(src.a[i])
		}
		if dst.alpha {
			da = int(dst.a[i])
		}
		m := int(ma[i])
		var fs, fd int
		srcterm := true
		switch op {
		case DoutS, DinS:
			fd = mul(sa, m)
			if op == DoutS {
				fd = 255 - fd
			}
			srcterm = false
		case SoutD, SinD, S:
			d := da
			if op == SoutD {
				d = 255 - da
			}
			fs = m
			if op != S {
				fs = mul(fs, d)
			}
			fd = 0
		case DxorS, DatopS, DoverS, SatopD:
			if op == SatopD {
				fs = mul(m, da)
			} else {
				fs = mul(m, 255-da)
			}
			if op == DoverS {
				fd = 255
			} else {
				fd = mul(sa, m)
				if op != DatopS {
					fd = 255 - fd
				}
			}
		case SoverD:
			fs = m
			fd = 255 - mul(sa, m)
		}
		comb := func(s, d byte) byte {
			v := mul(fd, int(d))
			if srcterm {
				v += mul(fs, int(s))
			}
			return byte(v)
		}
		if grey {
			dst.k[i] = comb(src.k[i], dst.k[i])
		} else if sl != nil {
			var sw, dw [4]byte
			sv := [4]byte{src.r[i], src.g[i], src.b[i], byte(sa)}
			dv := [4]byte{dst.r[i], dst.g[i], dst.b[i], byte(da)}
			for c := 0; c < 4; c++ {
				sw[sl[c]] = sv[c]
				dw[dl[c]] = dv[c]
			}
			for j := 0; j < 4; j++ {
				dw[j] = comb(sw[j], dw[j])
			}
			dst.r[i], dst.g[i], dst.b[i] = dw[dl[0]], dw[dl[1]], dw[dl[2]]
			if dst.alpha {
				dst.a[i] = dw[dl[3]]
			}
			continue
		} else {
			dst.r[i] = comb(src.r[i], dst.r[i])
			dst.g[i] = comb(src.g[i], dst.g[i])
			dst.b[i] = comb(src.b[i], dst.b[i])
		}
		if dst.alpha {
			dst.a[i] = comb(byte(sa), byte(da))
		}
	}
}

// alphadraw is the general case: it can handle anything.
func alphadraw(par *drawParam) {
	r, sr, mr := par.r, par.sr, par.mr
	dx, dy := r.Dx(), r.Dy()
	src, mask, dst := par.src, par.mask, par.dst
	op := par.op
	isgrey := dst.Flags&Fgrey != 0
	needbuf := src.Data == dst.Data
	dir := 1
	if needbuf && dst.ByteAddr(r.Min) > src.ByteAddr(sr.Min) {
		dir = -1
	}
	boolcopy := src.Flags&Falpha == 0 && mask.Chan == GREY1 && dst.Depth >= 8 && op == SoverD

	starty, endy := 0, dy
	if dir < 0 {
		starty, endy = dy-1, -1
	}
	srcy := (starty + sr.Min.Y - src.R.Min.Y) % src.R.Dy()
	masky := (starty + mr.Min.Y - mask.R.Min.Y) % mask.R.Dy()
	dsty := starty + r.Min.Y - dst.R.Min.Y
	clipy := func(img *Image, y *int) {
		d := img.R.Dy()
		if *y == d {
			*y = 0
		} else if *y == -1 {
			*y = d - 1
		}
	}
	// Find whether Inferno would composite words whose bytes are in
	// different orders (see calc).
	var sl, dl *lanes
	if !boolcopy && !isgrey && op != Clear && src.Chan == dst.Chan && src.Depth == 32 &&
		src.Flags&Falpha != 0 && src.Flags&Fbytes != 0 && src.Flags&Fgrey == 0 {
		direct := func(i *Image) bool { return i.Flags&Frepl == 0 }
		if direct(src) != direct(dst) {
			il := imageLanes(src)
			sl, dl = &il, &bufferLanes
			if !direct(src) {
				sl, dl = &bufferLanes, &il
			}
			if *sl == *dl {
				sl, dl = nil, nil
			}
		}
	}
	var bs, bm, bd row
	var conv []byte
	for y := starty; y != endy; y, srcy, masky, dsty = y+dir, srcy+dir, masky+dir, dsty+dir {
		clipy(src, &srcy)
		clipy(dst, &dsty)
		clipy(mask, &masky)
		sy, my, dy := src.R.Min.Y+srcy, mask.R.Min.Y+masky, dst.R.Min.Y+dsty
		if boolcopy {
			m := readmask(mask, mr.Min.X, dx, my, &bm)
			nb := dst.Depth / 8
			b := dst.Data.Bdata
			o := dst.rowbase(dy) + r.Min.X*nb
			if needbuf && dst.Chan == src.Chan && src.Flags&Frepl == 0 {
				// Inferno copies straight from the source's memory, a
				// pixel at a time from the left, so an overlapping copy
				// within a row repeats pixels. Do the same.
				so := src.rowbase(sy) + sr.Min.X*nb
				for i := 0; i < dx; i++ {
					if m[i] != 0 {
						copy(b[o+i*nb:o+i*nb+nb], b[so+i*nb:so+i*nb+nb])
					}
				}
				continue
			}
			conv = boolconv(dst, r.Min.X, src, sr.Min.X, dx, sy, isgrey, &bs, conv)
			for i := 0; i < dx; i++ {
				if m[i] != 0 {
					copy(b[o+i*nb:o+i*nb+nb], conv[i*nb:i*nb+nb])
				}
			}
			continue
		}
		readrow(src, sr.Min.X, dx, sy, isgrey, &bs)
		m := readmask(mask, mr.Min.X, dx, my, &bm)
		readrow(dst, r.Min.X, dx, dy, isgrey, &bd)
		calc(&bd, &bs, m, dx, isgrey, op, sl, dl)
		writerow(dst, r.Min.X, dx, dy, &bd, isgrey)
	}
}

// boolconv returns dx pixels of src row sy from sx converted to dst's
// format (Inferno's convfn: readptr, the cmap cheat, or genconv).
func boolconv(dst *Image, dx0 int, src *Image, sx, dx, sy int, grey bool, w *row, buf []byte) []byte {
	nb := dst.Depth / 8
	if cap(buf) < dx*nb {
		buf = make([]byte, dx*nb)
	}
	buf = buf[:dx*nb]
	if dst.Chan == src.Chan && src.Flags&Frepl == 0 {
		o := src.rowbase(sy) + sx*nb
		copy(buf, src.Data.Bdata[o:o+dx*nb])
		return buf
	}
	if dst.Chan == CMAP8 && (src.Chan == GREY1 || src.Chan == GREY2 || src.Chan == GREY4) {
		// The replicated grey value is exactly the color map entry.
		readrow(src, sx, dx, sy, true, w)
		copy(buf, w.k)
		return buf
	}
	readrow(src, sx, dx, sy, grey, w)
	tmp := &Image{Depth: dst.Depth, Chan: dst.Chan, Cmap: dst.Cmap, Flags: dst.Flags, Width: (len(buf) + 3) / 4,
		shift: dst.shift, mask: dst.mask, nbits: dst.nbits, Data: &Data{Bdata: buf}}
	writerow(tmp, 0, dx, 0, w, grey)
	return buf
}
