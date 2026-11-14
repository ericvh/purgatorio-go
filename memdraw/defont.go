package memdraw

import "github.com/ericvh/purgatorio-go/memdraw/defont"

// Defont returns the built-in subfont (see package defont).
func Defont() *Subfont {
	info, width, bits := defont.Font()
	img, err := Alloc(Rect(0, 0, width, defont.Height), GREY1)
	if err != nil {
		return nil
	}
	if _, err := LoadImage(img, img.R, bits); err != nil {
		return nil
	}
	f := &Subfont{Name: "*default*", N: defont.N, Height: defont.Height, Ascent: defont.Ascent, Bits: img}
	f.Info = make([]Fontchar, len(info))
	for i, c := range info {
		f.Info[i] = Fontchar{X: c.X, Top: byte(c.Top), Bottom: byte(c.Bottom), Left: int8(c.Left), Width: byte(c.Width)}
	}
	return f
}
