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

// Resizer is implemented by screens whose size can change, such as a
// host window the user resizes. Such a screen first makes Info report
// the new size and then sends on the channel, without blocking (the
// channel holds one pending notice). The draw device then makes a new
// screen image of that size and tells its clients (#i's refresh file).
// Flush may still be given a rectangle and frame buffer of the old size
// just after; the screen clips them.
type Resizer interface {
	Resized() <-chan struct{}
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
	resize  chan struct{}
}

// NewHeadless makes a w×h headless screen with the given channel string.
func NewHeadless(w, h int, chans string) *Headless {
	return &Headless{w: w, h: h, chans: chans, ptr: make(chan PointerEvent, 64), resize: make(chan struct{}, 1)}
}

func (s *Headless) Info() (int, int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w, s.h, s.chans
}

// Resize changes the size, as a window the user resized would.
func (s *Headless) Resize(w, h int) {
	s.mu.Lock()
	s.w, s.h = w, h
	s.pix, s.stride = nil, 0
	s.mu.Unlock()
	select {
	case s.resize <- struct{}{}:
	default:
	}
}

func (s *Headless) Resized() <-chan struct{} { return s.resize }

func (s *Headless) Flush(x0, y0, x1, y1 int, pix []byte, stride int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pix == nil || s.stride != stride || len(s.pix) != len(pix) {
		s.pix = make([]byte, len(pix))
		s.stride = stride
	}
	depth := chanDepth(s.chans)
	if stride <= 0 || depth == 0 {
		return
	}
	// Clip to the screen and to the frame buffer, which may be of the
	// size before a resize.
	x1 = min(x1, s.w, stride*8/depth)
	y1 = min(y1, s.h, len(pix)/stride)
	if x0 < 0 || y0 < 0 || x0 >= x1 || y0 >= y1 {
		return
	}
	a, b := x0*depth/8, (x1*depth+7)/8
	if depth < 8 {
		a, b = 0, stride
	}
	for y := y0; y < y1; y++ {
		o := y * stride
		copy(s.pix[o+a:o+b], pix[o+a:o+b])
	}
	s.flushes++
}

// chanDepth is the bits per pixel of a channel string such as
// "x8r8g8b8": the sum of its channels' sizes.
func chanDepth(chans string) int {
	d := 0
	for i := 1; i < len(chans); i += 2 {
		if c := chans[i]; c >= '0' && c <= '9' {
			d += int(c - '0')
		} else {
			return 0
		}
	}
	return d
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
