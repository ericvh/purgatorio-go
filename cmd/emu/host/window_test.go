package host

import (
	"bytes"
	"image"
	"testing"

	"github.com/ericvh/purgatorio-go/platform"
)

func TestX11KeyRune(t *testing.T) {
	for _, k := range []struct {
		sym  uint32
		ctrl bool
		want rune
		ok   bool
	}{
		{'a', false, 'a', true},
		{'A', false, 'A', true},
		{'a', true, 0x01, true},
		{'[', true, 0x1B, true},
		{0xE9, false, 'é', true}, // eacute
		{0xAD, false, '-', true}, // hyphen
		{0x01000416, false, 'Ж', true},
		{xkEuroSign, false, '€', true},
		{xkReturn, false, '\n', true},
		{xkKPEnter, false, '\n', true},
		{xkLinefeed, false, '\r', true},
		{xkBackSpace, false, '\b', true},
		{xkTab, false, '\t', true},
		{xkEscape, false, 0x1B, true},
		{xkDelete, false, 0x7F, true},
		{xkHome, false, 0xE010, true},
		{xkEnd, false, 0xE011, true},
		{xkUp, false, 0xE012, true},
		{xkDown, false, 0xE013, true},
		{xkLeft, false, 0xE014, true},
		{xkRight, false, 0xE015, true},
		{xkPageUp, false, 0xE016, true},
		{xkPageDown, false, 0xE017, true},
		{xkKPHome, false, 0xE010, true},
		{xkInsert, false, 0xE063, true},
		{xkHome, true, 0xE010, true},
		{xkF1, false, 0xE041, true},
		{xkF12, false, 0xE04C, true},
		{0xFFB7, false, '7', true}, // KP_7
		{0xFFAB, false, '+', true}, // KP_Add
		{0xFFAE, false, '.', true}, // KP_Decimal
		{xkShiftL, false, 0, false},
		{0xFFE3, false, 0, false}, // Control_L
		{0xFFE9, false, 0, false}, // Alt_L
		{xkMultiKey, false, 0, false},
		{0, false, 0, false},
		{0x06C1, false, 0, false}, // a legacy Cyrillic symbol: none
	} {
		r, ok := x11KeyRune(k.sym, k.ctrl)
		if r != k.want || ok != k.ok {
			t.Errorf("x11KeyRune(%#x, %v) = %#x %v, want %#x %v", k.sym, k.ctrl, r, ok, k.want, k.ok)
		}
	}
}

func TestX11Keysym(t *testing.T) {
	const numlock, modeswitch = 1 << 4, 1 << 7
	a := []uint32{'a', 'A'}
	one := []uint32{'1', '!'}
	kp7 := []uint32{xkKPHome, 0xFFB7}
	alpha := []uint32{'q', 0} // an upper case left out
	grp := []uint32{'e', 'E', 0x20AC, 0}
	for _, k := range []struct {
		syms  []uint32
		state uint16
		want  uint32
	}{
		{a, 0, 'a'},
		{a, xShiftMask, 'A'},
		{a, xLockMask, 'A'},
		{a, xLockMask | xShiftMask, 'A'},
		{one, xLockMask, '1'},
		{one, xShiftMask, '!'},
		{alpha, xShiftMask, 'Q'},
		{alpha, 0, 'q'},
		{kp7, 0, xkKPHome},
		{kp7, numlock, 0xFFB7},
		{kp7, numlock | xShiftMask, xkKPHome},
		{grp, modeswitch, 0x20AC},
		{grp, 0, 'e'},
		{[]uint32{xkReturn}, xShiftMask, xkReturn},
		{nil, 0, 0},
	} {
		if got := x11Keysym(k.syms, k.state, numlock, modeswitch); got != k.want {
			t.Errorf("x11Keysym(%#x, %#x) = %#x, want %#x", k.syms, k.state, got, k.want)
		}
	}
}

func TestX11Buttons(t *testing.T) {
	const b1, b2, b3 = 1 << 8, 1 << 9, 1 << 10
	for _, k := range []struct {
		state  uint16
		button byte
		press  bool
		want   int
	}{
		{0, 1, true, 1},
		{b1, 1, false, 0},
		{0, 2, true, 2},
		{0, 3, true, 4},
		{b1, 3, true, 5},
		{b1 | b3, 1, false, 4},
		{0, 4, true, 8}, // wheel up
		{1 << 11, 4, false, 0},
		{0, 5, true, 16},  // wheel down
		{b2, 0, false, 2}, // a motion with button 2 held
		{xShiftMask | xControlMask, 1, true, 1},
	} {
		if got := x11Buttons(k.state, k.button, k.press); got != k.want {
			t.Errorf("x11Buttons(%#x, %d, %v) = %d, want %d", k.state, k.button, k.press, got, k.want)
		}
	}
}

func TestMacKeyRune(t *testing.T) {
	for _, k := range []struct {
		code  uint16
		chars string
		want  rune
		ok    bool
	}{
		{0x00, "a", 'a', true},
		{0x00, "\x01", 0x01, true}, // control-a
		{0x24, "\r", '\n', true},
		{0x4C, "\x03", '\n', true},
		{0x33, "\x7f", '\b', true},
		{0x75, "", 0x7F, true},
		{0x35, "\x1b", 0x1B, true},
		{0x30, "\t", '\t', true},
		{0x7E, "", 0xE012, true},
		{0x7D, "", 0xE013, true},
		{0x7B, "", 0xE014, true},
		{0x7C, "", 0xE015, true},
		{0x73, "", 0xE010, true},
		{0x77, "", 0xE011, true},
		{0x74, "", 0xE016, true},
		{0x79, "", 0xE017, true},
		{0x72, "", 0xE063, true},
		{0x7A, "", 0xE041, true},
		{0x6F, "", 0xE04C, true},
		{0x5B, "8", '8', true},
		{0x0E, "é", 'é', true},
		{0x69, "", 0, false}, // F13: none
		{0x38, "", 0, false},  // shift alone
	} {
		r, ok := macKeyRune(k.code, k.chars)
		if r != k.want || ok != k.ok {
			t.Errorf("macKeyRune(%#x, %q) = %#x %v, want %#x %v", k.code, k.chars, r, ok, k.want, k.ok)
		}
	}
}

func TestMacButtons(t *testing.T) {
	for _, k := range []struct {
		pressed, flags uint
		want           int
	}{
		{0, 0, 0},
		{1, 0, 1},
		{1, macOption, 2},
		{1, macCommand, 4},
		{2, 0, 4},
		{4, 0, 2},
		{3, 0, 5},
		{0, macOption, 0},
	} {
		if got := macButtons(k.pressed, k.flags); got != k.want {
			t.Errorf("macButtons(%d, %#x) = %d, want %d", k.pressed, k.flags, got, k.want)
		}
	}
}

// TestConvert checks the frame's pixels in the formats an X server may
// want, and back.
func TestConvert(t *testing.T) {
	// Two rows of three x8r8g8b8 pixels, 16 bytes a row with a spare
	// pixel: red, green, blue; white, black, a grey.
	src := []byte{
		0, 0, 0xFF, 0, 0, 0xFF, 0, 0, 0xFF, 0, 0, 0, 9, 9, 9, 9,
		0xFF, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0x80, 0x80, 0x80, 0, 9, 9, 9, 9,
	}
	r := image.Rect(0, 0, 3, 2)
	for _, k := range []struct {
		name string
		f    pixFormat
		want []byte
	}{
		{"x8r8g8b8 LSB", pixFormat{bpp: 32, pad: 32, rmask: 0xFF0000, gmask: 0xFF00, bm: 0xFF}, []byte{
			0, 0, 0xFF, 0, 0, 0xFF, 0, 0, 0xFF, 0, 0, 0,
			0xFF, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0x80, 0x80, 0x80, 0}},
		{"x8r8g8b8 MSB", pixFormat{bpp: 32, pad: 32, rmask: 0xFF0000, gmask: 0xFF00, bm: 0xFF, msbFirst: true}, []byte{
			0, 0xFF, 0, 0, 0, 0, 0xFF, 0, 0, 0, 0, 0xFF,
			0, 0xFF, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0x80, 0x80, 0x80}},
		{"x8b8g8r8 LSB", pixFormat{bpp: 32, pad: 32, rmask: 0xFF, gmask: 0xFF00, bm: 0xFF0000}, []byte{
			0xFF, 0, 0, 0, 0, 0xFF, 0, 0, 0, 0, 0xFF, 0,
			0xFF, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0x80, 0x80, 0x80, 0}},
		{"r5g6b5 LSB", pixFormat{bpp: 16, pad: 32, rmask: 0xF800, gmask: 0x07E0, bm: 0x001F}, []byte{
			0x00, 0xF8, 0xE0, 0x07, 0x1F, 0x00, 0, 0, // padded to 8 bytes
			0xFF, 0xFF, 0x00, 0x00, 0x10, 0x84, 0, 0}},
		{"r8g8b8 24-bit", pixFormat{bpp: 24, pad: 32, rmask: 0xFF0000, gmask: 0xFF00, bm: 0xFF}, []byte{
			0, 0, 0xFF, 0, 0xFF, 0, 0xFF, 0, 0, 0, 0, 0,
			0xFF, 0xFF, 0xFF, 0, 0, 0, 0x80, 0x80, 0x80, 0, 0, 0}},
	} {
		got := k.f.convert(nil, src, 16, r)
		if !bytes.Equal(got, k.want) {
			t.Errorf("%s: got % x, want % x", k.name, got, k.want)
			continue
		}
		img, err := k.f.toRGBA(got, 3, 2)
		if err != nil {
			t.Errorf("%s: %v", k.name, err)
			continue
		}
		for i, c := range [][3]byte{{0xFF, 0, 0}, {0, 0xFF, 0}, {0, 0, 0xFF}, {0xFF, 0xFF, 0xFF}, {0, 0, 0}, {0x80, 0x80, 0x80}} {
			o := img.PixOffset(i%3, i/3)
			p := img.Pix[o : o+3]
			if k.f.bpp == 16 && c == [3]byte{0x80, 0x80, 0x80} {
				c = [3]byte{0x84, 0x82, 0x84} // 5 and 6 bits, repeated
			}
			if p[0] != c[0] || p[1] != c[1] || p[2] != c[2] {
				t.Errorf("%s: back, pixel %d = % x, want % x", k.name, i, p, c)
			}
		}
	}
	// A rectangle inside the frame.
	f := pixFormat{bpp: 32, pad: 32, rmask: 0xFF0000, gmask: 0xFF00, bm: 0xFF}
	if got := f.convert(nil, src, 16, image.Rect(1, 1, 3, 2)); !bytes.Equal(got, src[20:28]) {
		t.Errorf("sub-rectangle: % x", got)
	}
}

// TestFrame checks flushing, damage and resizing of a window's frame.
func TestFrame(t *testing.T) {
	f := newFrame(4, 2)
	woken := 0
	f.wake = func() { woken++ }
	pix := make([]byte, 4*4*2)
	for i := range pix {
		pix[i] = byte(i)
	}
	f.Flush(1, 0, 3, 2, pix, 16)
	if r := f.take(); r != image.Rect(1, 0, 3, 2) || woken != 1 {
		t.Fatalf("damage %v, woken %d", r, woken)
	}
	if !bytes.Equal(f.pix[4:12], pix[4:12]) || !bytes.Equal(f.pix[0:4], []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Errorf("pixels % x", f.pix)
	}
	// A flush outside, or from a frame buffer smaller than the frame
	// (the draw device's before it has caught up with a resize), is
	// clipped.
	f.Flush(-5, -5, 100, 100, pix[:16], 8)
	if r := f.take(); r != image.Rect(0, 0, 2, 2) {
		t.Errorf("clipped damage %v", r)
	}
	f.track(platform.PointerEvent{X: 1, Y: 1, Buttons: 4})
	<-f.Pointer()
	f.setSize(6, 3)
	if w, h, ch := f.Info(); w != 6 || h != 3 || ch != "x8r8g8b8" {
		t.Errorf("info %d %d %s", w, h, ch)
	}
	select {
	case <-f.Resized():
	default:
		t.Error("no resize notice")
	}
	if e := <-f.Pointer(); e != (platform.PointerEvent{X: 1, Y: 1, Buttons: 4}) {
		t.Errorf("pointer after resize %+v", e)
	}
	if r := f.take(); r != image.Rect(0, 0, 6, 3) {
		t.Errorf("damage after resize %v", r)
	}
	if !bytes.Equal(f.pix[4:8], pix[4:8]) || !bytes.Equal(f.pix[4*6*2:4*6*2+4], []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Errorf("pixels after resize % x", f.pix)
	}
	// The wheel is a press and release of button 4 or 5.
	f.wheel(2, 2, 1, true)
	f.wheel(2, 2, 0, false)
	for _, want := range []int{9, 1, 16, 0} {
		if e := <-f.Pointer(); e.Buttons != want {
			t.Errorf("wheel buttons %d, want %d", e.Buttons, want)
		}
	}
}
