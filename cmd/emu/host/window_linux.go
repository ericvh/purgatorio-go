package host

// The native window on Linux: an X11 client in pure Go (jezek/xgb), so
// the emu needs no C libraries and cross builds keep working. On a
// Wayland desktop it runs under XWayland: a Wayland client would need
// the XKB keymap compiler to turn key codes into symbols, which is a
// large piece of C (libxkbcommon) or a large port of it.
//
// The window is in the display's pixels; the frame is converted to the
// root visual's format (copied as it is on the usual 24-bit TrueColor
// visual) and sent with PutImage, in bands that fit a request. Expose
// events resend what was lost, a ConfigureNotify of a new size resizes
// the screen, and WM_DELETE_WINDOW or the connection's end exits the
// emu with status 0, as Inferno's emu exits on DestroyNotify.

import (
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"sync"

	"github.com/ericvh/purgatorio-go/platform"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// Window is a screen shown in an X11 window.
type Window struct {
	*frame
	conn  *xgb.Conn
	setup *xproto.SetupInfo
	win   xproto.Window
	gc    xproto.Gcontext
	depth byte
	pf    pixFormat
	maxrq int // the most bytes of image a PutImage may carry

	wmProtocols, wmDelete xproto.Atom

	kmu        sync.Mutex
	minCode    xproto.Keycode
	perCode    int
	keysyms    []xproto.Keysym
	numlock    uint16
	modeswitch uint16

	cmu    sync.Mutex
	cursor xproto.Cursor

	wakec chan struct{}
}

var (
	_ platform.Screen        = (*Window)(nil)
	_ platform.Pointerer     = (*Window)(nil)
	_ platform.Keyboarder    = (*Window)(nil)
	_ platform.PointerSetter = (*Window)(nil)
	_ platform.Resizer       = (*Window)(nil)
)

// NewWindow opens a w×h window on the X display named by $DISPLAY.
func NewWindow(w, h int) (*Window, error) {
	xgb.Logger = log.New(io.Discard, "", 0)
	disp := os.Getenv("DISPLAY")
	if disp == "" {
		return nil, errors.New("cannot open display: $DISPLAY is not set")
	}
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("cannot open display %s: %v", disp, err)
	}
	x := &Window{frame: newFrame(w, h), conn: conn, wakec: make(chan struct{}, 1)}
	x.frame.wake = func() {
		select {
		case x.wakec <- struct{}{}:
		default:
		}
	}
	if err := x.open(w, h); err != nil {
		conn.Close()
		return nil, fmt.Errorf("display %s: %v", disp, err)
	}
	return x, nil
}

func (x *Window) open(w, h int) error {
	c := x.conn
	x.setup = xproto.Setup(c)
	scr := x.setup.DefaultScreen(c)
	x.depth = scr.RootDepth
	var vis *xproto.VisualInfo
	for _, d := range scr.AllowedDepths {
		for i, v := range d.Visuals {
			if d.Depth == scr.RootDepth && v.VisualId == scr.RootVisual {
				vis = &d.Visuals[i]
			}
		}
	}
	if vis == nil || (vis.Class != xproto.VisualClassTrueColor && vis.Class != xproto.VisualClassDirectColor) {
		return errors.New("the root visual is not TrueColor")
	}
	x.pf = pixFormat{rmask: vis.RedMask, gmask: vis.GreenMask, bm: vis.BlueMask,
		msbFirst: x.setup.ImageByteOrder == xproto.ImageOrderMSBFirst}
	for _, f := range x.setup.PixmapFormats {
		if f.Depth == scr.RootDepth {
			x.pf.bpp, x.pf.pad = int(f.BitsPerPixel), int(f.ScanlinePad)
		}
	}
	if x.pf.bpp != 16 && x.pf.bpp != 24 && x.pf.bpp != 32 {
		return fmt.Errorf("unsupported depth %d (%d bits a pixel)", scr.RootDepth, x.pf.bpp)
	}
	x.maxrq = int(x.setup.MaximumRequestLength)*4 - 24

	var err error
	if x.win, err = xproto.NewWindowId(c); err != nil {
		return err
	}
	mask := uint32(xproto.EventMaskExposure | xproto.EventMaskKeyPress | xproto.EventMaskButtonPress |
		xproto.EventMaskButtonRelease | xproto.EventMaskPointerMotion | xproto.EventMaskStructureNotify)
	if err := xproto.CreateWindowChecked(c, scr.RootDepth, x.win, scr.Root, 0, 0, uint16(w), uint16(h), 0,
		xproto.WindowClassInputOutput, scr.RootVisual, xproto.CwBackPixel|xproto.CwEventMask,
		[]uint32{scr.WhitePixel, mask}).Check(); err != nil {
		return err
	}
	title := "inferno"
	xproto.ChangeProperty(c, xproto.PropModeReplace, x.win, xproto.AtomWmName, xproto.AtomString, 8,
		uint32(len(title)), []byte(title))
	x.wmProtocols = x.atom("WM_PROTOCOLS")
	x.wmDelete = x.atom("WM_DELETE_WINDOW")
	if x.wmProtocols != 0 && x.wmDelete != 0 {
		var b [4]byte
		xgb.Put32(b[:], uint32(x.wmDelete))
		xproto.ChangeProperty(c, xproto.PropModeReplace, x.win, x.wmProtocols, xproto.AtomAtom, 32, 1, b[:])
	}
	if x.gc, err = xproto.NewGcontextId(c); err != nil {
		return err
	}
	xproto.CreateGC(c, x.gc, xproto.Drawable(x.win), 0, nil)
	if err := x.loadKeymap(); err != nil {
		return err
	}
	return xproto.MapWindowChecked(c, x.win).Check()
}

func (x *Window) atom(name string) xproto.Atom {
	r, err := xproto.InternAtom(x.conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0
	}
	return r.Atom
}

// loadKeymap reads the keyboard mapping and which modifiers are Num
// Lock and Mode_switch.
func (x *Window) loadKeymap() error {
	c := x.conn
	lo, hi := x.setup.MinKeycode, x.setup.MaxKeycode
	km, err := xproto.GetKeyboardMapping(c, lo, byte(hi-lo+1)).Reply()
	if err != nil {
		return err
	}
	mm, err := xproto.GetModifierMapping(c).Reply()
	if err != nil {
		return err
	}
	x.kmu.Lock()
	defer x.kmu.Unlock()
	x.minCode, x.perCode, x.keysyms = lo, int(km.KeysymsPerKeycode), km.Keysyms
	x.numlock, x.modeswitch = 0, 0
	per := int(mm.KeycodesPerModifier)
	for mod := 0; mod < 8; mod++ {
		for _, kc := range mm.Keycodes[mod*per : (mod+1)*per] {
			for _, s := range x.symsLocked(kc) {
				switch s {
				case xkNumLock:
					x.numlock |= 1 << mod
				case xkModeSwitch:
					x.modeswitch |= 1 << mod
				}
			}
		}
	}
	return nil
}

func (x *Window) symsLocked(kc xproto.Keycode) []uint32 {
	if kc < x.minCode || x.perCode == 0 {
		return nil
	}
	i := int(kc-x.minCode) * x.perCode
	if i+x.perCode > len(x.keysyms) {
		return nil
	}
	out := make([]uint32, x.perCode)
	for j := range out {
		out[j] = uint32(x.keysyms[i+j])
	}
	return out
}

// Run runs main, the emu, while the window takes events, and exits with
// main's status, or with 0 when the window is closed.
func (x *Window) Run(main func() int) {
	go func() { os.Exit(main()) }()
	x.Serve()
	os.Exit(0)
}

// Serve shows the screen and takes the window's events until the window
// is closed or the connection to the display ends.
func (x *Window) Serve() {
	go x.painter()
	x.wake()
	for {
		ev, err := x.conn.WaitForEvent()
		if ev == nil && err == nil {
			return // the connection is gone
		}
		if ev != nil && !x.event(ev) {
			return
		}
	}
}

// Close closes the connection to the display, which ends Serve.
func (x *Window) Close() { x.conn.Close() }

// event handles an event, and reports false when the window has been
// closed.
func (x *Window) event(ev xgb.Event) bool {
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		x.expose(image.Rect(int(e.X), int(e.Y), int(e.X)+int(e.Width), int(e.Y)+int(e.Height)))
	case xproto.ConfigureNotifyEvent:
		if e.Window == x.win {
			x.setSize(int(e.Width), int(e.Height))
		}
	case xproto.KeyPressEvent:
		x.kmu.Lock()
		sym := x11Keysym(x.symsLocked(e.Detail), e.State, x.numlock, x.modeswitch)
		x.kmu.Unlock()
		if r, ok := x11KeyRune(sym, e.State&xControlMask != 0); ok {
			x.key(r)
		}
	case xproto.ButtonPressEvent:
		x.track(platform.PointerEvent{X: int(e.EventX), Y: int(e.EventY), Buttons: x11Buttons(e.State, byte(e.Detail), true)})
	case xproto.ButtonReleaseEvent:
		x.track(platform.PointerEvent{X: int(e.EventX), Y: int(e.EventY), Buttons: x11Buttons(e.State, byte(e.Detail), false)})
	case xproto.MotionNotifyEvent:
		x.track(platform.PointerEvent{X: int(e.EventX), Y: int(e.EventY), Buttons: x11Buttons(e.State, 0, false)})
	case xproto.MappingNotifyEvent:
		if e.Request == xproto.MappingKeyboard || e.Request == xproto.MappingModifier {
			_ = x.loadKeymap()
		}
	case xproto.ClientMessageEvent:
		if e.Type == x.wmProtocols && e.Format == 32 && len(e.Data.Data32) > 0 && xproto.Atom(e.Data.Data32[0]) == x.wmDelete {
			return false
		}
	case xproto.DestroyNotifyEvent:
		if e.Window == x.win {
			return false
		}
	}
	return true
}

// painter sends the damage to the window whenever there is some.
func (x *Window) painter() {
	var buf []byte
	for range x.wakec {
		x.mu.Lock()
		r := x.take()
		if r.Empty() {
			x.mu.Unlock()
			continue
		}
		stride := x.pf.stride(r.Dx())
		rows := max(1, x.maxrq/stride)
		type band struct {
			y, h int
			data []byte
		}
		var bands []band
		buf = buf[:0]
		for y := r.Min.Y; y < r.Max.Y; y += rows {
			b := image.Rect(r.Min.X, y, r.Max.X, min(y+rows, r.Max.Y))
			start := len(buf)
			buf = x.pf.convert(buf, x.pix, 4*x.w, b)
			bands = append(bands, band{y, b.Dy(), buf[start:len(buf):len(buf)]})
		}
		x.mu.Unlock()
		for _, b := range bands {
			xproto.PutImage(x.conn, xproto.ImageFormatZPixmap, xproto.Drawable(x.win), x.gc,
				uint16(r.Dx()), uint16(b.h), int16(r.Min.X), int16(b.y), 0, x.depth, b.data)
		}
		x.conn.Sync()
	}
}

// SetPointer moves the X pointer, which reports the move as an event.
func (x *Window) SetPointer(px, py int) {
	xproto.WarpPointer(x.conn, 0, x.win, 0, 0, 0, 0, int16(px), int16(py))
}

// SetCursor makes the window's cursor, as Inferno's X11 emu does: the
// set bits black, the clear bits white, the hot spot at -hot. A nil
// cursor gives the window system's own.
func (x *Window) SetCursor(hotx, hoty, dx, dy int, clr, set []byte) {
	x.cmu.Lock()
	defer x.cmu.Unlock()
	c := x.conn
	var cur xproto.Cursor
	if set != nil && dx > 0 && dy >= 2 {
		h := dy / 2
		var err error
		if cur, err = x.bitmapCursor(-hotx, -hoty, dx, h, clr, set); err != nil {
			cur = 0
		}
	}
	xproto.ChangeWindowAttributes(c, x.win, xproto.CwCursor, []uint32{uint32(cur)})
	if x.cursor != 0 {
		xproto.FreeCursor(c, x.cursor)
	}
	x.cursor = cur
}

func (x *Window) bitmapCursor(hotx, hoty, w, h int, clr, set []byte) (xproto.Cursor, error) {
	c := x.conn
	bpl := w / 8
	pad := max(int(x.setup.BitmapFormatScanlinePad), 8) / 8
	stride := (bpl + pad - 1) / pad * pad
	lsb := x.setup.BitmapFormatBitOrder == xproto.ImageOrderLSBFirst
	src := make([]byte, stride*h)
	msk := make([]byte, stride*h)
	for y := range h {
		for i := range bpl {
			s, m := set[y*bpl+i], set[y*bpl+i]|clr[y*bpl+i]
			if lsb {
				s, m = revbyte(s), revbyte(m)
			}
			src[y*stride+i], msk[y*stride+i] = s, m
		}
	}
	var pms [2]xproto.Pixmap
	for i, data := range [][]byte{src, msk} {
		pm, err := xproto.NewPixmapId(c)
		if err != nil {
			return 0, err
		}
		xproto.CreatePixmap(c, 1, pm, xproto.Drawable(x.win), uint16(w), uint16(h))
		gc, err := xproto.NewGcontextId(c)
		if err != nil {
			return 0, err
		}
		xproto.CreateGC(c, gc, xproto.Drawable(pm), 0, nil)
		xproto.PutImage(c, xproto.ImageFormatXYPixmap, xproto.Drawable(pm), gc, uint16(w), uint16(h), 0, 0, 0, 1, data)
		xproto.FreeGC(c, gc)
		pms[i] = pm
	}
	cur, err := xproto.NewCursorId(c)
	if err != nil {
		return 0, err
	}
	hx, hy := min(max(hotx, 0), w-1), min(max(hoty, 0), h-1)
	xproto.CreateCursor(c, cur, pms[0], pms[1], 0, 0, 0, 0xFFFF, 0xFFFF, 0xFFFF, uint16(hx), uint16(hy))
	xproto.FreePixmap(c, pms[0])
	xproto.FreePixmap(c, pms[1])
	return cur, nil
}

func revbyte(b byte) byte {
	b = b>>4 | b<<4
	b = b>>2&0x33 | b<<2&0xCC
	return b>>1&0x55 | b<<1&0xAA
}

// Snapshot reads the window's contents back from the X server (GetImage)
// as RGBA, for tests.
func (x *Window) Snapshot() (*image.RGBA, error) {
	x.mu.Lock()
	w, h := x.w, x.h
	x.mu.Unlock()
	x.conn.Sync()
	rep, err := xproto.GetImage(x.conn, xproto.ImageFormatZPixmap, xproto.Drawable(x.win), 0, 0,
		uint16(w), uint16(h), 0xFFFFFFFF).Reply()
	if err != nil {
		return nil, err
	}
	return x.pf.toRGBA(rep.Data, w, h)
}
