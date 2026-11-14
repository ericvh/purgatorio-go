package memdraw

import "errors"

// ErrLoad is returned for a load or unload outside the image or with too
// little data.
var ErrLoad = errors.New("bad image load")

// LoadImage copies rows of data in i's format into r of i, which must be
// a plain image (loadmemimage). It returns the bytes used.
func LoadImage(i *Image, r Rectangle, data []byte) (int, error) {
	if !r.In(i.R) {
		return 0, ErrLoad
	}
	l := BytesPerLine(r, i.Depth)
	if len(data) < l*r.Dy() {
		return 0, ErrLoad
	}
	ndata := l * r.Dy()
	b := i.Data.Bdata
	q := i.ByteAddr(r.Min)
	mx := 7 / i.Depth
	lpart := (r.Min.X & mx) * i.Depth
	rpart := (r.Max.X & mx) * i.Depth
	m := byte(0xFF >> lpart)
	wid := 4 * i.Width
	if l == 1 { // all in one byte
		if rpart != 0 {
			m ^= byte(0xFF >> rpart)
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			b[q] ^= (data[0] ^ b[q]) & m
			q += wid
			data = data[1:]
		}
		return ndata, nil
	}
	mr := byte(0xFF ^ (0xFF >> rpart))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		switch {
		case lpart == 0 && rpart == 0:
			copy(b[q:q+l], data[:l])
		case lpart != 0 && rpart == 0:
			b[q] ^= (data[0] ^ b[q]) & m
			copy(b[q+1:q+l], data[1:l])
		case lpart == 0:
			copy(b[q:q+l-1], data[:l-1])
			b[q+l-1] ^= (data[l-1] ^ b[q+l-1]) & mr
		default:
			b[q] ^= (data[0] ^ b[q]) & m
			if l > 2 {
				copy(b[q+1:q+l-1], data[1:l-1])
			}
			b[q+l-1] ^= (data[l-1] ^ b[q+l-1]) & mr
		}
		q += wid
		data = data[l:]
	}
	return ndata, nil
}

// UnloadImage copies r of plain image i into data (unloadmemimage).
func UnloadImage(i *Image, r Rectangle, data []byte) (int, error) {
	if !r.In(i.R) {
		return 0, ErrLoad
	}
	l := BytesPerLine(r, i.Depth)
	if len(data) < l*r.Dy() {
		return 0, ErrLoad
	}
	b := i.Data.Bdata
	q := i.ByteAddr(r.Min)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(data[:l], b[q:q+l])
		q += 4 * i.Width
		data = data[l:]
	}
	return l * r.Dy(), nil
}

// Compression parameters of Inferno's compressed image format.
const (
	NMATCH  = 3           // shortest match possible
	NRUN    = NMATCH + 31 // longest match possible
	NMEM    = 1024        // window size
	NDUMP   = 128         // maximum length of dump
	NCBLOCK = 6000        // size of compressed blocks
)

// CLoadImage decompresses data into r of plain image i (cloadmemimage).
// It returns the bytes of data used.
func CLoadImage(i *Image, r Rectangle, data []byte) (int, error) {
	if !r.In(i.R) {
		return 0, ErrLoad
	}
	bpl := BytesPerLine(r, i.Depth)
	b := i.Data.Bdata
	var mem [NMEM]byte
	memp := 0
	u := 0
	y := r.Min.Y
	linep := i.ByteAddr(Pt(r.Min.X, y))
	elinep := linep + bpl
	for {
		if linep == elinep {
			y++
			if y == r.Max.Y {
				break
			}
			linep = i.ByteAddr(Pt(r.Min.X, y))
			elinep = linep + bpl
		}
		if u == len(data) {
			return 0, ErrLoad
		}
		c := int(data[u])
		u++
		if c >= 128 {
			for cnt := c - 128 + 1; cnt != 0; cnt-- {
				if u == len(data) || linep == elinep {
					return 0, ErrLoad
				}
				b[linep] = data[u]
				linep++
				mem[memp] = data[u]
				memp = (memp + 1) % NMEM
				u++
			}
			continue
		}
		if u == len(data) {
			return 0, ErrLoad
		}
		offs := int(data[u]) + (c&3)<<8 + 1
		u++
		omemp := memp - offs
		if omemp < 0 {
			omemp += NMEM
		}
		for cnt := c>>2 + NMATCH; cnt != 0; cnt-- {
			if linep == elinep {
				return 0, ErrLoad
			}
			b[linep] = mem[omemp]
			linep++
			mem[memp] = mem[omemp]
			omemp = (omemp + 1) % NMEM
			memp = (memp + 1) % NMEM
		}
	}
	return u, nil
}
