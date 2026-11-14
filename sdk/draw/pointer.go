package draw

import (
	"fmt"
	"strconv"
	"strings"
)

// Pointer is a pointer event (Draw->Pointer): the buttons held (bit 0
// for button 1), the position and a millisecond time stamp.
type Pointer struct {
	Buttons int
	XY      Point
	Msec    int
}

// PointerSize is the length of a pointer event as pointer(3) gives it:
// "m" and four 12-byte decimal numbers.
const PointerSize = 1 + 4*12

// ParsePointer decodes an event read from /dev/pointer.
func ParsePointer(b []byte) (Pointer, bool) {
	if len(b) < PointerSize || b[0] != 'm' {
		return Pointer{}, false
	}
	f := strings.Fields(string(b[1:PointerSize]))
	if len(f) != 4 {
		return Pointer{}, false
	}
	var v [4]int
	for i := range f {
		n, err := strconv.ParseInt(f[i], 10, 64)
		if err != nil {
			return Pointer{}, false
		}
		v[i] = int(n)
	}
	return Pointer{Buttons: v[2], XY: Pt(v[0], v[1]), Msec: v[3]}, true
}

// Bytes encodes p as /dev/pointer does.
func (p Pointer) Bytes() []byte {
	return fmt.Appendf(nil, "m%11d %11d %11d %11d ", p.XY.X, p.XY.Y, p.Buttons, uint32(p.Msec))
}

// Keyboard codes for keys that are not characters (keyboard(6)).
const (
	KeyEsc    = 0x1b
	KeySpec   = 0xE000
	KeyView   = KeySpec | 0x10
	KeyHome   = KeyView | 0
	KeyEnd    = KeyView | 1
	KeyUp     = KeyView | 2
	KeyDown   = KeyView | 3
	KeyLeft   = KeyView | 4
	KeyRight  = KeyView | 5
	KeyPgup   = KeyView | 6
	KeyPgdown = KeyView | 7
	KeyIns    = KeySpec | 0x63
	KeyDel    = KeySpec | 0x64
	KeyKF     = KeySpec | 0x40 // F1 is KeyKF|1
)
