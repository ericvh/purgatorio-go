package host

// The emu's screen in a native host window (emu -g WxH): the parts the
// backends share. The X11 backend is window_linux.go and the Cocoa one
// window_darwin.go; each has a Window type with NewWindow and Run.
//
// A frame is the screen as the window shows it: x8r8g8b8 pixels the
// draw device flushes into, the damage the window has not yet shown,
// and the pointer and keyboard queues #m and #c/keyboard read. A flush
// only copies pixels and adds damage, then wakes the backend, which
// shows the damage when it can, so a slow display never holds up a
// prog. A resize (the user resizing the window) makes a new frame of
// the new size, keeping the pixels that fit, and tells the draw device
// (platform.Resizer), which makes a new screen image and tells its
// clients; the frame also queues a pointer event with the current
// state, so programs that only read the pointer wake up and can look.
//
// Input follows Inferno's emu (emu/port/win-x11a.c and emu/MacOSX):
// buttons 1 to 3 are bits 1, 2 and 4 of /dev/pointer, a wheel step up
// or down is a press and release of button 4 or 5 (bits 8 and 16), and
// keys are runes, with keyboard(6)'s codes for Home, End, the arrows,
// Page Up and Down, Insert and the function keys.

import (
	"errors"
	"image"
	"math/bits"
	"sync"

	"github.com/ericvh/purgatorio-go/platform"
)

// DefaultScreenSize is the screen's size when a display is asked for
// without one.
const DefaultScreenSize = "800x600"

// frame is the screen a window shows, and its input queues.
type frame struct {
	mu     sync.Mutex
	w, h   int
	pix    []byte // x8r8g8b8, 4*w bytes a row
	damage image.Rectangle
	last   platform.PointerEvent
	wake   func() // tells the backend there is damage; called without mu

	ptr    chan platform.PointerEvent
	kbd    chan rune
	resize chan struct{}
}

func newFrame(w, h int) *frame {
	f := &frame{w: w, h: h, pix: make([]byte, 4*w*h), wake: func() {},
		ptr: make(chan platform.PointerEvent, 256), kbd: make(chan rune, 256), resize: make(chan struct{}, 1)}
	for i := range f.pix {
		f.pix[i] = 0xFF
	}
	return f
}

func (f *frame) Info() (int, int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.w, f.h, "x8r8g8b8"
}

// Flush copies the rectangle into the frame and wakes the backend. Just
// after a resize pix may still be of the old size, so the rectangle is
// clipped to it as well as to the frame.
func (f *frame) Flush(x0, y0, x1, y1 int, pix []byte, stride int) {
	if stride < 4 {
		return
	}
	f.mu.Lock()
	r := image.Rect(x0, y0, x1, y1).Intersect(image.Rect(0, 0, min(f.w, stride/4), min(f.h, len(pix)/stride)))
	if r.Empty() {
		f.mu.Unlock()
		return
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := y * stride
		copy(f.pix[4*(y*f.w+r.Min.X):4*(y*f.w+r.Max.X)], pix[o+4*r.Min.X:o+4*r.Max.X])
	}
	f.damage = f.damage.Union(r)
	f.mu.Unlock()
	f.wake()
}

// expose adds r to the damage, for a window system that lost the
// window's contents.
func (f *frame) expose(r image.Rectangle) {
	f.mu.Lock()
	f.damage = f.damage.Union(r.Intersect(image.Rect(0, 0, f.w, f.h)))
	f.mu.Unlock()
	f.wake()
}

// take returns the damage and clears it. The caller reads f.pix for it
// under f.mu.
func (f *frame) take() image.Rectangle {
	r := f.damage
	f.damage = image.Rectangle{}
	return r
}

// setSize makes the frame w×h, keeping the pixels that fit and white
// elsewhere, and tells the draw device and the pointer.
func (f *frame) setSize(w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	f.mu.Lock()
	if w == f.w && h == f.h {
		f.mu.Unlock()
		return
	}
	pix := make([]byte, 4*w*h)
	for i := range pix {
		pix[i] = 0xFF
	}
	cw := min(w, f.w)
	for y := 0; y < min(h, f.h); y++ {
		copy(pix[4*y*w:4*(y*w+cw)], f.pix[4*y*f.w:4*(y*f.w+cw)])
	}
	f.w, f.h, f.pix = w, h, pix
	f.damage = image.Rect(0, 0, w, h)
	last := f.last
	f.mu.Unlock()
	select {
	case f.resize <- struct{}{}:
	default:
	}
	f.track(last)
	f.wake()
}

func (f *frame) Pointer() <-chan platform.PointerEvent { return f.ptr }
func (f *frame) Keyboard() <-chan rune                 { return f.kbd }
func (f *frame) Resized() <-chan struct{}              { return f.resize }

func (f *frame) track(e platform.PointerEvent) {
	f.mu.Lock()
	f.last = e
	f.mu.Unlock()
	select {
	case f.ptr <- e:
	default:
	}
}

func (f *frame) key(r rune) {
	select {
	case f.kbd <- r:
	default:
	}
}

// wheel reports a wheel step as Inferno's emu does: a press and a
// release of button 4 (up) or 5 (down), with the buttons held.
func (f *frame) wheel(x, y, held int, up bool) {
	b := 16
	if up {
		b = 8
	}
	f.track(platform.PointerEvent{X: x, Y: y, Buttons: held | b})
	f.track(platform.PointerEvent{X: x, Y: y, Buttons: held})
}

// Keyboard codes (keyboard(6)) beyond those in web.go.
const (
	kHome   = kView | 0
	kEnd    = kView | 1
	kUp     = kView | 2
	kDown   = kView | 3
	kLeft   = kView | 4
	kRight  = kView | 5
	kPgup   = kView | 6
	kPgdown = kView | 7
	kIns    = kSpec | 0x63
)

// ctrlKey applies the control key as Inferno's X11 emu does.
func ctrlKey(r rune) rune {
	if r < 0x100 {
		return r & 0x9F
	}
	return r
}

// X11 key symbols (X11/keysymdef.h) that are not characters.
const (
	xkBackSpace   = 0xFF08
	xkTab         = 0xFF09
	xkLinefeed    = 0xFF0A
	xkReturn      = 0xFF0D
	xkEscape      = 0xFF1B
	xkDelete      = 0xFFFF
	xkHome        = 0xFF50
	xkLeft        = 0xFF51
	xkUp          = 0xFF52
	xkRight       = 0xFF53
	xkDown        = 0xFF54
	xkPageUp      = 0xFF55
	xkPageDown    = 0xFF56
	xkEnd         = 0xFF57
	xkInsert      = 0xFF63
	xkModeSwitch  = 0xFF7E
	xkNumLock     = 0xFF7F
	xkKPSpace     = 0xFF80
	xkKPTab       = 0xFF89
	xkKPEnter     = 0xFF8D
	xkKPHome      = 0xFF95
	xkKPLeft      = 0xFF96
	xkKPUp        = 0xFF97
	xkKPRight     = 0xFF98
	xkKPDown      = 0xFF99
	xkKPPageUp    = 0xFF9A
	xkKPPageDown  = 0xFF9B
	xkKPEnd       = 0xFF9C
	xkKPInsert    = 0xFF9E
	xkKPDelete    = 0xFF9F
	xkKPEqual     = 0xFFBD
	xkKPMultiply  = 0xFFAA
	xkKP9         = 0xFFB9
	xkF1          = 0xFFBE
	xkF12         = 0xFFC9
	xkISOLeftTab  = 0xFE20
	xkMultiKey    = 0xFF20
	xkShiftL      = 0xFFE1
	xkHyperR      = 0xFFEE
	xkEuroSign    = 0x20AC
	xkUnicodeBase = 0x01000000
)

var x11Named = map[uint32]rune{
	xkBackSpace: '\b', xkTab: '\t', xkISOLeftTab: '\t', xkKPTab: '\t', xkEscape: 0x1B, xkDelete: 0x7F,
	xkKPDelete: 0x7F, xkLinefeed: '\r', xkKPSpace: ' ', xkReturn: '\n', xkKPEnter: '\n',
	xkHome: kHome, xkKPHome: kHome, xkLeft: kLeft, xkKPLeft: kLeft, xkUp: kUp, xkKPUp: kUp,
	xkDown: kDown, xkKPDown: kDown, xkRight: kRight, xkKPRight: kRight,
	xkPageDown: kPgdown, xkKPPageDown: kPgdown, xkEnd: kEnd, xkKPEnd: kEnd,
	xkPageUp: kPgup, xkKPPageUp: kPgup, xkInsert: kIns, xkKPInsert: kIns,
	xkEuroSign: 0x20AC,
}

// x11KeyRune turns an X11 key symbol into the rune Inferno's X11 emu
// gives for it (xkeyboard in emu/port/win-x11a.c), or reports false for
// a key that gives none, such as a modifier. The keypad's digits and
// operators are their characters; F1 to F12 are keyboard(6)'s function
// keys, as on Inferno's macOS emu.
func x11KeyRune(sym uint32, ctrl bool) (rune, bool) {
	var r rune
	switch {
	case sym == 0 || sym == xkMultiKey:
		return 0, false
	case sym < 0x100:
		r = rune(sym)
		if r == 0xAD { // a hyphen is a minus
			r = '-'
		}
	case sym >= xkUnicodeBase && sym <= xkUnicodeBase+0x10FFFF:
		r = rune(sym - xkUnicodeBase)
	case sym >= xkKPMultiply && sym <= xkKP9 || sym == xkKPEqual:
		r = rune(sym & 0x7F)
	case sym >= xkF1 && sym <= xkF12:
		r = rune(kKF | int(sym-xkF1+1))
	case sym >= xkShiftL && sym <= xkHyperR:
		return 0, false
	default:
		var ok bool
		if r, ok = x11Named[sym]; !ok {
			return 0, false
		}
	}
	if ctrl {
		r = ctrlKey(r)
	}
	return r, true
}

// X11 modifier masks in key and button events.
const (
	xShiftMask   = 1 << 0
	xLockMask    = 1 << 1
	xControlMask = 1 << 2
	xButton1Mask = 1 << 8
)

// x11Keysym picks the key symbol a key gives with the modifiers in
// state, from its symbols in the keyboard mapping (the core protocol's
// rules, as XLookupString applies them): group 2 when the Mode_switch
// modifier is down, the keypad's second symbol with Num Lock, the
// second with Shift, and the upper case of a letter with Caps Lock.
func x11Keysym(syms []uint32, state, numlock, modeswitch uint16) uint32 {
	for len(syms) > 0 && syms[len(syms)-1] == 0 {
		syms = syms[:len(syms)-1]
	}
	g := syms
	if modeswitch != 0 && state&modeswitch != 0 && len(syms) > 2 {
		g = syms[2:]
	}
	if len(g) > 2 {
		g = g[:2]
	}
	var lo, hi uint32
	switch len(g) {
	case 0:
		return 0
	case 1:
		lo, hi = x11Case(g[0])
	default:
		lo, hi = g[0], g[1]
		if hi == 0 {
			lo, hi = x11Case(lo)
		}
	}
	shift := state&xShiftMask != 0
	if numlock != 0 && state&numlock != 0 && hi >= xkKPSpace && hi <= xkKPEqual {
		if shift {
			return lo
		}
		return hi
	}
	sym := lo
	if shift {
		sym = hi
	}
	if state&xLockMask != 0 {
		_, sym = x11Case(sym)
	}
	return sym
}

// x11Case returns the lower and upper case of a Latin-1 or Unicode key
// symbol, which are the same for symbols without case.
func x11Case(sym uint32) (uint32, uint32) {
	switch {
	case sym >= 'a' && sym <= 'z', sym >= 0xE0 && sym <= 0xFE && sym != 0xF7:
		return sym, sym - 0x20
	case sym >= 'A' && sym <= 'Z', sym >= 0xC0 && sym <= 0xDE && sym != 0xD7:
		return sym + 0x20, sym
	}
	return sym, sym
}

// x11Buttons returns the pointer buttons after an X11 button event, from
// the buttons held before it (state) and the button pressed or released
// (0 for a motion), as Inferno's xmouse does.
func x11Buttons(state uint16, button byte, press bool) int {
	s := int(state)
	if button >= 1 && button <= 5 {
		m := xButton1Mask << (button - 1)
		if press {
			s |= m
		} else {
			s &^= m
		}
	}
	return s >> 8 & 0x1F
}

// pixFormat is how a window system lays out a pixel: bits per pixel
// (16, 24 or 32), each row padded to pad bits, the masks of red, green
// and blue in the pixel value, and the order of its bytes.
type pixFormat struct {
	bpp, pad         int
	rmask, gmask, bm uint32
	msbFirst         bool
}

// stride is the bytes in a row of w pixels.
func (f pixFormat) stride(w int) int {
	pad := max(f.pad, 8)
	return (w*f.bpp + pad - 1) / pad * pad / 8
}

// native reports whether the format is the frame's own: x8r8g8b8 in
// little-endian 32-bit words, so rows can be copied as they are.
func (f pixFormat) native() bool {
	return f.bpp == 32 && !f.msbFirst && f.rmask == 0xFF0000 && f.gmask == 0xFF00 && f.bm == 0xFF
}

// convert appends to dst the rectangle r of src (x8r8g8b8, sstride bytes
// a row) in format f, each row padded to f.stride(r.Dx()).
func (f pixFormat) convert(dst, src []byte, sstride int, r image.Rectangle) []byte {
	n := f.stride(r.Dx())
	bpp := f.bpp / 8
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := src[y*sstride+4*r.Min.X : y*sstride+4*r.Max.X]
		start := len(dst)
		if f.native() {
			dst = append(dst, row...)
		} else {
			for x := 0; x < len(row); x += 4 {
				v := scale(row[x+2], f.rmask) | scale(row[x+1], f.gmask) | scale(row[x], f.bm)
				for i := range bpp {
					if f.msbFirst {
						dst = append(dst, byte(v>>(8*(bpp-1-i))))
					} else {
						dst = append(dst, byte(v>>(8*i)))
					}
				}
			}
		}
		for len(dst)-start < n {
			dst = append(dst, 0)
		}
	}
	return dst
}

// toRGBA turns w×h pixels in format f, as GetImage returns them, into
// an RGBA image.
func (f pixFormat) toRGBA(data []byte, w, h int) (*image.RGBA, error) {
	n := f.stride(w)
	if len(data) < n*h || f.bpp%8 != 0 || f.bpp == 0 {
		return nil, errors.New("short image")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bpp := f.bpp / 8
	for y := range h {
		for x := range w {
			p := data[y*n+x*bpp : y*n+x*bpp+bpp]
			var v uint32
			for i := range bpp {
				if f.msbFirst {
					v = v<<8 | uint32(p[i])
				} else {
					v |= uint32(p[i]) << (8 * i)
				}
			}
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = unscale(v, f.rmask), unscale(v, f.gmask), unscale(v, f.bm), 0xFF
		}
	}
	return img, nil
}

// unscale takes a channel's bits out of a pixel value as 8 bits,
// repeating them to fill the low bits.
func unscale(v, mask uint32) byte {
	if mask == 0 {
		return 0
	}
	n := bits.OnesCount32(mask)
	c := (v & mask) >> bits.TrailingZeros32(mask)
	if n >= 8 {
		return byte(c >> (n - 8))
	}
	out := uint32(0)
	for s := 8 - n; s > -n; s -= n {
		if s >= 0 {
			out |= c << s
		} else {
			out |= c >> -s
		}
	}
	return byte(out)
}

// scale puts an 8-bit channel value in the bits of mask.
func scale(c byte, mask uint32) uint32 {
	if mask == 0 {
		return 0
	}
	shift := bits.TrailingZeros32(mask)
	n := bits.OnesCount32(mask)
	v := uint32(c)
	if n < 8 {
		v >>= 8 - n
	} else {
		v = v << (n - 8) // the top bits; the rest stay zero
	}
	return v << shift & mask
}

// Mac virtual key codes (Inside Macintosh, as Inferno's
// emu/MacOSX/keycodes.h names them) of keys that are not characters.
var macNamed = map[uint16]rune{
	0x24: '\n', 0x34: '\n', 0x4C: '\n', // Return, iBook Enter, keypad Enter
	0x35: 0x1B, 0x33: '\b', 0x30: '\t', 0x75: 0x7F,
	0x72: kIns, 0x73: kHome, 0x77: kEnd, 0x74: kPgup, 0x79: kPgdown,
	0x7E: kUp, 0x7D: kDown, 0x7B: kLeft, 0x7C: kRight,
	0x45: '+', 0x4E: '-', 0x43: '*', 0x4B: '/', 0x41: '.', 0x51: '=',
	0x52: '0', 0x53: '1', 0x54: '2', 0x55: '3', 0x56: '4', 0x57: '5', 0x58: '6', 0x59: '7', 0x5B: '8', 0x5C: '9',
	0x7A: kKF | 1, 0x78: kKF | 2, 0x63: kKF | 3, 0x76: kKF | 4, 0x60: kKF | 5, 0x61: kKF | 6,
	0x62: kKF | 7, 0x64: kKF | 8, 0x65: kKF | 9, 0x6D: kKF | 10, 0x67: kKF | 11, 0x6F: kKF | 12,
}

// macKeyRune turns a Mac key, its virtual key code and the characters
// it typed (NSEvent's characters, with Control already applied), into
// the rune Inferno's macOS emu gives (convert_key in emu/MacOSX/win.c),
// or reports false for none.
func macKeyRune(code uint16, chars string) (rune, bool) {
	if r, ok := macNamed[code]; ok {
		return r, true
	}
	for _, r := range chars {
		if r >= 0xF700 && r <= 0xF8FF { // AppKit's function key codes
			return 0, false
		}
		return r, true
	}
	return 0, false
}

// Mac modifier flags (NSEventModifierFlags).
const (
	macOption  = 1 << 19
	macCommand = 1 << 20
)

// macButtons returns the pointer buttons from the Mac's pressed buttons
// (NSEvent's pressedMouseButtons: left, right, then middle) and modifier
// flags, as Inferno's macOS emu does: Option with the left button is
// button 2 and Command with it button 3, for one-button mice.
func macButtons(pressed uint, flags uint) int {
	b := 0
	if pressed&1 != 0 {
		switch {
		case flags&macOption != 0:
			b = 2
		case flags&macCommand != 0:
			b = 4
		default:
			b = 1
		}
	}
	if pressed&2 != 0 {
		b |= 4
	}
	if pressed&4 != 0 {
		b |= 2
	}
	return b
}
