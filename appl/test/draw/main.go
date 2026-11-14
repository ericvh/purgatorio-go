// Command draw checks graphics end to end: it draws lines, an ellipse, an
// image and text through sdk/draw and the draw device, reads the screen
// back and checks the pixels.
package main

import (
	"bytes"
	"fmt"

	"github.com/ericvh/purgatorio-go/memdraw/defont"
	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func fail(f string, a ...any) {
	fmt.Printf(f+"\n", a...)
	sys.Exits("fail:draw")
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

// px returns the pixel at p of a 32-bit screen as 0xRRGGBB.
func px(scr *draw.Image, p draw.Point) uint32 {
	b, err := scr.Unload(draw.Rect(p.X, p.Y, p.X+1, p.Y+1))
	check(err)
	return uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
}

func main() {
	d, err := draw.Init("")
	check(err)
	scr := d.Image
	fmt.Printf("screen %d %d %s\n", scr.R.Dx(), scr.R.Dy(), scr.Chan)
	if scr.Chan != draw.XRGB32 {
		fail("unexpected screen format")
	}
	red, err := d.Color(draw.Red)
	check(err)
	blue, err := d.Color(draw.Blue)
	check(err)
	check(scr.Draw(scr.R, d.White, nil, draw.ZP))

	// Lines: a thin horizontal one and a thick vertical one.
	check(scr.Line(draw.Pt(10, 10), draw.Pt(50, 10), draw.Endsquare, draw.Endsquare, 0, red, draw.ZP))
	check(scr.Line(draw.Pt(60, 10), draw.Pt(60, 40), draw.Endsquare, draw.Endsquare, 2, d.Black, draw.ZP))
	check(d.Flush(true))
	for x := 10; x <= 50; x++ {
		if px(scr, draw.Pt(x, 10)) != 0xFF0000 || px(scr, draw.Pt(x, 11)) != 0xFFFFFF {
			fail("thin line at %d", x)
		}
	}
	for x := 57; x <= 63; x++ {
		want := uint32(0xFFFFFF)
		if x >= 58 && x <= 62 {
			want = 0
		}
		if got := px(scr, draw.Pt(x, 25)); got != want {
			fail("thick line at %d: %06x", x, got)
		}
	}
	fmt.Println("lines ok")

	// A filled circle: its axes span the diameter, its corners stay clear.
	c := draw.Pt(100, 100)
	check(scr.FillEllipse(c, 20, 20, blue, draw.ZP))
	check(d.Flush(true))
	if px(scr, c) != 0x0000FF || px(scr, c.Add(draw.Pt(20, 0))) != 0x0000FF ||
		px(scr, c.Add(draw.Pt(21, 0))) != 0xFFFFFF || px(scr, c.Add(draw.Pt(0, -20))) != 0x0000FF ||
		px(scr, c.Add(draw.Pt(15, 15))) != 0xFFFFFF {
		fail("ellipse")
	}
	fmt.Println("ellipse ok")

	// An image: a grey ramp, drawn and read back.
	img, err := d.AllocImage(draw.Rect(0, 0, 16, 4), draw.GREY8, false, draw.Black)
	check(err)
	ramp := make([]byte, 16*4)
	for k := range ramp {
		ramp[k] = byte(17 * (k % 16))
	}
	_, err = img.Load(img.R, ramp)
	check(err)
	check(scr.Draw(draw.Rect(200, 200, 216, 204), img, nil, draw.ZP))
	check(d.Flush(true))
	got, err := scr.Unload(draw.Rect(200, 202, 216, 203))
	check(err)
	for x := 0; x < 16; x++ {
		v := byte(17 * x)
		if !bytes.Equal(got[4*x:4*x+3], []byte{v, v, v}) {
			fail("image pixel %d: % x", x, got[4*x:4*x+4])
		}
	}
	fmt.Println("image ok")

	// Text in the built-in font: every pixel must match the glyph bits.
	f, err := d.GetDefaultFont()
	check(err)
	const text = "Hello, world"
	at := draw.Pt(300, 50)
	end, err := scr.String(at, d.Black, draw.ZP, f, text)
	check(err)
	check(d.Flush(true))
	if end.X-at.X != f.Width(text) || f.Width(text) != len(text)*defont.Advance {
		fail("text width %d", end.X-at.X)
	}
	info, width, bits := defont.Font()
	bpl := defont.BytesPerLine(width)
	region, err := scr.Unload(draw.Rect(at.X, at.Y, end.X, at.Y+f.Height))
	check(err)
	rw := end.X - at.X
	for k, ch := range text {
		gx := info[ch].X
		for y := 0; y < defont.Height; y++ {
			for x := 0; x < defont.Advance; x++ {
				set := x < 4 && bits[y*bpl+(gx+x)/8]&(0x80>>((gx+x)%8)) != 0
				p := region[4*(y*rw+k*defont.Advance+x):]
				if set != (p[0] == 0 && p[1] == 0 && p[2] == 0) {
					fail("text %q pixel %d,%d", ch, x, y)
				}
			}
		}
	}
	fmt.Println("text ok")
	check(d.Close())
}
