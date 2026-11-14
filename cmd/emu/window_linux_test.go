package main

import (
	"bufio"
	"fmt"
	"image"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// startXvfb starts an X server for the test and sets $DISPLAY to it,
// or skips the test if there is no Xvfb.
func startXvfb(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Skip("no Xvfb")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-displayfd", "3", "-screen", "0", "1024x768x24", "-nolisten", "tcp")
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Xvfb: %v", err)
	}
	w.Close()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		got <- strings.TrimSpace(line)
	}()
	select {
	case n := <-got:
		if n == "" {
			t.Fatal("Xvfb gave no display")
		}
		t.Setenv("DISPLAY", ":"+n)
	case <-time.After(30 * time.Second):
		t.Fatal("Xvfb did not start")
	}
}

// xclient is the test's own connection to the X server, to drive and
// read the emu's window as a user and a screenshot tool would.
type xclient struct {
	t    *testing.T
	c    *xgb.Conn
	root xproto.Window
	min  xproto.Keycode
	per  int
	syms []xproto.Keysym
}

func newXClient(t *testing.T) *xclient {
	t.Helper()
	c, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := xtest.Init(c); err != nil {
		t.Fatalf("XTEST: %v", err)
	}
	s := xproto.Setup(c)
	x := &xclient{t: t, c: c, root: s.DefaultScreen(c).Root, min: s.MinKeycode}
	km, err := xproto.GetKeyboardMapping(c, s.MinKeycode, byte(s.MaxKeycode-s.MinKeycode+1)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	x.per, x.syms = int(km.KeysymsPerKeycode), km.Keysyms
	return x
}

// window returns the emu's window: the root's only child, as there is
// no window manager.
func (x *xclient) window() xproto.Window {
	x.t.Helper()
	tr, err := xproto.QueryTree(x.c, x.root).Reply()
	if err != nil || len(tr.Children) != 1 {
		x.t.Fatalf("query tree: %v %v", tr, err)
	}
	return tr.Children[0]
}

// key presses and releases the key that gives sym without shift.
func (x *xclient) key(sym uint32) {
	x.t.Helper()
	for i := 0; i < len(x.syms); i += x.per {
		if uint32(x.syms[i]) == sym {
			kc := byte(int(x.min) + i/x.per)
			xtest.FakeInput(x.c, xproto.KeyPress, kc, 0, x.root, 0, 0, 0)
			xtest.FakeInput(x.c, xproto.KeyRelease, kc, 0, x.root, 0, 0, 0)
			return
		}
	}
	x.t.Fatalf("no key for %#x", sym)
}

// shot reads the window's contents as RGBA.
func (x *xclient) shot(win xproto.Window, w, h int) *image.RGBA {
	x.t.Helper()
	rep, err := xproto.GetImage(x.c, xproto.ImageFormatZPixmap, xproto.Drawable(win), 0, 0, uint16(w), uint16(h), 0xFFFFFFFF).Reply()
	if err != nil {
		x.t.Fatalf("GetImage: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if len(rep.Data) < 4*w*h {
		x.t.Fatalf("GetImage: %d bytes for %dx%d (the test needs a 24-bit TrueColor screen)", len(rep.Data), w, h)
	}
	for i := 0; i < w*h; i++ {
		p := rep.Data[4*i:]
		img.Pix[4*i], img.Pix[4*i+1], img.Pix[4*i+2], img.Pix[4*i+3] = p[2], p[1], p[0], 0xFF
	}
	return img
}

// screenDiff compares the draw device's screen with the window's
// contents and describes the first difference, or returns "".
func screenDiff(sys *host.System, img *image.RGBA) string {
	scr := sys.Kernel.DevTab.Lookup('i').(*core.DevDraw).Screen()
	if scr == nil {
		return "no screen image"
	}
	w, h := scr.R.Dx(), scr.R.Dy()
	if img.Rect.Dx() != w || img.Rect.Dy() != h {
		return fmt.Sprintf("window %v, screen %v", img.Rect, scr.R)
	}
	for y := range h {
		for x := range w {
			p := scr.Data.Bdata[4*(y*scr.Width+x):]
			o := img.PixOffset(x, y)
			if img.Pix[o] != p[2] || img.Pix[o+1] != p[1] || img.Pix[o+2] != p[0] {
				return fmt.Sprintf("pixel (%d,%d): window %02x%02x%02x, screen %02x%02x%02x",
					x, y, img.Pix[o], img.Pix[o+1], img.Pix[o+2], p[2], p[1], p[0])
			}
		}
	}
	return ""
}

// TestWindowX11 runs the emu with its screen in an X11 window on Xvfb:
// keys and pointer moves typed at the window reach #c/keyboard and #m;
// wm's screen in the window matches the draw device's screen, read back
// from the X server; and resizing the window resizes the screen, which
// still matches.
func TestWindowX11(t *testing.T) {
	startXvfb(t)
	win, err := host.NewWindow(320, 240)
	if err != nil {
		t.Fatal(err)
	}
	go win.Serve()
	t.Cleanup(win.Close)
	x := newXClient(t)
	xwin := x.window()

	mode := testModes()[0]
	var out lockedBuffer
	h := &host.Host{In: strings.NewReader(""), Out: &out, Screen: win}
	sys, err := host.Boot(h, root, getEngines(t)[mode])
	if err != nil {
		t.Fatal(err)
	}

	// Input: a pointer move into the window, then "hi" and Return.
	xtest.FakeInput(x.c, xproto.MotionNotify, 0, 0, x.root, 10, 20, 0)
	x.key('h')
	x.key('i')
	x.key(0xFF0D)
	x.c.Sync()
	time.Sleep(500 * time.Millisecond)
	st, err := runWatched(t, sys, mode, &out, []string{"sh", "-c",
		"read 49 < /dev/pointer; echo; for i in 1 2 3 {read 1 < /dev/keyboard}"})
	if err != nil || st != "" {
		t.Fatalf("input: %q %v", st, err)
	}
	f := strings.Fields(out.String())
	if len(f) < 6 || f[0] != "m" || f[1] != "10" || f[2] != "20" || f[3] != "0" || f[5] != "hi" {
		t.Fatalf("input: got %q", out.String())
	}

	// wm with wm/about, until the window shows what the device has.
	go func() {
		_, _ = sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", []string{"emuinit", "wm/wm", "wm/about"})
	}()
	match := func(w, h int, what string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		diff := ""
		for time.Now().Before(deadline) {
			time.Sleep(300 * time.Millisecond)
			img := x.shot(xwin, w, h)
			if diff = screenDiff(sys, img); diff == "" {
				// Not just a blank window: wm's grey background shows.
				grey := 0
				for i := 0; i < len(img.Pix); i += 4 {
					if img.Pix[i] == 0x77 && img.Pix[i+1] == 0x77 && img.Pix[i+2] == 0x77 {
						grey++
					}
				}
				if grey > w*h/4 {
					return
				}
				diff = fmt.Sprintf("only %d grey pixels", grey)
			}
		}
		t.Fatalf("%s: the window does not show the screen: %s", what, diff)
	}
	match(320, 240, "wm")

	// Resize the window as a window manager would.
	xproto.ConfigureWindow(x.c, xwin, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{400, 300})
	x.c.Sync()
	match(400, 300, "resized")

	// Shrink it: still the same.
	xproto.ConfigureWindow(x.c, xwin, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{300, 200})
	x.c.Sync()
	match(300, 200, "shrunk")
}
