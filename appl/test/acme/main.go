// Command acme drives acme through /mnt/acme, as acme(4) describes it,
// and prints what it finds. Run it with acme running in the same name
// space (sh -c 'acme & /bin/test/acme.wasm'). It makes a window, writes
// its body, tag, addr, data and ctl files, reads index and events, and
// sends pointer and keyboard input through #m and #c to check the
// events that the mouse and typing make.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func fail(f string, a ...any) {
	fmt.Printf("fail: "+f+"\n", a...)
	sys.Exits("fail:acme")
}

func open(name string, mode int) int {
	fd, err := sys.Open(name, mode)
	if err != nil {
		fail("open %s: %v", name, err)
	}
	return fd
}

func write(name, s string) {
	fd := open(name, sys.OWRITE)
	defer syscall.Close(fd)
	if _, err := syscall.Write(fd, []byte(s)); err != nil {
		fail("write %s %q: %s", name, s, sys.Errstr())
	}
}

// try writes and returns the error, if any.
func try(name, s string) string {
	fd := open(name, sys.OWRITE)
	defer syscall.Close(fd)
	if _, err := syscall.Write(fd, []byte(s)); err != nil {
		return sys.Errstr()
	}
	return ""
}

func read(name string) string {
	b, err := os.ReadFile(name)
	if err != nil {
		fail("read %s: %v", name, err)
	}
	return string(b)
}

// readfd reads once from fd.
func readfd(fd int) string {
	b := make([]byte, 8192)
	n, err := syscall.Read(fd, b)
	if err != nil || n < 0 {
		fail("read: %v", err)
	}
	return string(b[:n])
}

// events holds events read but not yet looked at.
var events []string

// event reads events until one starting with prefix comes, dropping
// those before it.
func event(fd int, prefix string) string {
	for i := 0; i < 20; i++ {
		for len(events) > 0 {
			e := events[0]
			events = events[1:]
			if strings.HasPrefix(e, prefix) {
				return e
			}
		}
		for _, e := range strings.SplitAfter(readfd(fd), "\n") {
			if e != "" {
				events = append(events, strings.TrimSuffix(e, "\n"))
			}
		}
	}
	fail("no event %q", prefix)
	return ""
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

func ptr(p draw.Point, b int) {
	write("/dev/pointer", fmt.Sprintf("m%d %d %d", p.X, p.Y, b))
}

// px returns the screen's pixel at p as 0xRRGGBB.
func px(d *draw.Display, p draw.Point) uint32 {
	b, err := d.Image.Unload(draw.Rect(p.X, p.Y, p.X+1, p.Y+1))
	if err != nil {
		fail("unload: %v", err)
	}
	return uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
}

func main() {
	await("/mnt/acme", func() bool { _, err := os.Stat("/mnt/acme/index"); return err == nil })
	d, err := draw.Init("")
	if err != nil {
		fail("%v", err)
	}
	font, err := d.GetDefaultFont()
	if err != nil {
		fail("%v", err)
	}
	ents, err := os.ReadDir("/mnt/acme")
	if err != nil {
		fail("%v", err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	fmt.Println("mnt/acme", strings.Join(names, " "))

	// A new window: reading new/ctl gives its number.
	fd := open("/mnt/acme/new/ctl", sys.ORDWR)
	ctl := readfd(fd)
	f := strings.Fields(ctl)
	if len(f) < 8 {
		fail("ctl %q", ctl)
	}
	id := f[0]
	fmt.Printf("new window %s: tag %s body %s isdir %s dirty %s\n", id, f[1], f[2], f[3], f[4])
	dir := "/mnt/acme/" + id + "/"
	os.Remove("/tmp/acmetest.txt")
	if _, err := syscall.Write(fd, []byte("name /tmp/acmetest.txt\n")); err != nil {
		fail("name: %v", err)
	}
	write(dir+"body", "hello world\nsecond line\n")
	fmt.Printf("body %q\n", read(dir+"body"))
	tag := read(dir + "tag")
	fmt.Printf("tag %q\n", tag)
	if _, err := syscall.Write(fd, []byte("wobble\n")); err == nil {
		fail("bad ctl accepted")
	}
	fmt.Printf("bad ctl: %s\n", sys.Errstr())

	// addr and data: replace "world" with "there".
	afd := open(dir+"addr", sys.ORDWR)
	syscall.Write(afd, []byte("/world/"))
	fmt.Printf("addr %q\n", strings.Join(strings.Fields(readfd(afd)), " "))
	write(dir+"data", "there")
	syscall.Write(afd, []byte("2"))
	fmt.Printf("line 2 %q\n", read(dir+"xdata"))
	if e := try(dir+"addr", "9"); e != "" {
		fmt.Printf("addr 9: %s\n", e)
	}
	syscall.Close(afd)
	fmt.Printf("body %q\n", read(dir+"body"))

	// Dirty, then put.
	c := strings.Fields(read(dir + "ctl"))
	fmt.Printf("dirty %s\n", c[4])
	syscall.Write(fd, []byte("put\n"))
	b, _ := os.ReadFile("/tmp/acmetest.txt")
	fmt.Printf("put wrote %q; dirty %s\n", b, strings.Fields(read(dir + "ctl"))[4])

	// The index.
	for _, l := range strings.Split(read("/mnt/acme/index"), "\n") {
		if g := strings.Fields(l); len(g) > 5 && g[0] == id {
			fmt.Printf("index %s %s\n", g[0], g[5])
		}
	}

	// Events: a write to the body is an E event.
	efd := open(dir+"event", sys.ORDWR)
	write(dir+"body", "more\n")
	fmt.Println("event", event(efd, "EI"))

	// Typing into the body under the mouse is a K event. The window
	// is the only one in the second column of a 640-wide screen: find
	// the top of its body, the first pale yellow down the column.
	h := font.Height
	x0 := 320 + 16
	body := -1
	for y := 0; y < 200; y++ {
		if px(d, draw.Pt(x0+100, y)) == 0xFFFFEA {
			body = y
			break
		}
	}
	if body < 0 {
		fail("no body on the screen")
	}
	ptr(draw.Pt(x0+20, body+h/2), 0)
	time.Sleep(200 * time.Millisecond)
	write("/dev/keyboard", "Z")
	fmt.Println("event", event(efd, "KI"))

	// Button 2 on Snarf in the tag sends an x event; written back,
	// acme carries it out.
	name := "/tmp/acmetest.txt "
	sx := x0 + font.Width(name+"Del ") + font.Width("Sn")
	ty := body - 1 - h + h/2
	ptr(draw.Pt(sx, ty), 0)
	ptr(draw.Pt(sx, ty), 2)
	ptr(draw.Pt(sx, ty), 0)
	// A click: the first event has the click's empty range and flags
	// 1 (built in) and 2 (expanded); the second has the expansion.
	ev := event(efd, "Mx")
	fmt.Printf("event %q\n", ev)
	fmt.Printf("event %q\n", event(efd, "Mx"))
	g := strings.Fields(ev[2:])
	syscall.Write(efd, []byte("Mx"+g[0]+" "+g[1]+"\n"))
	if _, err := syscall.Write(efd, []byte("bogus\n")); err == nil {
		fail("bad event accepted")
	}
	fmt.Printf("bad event: %s\n", sys.Errstr())

	// A command run from the body writes to /tmp/+Errors.
	n0 := len([]rune(read(dir + "body")))
	write(dir+"body", "echo from acme\n")
	event(efd, "EI")
	syscall.Write(efd, []byte(fmt.Sprintf("MX%d %d\n", n0, n0+len("echo from acme"))))
	var errs string
	await("+Errors", func() bool {
		for _, l := range strings.Split(read("/mnt/acme/index"), "\n") {
			if g := strings.Fields(l); len(g) > 5 && g[5] == "/tmp/+Errors" {
				errs = read("/mnt/acme/" + g[0] + "/body")
				return errs != ""
			}
		}
		return false
	})
	fmt.Printf("+Errors %q\n", errs)

	// Del through ctl fails while dirty; delete works.
	if e := try(dir+"ctl", "del\n"); e != "" {
		fmt.Printf("del: %s\n", e)
	}
	syscall.Close(efd)
	write(dir+"ctl", "delete\n")
	syscall.Close(fd)
	n := 0
	for _, l := range strings.Split(read("/mnt/acme/index"), "\n") {
		if g := strings.Fields(l); len(g) > 0 && g[0] == id {
			n++
		}
	}
	fmt.Printf("after delete, window %s listed %d times\n", id, n)
	if _, err := os.Stat(dir); err != nil {
		fmt.Printf("stat %s: %s\n", id, sys.Errstr())
	}
	_ = strconv.Itoa
}
