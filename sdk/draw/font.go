package draw

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/memdraw/defont"
)

// Fontchar describes one character of a subfont (font(6)).
type Fontchar struct {
	X      int   // left edge of bits in the subfont image
	Top    uint8 // first non-zero scan line
	Bottom uint8 // last non-zero scan line plus one
	Left   int8  // offset of the bits from the drawing point
	Width  uint8 // advance
}

// Subfont is N characters in one image, which the device also holds as
// a font: its characters are drawn by index with the 's' message.
type Subfont struct {
	Name   string
	N      int
	Height int
	Ascent int
	Info   []Fontchar // N+1 entries; the last gives the image width
	Bits   *Image
}

// AllocSubfont makes a subfont from an image already in the device and
// tells the device where its characters are.
func (d *Display) AllocSubfont(name string, height, ascent int, info []Fontchar, bits *Image) (*Subfont, error) {
	n := len(info) - 1
	if n <= 0 || n > 4096 || ascent > 255 {
		return nil, errors.New("allocsubfont: bad size")
	}
	b, err := d.bufimage(1 + 4 + 4 + 1)
	if err != nil {
		return nil, err
	}
	b[0] = 'i'
	putl(b[1:], bits.ID)
	putl(b[5:], n)
	b[9] = byte(ascent)
	for k := 0; k < n; k++ {
		fc := info[k]
		r := Rect(fc.X, int(fc.Top), info[k+1].X, int(fc.Bottom))
		b, err := d.bufimage(1 + 4 + 4 + 2 + 4*4 + 2*4 + 1 + 1)
		if err != nil {
			return nil, err
		}
		b[0] = 'l'
		putl(b[1:], bits.ID)
		putl(b[5:], bits.ID)
		puts(b[9:], k)
		putr(b[11:], r)
		putp(b[27:], r.Min)
		b[35] = byte(fc.Left)
		b[36] = fc.Width
	}
	if err := d.doflush(); err != nil {
		return nil, err
	}
	return &Subfont{Name: name, N: n, Height: height, Ascent: ascent, Info: info, Bits: bits}, nil
}

// DefaultSubfont returns the built-in subfont (see memdraw/defont).
func (d *Display) DefaultSubfont() (*Subfont, error) {
	cinfo, width, bits := defont.Font()
	img, err := d.AllocImage(Rect(0, 0, width, defont.Height), GREY1, false, Black)
	if err != nil {
		return nil, err
	}
	if _, err := img.Load(img.R, bits); err != nil {
		return nil, err
	}
	info := make([]Fontchar, len(cinfo))
	for k, c := range cinfo {
		info[k] = Fontchar{X: c.X, Top: uint8(c.Top), Bottom: uint8(c.Bottom), Left: int8(c.Left), Width: uint8(c.Width)}
	}
	return d.AllocSubfont("*default*", defont.Height, defont.Ascent, info, img)
}

// Font is a set of subfonts covering ranges of runes (font(6)).
type Font struct {
	Display *Display
	Name    string
	Height  int
	Ascent  int
	sub     []*cachefont
}

type cachefont struct {
	min, max, offset int
	file             string // subfont file, relative to the font's directory
	sf               *Subfont
	failed           bool
}

// GetDefaultFont returns a font of the built-in subfont, which is also the
// Display's DefaultFont.
func (d *Display) GetDefaultFont() (*Font, error) {
	if d.DefaultFont != nil {
		return d.DefaultFont, nil
	}
	sf, err := d.DefaultSubfont()
	if err != nil {
		return nil, err
	}
	d.DefaultFont = &Font{Display: d, Name: "*default*", Height: sf.Height, Ascent: sf.Ascent,
		sub: []*cachefont{{min: 0, max: sf.N - 1, sf: sf}}}
	return d.DefaultFont, nil
}

// OpenFont reads a font file such as /fonts/lucidasans/unicode.8.font.
// Subfonts are read when first used.
func (d *Display) OpenFont(name string) (*Font, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return d.BuildFont(string(b), name)
}

// BuildFont makes a font from the text of a font file named name.
func (d *Display) BuildFont(text, name string) (*Font, error) {
	tok := strings.Fields(text)
	num := func(s string) (int, error) {
		v, err := strconv.ParseInt(s, 0, 64)
		return int(v), err
	}
	if len(tok) < 2 {
		return nil, errors.New("buildfont: bad font header")
	}
	f := &Font{Display: d, Name: name}
	var err error
	if f.Height, err = num(tok[0]); err != nil {
		return nil, fmt.Errorf("buildfont: bad height %q", tok[0])
	}
	if f.Ascent, err = num(tok[1]); err != nil || f.Ascent > f.Height {
		return nil, fmt.Errorf("buildfont: bad ascent %q", tok[1])
	}
	tok = tok[2:]
	for len(tok) > 0 {
		if len(tok) < 3 {
			return nil, errors.New("buildfont: bad subfont range")
		}
		cf := &cachefont{}
		if cf.min, err = num(tok[0]); err != nil {
			return nil, fmt.Errorf("buildfont: bad min %q", tok[0])
		}
		if cf.max, err = num(tok[1]); err != nil {
			return nil, fmt.Errorf("buildfont: bad max %q", tok[1])
		}
		tok = tok[2:]
		if v, err := num(tok[0]); err == nil && len(tok) > 1 {
			cf.offset = v
			tok = tok[1:]
		}
		cf.file = tok[0]
		tok = tok[1:]
		if cf.min > cf.max || cf.max > 0x10FFFF {
			return nil, errors.New("buildfont: bad range")
		}
		f.sub = append(f.sub, cf)
	}
	return f, nil
}

// subfont returns the subfont and index of rune c, loading the subfont
// if need be.
func (f *Font) subfont(c rune) (*Subfont, int) {
	for _, cf := range f.sub {
		if int(c) < cf.min || int(c) > cf.max {
			continue
		}
		if cf.sf == nil && !cf.failed {
			file := cf.file
			if !strings.HasPrefix(file, "/") {
				file = path.Join(path.Dir(f.Name), file)
			}
			sf, err := f.Display.ReadSubfont(file)
			if err != nil {
				cf.failed = true
				continue
			}
			cf.sf = sf
		}
		if cf.sf == nil {
			continue
		}
		i := int(c) - cf.min + cf.offset
		if i >= cf.sf.N || cf.sf.Info[i].Width == 0 {
			continue
		}
		return cf.sf, i
	}
	if f != f.Display.DefaultFont && f.Display.DefaultFont != nil {
		return f.Display.DefaultFont.subfont(c)
	}
	return nil, 0
}

// Width returns the width of s in f (stringwidth).
func (f *Font) Width(s string) int {
	w := 0
	for _, c := range s {
		if sf, i := f.subfont(c); sf != nil {
			w += int(sf.Info[i].Width)
		}
	}
	return w
}

// String draws s with font f at p, its top left, in src from sp, and
// returns the point after it.
func (i *Image) String(p Point, src *Image, sp Point, f *Font, s string) (Point, error) {
	return i.string(p, src, sp, f, s, nil, ZP, SoverD)
}

// StringBg is String over a background painted from bg at bgp.
func (i *Image) StringBg(p Point, src *Image, sp Point, f *Font, s string, bg *Image, bgp Point) (Point, error) {
	return i.string(p, src, sp, f, s, bg, bgp, SoverD)
}

func (i *Image) string(p Point, src *Image, sp Point, f *Font, s string, bg *Image, bgp Point, op Op) (Point, error) {
	d := i.Display
	for len(s) > 0 {
		// The longest run of characters in one subfont.
		var sf *Subfont
		var idx []int
		wid := 0
		for len(s) > 0 && len(idx) < 100 {
			c, n := utf8.DecodeRuneInString(s)
			csf, ci := f.subfont(c)
			if csf == nil {
				s = s[n:]
				continue
			}
			if sf != nil && csf != sf {
				break
			}
			sf = csf
			idx = append(idx, ci)
			wid += int(sf.Info[ci].Width)
			s = s[n:]
		}
		if sf == nil {
			break
		}
		if err := d.setop(op); err != nil {
			return p, err
		}
		m := 47 + 2*len(idx)
		if bg != nil {
			m += 4 + 2*4
		}
		b, err := d.bufimage(m)
		if err != nil {
			return p, err
		}
		b[0] = 's'
		if bg != nil {
			b[0] = 'x'
		}
		putl(b[1:], i.ID)
		putl(b[5:], src.ID)
		putl(b[9:], sf.Bits.ID)
		putp(b[13:], Pt(p.X, p.Y+f.Ascent))
		putr(b[21:], i.Clipr)
		putp(b[37:], sp)
		puts(b[45:], len(idx))
		o := 47
		if bg != nil {
			putl(b[47:], bg.ID)
			putp(b[51:], bgp)
			o += 12
		}
		for k, ci := range idx {
			puts(b[o+2*k:], ci)
		}
		p.X += wid
		sp.X += wid
		bgp.X += wid
	}
	return p, nil
}

// ReadSubfont reads a subfont file: an image followed by the character
// descriptions (readsubfont).
func (d *Display) ReadSubfont(name string) (*Subfont, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	img, rest, err := d.ReadImage(b, true)
	if err != nil {
		return nil, fmt.Errorf("readsubfont: %v", err)
	}
	if len(rest) < 3*12 {
		img.Free()
		return nil, errors.New("readsubfont: header read error")
	}
	n := atoi12(rest[0:12])
	height := atoi12(rest[12:24])
	ascent := atoi12(rest[24:36])
	rest = rest[36:]
	if n <= 0 || len(rest) < 6*(n+1) {
		img.Free()
		return nil, errors.New("readsubfont: fontchar read error")
	}
	info := make([]Fontchar, n+1)
	for k := range info {
		p := rest[6*k:]
		info[k] = Fontchar{X: int(p[0]) | int(p[1])<<8, Top: p[2], Bottom: p[3], Left: int8(p[4]), Width: p[5]}
	}
	return d.AllocSubfont(name, height, ascent, info, img)
}

func atoi12(b []byte) int {
	v, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return v
}

var ldepthchan = []Chan{GREY1, GREY2, GREY4, CMAP8}

// ReadImage loads an image in image(6) format, plain or compressed, into
// the device and returns it with the bytes after it (readimage).
func (d *Display) ReadImage(b []byte, font bool) (*Image, []byte, error) {
	compressed := false
	if len(b) >= 11 && string(b[:11]) == "compressed\n" {
		compressed = true
		b = b[11:]
	}
	if len(b) < 5*12 {
		return nil, nil, errors.New("readimage: short header")
	}
	hdr := b[:5*12]
	b = b[5*12:]
	isnew := false
	for m := 0; m < 10; m++ {
		if hdr[m] != ' ' {
			isnew = true
			break
		}
	}
	if hdr[11] != ' ' {
		return nil, nil, errors.New("readimage: bad format")
	}
	var ch Chan
	if isnew {
		ch = ParseChan(strings.TrimSpace(string(hdr[:11])))
		if ch == 0 {
			return nil, nil, fmt.Errorf("readimage: bad channel string %q", hdr[:11])
		}
	} else {
		ld := int(hdr[10]) - '0'
		if ld < 0 || ld > 3 {
			return nil, nil, fmt.Errorf("readimage: bad ldepth %d", ld)
		}
		ch = ldepthchan[ld]
	}
	r := Rect(atoi12(hdr[12:24]), atoi12(hdr[24:36]), atoi12(hdr[36:48]), atoi12(hdr[48:60]))
	if r.Min.X > r.Max.X || r.Min.Y > r.Max.Y {
		return nil, nil, errors.New("readimage: bad rectangle")
	}
	i, err := d.AllocImage(r, ch, false, Notacolor)
	if err != nil {
		return nil, nil, err
	}
	if !compressed {
		n := BytesPerLine(r, ch.Depth()) * r.Dy()
		if len(b) < n {
			i.Free()
			return nil, nil, errors.New("readimage: short data")
		}
		data := b[:n]
		if !isnew { // an old image: flip all the bits
			data = append([]byte(nil), data...)
			for k := range data {
				data[k] ^= 0xFF
			}
		}
		if _, err := i.Load(r, data); err != nil {
			i.Free()
			return nil, nil, err
		}
		return i, b[n:], nil
	}
	if !isnew && !font {
		i.Free()
		return nil, nil, errors.New("readimage: old compressed image")
	}
	miny := r.Min.Y
	for miny != r.Max.Y {
		if len(b) < 2*12 {
			i.Free()
			return nil, nil, errors.New("creadimage: short block header")
		}
		maxy := atoi12(b[:12])
		nb := atoi12(b[12:24])
		b = b[24:]
		if maxy <= miny || r.Max.Y < maxy || nb < 0 || nb > len(b) || nb > bufsize-21 {
			i.Free()
			return nil, nil, fmt.Errorf("creadimage: bad block %d %d", maxy, nb)
		}
		a, err := d.bufimage(21 + nb)
		if err != nil {
			i.Free()
			return nil, nil, err
		}
		a[0] = 'Y'
		putl(a[1:], i.ID)
		putr(a[5:], Rect(r.Min.X, miny, r.Max.X, maxy))
		copy(a[21:], b[:nb])
		b = b[nb:]
		miny = maxy
	}
	return i, b, nil
}
