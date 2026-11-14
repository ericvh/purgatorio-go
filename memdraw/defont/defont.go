// Package defont is a small built-in subfont for tests and for programs
// that find no font: printable ASCII in a 4x7 pixel design of our own on
// a 5x8 cell. It is not one of Inferno's fonts.
package defont

// Geometry of the font.
const (
	Height  = 8 // rows in the subfont image
	Ascent  = 7 // rows above the baseline
	Advance = 5 // width of every printable character
	glyphw  = 4 // columns of bits per glyph
	First   = 0x20
	Last    = 0x7E
	N       = 128 // characters in the subfont, 0 to 127
)

// Each glyph is eight rows, one hex digit a row, bit 3 leftmost.
var glyphs = [Last - First + 1]string{
	"00000000", "04444040", "0AA00000", "0AFAFA00", "47C63E40", "09124900", "04A4A960", "04400000",
	"02444420", "04222240", "00A4A000", "004E4000", "00000448", "000E0000", "00000040", "01124880",
	"069BD960", "04C444E0", "069124F0", "0E1611E0", "026AF220", "0F8E11E0", "068E9960", "0F124440",
	"06969960", "06997160", "00400400", "00400448", "01242100", "00F0F000", "08424800", "06924040",
	"069BB860", "0699F990", "0E9E99E0", "06988960", "0E9999E0", "0F8E88F0", "0F8E8880", "068B9970",
	"099F9990", "0E4444E0", "01111960", "09ACA990", "088888F0", "09FF9990", "09DB9990", "06999960",
	"0E99E880", "06999A50", "0E99EA90", "078611E0", "0E444440", "09999960", "09999660", "0999FF90",
	"09966990", "0AA44440", "0F1248F0", "06444460", "08842110", "06222260", "04A00000", "000000F0",
	"04200000", "00617970", "088E99E0", "00078870", "01179970", "0069F870", "034E4440", "00799716",
	"088E9990", "040C44E0", "020222A4", "089ACA90", "0C4444E0", "000AF990", "000E9990", "00069960",
	"00E99E88", "00799711", "000BC880", "007861E0", "044E4430", "00099970", "00099660", "00099F60",
	"00096690", "00999716", "00F168F0", "024C4420", "04444440", "04232240", "005A0000",
}

// Char describes one character as Inferno's Fontchar does.
type Char struct {
	X           int // left column in the image
	Top, Bottom int // rows used
	Left        int // offset of the bits from the drawing point
	Width       int // advance
}

// Font returns the N+1 character descriptors, the image width in pixels,
// and the image as k1 rows of BytesPerLine bytes each.
func Font() (info []Char, width int, bits []byte) {
	info = make([]Char, N+1)
	x := 0
	for c := 0; c < N; c++ {
		info[c] = Char{X: x, Top: 0, Bottom: Height}
		if c >= First && c <= Last {
			info[c].Width = Advance
			x += glyphw
		}
	}
	info[N] = Char{X: x}
	width = x
	bpl := (width + 7) / 8
	bits = make([]byte, bpl*Height)
	for c := First; c <= Last; c++ {
		g := glyphs[c-First]
		x0 := info[c].X
		for y := 0; y < Height; y++ {
			v := hexval(g[y])
			for b := 0; b < glyphw; b++ {
				if v&(8>>b) != 0 {
					px := x0 + b
					bits[y*bpl+px/8] |= 0x80 >> (px % 8)
				}
			}
		}
	}
	return info, width, bits
}

// BytesPerLine is the row size of the image Font returns.
func BytesPerLine(width int) int { return (width + 7) / 8 }

func hexval(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}
