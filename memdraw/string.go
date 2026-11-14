package memdraw

import "unicode/utf8"

// Fontchar describes one character of a subfont: its column X in the
// subfont image (the next character's X ends it), the rows it occupies,
// its offset from the drawing point and its advance.
type Fontchar struct {
	X      int
	Top    byte
	Bottom byte
	Left   int8
	Width  byte
}

// Subfont is a Memsubfont: N characters in one image, with N+1 Fontchars.
type Subfont struct {
	Name   string
	N      int
	Height int
	Ascent int
	Info   []Fontchar
	Bits   *Image
}

// ImageString draws s with subfont f at p in b, colored by color from cp
// (memimagestring). It returns the point after the last character.
func ImageString(b *Image, p Point, color *Image, cp Point, f *Subfont, s string) Point {
	for len(s) > 0 {
		c, w := utf8.DecodeRuneInString(s)
		if c == utf8.RuneError && w <= 1 {
			if w == 0 {
				break
			}
		}
		s = s[w:]
		if int(c) >= f.N {
			continue
		}
		i := &f.Info[c]
		width := int(i.Width)
		Draw(b, Rect(p.X+int(i.Left), p.Y+int(i.Top), p.X+int(i.Left)+(f.Info[c+1].X-i.X), p.Y+int(i.Bottom)),
			color, cp, f.Bits, Pt(i.X, int(i.Top)), SoverD)
		p.X += width
		cp.X += width
	}
	return p
}

// SubfontWidth is the size of s in f (memsubfontwidth).
func SubfontWidth(f *Subfont, s string) Point {
	p := Pt(0, f.Height)
	for _, c := range s {
		if int(c) < f.N {
			p.X += int(f.Info[c].Width)
		}
	}
	return p
}
