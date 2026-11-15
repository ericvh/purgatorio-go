// Command rio checks the rio window system end to end. Run beside rio,
// it drives rio through #m and #c as a user would: it sweeps a window
// with the menu's New, types commands into the window's shell and
// reads what they write, runs a program that draws through the SDK in
// the window, clicks in it, resizes the window and ends the program,
// and ends rio with Exit. It checks pixels of the screen on the way and
// prints what it found.
//
//	test/rio [-resize]
//
// With -resize it makes a window, creates /tmp/rio.ready and waits for
// the screen to change size (the Go test resizes it), then checks that
// the window was scaled.
package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	grey     = 0x777777
	focus    = 0x2E6E6E
	magenta  = 0xDD22DD
	white    = 0xFFFFFF
	pressure = 4 // button 3
)

var (
	disp *draw.Display
	ih   int // a menu item's height
)

func fail(f string, a ...any) {
	fmt.Printf("fail: "+f+"\n", a...)
	sys.Exits("fail:rio")
}

func write(file, s string) {
	fd, err := sys.Open(file, sys.OWRITE)
	if err != nil {
		fail("open %s: %v", file, err)
	}
	if _, err := syscall.Write(fd, []byte(s)); err != nil {
		fail("write %s: %s", file, sys.Errstr())
	}
	syscall.Close(fd)
}

// ptr sends a pointer event, with a pause so that rio sees each.
func ptr(x, y, buttons int) {
	write("/dev/pointer", fmt.Sprintf("m%d %d %d", x, y, buttons))
	time.Sleep(30 * time.Millisecond)
}

func typed(s string) { write("/dev/keyboard", s) }

// px returns the screen's pixel at x, y as 0xRRGGBB.
func px(x, y int) uint32 {
	b, err := disp.Image.Unload(draw.Rect(x, y, x+1, y+1))
	if err != nil {
		fail("unload: %v", err)
	}
	return uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
}

// await polls until f holds or twenty seconds pass.
func await(what string, f func() bool) {
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	fail("%s: timed out", what)
}

func pixel(x, y int, c uint32) func() bool { return func() bool { return px(x, y) == c } }

// file waits for a file to hold something and returns it.
func file(name string) string {
	var b []byte
	await(name, func() bool {
		b, _ = os.ReadFile(name)
		return len(b) > 0
	})
	return string(b)
}

// choose presses button 3 at x, y, where item last of the menu comes
// up, and releases it on item k.
func choose(x, y, last, k int) {
	ptr(x, y, 0)
	ptr(x, y, pressure)
	ptr(x, y+(k-last)*ih, pressure)
	ptr(x, y+(k-last)*ih, 0)
}

// sweep sweeps a rectangle with button 3.
func sweep(x0, y0, x1, y1 int) {
	ptr(x0, y0, pressure)
	ptr(x1, y1, pressure)
	ptr(x1, y1, 0)
}

func main() {
	var err error
	if disp, err = draw.Init(""); err != nil {
		fail("%v", err)
	}
	f, err := disp.GetDefaultFont()
	if err != nil {
		fail("%v", err)
	}
	ih = f.Height + 4
	for _, n := range []string{"rio.wctl", "rio.text", "rio.ok", "riodraw.ptr", "rio.after", "rio.ready",
		"rio.scaled", "rio.sent", "rio.snarf"} {
		os.Remove("/tmp/" + n)
	}
	await("rio's background", pixel(320, 240, grey))
	fmt.Println("rio up")

	// New, and a window swept from 60 60 to 460 300: current, so its
	// border is the dark one.
	choose(320, 240, 0, 0)
	sweep(60, 60, 460, 300)
	await("new window", pixel(61, 61, focus))
	fmt.Println("window made")

	if len(os.Args) > 1 && os.Args[1] == "-resize" {
		resize()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "-edit" {
		edit(f.Height)
		return
	}

	// The window's shell runs what is typed; output goes before typing
	// not yet read.
	typed("echo hello world\n")
	typed("cat /dev/winid /dev/wctl /dev/label >/tmp/rio.wctl; ls /dev/wsys >>/tmp/rio.wctl; " +
		"cat /dev/text >/tmp/rio.text; echo >/tmp/rio.ok\n")
	file("/tmp/rio.ok")
	fmt.Printf("wctl %q\n", file("/tmp/rio.wctl"))
	fmt.Printf("text %q\n", file("/tmp/rio.text"))

	// A program drawing through the SDK draws inside the window's
	// border and nowhere else.
	typed("/bin/test/riodraw.wasm\n")
	await("riodraw", pixel(260, 180, magenta))
	if px(65, 65) != magenta || px(61, 61) != focus || px(500, 400) != grey || px(470, 200) != grey {
		fail("riodraw: %06x %06x %06x %06x", px(65, 65), px(61, 61), px(500, 400), px(470, 200))
	}
	fmt.Println("riodraw in the window")

	// A click in the window is the program's.
	ptr(260, 180, 0)
	ptr(260, 180, 1)
	ptr(260, 180, 0)
	fmt.Printf("riodraw got %q\n", file("/tmp/riodraw.ptr"))

	// Resize: the program draws in the new rectangle and the old place
	// shows the background.
	choose(550, 200, 0, 1)
	ptr(260, 180, pressure)
	ptr(260, 180, 0)
	sweep(100, 100, 500, 400)
	await("resize", pixel(490, 390, magenta))
	await("old place", pixel(70, 70, grey))
	fmt.Println("resized")

	// q ends the program: rio shows the text again.
	typed("q")
	await("text again", pixel(490, 390, white))
	typed("echo after >/tmp/rio.after\n")
	fmt.Printf("shell still runs: %q\n", file("/tmp/rio.after"))

	fmt.Println("done")
	choose(550, 200, 1, 5)
}

// edit checks selection and button 2's menu, hiding, dragging a border,
// moving, wctl's new and deleting, in the window at 60 60 460 300 whose
// text starts at 68 66 in lines fh high.
func edit(fh int) {
	// A double click at the start of an output line selects the line;
	// snarf puts it in the snarf buffer and send runs it.
	typed("echo echo sent '>/tmp/rio.sent'\n")
	line2 := 66 + fh + fh/2
	await("output", func() bool { return px(70, line2) != white })
	for i := 0; i < 2; i++ {
		ptr(68, line2, 1)
		ptr(68, line2, 0)
	}
	await("selection", pixel(400, line2, 0xEEEE9E))
	// With no plumber the menu is cut, paste, snarf, send, noscroll.
	ptr(400, line2, 2) // cut is under the pointer
	ptr(400, line2+2*ih, 2)
	ptr(400, line2+2*ih, 0) // snarf
	ptr(400, line2, 2)      // snarf is under the pointer now
	ptr(400, line2+ih, 2)
	ptr(400, line2+ih, 0) // send
	fmt.Printf("send ran %q\n", file("/tmp/rio.sent"))
	typed("cat '#^/snarf' >/tmp/rio.snarf\n")
	fmt.Printf("snarf %q\n", file("/tmp/rio.snarf"))

	// Hide, and the hidden window's label in the menu shows it again.
	choose(550, 200, 0, 4)
	ptr(200, 200, pressure)
	ptr(200, 200, 0)
	await("hidden", pixel(61, 61, grey))
	choose(550, 200, 4, 6)
	await("shown", pixel(61, 61, focus))
	fmt.Println("hide and show")

	// Button 1 on the bottom right corner drags it.
	ptr(458, 298, 0)
	ptr(458, 298, 1)
	ptr(500, 350, 1)
	ptr(500, 350, 0)
	await("corner", pixel(498, 348, focus))
	fmt.Println("corner dragged")

	// Move drags the window with button 3. The last item chosen was
	// the hidden window's, now gone: the last item comes up.
	choose(550, 400, 5, 2)
	ptr(200, 200, pressure)
	ptr(250, 230, pressure)
	ptr(250, 230, 0)
	await("moved", pixel(111, 91, focus))
	await("old corner", pixel(61, 61, grey))
	fmt.Println("moved")

	// wctl's new makes a window, current; Delete takes it away.
	typed("echo new -r 300 300 600 450 >/dev/wctl\n")
	await("wctl new", pixel(598, 448, focus))
	choose(20, 200, 2, 3)
	ptr(590, 440, pressure)
	ptr(590, 440, 0)
	await("deleted", pixel(598, 448, grey))
	typed("cat /dev/wctl >/tmp/rio.wctl\n")
	fmt.Printf("wctl %q\n", strings.Fields(file("/tmp/rio.wctl")))

	fmt.Println("done")
	choose(20, 200, 3, 5)
}

// resize waits for the Go test to make the screen 800×600 and checks
// that the window has been scaled with it, from rio's border and the
// window's wctl.
func resize() {
	if err := os.WriteFile("/tmp/rio.ready", nil, 0o666); err != nil {
		fail("%v", err)
	}
	await("screen resize", func() bool {
		_, err := disp.CheckResize()
		return err == nil && disp.Image.R.Dx() == 800
	})
	await("scaled window", pixel(76, 76, focus))
	typed("cat /dev/wctl >/tmp/rio.scaled\n")
	var s string
	await("wctl", func() bool { b, _ := os.ReadFile("/tmp/rio.scaled"); s = string(b); return len(s) >= 72 })
	fmt.Printf("scaled %q\n", strings.Fields(s))
	fmt.Println("done")
	choose(700, 300, 0, 5)
}
