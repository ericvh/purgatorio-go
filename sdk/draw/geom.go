// Package draw is the guest side of the draw device: Inferno's libdraw
// for Go guests. A Display is a connection to /dev/draw; Images, Screens
// and windows live in the device and are drawn on by messages that the
// Display buffers and writes to its data file.
package draw

import "fmt"

// Point is a point in the plane; y grows downward.
type Point struct{ X, Y int }

// Rectangle is half open: it contains Min but not Max.
type Rectangle struct{ Min, Max Point }

// ZP is the zero point; ZR the zero rectangle.
var (
	ZP Point
	ZR Rectangle
)

func Pt(x, y int) Point                 { return Point{x, y} }
func Rect(x0, y0, x1, y1 int) Rectangle { return Rectangle{Point{x0, y0}, Point{x1, y1}} }
func Rpt(min, max Point) Rectangle      { return Rectangle{min, max} }

func (p Point) Add(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }
func (p Point) Sub(q Point) Point { return Point{p.X - q.X, p.Y - q.Y} }
func (p Point) Mul(k int) Point   { return Point{p.X * k, p.Y * k} }
func (p Point) Div(k int) Point   { return Point{p.X / k, p.Y / k} }
func (p Point) In(r Rectangle) bool {
	return p.X >= r.Min.X && p.X < r.Max.X && p.Y >= r.Min.Y && p.Y < r.Max.Y
}
func (p Point) String() string            { return fmt.Sprintf("[%d %d]", p.X, p.Y) }
func (r Rectangle) Dx() int               { return r.Max.X - r.Min.X }
func (r Rectangle) Dy() int               { return r.Max.Y - r.Min.Y }
func (r Rectangle) Size() Point           { return Point{r.Dx(), r.Dy()} }
func (r Rectangle) Add(p Point) Rectangle { return Rectangle{r.Min.Add(p), r.Max.Add(p)} }
func (r Rectangle) Sub(p Point) Rectangle { return Rectangle{r.Min.Sub(p), r.Max.Sub(p)} }
func (r Rectangle) Inset(n int) Rectangle { return Rect(r.Min.X+n, r.Min.Y+n, r.Max.X-n, r.Max.Y-n) }
func (r Rectangle) Empty() bool           { return r.Min.X >= r.Max.X || r.Min.Y >= r.Max.Y }
func (r Rectangle) String() string        { return r.Min.String() + " " + r.Max.String() }

// In reports whether r lies within s.
func (r Rectangle) In(s Rectangle) bool {
	return s.Min.X <= r.Min.X && r.Max.X <= s.Max.X && s.Min.Y <= r.Min.Y && r.Max.Y <= s.Max.Y
}

// Clip returns r clipped to s, and whether they overlap.
func (r Rectangle) Clip(s Rectangle) (Rectangle, bool) {
	if !(r.Min.X < s.Max.X && s.Min.X < r.Max.X && r.Min.Y < s.Max.Y && s.Min.Y < r.Max.Y) {
		return r, false
	}
	r.Min.X, r.Min.Y = max(r.Min.X, s.Min.X), max(r.Min.Y, s.Min.Y)
	r.Max.X, r.Max.Y = min(r.Max.X, s.Max.X), min(r.Max.Y, s.Max.Y)
	return r, true
}

// Chan is a channel descriptor, as in Inferno's draw.h.
type Chan uint32

// Channel types.
const (
	CRed = iota
	CGreen
	CBlue
	CGrey
	CAlpha
	CMap
	CIgnore
	NChan
)

func dc(t, n int) Chan { return Chan((t&15)<<4 | n&15) }

// The common descriptors.
var (
	GREY1  = dc(CGrey, 1)
	GREY2  = dc(CGrey, 2)
	GREY4  = dc(CGrey, 4)
	GREY8  = dc(CGrey, 8)
	CMAP8  = dc(CMap, 8)
	RGB16  = dc(CRed, 5)<<16 | dc(CGreen, 6)<<8 | dc(CBlue, 5)
	RGB24  = dc(CRed, 8)<<16 | dc(CGreen, 8)<<8 | dc(CBlue, 8)
	RGBA32 = dc(CRed, 8)<<24 | dc(CGreen, 8)<<16 | dc(CBlue, 8)<<8 | dc(CAlpha, 8)
	ARGB32 = dc(CAlpha, 8)<<24 | dc(CRed, 8)<<16 | dc(CGreen, 8)<<8 | dc(CBlue, 8)
	XRGB32 = dc(CIgnore, 8)<<24 | dc(CRed, 8)<<16 | dc(CGreen, 8)<<8 | dc(CBlue, 8)
)

const channames = "rgbkamx"

// Depth is the bits per pixel of c, or 0 if c is invalid.
func (c Chan) Depth() int {
	n := 0
	for ; c != 0; c >>= 8 {
		t, b := int(c>>4)&15, int(c&15)
		if t >= NChan || b > 8 || b <= 0 {
			return 0
		}
		n += b
	}
	if n == 0 || (n > 8 && n%8 != 0) || (n < 8 && 8%n != 0) {
		return 0
	}
	return n
}

func (c Chan) String() string {
	if c.Depth() == 0 {
		return ""
	}
	var rc Chan
	for x := c; x != 0; x >>= 8 {
		rc = rc<<8 | x&0xFF
	}
	var b []byte
	for x := rc; x != 0; x >>= 8 {
		b = append(b, channames[int(x>>4)&15], byte('0'+x&15))
	}
	return string(b)
}

// ParseChan parses a descriptor such as "x8r8g8b8"; 0 if malformed.
func ParseChan(s string) Chan {
	var c Chan
	for i := 0; i < len(s); i += 2 {
		if s[i] == ' ' || s[i] == '\n' {
			break
		}
		t := -1
		for j := range channames {
			if channames[j] == s[i] {
				t = j
			}
		}
		if t < 0 || i+1 >= len(s) || s[i+1] < '0' || s[i+1] > '9' {
			return 0
		}
		c = c<<8 | dc(t, int(s[i+1]-'0'))
	}
	return c
}

// Compositing operators.
type Op int

const (
	Clear  Op = 0
	DoutS  Op = 1 << 0
	SoutD  Op = 1 << 1
	DinS   Op = 1 << 2
	SinD   Op = 1 << 3
	S      Op = SinD | SoutD
	SoverD Op = SinD | SoutD | DoutS
	SatopD Op = SinD | DoutS
	SxorD  Op = SoutD | DoutS
	D      Op = DinS | DoutS
	DoverS Op = DinS | DoutS | SoutD
	DatopS Op = DinS | SoutD
	DxorS  Op = DoutS | SoutD
)

// Colors as rrggbbaa.
const (
	Opaque        = 0xFFFFFFFF
	Transparent   = 0x00000000
	Black         = 0x000000FF
	White         = 0xFFFFFFFF
	Red           = 0xFF0000FF
	Green         = 0x00FF00FF
	Blue          = 0x0000FFFF
	Cyan          = 0x00FFFFFF
	Magenta       = 0xFF00FFFF
	Yellow        = 0xFFFF00FF
	Paleyellow    = 0xFFFFAAFF
	Darkyellow    = 0xEEEE9EFF
	Darkgreen     = 0x448844FF
	Palegreen     = 0xAAFFAAFF
	Medgreen      = 0x88CC88FF
	Darkblue      = 0x000055FF
	Palebluegreen = 0xAAFFFFFF
	Paleblue      = 0x0000BBFF
	Bluegreen     = 0x008888FF
	Greygreen     = 0x55AAAAFF
	Palegreygreen = 0x9EEEEEFF
	Yellowgreen   = 0x99994CFF
	Medblue       = 0x000099FF
	Greyblue      = 0x005DBBFF
	Palegreyblue  = 0x4993DDFF
	Purpleblue    = 0x8888CCFF
	Notacolor     = 0xFFFFFF00
	Nofill        = Notacolor
)

// Line ends.
const (
	Endsquare = 0
	Enddisc   = 1
	Endarrow  = 2
)

// Arrow is a line end with an arrowhead of the given geometry.
func Arrow(a, b, c int) int { return Endarrow | a<<5 | b<<14 | c<<23 }

// Window refresh methods.
const (
	Refbackup = 0
	Refnone   = 1
	Refmesg   = 2
)

// SetAlpha premultiplies the color by alpha.
func SetAlpha(color uint32, alpha uint8) uint32 {
	r, g, b := color>>24&0xFF, color>>16&0xFF, color>>8&0xFF
	a := uint32(alpha)
	return (r*a/255)<<24 | (g*a/255)<<16 | (b*a/255)<<8 | a
}

// BytesPerLine is the size of one row of r at depth d in the device's
// image format.
func BytesPerLine(r Rectangle, d int) int {
	if r.Min.X >= 0 {
		return (r.Max.X*d+7)/8 - (r.Min.X*d)/8
	}
	return (-r.Min.X*d+7)/8 + (r.Max.X*d+7)/8
}
