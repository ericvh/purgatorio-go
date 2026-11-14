package memdraw

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func alloc(t *testing.T, r Rectangle, c Chan, val uint32) *Image {
	t.Helper()
	i, err := Alloc(r, c)
	if err != nil {
		t.Fatal(err)
	}
	i.FillColor(val)
	return i
}

// color makes a 1x1 replicated image of val, as devdraw clients do.
func color(t *testing.T, c Chan, val uint32) *Image {
	i := alloc(t, Rect(0, 0, 1, 1), c, val)
	i.Flags |= Frepl
	i.Clipr = Rect(-0x3FFFFFF, -0x3FFFFFF, 0x3FFFFFF, 0x3FFFFFF)
	return i
}

func unload(t *testing.T, i *Image, r Rectangle) []byte {
	t.Helper()
	b := make([]byte, BytesPerLine(r, i.Depth)*r.Dy())
	if _, err := Unload(i, r, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// pix returns the pixel at p as rrggbbaa.
func pix(t *testing.T, i *Image, p Point) uint32 {
	t.Helper()
	v := pixelbits(i, p)
	if i.Depth < 32 {
		v &= 1<<i.Depth - 1
	}
	return imgtorgba(i, v)
}

// art renders a k1 or other image as text: '#' for a nonzero pixel.
func art(i *Image) string {
	var s strings.Builder
	for y := i.R.Min.Y; y < i.R.Max.Y; y++ {
		for x := i.R.Min.X; x < i.R.Max.X; x++ {
			if i.pixel(i.rowbase(y), x) != 0 {
				s.WriteByte('#')
			} else {
				s.WriteByte('.')
			}
		}
		s.WriteByte('\n')
	}
	return s.String()
}

func TestChan(t *testing.T) {
	for _, c := range []struct {
		s     string
		depth int
	}{{"k1", 1}, {"k2", 2}, {"k4", 4}, {"k8", 8}, {"m8", 8}, {"r5g6b5", 16}, {"x1r5g5b5", 16},
		{"r8g8b8", 24}, {"x8r8g8b8", 32}, {"a8r8g8b8", 32}, {"r8g8b8a8", 32}, {"a8k8", 16}} {
		ch := ParseChan(c.s)
		if ch.Depth() != c.depth || ch.String() != c.s {
			t.Errorf("%s: depth %d string %q", c.s, ch.Depth(), ch.String())
		}
	}
	if ParseChan("r8g8b8") != RGB24 || ParseChan("x8r8g8b8") != XRGB32 || ParseChan(" k1 ") != GREY1 {
		t.Error("constants")
	}
	for _, s := range []string{"q8", "r9", "k3", "r8g8b", ""} {
		if c := ParseChan(s); c.Depth() != 0 {
			t.Errorf("%q parsed as %v", s, c)
		}
	}
}

func TestTables(t *testing.T) {
	// Values of the trigonometric tables that can be checked by hand.
	if sinus[30] != 512 || sinus[90] != 1024 || sinus[0] != 0 || sinus[45] != 724 {
		t.Errorf("sinus %d %d %d %d", sinus[30], sinus[90], sinus[0], sinus[45])
	}
	if c, s := Icossin(180); c != -1024 || s != 0 {
		t.Errorf("icossin 180 = %d %d", c, s)
	}
	if c, s := Icossin(-90); c != 0 || s != -1024 {
		t.Errorf("icossin -90 = %d %d", c, s)
	}
	if c, s := Icossin2(1, 1); c != 724 || s != 724 {
		t.Errorf("icossin2 1 1 = %d %d", c, s)
	}
	// rgbv: index 0 is black, 255 white, and the map inverts itself.
	if CmapRGB(0) != 0 || CmapRGB(255) != 0xFFFFFF {
		t.Error("cmap ends")
	}
	for i := 0; i < 256; i++ {
		v := CmapRGB(i)
		if j := RGBCmap(int(v>>16), int(v>>8&0xFF), int(v&0xFF)); CmapRGB(j) != v {
			t.Errorf("cmap %d -> %06x -> %d", i, v, j)
		}
	}
	for n, want := range map[int][]byte{1: {0, 255}, 2: {0, 85, 170, 255}, 4: {0, 17, 34}} {
		for v, w := range want {
			if replbit[n][v] != w {
				t.Errorf("replbit[%d][%d] = %d want %d", n, v, replbit[n][v], w)
			}
		}
	}
	if replbit[5][31] != 255 || replbit[5][16] != 132 || replbit[6][32] != 130 {
		t.Errorf("replbit 5,6: %d %d %d", replbit[5][31], replbit[5][16], replbit[6][32])
	}
}

func TestFill(t *testing.T) {
	r := Rect(0, 0, 3, 2)
	cases := []struct {
		ch   Chan
		val  uint32
		want []byte // first pixel's bytes
	}{
		{XRGB32, DRed, []byte{0, 0, 0xFF, 0}},
		{ARGB32, 0x11223344, []byte{0x33, 0x22, 0x11, 0x44}},
		{RGBA32, 0x11223344, []byte{0x44, 0x33, 0x22, 0x11}},
		{RGB24, DBlue, []byte{0xFF, 0, 0}},
		{BGR24, DBlue, []byte{0, 0, 0xFF}},
		{RGB16, DWhite, []byte{0xFF, 0xFF}},
		{RGB16, DGreen, []byte{0xE0, 0x07}},
		{GREY8, DRed, []byte{76}}, // .299*255
		{GREY8, DWhite, []byte{255}},
		{CMAP8, DWhite, []byte{255}},
		{CMAP8, DBlack, []byte{0}},
	}
	for _, c := range cases {
		i := alloc(t, r, c.ch, c.val)
		b := unload(t, i, r)
		nb := len(c.want)
		for p := 0; p < 6; p++ {
			if !bytes.Equal(b[p*nb:p*nb+nb], c.want) {
				t.Errorf("%v %08x: pixel %d = % x want % x", c.ch, c.val, p, b[p*nb:p*nb+nb], c.want)
				break
			}
		}
	}
	k1 := alloc(t, Rect(0, 0, 10, 1), GREY1, DWhite)
	if b := unload(t, k1, k1.R); !bytes.Equal(b, []byte{0xFF, 0xFF}) {
		t.Errorf("k1 white % x", b)
	}
	k2 := alloc(t, Rect(0, 0, 4, 1), GREY2, 0x555555FF)
	if b := unload(t, k2, k2.R); !bytes.Equal(b, []byte{0x55}) {
		t.Errorf("k2 % x", b)
	}
}

// over is the Porter-Duff S over D on premultiplied 8-bit values, with
// Inferno's rounding, computed independently: s + d*(255-sa)/255.
func over(s, d, sa int) int {
	x := d * (255 - sa)
	return s + (x+127)/255
}

func TestCompositing(t *testing.T) {
	r := Rect(0, 0, 4, 4)
	// Opaque S onto x8r8g8b8.
	dst := alloc(t, r, XRGB32, DWhite)
	ImageDraw(dst, r, color(t, RGB24, DRed), ZP, nil, ZP, S)
	if b := unload(t, dst, Rect(1, 1, 2, 2)); !bytes.Equal(b, []byte{0, 0, 0xFF, 0}) {
		t.Errorf("S red: % x", b)
	}
	// Half-transparent premultiplied red over white.
	for _, sa := range []int{0x80, 0x40, 0x01, 0xFE} {
		dst = alloc(t, r, XRGB32, DWhite)
		src := color(t, ARGB32, uint32(sa)<<24|uint32(sa))
		ImageDraw(dst, r, src, ZP, nil, ZP, SoverD)
		got := pix(t, dst, Pt(2, 2))
		want := uint32(over(sa, 255, sa))<<24 | uint32(over(0, 255, sa))<<16 | uint32(over(0, 255, sa))<<8 | 0xFF
		if got != want {
			t.Errorf("alpha %#x over white: %08x want %08x", sa, got, want)
		}
	}
	// A grey mask: black through 0x40 onto white.
	dst = alloc(t, r, XRGB32, DWhite)
	mask := color(t, GREY8, 0x404040FF)
	ImageDraw(dst, r, Black, ZP, mask, ZP, SoverD)
	if got := pix(t, dst, Pt(0, 0)); got != 0xBFBFBFFF {
		t.Errorf("grey mask: %08x", got)
	}
	// Onto an image with alpha: transparent dst takes the source.
	dst = alloc(t, r, ARGB32, DTransparent)
	ImageDraw(dst, r, color(t, RGBA32, 0x40000040), ZP, nil, ZP, SoverD)
	if got := pix(t, dst, Pt(3, 3)); got != 0x40000040 {
		t.Errorf("over transparent: %08x", got)
	}
	// The operators on one pixel: src 0x80 red alpha 0x80, dst 0x60
	// blue alpha 0xC0.
	sa, da := 0x80, 0xC0
	m := func(a, b int) int { return (a*b + 127) / 255 }
	ops := map[int][2]int{ // op -> (fs, fd) in Porter-Duff terms
		S: {255, 0}, SinD: {da, 0}, SoutD: {255 - da, 0}, SatopD: {da, 255 - sa}, SoverD: {255, 255 - sa},
		SxorD: {255 - da, 255 - sa}, D: {0, 255}, DinS: {0, sa}, DoutS: {0, 255 - sa}, DatopS: {255 - da, sa},
		DoverS: {255 - da, 255}, Clear: {0, 0},
	}
	for op, f := range ops {
		dst = alloc(t, Rect(0, 0, 1, 1), ARGB32, 0x000060C0)
		ImageDraw(dst, dst.R, color(t, RGBA32, 0x80000080), ZP, nil, ZP, op)
		want := uint32(m(f[0], 0x80))<<24 | uint32(m(f[1], 0x60))<<8 | uint32(m(f[0], sa)+m(f[1], da))
		if got := pix(t, dst, ZP); got != want {
			t.Errorf("op %d: %08x want %08x", op, got, want)
		}
	}
}

// Inferno composites whole words when source and destination have the
// same 32-bit channels, even when the source is a replicated image whose
// bytes its buffer holds in another order. The channels then mix; this
// checks that they mix as they do in Inferno.
func TestWordMix(t *testing.T) {
	dst := alloc(t, Rect(0, 0, 1, 1), ARGB32, 0x000060C0)
	ImageDraw(dst, dst.R, color(t, ARGB32, 0x80000080), ZP, nil, ZP, SoverD)
	// Memory holds b, g, r, a; the buffer a, b, g, r. Lane by lane:
	// src (a=80, b=0, g=0, r=80) over dst (b=60, g=0, r=0, a=c0) with
	// fd = 255-0x80.
	m := func(a, b int) int { return (a*b + 127) / 255 }
	fd := 255 - 0x80
	want := []byte{byte(0x80 + m(fd, 0x60)), 0, 0, byte(0x80 + m(fd, 0xC0))}
	if b := unload(t, dst, dst.R); !bytes.Equal(b, want) {
		t.Errorf("word mix: % x want % x", b, want)
	}
}

func TestGreyAndCmap(t *testing.T) {
	r := Rect(0, 0, 2, 2)
	// Color source onto grey: NTSC grey of the source.
	dst := alloc(t, r, GREY8, DBlack)
	ImageDraw(dst, r, color(t, RGB24, 0x00FF00FF), ZP, nil, ZP, S)
	if b := unload(t, dst, r); b[0] != 149 { // .587*255
		t.Errorf("green as grey: %d", b[0])
	}
	// k1 with a grey mask at half: values at or above 0x80 set the bit.
	k1 := alloc(t, Rect(0, 0, 8, 1), GREY1, DBlack)
	ImageDraw(k1, k1.R, White, ZP, color(t, GREY8, 0x808080FF), ZP, SoverD)
	if b := unload(t, k1, k1.R); b[0] != 0xFF {
		t.Errorf("k1 half mask: %x", b[0])
	}
	// Onto m8, a color goes through the inverse map.
	m8 := alloc(t, r, CMAP8, DBlack)
	ImageDraw(m8, r, color(t, RGB24, DRed), ZP, nil, ZP, S)
	want := byte(RGBCmap(255, 0, 0))
	if b := unload(t, m8, r); b[3] != want || CmapRGB(int(want)) != 0xFF0000 {
		t.Errorf("red in m8: %d want %d", b[3], want)
	}
	// m8 read back as RGB.
	dst = alloc(t, r, RGB24, DBlack)
	ImageDraw(dst, r, m8, ZP, nil, ZP, S)
	if got := pix(t, dst, Pt(1, 1)); got != 0xFF0000FF {
		t.Errorf("m8 to rgb: %08x", got)
	}
}

func TestReplClip(t *testing.T) {
	// A 2x2 checkerboard tile, replicated, drawn with an offset.
	tile := alloc(t, Rect(0, 0, 2, 2), GREY1, DBlack)
	LoadImage(tile, tile.R, []byte{0x80, 0x40})
	tile.Flags |= Frepl
	tile.Clipr = Rect(-100, -100, 100, 100)
	dst := alloc(t, Rect(0, 0, 6, 3), GREY1, DBlack)
	ImageDraw(dst, Rect(1, 0, 6, 3), tile, Pt(1, 0), nil, ZP, S)
	want := "..#.#.\n.#.#.#\n..#.#.\n"
	if got := art(dst); got != want {
		t.Errorf("repl:\n%swant\n%s", got, want)
	}
	// Clipping to dst.Clipr.
	dst = alloc(t, Rect(0, 0, 4, 2), GREY1, DBlack)
	dst.Clipr = Rect(1, 0, 3, 1)
	ImageDraw(dst, dst.R, White, ZP, nil, ZP, S)
	if got := art(dst); got != ".##.\n....\n" {
		t.Errorf("clip:\n%s", got)
	}
	// Negative origins.
	neg := alloc(t, Rect(-3, -2, 2, 1), XRGB32, DBlack)
	ImageDraw(neg, Rect(-1, -1, 0, 0), color(t, RGB24, DWhite), ZP, nil, ZP, S)
	if pix(t, neg, Pt(-1, -1)) != 0xFFFFFFFF || pix(t, neg, Pt(0, -1)) != 0x000000FF {
		t.Error("negative origin")
	}
	k1 := alloc(t, Rect(-5, 0, 3, 1), GREY1, DBlack)
	ImageDraw(k1, Rect(-3, 0, 1, 1), White, ZP, nil, ZP, S)
	if got := art(k1); got != "..####..\n" {
		t.Errorf("negative k1: %s", got)
	}
}

func TestCopyOverlap(t *testing.T) {
	// Scrolling within one image, both directions.
	for _, dir := range []int{1, -1} {
		img := alloc(t, Rect(0, 0, 4, 4), GREY8, DBlack)
		for y := 0; y < 4; y++ {
			LoadImage(img, Rect(0, y, 4, y+1), []byte{byte(y), byte(y), byte(y), byte(y)})
		}
		ImageDraw(img, Rect(0, 1, 4, 3).Add(Pt(0, dir)), img, Pt(0, 1), nil, ZP, S)
		b := unload(t, img, img.R)
		var want []byte
		if dir == 1 {
			want = []byte{0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2}
		} else {
			want = []byte{1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 3, 3, 3, 3}
		}
		if !bytes.Equal(b, want) {
			t.Errorf("scroll %d: %v", dir, b)
		}
	}
}

func TestLoadUnload(t *testing.T) {
	k1 := alloc(t, Rect(0, 0, 16, 2), GREY1, DBlack)
	// An unaligned load touches only its own bits.
	if _, err := LoadImage(k1, Rect(3, 0, 13, 1), []byte{0xFF, 0xFF}); err != nil {
		t.Fatal(err)
	}
	if got := art(k1); got != "...##########...\n................\n" {
		t.Errorf("unaligned load:\n%s", got)
	}
	if _, err := LoadImage(k1, Rect(0, 0, 17, 1), make([]byte, 8)); err == nil {
		t.Error("load outside")
	}
	if _, err := LoadImage(k1, k1.R, make([]byte, 3)); err == nil {
		t.Error("short load")
	}
	// Compressed data: a literal run of 4, then a match of 4 at offset 4.
	img := alloc(t, Rect(0, 0, 8, 1), GREY8, DBlack)
	n, err := CLoadImage(img, img.R, []byte{0x83, 1, 2, 3, 4, (4 - NMATCH) << 2, 3})
	if err != nil || n != 7 {
		t.Fatalf("cload %d %v", n, err)
	}
	if b := unload(t, img, img.R); !bytes.Equal(b, []byte{1, 2, 3, 4, 1, 2, 3, 4}) {
		t.Errorf("cload: %v", b)
	}
}

func TestLines(t *testing.T) {
	k1 := alloc(t, Rect(0, 0, 8, 5), GREY1, DBlack)
	Line(k1, Pt(1, 1), Pt(6, 1), Endsquare, Endsquare, 0, White, ZP, S)
	Line(k1, Pt(0, 0), Pt(0, 4), Endsquare, Endsquare, 0, White, ZP, S)
	Line(k1, Pt(3, 3), Pt(7, 3), Endsquare, Endsquare, 1, White, ZP, S)
	want := "#.......\n" +
		"#######.\n" +
		"#..#####\n" +
		"#..#####\n" +
		"#..#####\n"
	if got := art(k1); got != want {
		t.Errorf("lines:\n%swant\n%s", got, want)
	}
	// A diagonal line of radius 0 is one pixel per row, symmetric.
	k1 = alloc(t, Rect(0, 0, 5, 5), GREY1, DBlack)
	Line(k1, Pt(0, 0), Pt(4, 4), Endsquare, Endsquare, 0, White, ZP, S)
	if got := art(k1); got != "#....\n.#...\n..#..\n...#.\n....#\n" {
		t.Errorf("diagonal:\n%s", got)
	}
	if r := LineBBox(Pt(5, 5), Pt(1, 2), Endsquare, Endsquare, 1); r != Rect(0, 1, 7, 7) {
		t.Errorf("bbox %v", r)
	}
}

func TestEllipseArcPoly(t *testing.T) {
	k1 := alloc(t, Rect(-4, -4, 5, 5), GREY1, DBlack)
	Ellipse(k1, ZP, 3, 3, -1, White, ZP, S)
	got := art(k1)
	// A filled circle of radius 3 is symmetric and spans 7 pixels on its
	// axes.
	rows := strings.Split(strings.TrimSpace(got), "\n")
	for i := range rows {
		j := len(rows) - 1 - i
		if rows[i] != rows[j] {
			t.Errorf("not symmetric:\n%s", got)
		}
		r := []byte(rows[i])
		for a, b := 0, len(r)-1; a < b; a, b = a+1, b-1 {
			if r[a] != r[b] {
				t.Errorf("not symmetric:\n%s", got)
			}
		}
	}
	if rows[4] != ".#######." || rows[0] != "........." || rows[1] != "...###..." {
		t.Errorf("circle:\n%s", got)
	}
	// An outline of thickness 0 leaves the centre clear.
	k1 = alloc(t, Rect(-4, -4, 5, 5), GREY1, DBlack)
	Ellipse(k1, ZP, 3, 3, 0, White, ZP, S)
	o := strings.Split(art(k1), "\n")
	if o[4] != ".#.....#." {
		t.Errorf("outline:\n%s", art(k1))
	}
	// A quarter arc (0 to 90 degrees) is in the upper right quadrant.
	k1 = alloc(t, Rect(-4, -4, 5, 5), GREY1, DBlack)
	Arc(k1, ZP, 3, 3, -1, White, ZP, 0, 90, S)
	a := strings.Split(art(k1), "\n")
	if a[6] != "........." || a[1][1:4] != "..." || a[1][4:7] == "..." {
		t.Errorf("arc:\n%s", art(k1))
	}
	// A filled rectangle polygon covers exactly the half-open pixels.
	k1 = alloc(t, Rect(0, 0, 6, 5), GREY1, DBlack)
	FillPoly(k1, []Point{{1, 1}, {5, 1}, {5, 4}, {1, 4}}, ^0, White, ZP, S)
	if got := art(k1); got != "......\n.####.\n.####.\n.####.\n......\n" {
		t.Errorf("fillpoly:\n%s", got)
	}
	// A triangle.
	k1 = alloc(t, Rect(0, 0, 7, 4), GREY1, DBlack)
	FillPoly(k1, []Point{{0, 0}, {6, 0}, {3, 3}}, ^0, White, ZP, S)
	if got := art(k1); got != ".#####.\n..###..\n...#...\n.......\n" {
		t.Errorf("triangle:\n%s", got)
	}
}

func TestString(t *testing.T) {
	f := Defont()
	dst := alloc(t, Rect(0, 0, 20, f.Height), GREY1, DBlack)
	p := ImageString(dst, Pt(0, 0), White, ZP, f, "Hi!")
	if p.X != 15 {
		t.Errorf("advance %d", p.X)
	}
	want := "...................." + "\n" +
		"#..#..#....#........\n" +
		"#..#.......#........\n" +
		"####.##....#........\n" +
		"#..#..#....#........\n" +
		"#..#..#.............\n" +
		"#..#.###...#........\n" +
		"....................\n"
	if got := art(dst); got != want {
		t.Errorf("string:\n%swant\n%s", got, want)
	}
	if w := SubfontWidth(f, "Hi!\x01é"); w != Pt(15, f.Height) {
		t.Errorf("width %v", w)
	}
	// Colored text on a 32-bit image goes through chardraw.
	rgb := alloc(t, Rect(0, 0, 10, 8), XRGB32, DWhite)
	ImageString(rgb, ZP, color(t, RGB24, DBlue), ZP, f, "I")
	if pix(t, rgb, Pt(1, 1)) != 0x0000FFFF || pix(t, rgb, Pt(3, 1)) != 0xFFFFFFFF {
		t.Error("blue I")
	}
}

func TestLayers(t *testing.T) {
	scr := alloc(t, Rect(0, 0, 20, 10), GREY8, 0x101010FF)
	s := &Screen{Image: scr, Fill: color(t, GREY8, 0x202020FF)}
	w1, err := Lalloc(s, Rect(2, 2, 10, 8), nil, nil, 0x404040FF)
	if err != nil {
		t.Fatal(err)
	}
	w2, err := Lalloc(s, Rect(6, 4, 14, 9), nil, nil, 0x808080FF)
	if err != nil {
		t.Fatal(err)
	}
	at := func(p Point) byte { return scr.Data.Bdata[scr.ByteAddr(p)] }
	if at(Pt(3, 3)) != 0x40 || at(Pt(7, 5)) != 0x80 || at(Pt(0, 0)) != 0x10 {
		t.Fatalf("windows: %x %x %x", at(Pt(3, 3)), at(Pt(7, 5)), at(Pt(0, 0)))
	}
	// Draw all of w1, which is partly behind w2.
	Draw(w1, w1.R, color(t, GREY8, 0xF0F0F0FF), ZP, nil, ZP, S)
	if at(Pt(3, 3)) != 0xF0 || at(Pt(7, 5)) != 0x80 {
		t.Errorf("obscured draw: %x %x", at(Pt(3, 3)), at(Pt(7, 5)))
	}
	// Reading w1 back sees the hidden part too.
	b := make([]byte, 8*6)
	if _, err := Unload(w1, w1.R, b); err != nil {
		t.Fatal(err)
	}
	for i, v := range b {
		if v != 0xF0 {
			t.Fatalf("unload w1 byte %d = %x", i, v)
		}
	}
	// To the front: the hidden part comes from the save area.
	Ltofront(w1)
	if at(Pt(7, 5)) != 0xF0 || at(Pt(12, 7)) != 0x80 {
		t.Errorf("tofront: %x %x", at(Pt(7, 5)), at(Pt(12, 7)))
	}
	// Move w2; its old place is repainted with the screen's fill.
	if n, err := Lorigin(w2, w2.R.Min, Pt(10, 0)); n != 1 || err != nil {
		t.Fatalf("origin %d %v", n, err)
	}
	if at(Pt(12, 2)) != 0x80 || at(Pt(12, 8)) != 0x20 {
		t.Errorf("moved: %x %x", at(Pt(12, 2)), at(Pt(12, 8)))
	}
	// A line in a window lands at the window's screen position.
	Line(w2, Pt(7, 5), Pt(9, 5), Endsquare, Endsquare, 0, White, ZP, S)
	if at(Pt(11, 1)) != 0xFF || at(Pt(14, 1)) != 0x80 {
		t.Errorf("line in window: %x %x", at(Pt(11, 1)), at(Pt(14, 1)))
	}
	Ldelete(w1)
	if at(Pt(3, 3)) != 0x20 {
		t.Errorf("deleted: %x", at(Pt(3, 3)))
	}
}

func ExampleChan_String() {
	fmt.Println(XRGB32, GREY1, RGB16, CMAP8)
	// Output: x8r8g8b8 k1 r5g6b5 m8
}
