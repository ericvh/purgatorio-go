package memdraw

import "errors"

// Image flags.
const (
	Frepl   = 1 << 0 // is replicated
	Fsimple = 1 << 1 // is 1x1
	Fgrey   = 1 << 2 // is grey
	Falpha  = 1 << 3 // has explicit alpha
	Fcmap   = 1 << 4 // has cmap channel
	Fbytes  = 1 << 5 // has only 8-bit channels
)

// Data is the pixel store of an image, shared by the windows on a screen.
type Data struct {
	Bdata []byte
	Ref   int
}

// Image is a Memimage: a rectangle of pixels in a channel format. Rows
// are Width 32-bit words apart in Data; Zero is the byte offset of the
// byte holding (0, 0), which may lie outside the data.
type Image struct {
	R     Rectangle // rectangle in data area, local coordinates
	Clipr Rectangle // clipping region
	Depth int       // bits per pixel
	Nchan int
	Chan  Chan
	Cmap  *Cmap
	Data  *Data
	Zero  int
	Width int    // words per scan line
	Layer *Layer // nil if not a layer
	Flags int

	shift [NChan]int
	mask  [NChan]int
	nbits [NChan]int
}

// ErrBadChan is returned for an invalid channel descriptor.
var ErrBadChan = errors.New("bad channel descriptor")

// AllocData makes an image on existing data (allocmemimaged).
func AllocData(r Rectangle, c Chan, md *Data) (*Image, error) {
	d := c.Depth()
	if d == 0 {
		return nil, ErrBadChan
	}
	l := WordsPerLine(r, d)
	i := &Image{Data: md, Width: l, R: r, Clipr: r, Cmap: DefCmap}
	i.Zero = 4 * l * r.Min.Y
	if r.Min.X >= 0 {
		i.Zero += (r.Min.X * d) / 8
	} else {
		i.Zero -= (-r.Min.X*d + 7) / 8
	}
	i.Zero = -i.Zero
	if err := i.SetChan(c); err != nil {
		return nil, err
	}
	return i, nil
}

// Alloc makes an image with fresh, zeroed storage (allocmemimage).
func Alloc(r Rectangle, c Chan) (*Image, error) {
	d := c.Depth()
	if d == 0 {
		return nil, ErrBadChan
	}
	if r.Dx() < 0 || r.Dy() < 0 {
		return nil, errors.New("bad rectangle")
	}
	nw := WordsPerLine(r, d) * r.Dy()
	if nw > 1<<28 {
		return nil, errors.New("image too large")
	}
	md := &Data{Bdata: make([]byte, 4*nw), Ref: 1}
	return AllocData(r, c, md)
}

// Free releases the image's share of its data.
func (i *Image) Free() {
	if i != nil && i.Data != nil {
		i.Data.Ref--
	}
}

// ByteAddr returns the offset in Data.Bdata of the byte holding p.
func (i *Image) ByteAddr(p Point) int {
	a := i.Zero + 4*p.Y*i.Width
	if i.Depth < 8 {
		np := 8 / i.Depth
		if p.X < 0 {
			return a + (p.X-np+1)/np
		}
		return a + p.X/np
	}
	return a + p.X*(i.Depth/8)
}

// SetChan changes the channel format of i, which must keep its depth's
// storage (memsetchan).
func (i *Image) SetChan(c Chan) error {
	d := c.Depth()
	if d == 0 {
		return ErrBadChan
	}
	i.Depth = d
	i.Chan = c
	i.Flags &^= Fgrey | Falpha | Fcmap | Fbytes
	i.shift, i.mask, i.nbits = [NChan]int{}, [NChan]int{}, [NChan]int{}
	bytes := true
	k := 0
	for cc, j := c, 0; cc != 0; j, cc = j+nbits(cc), cc>>8 {
		t := ctype(cc)
		switch t {
		case CGrey:
			i.Flags |= Fgrey
		case CAlpha:
			i.Flags |= Falpha
		case CMap:
			if i.Cmap == nil {
				i.Cmap = DefCmap
				i.Flags |= Fcmap
			}
		}
		i.shift[t] = j
		i.mask[t] = 1<<nbits(cc) - 1
		i.nbits[t] = nbits(cc)
		if nbits(cc) != 8 {
			bytes = false
		}
		k++
	}
	i.Nchan = k
	if bytes {
		i.Flags |= Fbytes
	}
	return nil
}

// FillColor fills all of i with the rrggbbaa color val (memfillcolor).
func (i *Image) FillColor(val uint32) {
	if val == DNofill {
		return
	}
	bits := rgbatoimg(i, val)
	b := i.Data.Bdata
	if i.Depth == 24 {
		for y := i.R.Min.Y; y < i.R.Max.Y; y++ {
			o := i.ByteAddr(Pt(i.R.Min.X, y))
			for x := 0; x < i.R.Dx(); x++ {
				b[o], b[o+1], b[o+2] = byte(bits), byte(bits>>8), byte(bits>>16)
				o += 3
			}
		}
		return
	}
	for d := i.Depth; d < 32; d *= 2 {
		bits = bits<<d | bits
	}
	o := i.ByteAddr(i.R.Min)
	o -= o & 3 // the word holding r.min; data is word aligned
	n := 4 * i.Width * i.R.Dy()
	for k := max(o, 0); k < o+n && k < len(b); k++ {
		b[k] = byte(bits >> (8 * ((k - o) & 3)))
	}
}

// pixelbits returns the pixel at pt replicated to fill 32 bits.
func pixelbits(i *Image, pt Point) uint32 {
	b := i.Data.Bdata
	p := i.ByteAddr(pt)
	var val uint32
	bpp := i.Depth
	switch bpp {
	case 1, 2, 4:
		npack := 8 / bpp
		// Inferno's C remainder makes off negative for negative x,
		// which shifts the pixel away; keep that.
		off := pt.X % npack
		val = uint32(b[p]) >> (bpp * (npack - 1 - off))
		val &= 1<<bpp - 1
	case 8:
		val = uint32(b[p])
	case 16:
		val = uint32(b[p]) | uint32(b[p+1])<<8
	case 24:
		val = uint32(b[p]) | uint32(b[p+1])<<8 | uint32(b[p+2])<<16
	case 32:
		val = uint32(b[p]) | uint32(b[p+1])<<8 | uint32(b[p+2])<<16 | uint32(b[p+3])<<24
	}
	for bpp < 32 {
		val |= val << bpp
		bpp *= 2
	}
	return val
}

// imgtorgba converts a pixel value of img to rrggbbaa.
func imgtorgba(img *Image, val uint32) uint32 {
	a := uint32(0xFF)
	r, g, b := uint32(0xAA), uint32(0xAA), uint32(0xAA)
	for c := img.Chan; c != 0; c >>= 8 {
		nb := nbits(c)
		v := val & (1<<nb - 1)
		ov := v
		val >>= nb
		for nb < 8 {
			v |= v << nb
			nb *= 2
		}
		v >>= nb - 8
		switch ctype(c) {
		case CRed:
			r = v
		case CGreen:
			g = v
		case CBlue:
			b = v
		case CAlpha:
			a = v
		case CGrey:
			r, g, b = v, v, v
		case CMap:
			p := img.Cmap.Cmap2rgb[3*ov:]
			r, g, b = uint32(p[0]), uint32(p[1]), uint32(p[2])
		}
	}
	return (r&0xFF)<<24 | (g&0xFF)<<16 | (b&0xFF)<<8 | a&0xFF
}

// rgbatoimg converts rrggbbaa to a pixel value of img.
func rgbatoimg(img *Image, rgba uint32) uint32 {
	var v uint32
	r, g, b, a := rgba>>24&0xFF, rgba>>16&0xFF, rgba>>8&0xFF, rgba&0xFF
	d := 0
	for c := img.Chan; c != 0; c >>= 8 {
		nb := nbits(c)
		switch ctype(c) {
		case CRed:
			v |= (r >> (8 - nb)) << d
		case CGreen:
			v |= (g >> (8 - nb)) << d
		case CBlue:
			v |= (b >> (8 - nb)) << d
		case CAlpha:
			v |= (a >> (8 - nb)) << d
		case CMap:
			m := uint32(img.Cmap.Rgb2cmap[(r>>4)*256+(g>>4)*16+(b>>4)])
			v |= (m >> (8 - nb)) << d
		case CGrey:
			m := rgb2k(r, g, b)
			v |= (m >> (8 - nb)) << d
		}
		d += nb
	}
	return v
}

// rgb2k is the NTSC grey of an RGB triple, exact for 0 ≤ r,g,b < 256.
func rgb2k(r, g, b uint32) uint32 { return (156763*r + 307758*g + 59769*b) >> 19 }

// Predefined images, as memimageinit makes them.
var (
	ones, zeros                       *Image
	Opaque, Transparent, White, Black *Image
)

func init() {
	ones, _ = Alloc(Rect(0, 0, 1, 1), GREY1)
	ones.Flags |= Frepl
	ones.Clipr = Rect(-0x3FFFFFF, -0x3FFFFFF, 0x3FFFFFF, 0x3FFFFFF)
	ones.Data.Bdata[0] = 0xFF
	zeros, _ = Alloc(Rect(0, 0, 1, 1), GREY1)
	zeros.Flags |= Frepl
	zeros.Clipr = ones.Clipr
	White, Opaque = ones, ones
	Black, Transparent = zeros, zeros
}
