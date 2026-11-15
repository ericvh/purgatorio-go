// Command acmewin checks win and B against acme. Run it with acme (and
// for B the plumber) running in the same name space. It starts win,
// finds its window through /mnt/acme, types a command into the body
// through #c and #m, sends text with Send, deletes the window with
// Delete, and then sends a file to acme with B. It prints what it
// finds.
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

func fail(f string, a ...any) {
	fmt.Printf("fail: "+f+"\n", a...)
	sys.Exits("fail:acmewin")
}

func write(name, s string) {
	fd, err := sys.Open(name, sys.OWRITE)
	if err != nil {
		fail("open %s: %v", name, err)
	}
	defer syscall.Close(fd)
	if _, err := syscall.Write(fd, []byte(s)); err != nil {
		fail("write %s %q: %s", name, s, sys.Errstr())
	}
}

func read(name string) string {
	b, err := os.ReadFile(name)
	if err != nil {
		fail("read %s: %v", name, err)
	}
	return string(b)
}

func await(what string, f func() bool) {
	for i := 0; i < 300; i++ {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	fail("%s: timed out", what)
}

func ptr(p draw.Point, b int) {
	write("/dev/pointer", fmt.Sprintf("m%d %d %d", p.X, p.Y, b))
	time.Sleep(50 * time.Millisecond)
}

func px(d *draw.Display, p draw.Point) uint32 {
	b, err := d.Image.Unload(draw.Rect(p.X, p.Y, p.X+1, p.Y+1))
	if err != nil {
		fail("unload: %v", err)
	}
	return uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
}

// window returns the number of the window whose name ends in suffix,
// or "".
func window(suffix string) string {
	for _, l := range strings.Split(read("/mnt/acme/index"), "\n") {
		if f := strings.Fields(l); len(f) > 5 && strings.HasSuffix(f[5], suffix) {
			return f[0]
		}
	}
	return ""
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
	sysname := strings.TrimSpace(read("/dev/sysname"))
	if _, err := sys.Spawn("/bin/win.wasm", []string{"win"}, sys.NEWFD,
		[][2]int{{0, 0}, {1, 1}, {2, 2}}); err != nil {
		fail("spawn win: %v", err)
	}
	var id string
	await("win's window", func() bool { id = window("/-" + sysname); return id != "" })
	dir := "/mnt/acme/" + id + "/"
	body := func() string { return read(dir + "body") }
	await("prompt", func() bool { return body() == "; " })
	fmt.Printf("body %q\n", body())
	tag := read(dir + "tag")
	fmt.Printf("tag %q\n", strings.Replace(tag, sysname, "SYSNAME", 1))
	fmt.Printf("acmewin %v\n", strings.TrimSpace(read("/env/acmewin")) == id)

	// The window is alone in the second column of a 640-wide screen:
	// its body starts at the first pale yellow down the column.
	h := font.Height
	x0 := 320 + 16
	top := -1
	for y := 0; y < 200; y++ {
		if px(d, draw.Pt(x0+100, y)) == 0xFFFFEA {
			top = y
			break
		}
	}
	if top < 0 {
		fail("no body on the screen")
	}
	// Button 1 below the text puts the cursor at the end; then type.
	p := draw.Pt(x0+100, top+5*h)
	ptr(p, 0)
	ptr(p, 1)
	ptr(p, 0)
	write("/dev/keyboard", "echo hello\n")
	await("echo", func() bool { return strings.HasSuffix(body(), "hello\n; ") })
	fmt.Printf("body %q\n", body())

	// Send puts the snarf buffer on the end of the body, with a
	// newline, and win sends it to the shell.
	write("#^/snarf", "echo sent")
	click := func(word string) {
		tag := read(dir + "tag")
		i := strings.Index(tag, word)
		if i < 0 {
			fail("no %s in tag %q", word, tag)
		}
		x := x0 + font.Width(tag[:i]) + font.Width(word)/2
		y := top - 1 - h + h/2
		ptr(draw.Pt(x, y), 0)
		ptr(draw.Pt(x, y), 2)
		ptr(draw.Pt(x, y), 0)
	}
	click("Send")
	await("Send", func() bool { return strings.HasSuffix(body(), "echo sent\nsent\n; ") })
	fmt.Printf("body %q\n", body())

	// Delete removes the window; win kills the shell and exits.
	click("Delete")
	await("delete", func() bool { return window("/-"+sysname) == "" })
	fmt.Println("window deleted")

	// B sends a file to acme through the plumber's edit port.
	if _, err := os.Stat("/chan/plumb.edit"); err != nil {
		return
	}
	pid, err := sys.Spawn("/bin/B.wasm", []string{"B", "-2", "/lib/plumbing"}, sys.NEWFD,
		[][2]int{{0, 0}, {1, 1}, {2, 2}})
	if err != nil {
		fail("spawn B: %v", err)
	}
	_ = pid
	var bid string
	await("B", func() bool { bid = window("/lib/plumbing"); return bid != "" })
	bdir := "/mnt/acme/" + bid + "/"
	afd, err := sys.Open(bdir+"addr", sys.ORDWR)
	if err != nil {
		fail("open addr: %v", err)
	}
	write(bdir+"ctl", "addr=dot\n")
	b := make([]byte, 64)
	n, _ := syscall.Read(afd, b)
	syscall.Close(afd)
	line1 := strings.SplitAfter(read("/lib/plumbing"), "\n")[0]
	f := strings.Fields(string(b[:max(n, 0)]))
	fmt.Printf("B opened /lib/plumbing; dot %s %s, line 2 starts at %d\n", f[0], f[1], len([]rune(line1)))
}
