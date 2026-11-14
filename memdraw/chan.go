package memdraw

// Chan is a channel descriptor: up to four channels, each a type in the
// high nibble and a bit count in the low nibble of a byte, the first
// channel (most significant bits of a pixel) in the highest byte.
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

func dc(typ, nbits int) Chan { return Chan((typ&15)<<4 | nbits&15) }

// Chan1 to Chan4 build descriptors from (type, bits) pairs.
func Chan1(a, b int) Chan                   { return dc(a, b) }
func Chan2(a, b, c, d int) Chan             { return Chan1(a, b)<<8 | dc(c, d) }
func Chan3(a, b, c, d, e, f int) Chan       { return Chan2(a, b, c, d)<<8 | dc(e, f) }
func Chan4(a, b, c, d, e, f, g, h int) Chan { return Chan3(a, b, c, d, e, f)<<8 | dc(g, h) }

// The common descriptors.
var (
	GREY1  = Chan1(CGrey, 1)
	GREY2  = Chan1(CGrey, 2)
	GREY4  = Chan1(CGrey, 4)
	GREY8  = Chan1(CGrey, 8)
	CMAP8  = Chan1(CMap, 8)
	RGB15  = Chan4(CIgnore, 1, CRed, 5, CGreen, 5, CBlue, 5)
	RGB16  = Chan3(CRed, 5, CGreen, 6, CBlue, 5)
	RGB24  = Chan3(CRed, 8, CGreen, 8, CBlue, 8)
	RGBA32 = Chan4(CRed, 8, CGreen, 8, CBlue, 8, CAlpha, 8)
	ARGB32 = Chan4(CAlpha, 8, CRed, 8, CGreen, 8, CBlue, 8)
	XRGB32 = Chan4(CIgnore, 8, CRed, 8, CGreen, 8, CBlue, 8)
	BGR24  = Chan3(CBlue, 8, CGreen, 8, CRed, 8)
	ABGR32 = Chan4(CAlpha, 8, CBlue, 8, CGreen, 8, CRed, 8)
	XBGR32 = Chan4(CIgnore, 8, CBlue, 8, CGreen, 8, CRed, 8)
)

func nbits(c Chan) int { return int(c & 15) }
func ctype(c Chan) int { return int(c>>4) & 15 }

const channames = "rgbkamx"

// Depth returns the bits per pixel of c, or 0 if c is not a valid
// descriptor (chantodepth).
func (c Chan) Depth() int {
	n := 0
	for ; c != 0; c >>= 8 {
		if ctype(c) >= NChan || nbits(c) > 8 || nbits(c) <= 0 {
			return 0
		}
		n += nbits(c)
	}
	if n == 0 || (n > 8 && n%8 != 0) || (n < 8 && 8%n != 0) {
		return 0
	}
	return n
}

// String returns the text form, such as "x8r8g8b8" (chantostr), or ""
// for an invalid descriptor.
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
		b = append(b, channames[ctype(x)], byte('0'+nbits(x)))
	}
	return string(b)
}

func isspace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// ParseChan parses the text form of a descriptor (strtochan); it returns
// 0 for a malformed one.
func ParseChan(s string) Chan {
	i := 0
	for i < len(s) && isspace(s[i]) {
		i++
	}
	var c Chan
	for i < len(s) && !isspace(s[i]) {
		t := -1
		for j := 0; j < len(channames); j++ {
			if channames[j] == s[i] {
				t = j
			}
		}
		if t < 0 || i+1 >= len(s) || s[i+1] < '0' || s[i+1] > '9' {
			return 0
		}
		c = c<<8 | dc(t, int(s[i+1]-'0'))
		i += 2
	}
	return c
}

// Compositing operators.
const (
	DoutS  = 1 << 0
	SoutD  = 1 << 1
	DinS   = 1 << 2
	SinD   = 1 << 3
	S      = SinD | SoutD
	SoverD = SinD | SoutD | DoutS
	SatopD = SinD | DoutS
	SxorD  = SoutD | DoutS
	D      = DinS | DoutS
	DoverS = DinS | DoutS | SoutD
	DatopS = DinS | SoutD
	DxorS  = DoutS | SoutD
	Clear  = 0
	Ncomp  = 12
)

// Colors, as 32-bit rrggbbaa values.
const (
	DOpaque      = 0xFFFFFFFF
	DTransparent = 0x00000000
	DBlack       = 0x000000FF
	DWhite       = 0xFFFFFFFF
	DRed         = 0xFF0000FF
	DGreen       = 0x00FF00FF
	DBlue        = 0x0000FFFF
	DCyan        = 0x00FFFFFF
	DMagenta     = 0xFF00FFFF
	DYellow      = 0xFFFF00FF
	DNotacolor   = 0xFFFFFF00
	DNofill      = DNotacolor
)

// Line ends.
const (
	Endsquare = 0
	Enddisc   = 1
	Endarrow  = 2
	Endmask   = 0x1F
)

// Arrow builds a line end for an arrowhead with the given geometry.
func Arrow(a, b, c int) int { return Endarrow | a<<5 | b<<14 | c<<23 }

// ICOSSCALE is the scale of icossin's results.
const ICOSSCALE = 1024

// BytesPerLine and WordsPerLine are the storage of one row of r at depth
// d.
func BytesPerLine(r Rectangle, d int) int { return unitsPerLine(r, d, 8) }
func WordsPerLine(r Rectangle, d int) int { return unitsPerLine(r, d, 32) }

func unitsPerLine(r Rectangle, d, bits int) int {
	if r.Min.X >= 0 {
		l := (r.Max.X*d + bits - 1) / bits
		return l - (r.Min.X*d)/bits
	}
	t := (-r.Min.X*d + bits - 1) / bits
	return t + (r.Max.X*d+bits-1)/bits
}
