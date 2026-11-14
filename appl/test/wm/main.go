// Command wm checks the window manager end to end. Run under wm/wm, it
// makes a Tk window, then drives it through the devices: pointer events
// written to /dev/pointer and keys written to /dev/keyboard go through
// wm/wm to the window, and Tk acts on them. It checks the window's
// pixels on the screen and the window manager's files, prints what it
// found, and tells the window manager to exit.
package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/tk"
	"github.com/ericvh/purgatorio-go/sdk/tkclient"
	"github.com/ericvh/purgatorio-go/sdk/wmclient"
)

var (
	top   *tk.Toplevel
	wmctl chan string
	cmd   = make(chan string, 16)
	disp  *draw.Display
	// focused is whether the window manager says this window has the
	// keyboard.
	focused bool
)

func fail(f string, a ...any) {
	fmt.Printf("fail: "+f+"\n", a...)
	if fd, err := sys.Open("/mnt/wm/clone", sys.ORDWR); err == nil {
		syscall.Write(fd, []byte("exit"))
	}
	sys.Exits("fail:wm")
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func write(file, s string) {
	fd, err := sys.Open(file, sys.OWRITE)
	check(err)
	if _, err := syscall.Write(fd, []byte(s)); err != nil {
		fail("write %s: %s", file, sys.Errstr())
	}
	syscall.Close(fd)
}

// ptr sends a pointer event through #m.
func ptr(p draw.Point, buttons int) {
	write("/dev/pointer", fmt.Sprintf("m%d %d %d", p.X, p.Y, buttons))
}

// run handles events until done reports true or there have been n
// quiet seconds.
func run(what string, done func(sent []string) bool) []string {
	var sent []string
	for quiet := 0; quiet < 20; {
		e, err := tkclient.Next(top, nil, 1000)
		check(err)
		if e.Kind == wmclient.Timeout {
			quiet++
		}
		if e.Kind == wmclient.Ctl && strings.HasPrefix(e.Ctl, "haskbdfocus ") {
			focused = e.Ctl == "haskbdfocus 1"
		}
		if os.Getenv("wmdebug") != "" {
			fmt.Printf("event %+v\n", e)
		}
		for drained := false; !drained; {
			select {
			case s := <-wmctl:
				tkclient.Wmctl(top, s)
			case s := <-cmd:
				sent = append(sent, s)
			default:
				drained = true
			}
		}
		if done(sent) {
			return sent
		}
	}
	fail("%s: timed out, got %q, title %06x", what, sent, px(tk.Rect(top, ".Wm_t.title", tk.Border).Min.Add(draw.Pt(1, 1))))
	return nil
}

func center(name string) draw.Point {
	r := tk.Rect(top, name, tk.Border)
	return r.Min.Add(r.Max).Div(2)
}

// px returns the screen's pixel at p as 0xRRGGBB.
func px(p draw.Point) uint32 {
	b, err := disp.Image.Unload(draw.Rect(p.X, p.Y, p.X+1, p.Y+1))
	check(err)
	return uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
}

func main() {
	var err error
	disp, err = draw.Init("")
	check(err)
	_, err = disp.GetDefaultFont()
	check(err)
	top, wmctl, err = tkclient.Toplevel(disp, "-x 40 -y 30", "Test", tkclient.Appl)
	check(err)
	tk.NameChan(top, cmd, "cmd")
	for _, c := range []string{
		"frame .f -bg red -width 60 -height 20",
		"entry .e -bg white",
		"button .b -text Press -command {send cmd pressed}",
		"bind .e <Key-\n> {send cmd entered}",
		"pack .f .e .b -fill x",
		"update",
	} {
		if e := tk.Cmd(top, c); strings.HasPrefix(e, "!") {
			fail("tk %q: %s", c, e)
		}
	}
	check(tkclient.Onscreen(top, "exact"))
	check(tkclient.StartInput(top, "kbd", "ptr"))
	r := tk.Rect(top, ".", tk.Border)
	fmt.Printf("window %d %d %d %d\n", r.Min.X, r.Min.Y, r.Max.X, r.Max.Y)

	// The window manager's files.
	b, err := os.ReadFile("/mnt/wm/wmrect")
	check(err)
	fmt.Printf("wmrect %s\n", b)
	ents, err := os.ReadDir("/mnt/wm")
	check(err)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	fmt.Printf("mnt/wm %s\n", strings.Join(names, " "))

	// Focus comes with "start kbd": the title bar turns blue.
	title := func() draw.Point { return tk.Rect(top, ".Wm_t.title", tk.Border).Min.Add(draw.Pt(1, 1)) }
	run("focus", func([]string) bool { return px(title()) == 0x0000CC })
	if c := px(center(".f")); c != 0xFF0000 {
		fail("frame pixel %06x", c)
	}
	fmt.Println("focus ok")

	// A click on the button.
	p := center(".b")
	ptr(p, 0)
	ptr(p, 1)
	ptr(p, 0)
	run("button", func(s []string) bool { return len(s) > 0 })
	fmt.Println("button ok")

	// Click in the entry and type.
	ptr(center(".e"), 1)
	ptr(center(".e"), 0)
	write("/dev/keyboard", "hello, wm\n")
	sent := run("keyboard", func(s []string) bool { return len(s) > 0 })
	if v := tk.Cmd(top, ".e get"); sent[0] != "entered" || v != "hello, wm" {
		fail("entry sent %q and holds %q", sent[0], v)
	}
	fmt.Println("keyboard ok")

	// Drag the window by its title bar 30 to the right and 20 down. The
	// release has to come while the window manager drags, which is while
	// this prog waits for its request, so another prog sends it.
	t := center(".Wm_t.title")
	to := t.Add(draw.Pt(30, 20))
	ptr(t, 0)
	ptr(t, 1)
	_, err = sys.Spawn("/bin/sh.wasm", []string{"sh", "-c",
		fmt.Sprintf("sleep 1; echo m%d %d 1 > /dev/pointer; echo m%d %d 0 > /dev/pointer", to.X, to.Y, to.X, to.Y)},
		0, nil)
	check(err)
	run("drag", func([]string) bool { return tk.Rect(top, ".", tk.Border).Min == r.Min.Add(draw.Pt(30, 20)) })
	nr := tk.Rect(top, ".", tk.Border)
	fmt.Printf("moved %d %d\n", nr.Min.X-r.Min.X, nr.Min.Y-r.Min.Y)
	if c := px(center(".f")); c != 0xFF0000 {
		fail("frame pixel after move %06x", c)
	}
	if c := px(r.Min.Add(draw.Pt(2, 2))); c != 0x777777 {
		fail("background after move %06x", c)
	}
	fmt.Println("drag ok")

	// A shell window: when it starts it takes the keyboard, and what is
	// typed runs in its shell. It runs in a process group of its own, as
	// wmsetup's wmrun starts programs, so its killgrp when wm tells it
	// to exit does not reach wm.
	os.Remove("/tmp/wmsh.out")
	_, err = sys.Spawn("/bin/wm/sh.wasm", []string{"wm/sh"}, sys.NEWPGRP, nil)
	check(err)
	run("wm/sh focus", func([]string) bool { return !focused })
	write("/dev/keyboard", "echo hello from wm/sh > /tmp/wmsh.out\n")
	var out []byte
	run("wm/sh", func([]string) bool {
		out, _ = os.ReadFile("/tmp/wmsh.out")
		return len(out) > 0
	})
	fmt.Printf("wm/sh wrote %q\n", out)
	ents, err = os.ReadDir("/mnt/wm")
	check(err)
	fmt.Printf("clients %d\n", len(ents)-2)

	// Report before asking wm to exit: its exit ends this client too.
	fmt.Println("done")
	write("/mnt/wm/clone", "exit")
}
