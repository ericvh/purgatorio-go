package platform

import "sync"

// Displayer is implemented by platforms with a frame buffer, which the
// draw device #i paints and the pointer device #m reads input from.
type Displayer interface {
	// Display returns the screen, or nil if there is none.
	Display() Screen
}

// Screen is a frame buffer that the draw device keeps in memory and
// shows with Flush (Inferno's attachscreen and flushmemscreen).
type Screen interface {
	// Info returns the size in pixels and the pixel format as an Inferno
	// channel string, such as "x8r8g8b8".
	Info() (width, height int, chans string)
	// Flush shows the rectangle [x0,x1)×[y0,y1) of pix, which holds the
	// whole frame buffer in Info's format, stride bytes a row.
	Flush(x0, y0, x1, y1 int, pix []byte, stride int)
}

// PointerEvent is a pointer position and button state.
type PointerEvent struct {
	X, Y    int
	Buttons int
}

// Pointerer is implemented by screens with pointer input.
type Pointerer interface {
	// Pointer returns the channel of pointer events.
	Pointer() <-chan PointerEvent
}

// Keyboarder is implemented by screens with a keyboard. Its keys are
// runes, with Inferno's codes (keyboard(6)) for keys that are not
// characters; the console device's keyboard file reads them.
type Keyboarder interface {
	Keyboard() <-chan rune
}

// PointerSetter is implemented by screens that can move the pointer
// and change its cursor (writes to #m/pointer and #m/cursor). A nil
// cursor restores the default arrow; set and clr are dx/8*dy bytes.
type PointerSetter interface {
	SetPointer(x, y int)
	SetCursor(hotx, hoty, dx, dy int, clr, set []byte)
}

// Headless is a Screen with no display, for tests and for systems
// without one. It keeps a copy of what has been flushed and takes
// pointer input from Track.
type Headless struct {
	mu      sync.Mutex
	w, h    int
	chans   string
	pix     []byte
	stride  int
	flushes int
	ptr     chan PointerEvent
	last    PointerEvent
}

// NewHeadless makes a w×h headless screen with the given channel string.
func NewHeadless(w, h int, chans string) *Headless {
	return &Headless{w: w, h: h, chans: chans, ptr: make(chan PointerEvent, 64)}
}

func (s *Headless) Info() (int, int, string) { return s.w, s.h, s.chans }

func (s *Headless) Flush(x0, y0, x1, y1 int, pix []byte, stride int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pix == nil || s.stride != stride {
		s.pix = make([]byte, len(pix))
		s.stride = stride
	}
	if x0 < 0 || y0 < 0 || x1 > s.w || y1 > s.h || x0 >= x1 || y0 >= y1 || stride == 0 {
		return
	}
	bpp := stride / s.w // whole bytes per pixel at depths of 8 or more
	a, b := x0*bpp, x1*bpp
	if bpp == 0 {
		a, b = 0, stride
	}
	for y := y0; y < y1; y++ {
		o := y * stride
		copy(s.pix[o+a:o+b], pix[o+a:o+b])
	}
	s.flushes++
}

// Snapshot returns a copy of the flushed frame buffer, its stride, and
// the number of flushes so far.
func (s *Headless) Snapshot() ([]byte, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.pix...), s.stride, s.flushes
}

func (s *Headless) Pointer() <-chan PointerEvent { return s.ptr }

// Track queues a pointer event, dropping it if the queue is full.
func (s *Headless) Track(e PointerEvent) {
	s.mu.Lock()
	s.last = e
	s.mu.Unlock()
	select {
	case s.ptr <- e:
	default:
	}
}

// SetPointer moves the pointer, which reports the move as an event.
func (s *Headless) SetPointer(x, y int) {
	s.mu.Lock()
	b := s.last.Buttons
	s.mu.Unlock()
	s.Track(PointerEvent{X: x, Y: y, Buttons: b})
}

func (s *Headless) SetCursor(hotx, hoty, dx, dy int, clr, set []byte) {}
