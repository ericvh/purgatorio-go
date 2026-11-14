package host

// The native window on macOS: Cocoa driven through purego's Objective-C
// runtime calls, without cgo, so the emu still cross builds from any
// host with CGO_ENABLED=0.
//
// AppKit must run on the main thread, so this file's init locks the
// main goroutine to it. NewWindow and Run are called from main: Run
// starts the emu on another goroutine and runs [NSApp run] here. A
// flush wakes the main thread with performSelectorOnMainThread, and
// the view's drawRect: copies the frame into a CGImage (CFDataCreate
// copies the bytes, so Cocoa never holds Go memory) and draws it at
// the view's top left, one screen pixel to a point: on a Retina display
// each screen pixel is 2×2 device pixels, drawn without smoothing.
// windowDidResize: resizes the screen to the view's size in points;
// closing the window exits the emu with status 0, as Inferno's macOS
// emu does, and so does Command-Q. Other keys with Command held go
// nowhere, as in Inferno's emu.

import (
	"errors"
	"os"
	"runtime"
	"structs"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/ericvh/purgatorio-go/platform"
)

func init() { runtime.LockOSThread() }

type nsPoint struct {
	_    structs.HostLayout
	X, Y float64
}

type nsSize struct {
	_    structs.HostLayout
	W, H float64
}

type nsRect struct {
	_      structs.HostLayout
	Origin nsPoint
	Size   nsSize
}

const (
	nsWindowStyleTitled         = 1 << 0
	nsWindowStyleClosable       = 1 << 1
	nsWindowStyleMiniaturizable = 1 << 2
	nsWindowStyleResizable      = 1 << 3
	nsBackingStoreBuffered      = 2

	cgImageAlphaNoneSkipFirst = 6
	cgBitmapByteOrder32Little = 2 << 12
	cgInterpolationNone       = 1
)

var (
	cocoaOnce sync.Once
	cocoaErr  error

	cfDataCreate                    func(alloc uintptr, bytes unsafe.Pointer, n int) uintptr
	cfRelease                       func(uintptr)
	cgColorSpaceCreateDeviceRGB     func() uintptr
	cgDataProviderCreateWithCFData  func(data uintptr) uintptr
	cgDataProviderRelease           func(uintptr)
	cgImageCreate                   func(w, h, bpc, bpp, bpr int, space uintptr, info uint32, provider uintptr, decode uintptr, interp bool, intent int32) uintptr
	cgImageRelease                  func(uintptr)
	cgContextDrawImage              func(ctx uintptr, r nsRect, img uintptr)
	cgContextSetInterpolationQualty func(ctx uintptr, q int32)

	rgbSpace uintptr
	theWin   *Window // the one window; the classes' methods find it here

	sel = objc.RegisterName
)

func loadCocoa() error {
	cocoaOnce.Do(func() {
		const fw = "/System/Library/Frameworks/"
		for _, lib := range []string{"Cocoa", "CoreFoundation", "CoreGraphics"} {
			h, err := purego.Dlopen(fw+lib+".framework/"+lib, purego.RTLD_GLOBAL|purego.RTLD_LAZY)
			if err != nil {
				cocoaErr = err
				return
			}
			switch lib {
			case "CoreFoundation":
				purego.RegisterLibFunc(&cfDataCreate, h, "CFDataCreate")
				purego.RegisterLibFunc(&cfRelease, h, "CFRelease")
			case "CoreGraphics":
				purego.RegisterLibFunc(&cgColorSpaceCreateDeviceRGB, h, "CGColorSpaceCreateDeviceRGB")
				purego.RegisterLibFunc(&cgDataProviderCreateWithCFData, h, "CGDataProviderCreateWithCFData")
				purego.RegisterLibFunc(&cgDataProviderRelease, h, "CGDataProviderRelease")
				purego.RegisterLibFunc(&cgImageCreate, h, "CGImageCreate")
				purego.RegisterLibFunc(&cgImageRelease, h, "CGImageRelease")
				purego.RegisterLibFunc(&cgContextDrawImage, h, "CGContextDrawImage")
				purego.RegisterLibFunc(&cgContextSetInterpolationQualty, h, "CGContextSetInterpolationQuality")
			}
		}
		rgbSpace = cgColorSpaceCreateDeviceRGB()
		cocoaErr = registerClasses()
	})
	return cocoaErr
}

// Window is a screen shown in a Cocoa window.
type Window struct {
	*frame
	app, win, view objc.ID
	pending        atomic.Bool // an update is queued for the main thread
	buttons        int         // the buttons at the last press or release
	scroll         float64     // wheel movement not yet reported
}

var (
	_ platform.Screen        = (*Window)(nil)
	_ platform.Pointerer     = (*Window)(nil)
	_ platform.Keyboarder    = (*Window)(nil)
	_ platform.PointerSetter = (*Window)(nil)
	_ platform.Resizer       = (*Window)(nil)
)

// NewWindow opens a w×h window. It must be called on the main
// goroutine.
func NewWindow(w, h int) (*Window, error) {
	if theWin != nil {
		return nil, errors.New("a window is already open")
	}
	if err := loadCocoa(); err != nil {
		return nil, errors.New("cannot open display: " + err.Error())
	}
	x := &Window{frame: newFrame(w, h)}
	x.frame.wake = x.queueUpdate
	x.app = objc.ID(objc.GetClass("NSApplication")).Send(sel("sharedApplication"))
	if x.app == 0 {
		return nil, errors.New("cannot open display: no NSApplication")
	}
	x.app.Send(sel("setActivationPolicy:"), 0) // regular: a Dock icon and menu bar
	r := nsRect{Size: nsSize{W: float64(w), H: float64(h)}}
	x.win = objc.ID(objc.GetClass("NSWindow")).Send(sel("alloc")).Send(sel("initWithContentRect:styleMask:backing:defer:"),
		r, uint(nsWindowStyleTitled|nsWindowStyleClosable|nsWindowStyleMiniaturizable|nsWindowStyleResizable),
		uint(nsBackingStoreBuffered), false)
	if x.win == 0 {
		return nil, errors.New("cannot open display: no window")
	}
	x.view = objc.ID(objc.GetClass("PurgView")).Send(sel("alloc")).Send(sel("initWithFrame:"), r)
	title := objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), "inferno")
	x.win.Send(sel("setTitle:"), title)
	x.win.Send(sel("setContentView:"), x.view)
	x.win.Send(sel("makeFirstResponder:"), x.view)
	x.win.Send(sel("setAcceptsMouseMovedEvents:"), true)
	x.win.Send(sel("setDelegate:"), objc.ID(objc.GetClass("PurgWindowDelegate")).Send(sel("new")))
	x.win.Send(sel("center"))
	theWin = x
	return x, nil
}

// Run runs main, the emu, on another goroutine and the Cocoa event loop
// here, and exits with main's status, or with 0 when the window is
// closed. It must be called on the main goroutine.
func (x *Window) Run(main func() int) {
	go func() { os.Exit(main()) }()
	x.win.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
	x.app.Send(sel("activateIgnoringOtherApps:"), true)
	x.app.Send(sel("run"))
	os.Exit(0)
}

// queueUpdate asks the main thread to redraw, once until it has.
func (x *Window) queueUpdate() {
	if x.pending.Swap(true) {
		return
	}
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(sel("new"))
	x.view.Send(sel("performSelectorOnMainThread:withObject:waitUntilDone:"), sel("purgUpdate:"), objc.ID(0), false)
	pool.Send(sel("drain"))
}

// SetPointer reports the move as an event; the Mac's own pointer does
// not move.
func (x *Window) SetPointer(px, py int) {
	x.mu.Lock()
	b := x.last.Buttons
	x.mu.Unlock()
	x.track(platform.PointerEvent{X: px, Y: py, Buttons: b})
}

// SetCursor leaves the system's arrow.
func (x *Window) SetCursor(hotx, hoty, dx, dy int, clr, set []byte) {}

func registerClasses() error {
	view := objc.GetClass("NSView")
	button := func(_ objc.ID, _ objc.SEL, ev objc.ID) { theWin.mouse(ev, true) }
	move := func(_ objc.ID, _ objc.SEL, ev objc.ID) { theWin.mouse(ev, false) }
	yes := func(objc.ID, objc.SEL) bool { return true }
	_, err := objc.RegisterClass("PurgView", view, nil, nil, []objc.MethodDef{
		{Cmd: sel("acceptsFirstResponder"), Fn: yes},
		{Cmd: sel("acceptsFirstMouse:"), Fn: func(objc.ID, objc.SEL, objc.ID) bool { return true }},
		{Cmd: sel("isOpaque"), Fn: yes},
		{Cmd: sel("drawRect:"), Fn: func(self objc.ID, _ objc.SEL, _ nsRect) { theWin.draw(self) }},
		{Cmd: sel("purgUpdate:"), Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
			theWin.pending.Store(false)
			self.Send(sel("setNeedsDisplay:"), true)
		}},
		{Cmd: sel("mouseDown:"), Fn: button},
		{Cmd: sel("mouseUp:"), Fn: button},
		{Cmd: sel("rightMouseDown:"), Fn: button},
		{Cmd: sel("rightMouseUp:"), Fn: button},
		{Cmd: sel("otherMouseDown:"), Fn: button},
		{Cmd: sel("otherMouseUp:"), Fn: button},
		{Cmd: sel("mouseMoved:"), Fn: move},
		{Cmd: sel("mouseDragged:"), Fn: move},
		{Cmd: sel("rightMouseDragged:"), Fn: move},
		{Cmd: sel("otherMouseDragged:"), Fn: move},
		{Cmd: sel("scrollWheel:"), Fn: func(_ objc.ID, _ objc.SEL, ev objc.ID) { theWin.scrollWheel(ev) }},
		{Cmd: sel("keyDown:"), Fn: func(_ objc.ID, _ objc.SEL, ev objc.ID) { theWin.keyDown(ev) }},
	})
	if err != nil {
		return err
	}
	_, err = objc.RegisterClass("PurgWindowDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel("windowWillClose:"), Fn: func(objc.ID, objc.SEL, objc.ID) { os.Exit(0) }},
		{Cmd: sel("windowDidResize:"), Fn: func(objc.ID, objc.SEL, objc.ID) {
			b := objc.Send[nsRect](theWin.view, sel("bounds"))
			theWin.setSize(int(b.Size.W), int(b.Size.H))
		}},
	})
	return err
}

// draw paints the frame at the view's top left.
func (x *Window) draw(view objc.ID) {
	gc := objc.ID(objc.GetClass("NSGraphicsContext")).Send(sel("currentContext"))
	if gc == 0 {
		return
	}
	ctx := uintptr(gc.Send(sel("CGContext")))
	x.mu.Lock()
	w, h := x.w, x.h
	data := cfDataCreate(0, unsafe.Pointer(&x.pix[0]), len(x.pix))
	x.take()
	x.mu.Unlock()
	if data == 0 {
		return
	}
	defer cfRelease(data)
	prov := cgDataProviderCreateWithCFData(data)
	if prov == 0 {
		return
	}
	defer cgDataProviderRelease(prov)
	img := cgImageCreate(w, h, 8, 32, 4*w, rgbSpace, cgImageAlphaNoneSkipFirst|cgBitmapByteOrder32Little, prov, 0, false, 0)
	if img == 0 {
		return
	}
	defer cgImageRelease(img)
	b := objc.Send[nsRect](view, sel("bounds"))
	cgContextSetInterpolationQualty(ctx, cgInterpolationNone)
	cgContextDrawImage(ctx, nsRect{Origin: nsPoint{X: 0, Y: b.Size.H - float64(h)}, Size: nsSize{W: float64(w), H: float64(h)}}, img)
}

// point returns where an event happened, in screen pixels from the
// view's top left.
func (x *Window) point(ev objc.ID) (int, int) {
	p := objc.Send[nsPoint](ev, sel("locationInWindow"))
	q := objc.Send[nsPoint](x.view, sel("convertPoint:fromView:"), p, objc.ID(0))
	b := objc.Send[nsRect](x.view, sel("bounds"))
	return int(q.X), int(b.Size.H - q.Y)
}

// mouse reports a pointer event; a press or release (button) takes the
// buttons afresh, a move keeps those of the last.
func (x *Window) mouse(ev objc.ID, button bool) {
	px, py := x.point(ev)
	if button {
		pressed := objc.Send[uint](objc.ID(objc.GetClass("NSEvent")), sel("pressedMouseButtons"))
		flags := objc.Send[uint](ev, sel("modifierFlags"))
		x.buttons = macButtons(pressed, flags)
	}
	x.track(platform.PointerEvent{X: px, Y: py, Buttons: x.buttons})
}

func (x *Window) scrollWheel(ev objc.ID) {
	px, py := x.point(ev)
	x.scroll += objc.Send[float64](ev, sel("deltaY"))
	for x.scroll >= 1 {
		x.scroll--
		x.wheel(px, py, x.buttons, true)
	}
	for x.scroll <= -1 {
		x.scroll++
		x.wheel(px, py, x.buttons, false)
	}
}

func (x *Window) keyDown(ev objc.ID) {
	code := objc.Send[uint16](ev, sel("keyCode"))
	flags := objc.Send[uint](ev, sel("modifierFlags"))
	chars := ""
	if s := ev.Send(sel("characters")); s != 0 {
		chars = goString(objc.Send[*byte](s, sel("UTF8String")))
	}
	if flags&macCommand != 0 {
		if chars == "q" {
			os.Exit(0)
		}
		return
	}
	if r, ok := macKeyRune(code, chars); ok {
		x.key(r)
	}
}

// goString copies a C string.
func goString(p *byte) string {
	if p == nil {
		return ""
	}
	var b []byte
	for ; *p != 0; p = (*byte)(unsafe.Add(unsafe.Pointer(p), 1)) {
		b = append(b, *p)
	}
	return string(b)
}
