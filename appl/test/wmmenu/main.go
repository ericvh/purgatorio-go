// Command wmmenu checks the window manager's menu. Run under wm/wm, it
// presses on the background through /dev/pointer, checks that the menu
// is on the screen, releases on Edit, waits for wm/edit's window to
// appear in /mnt/wm, and tells the window manager to exit.
package main

import (
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
	sys.Exits("fail:wmmenu")
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

func px(p draw.Point) uint32 {
	b, err := disp.Image.Unload(draw.Rect(p.X, p.Y, p.X+1, p.Y+1))
	if err != nil {
		fail("unload: %v", err)
	}
	return uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
}

// clients counts the windows' directories in /mnt/wm.
func clients() int {
	ents, err := os.ReadDir("/mnt/wm")
	if err != nil {
		fail("read /mnt/wm: %v", err)
	}
	return len(ents) - 2
}

// await polls until f holds or ten seconds pass.
func await(what string, f func() bool) {
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	fail("%s: timed out", what)
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
	eh := f.Height + 4
	p := draw.Pt(400, 300)
	if c := px(p); c != 0x777777 {
		fail("background %06x", c)
	}
	ptr(p, 0)
	ptr(p, 1)
	// The menu is up, with its first entry, Shell, under the pointer.
	await("menu", func() bool { c := px(p.Add(draw.Pt(0, 2))); return c != 0x777777 })
	fmt.Println("menu up")
	edit := p.Add(draw.Pt(0, 2+eh))
	ptr(edit, 1)
	// Edit is highlighted: black across most of its row.
	await("highlight", func() bool {
		n := 0
		for dx := -40; dx <= 40; dx++ {
			if px(edit.Add(draw.Pt(dx, 0))) == 0x000000 {
				n++
			}
		}
		return n > 20
	})
	ptr(edit, 0)
	await("menu down", func() bool { return px(p.Add(draw.Pt(0, 2))) == 0x777777 || clients() > 0 })
	await("wm/edit", func() bool { return clients() == 1 })
	fmt.Println("wm/edit started")
	write("/mnt/wm/clone", "exit")
}
