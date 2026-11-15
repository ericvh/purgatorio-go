// Command wmcoffee checks wm/coffee. Run under wm/wm, it starts
// wm/coffee, waits for its window, finds the panel by its background
// colour, checks that cups are drawn and move, presses Stop and checks
// that they stop, and tells the window manager to exit.
package main

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var disp *draw.Display

func fail(f string, a ...any) {
	fmt.Printf("fail: "+f+"\n", a...)
	write("/mnt/wm/clone", "exit")
	sys.Exits("fail:wmcoffee")
}

func write(file, s string) {
	fd, err := sys.Open(file, sys.OWRITE)
	if err != nil {
		fmt.Printf("open %s: %v\n", file, err)
		return
	}
	syscall.Write(fd, []byte(s))
	syscall.Close(fd)
}

func ptr(p draw.Point, buttons int) {
	write("/dev/pointer", fmt.Sprintf("m%d %d %d", p.X, p.Y, buttons))
}

func unload(r draw.Rectangle) []byte {
	b, err := disp.Image.Unload(r)
	if err != nil {
		fail("unload: %v", err)
	}
	return b
}

func await(what string, f func() bool) {
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	fail("%s: timed out", what)
}

func clients() int {
	ents, err := os.ReadDir("/mnt/wm")
	if err != nil {
		fail("read /mnt/wm: %v", err)
	}
	return len(ents) - 2
}

// find returns the bounding box of the screen's pixels of colour c
// (0xRRGGBB), or an empty rectangle.
func find(c uint32) (draw.Rectangle, []byte) {
	r := disp.Image.R
	b := unload(r)
	var box draw.Rectangle
	found := false
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			o := 4 * ((y-r.Min.Y)*r.Dx() + x - r.Min.X)
			if uint32(b[o+2])<<16|uint32(b[o+1])<<8|uint32(b[o]) != c {
				continue
			}
			if !found {
				box, found = draw.Rect(x, y, x+1, y+1), true
			}
			box.Min.X, box.Min.Y = min(box.Min.X, x), min(box.Min.Y, y)
			box.Max.X, box.Max.Y = max(box.Max.X, x+1), max(box.Max.Y, y+1)
		}
	}
	return box, b
}

func main() {
	var err error
	if disp, err = draw.Init(""); err != nil {
		fail("%v", err)
	}
	if _, err := sys.Spawn("/bin/wm/coffee.wasm", []string{"wm/coffee"}, sys.NEWFD,
		[][2]int{{0, 0}, {1, 1}, {2, 2}}); err != nil {
		fail("spawn: %v", err)
	}
	await("window", func() bool { return clients() == 1 })
	fmt.Println("wm/coffee started")
	var panel draw.Rectangle
	await("panel", func() bool {
		panel, _ = find(0x3B2414)
		return panel.Dx() == 400 && panel.Dy() == 300
	})
	fmt.Printf("panel %dx%d\n", panel.Dx(), panel.Dy())
	await("red cup", func() bool { r, _ := find(0xC03030); return r.In(panel) && !r.Empty() })
	fmt.Println("cups drawn")
	moving := func() bool {
		a := unload(panel)
		time.Sleep(300 * time.Millisecond)
		return !bytes.Equal(a, unload(panel))
	}
	await("moving", moving)
	fmt.Println("cups move")
	// Stop is the first button on the row above the panel, whose
	// border is 3 wide; the row is as high as a scale, 24.
	stop := draw.Pt(panel.Min.X-3+8, panel.Min.Y-3-12)
	ptr(stop, 0)
	ptr(stop, 1)
	ptr(stop, 0)
	await("stopped", func() bool { return !moving() })
	fmt.Println("stopped")
	ptr(stop, 1)
	ptr(stop, 0)
	await("going", moving)
	fmt.Println("going")
	write("/mnt/wm/clone", "exit")
}
