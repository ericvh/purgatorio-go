package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/memdraw"
	"github.com/ericvh/purgatorio-go/platform"
)

// drawPlat is a test platform with a headless screen.
type drawPlat struct {
	testPlat
	scr *platform.Headless
}

func (p *drawPlat) Display() platform.Screen { return p.scr }

func newDrawProg(t *testing.T, w, h int, chans string) (*Prog, *drawPlat, *DevDraw) {
	t.Helper()
	plat := &drawPlat{scr: platform.NewHeadless(w, h, chans)}
	k := &Kernel{Platform: plat, Progs: NewProgs(), Owner: "inferno"}
	root := NewRoot("inferno")
	root.AddDir("dev")
	draw := NewDevDraw(k)
	k.DevTab = NewDevTab(root, NewDevCons(k), draw, NewDevPointer(k))
	rc, err := k.DevTab.Attach(context.Background(), '/', "")
	if err != nil {
		t.Fatal(err)
	}
	p := NewProg(k, NewPgrp(k.DevTab, rc), "inferno")
	if _, err := p.Bind("#i", "/dev", abi.MBEFORE); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Bind("#m", "/dev", abi.MBEFORE); err != nil {
		t.Fatal(err)
	}
	return p, plat, draw
}

// msg builds a draw message from a command byte and fields: int is four
// bytes, int16 two, byte one, string its bytes, rect four ints, point
// two.
func msg(c byte, fields ...any) []byte {
	b := []byte{c}
	for _, f := range fields {
		switch v := f.(type) {
		case int:
			b = binary.LittleEndian.AppendUint32(b, uint32(int32(v)))
		case uint32:
			b = binary.LittleEndian.AppendUint32(b, v)
		case int16:
			b = binary.LittleEndian.AppendUint16(b, uint16(v))
		case byte:
			b = append(b, v)
		case string:
			b = append(b, v...)
		case []byte:
			b = append(b, v...)
		case memdraw.Rectangle:
			for _, x := range []int{v.Min.X, v.Min.Y, v.Max.X, v.Max.Y} {
				b = binary.LittleEndian.AppendUint32(b, uint32(int32(x)))
			}
		case memdraw.Point:
			b = binary.LittleEndian.AppendUint32(b, uint32(int32(v.X)))
			b = binary.LittleEndian.AppendUint32(b, uint32(int32(v.Y)))
		default:
			panic("msg field")
		}
	}
	return b
}

// alloc is the 'b' message for a plain image.
func allocmsg(id int, r memdraw.Rectangle, ch memdraw.Chan, repl bool, color uint32) []byte {
	var rb byte
	clipr := r
	if repl {
		rb = 1
		clipr = memdraw.Rect(-0x3FFFFFFF, -0x3FFFFFFF, 0x3FFFFFFF, 0x3FFFFFFF)
	}
	return msg('b', id, 0, byte(0), uint32(ch), rb, r, clipr, color)
}

type drawConn struct {
	t          *testing.T
	p          *Prog
	ctl, data  int
	id         int
	info       []string
	screenRect memdraw.Rectangle
}

func opendraw(t *testing.T, p *Prog) *drawConn {
	t.Helper()
	ctl, err := p.Open("/dev/draw/new", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 12*12)
	n, err := p.Read(ctl, buf)
	if err != nil || n != 144 {
		t.Fatalf("ctl read %d %v", n, err)
	}
	info := strings.Fields(string(buf[:n]))
	data, err := p.Open("/dev/draw/"+info[0]+"/data", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
	return &drawConn{t: t, p: p, ctl: ctl, data: data, info: info,
		screenRect: memdraw.Rect(atoi(info[4]), atoi(info[5]), atoi(info[6]), atoi(info[7]))}
}

func (c *drawConn) write(b ...[]byte) error {
	_, err := c.p.Write(c.data, bytes.Join(b, nil))
	return err
}

func (c *drawConn) must(b ...[]byte) {
	c.t.Helper()
	if err := c.write(b...); err != nil {
		c.t.Fatalf("draw: %v", err)
	}
}

func (c *drawConn) read(id int, r memdraw.Rectangle, depth int) []byte {
	c.t.Helper()
	c.must(msg('r', id, r))
	buf := make([]byte, memdraw.BytesPerLine(r, depth)*r.Dy())
	n, err := c.p.Read(c.data, buf)
	if err != nil || n != len(buf) {
		c.t.Fatalf("read data: %d %v", n, err)
	}
	return buf
}

func TestDrawInfo(t *testing.T) {
	p, _, _ := newDrawProg(t, 64, 32, "x8r8g8b8")
	c := opendraw(t, p)
	want := []string{"1", "0", "x8r8g8b8", "0", "0", "0", "64", "32", "0", "0", "64", "32"}
	if strings.Join(c.info, " ") != strings.Join(want, " ") {
		t.Errorf("info %q", c.info)
	}
	if got := names(t, p, "/dev/draw"); got != "new 1" {
		t.Errorf("draw dir: %q", got)
	}
	if got := names(t, p, "/dev/draw/1"); got != "colormap ctl data refresh" {
		t.Errorf("client dir: %q", got)
	}
	// Info on another image via ctl.
	c.must(allocmsg(5, memdraw.Rect(1, 2, 3, 4), memdraw.GREY1, true, memdraw.DWhite))
	if _, err := p.Write(c.ctl, binary.LittleEndian.AppendUint32(nil, 5)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 200)
	n, _ := p.Read(c.ctl, buf)
	if got := strings.Fields(string(buf[:n])); got[1] != "5" || got[2] != "k1" || got[3] != "1" || got[4] != "1" {
		t.Errorf("info 5: %q", got)
	}
	if _, err := p.Read(c.ctl, buf); errstr(err) != enodrawimage {
		t.Errorf("second read: %v", err)
	}
	if _, err := p.Read(c.ctl, buf[:10]); errstr(err) != eshortread {
		t.Errorf("short read: %v", err)
	}
	// A second client.
	c2 := opendraw(t, p)
	if c2.info[0] != "2" {
		t.Errorf("second client %q", c2.info)
	}
}

func errstr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestDrawErrors(t *testing.T) {
	p, _, _ := newDrawProg(t, 16, 16, "x8r8g8b8")
	c := opendraw(t, p)
	cases := []struct {
		m    []byte
		want string
	}{
		{[]byte("Q"), "bad draw command"},
		{msg('d', 0, 0), eshortdraw},
		{msg('d', 0, 99, 0, memdraw.Rect(0, 0, 1, 1), memdraw.ZP, memdraw.ZP), enodrawimage},
		{allocmsg(0, memdraw.Rect(0, 0, 1, 1), memdraw.GREY1, false, 0), eimageexists},
		{msg('L', 0, memdraw.ZP, memdraw.Pt(3, 3), 0, 0, -1, 0, memdraw.ZP), "negative line width"},
		{msg('e', 0, 0, memdraw.Pt(3, 3), -1, 2, 0, memdraw.ZP, 0, 0), "invalid ellipse semidiameter"},
		{msg('r', 0, memdraw.Rect(0, 0, 17, 1)), ereadoutside},
		{msg('y', 0, memdraw.Rect(0, 0, 17, 1)), ewriteoutside},
		{msg('i', 0, 10, byte(5)), "cannot use display as font"},
		{msg('n', 7, byte(3), "foo"), enoname},
		{msg('f', 42), enodrawimage},
		{msg('F', 42), enodrawscreen},
		{msg('A', 0, 0, 0, byte(0)), abi.Ebadarg},
	}
	for _, tc := range cases {
		if err := c.write(tc.m); errstr(err) != tc.want {
			t.Errorf("%q: %v want %q", tc.m[0], err, tc.want)
		}
	}
	if _, err := p.Write(c.ctl, []byte("xx")); errstr(err) != "unknown draw control request" {
		t.Errorf("ctl: %v", err)
	}
	if _, err := p.Read(c.data, make([]byte, 10)); errstr(err) != "no draw data" {
		t.Errorf("read data: %v", err)
	}
}

func TestDrawPixels(t *testing.T) {
	p, plat, dev := newDrawProg(t, 16, 8, "x8r8g8b8")
	c := opendraw(t, p)
	scr := c.screenRect
	// Colors, an image with a pattern, and drawing onto the screen.
	c.must(
		allocmsg(9, memdraw.Rect(0, 0, 1, 1), memdraw.GREY1, true, memdraw.DOpaque),
		allocmsg(1, memdraw.Rect(0, 0, 1, 1), memdraw.RGB24, true, memdraw.DBlue),
		allocmsg(2, memdraw.Rect(0, 0, 4, 2), memdraw.GREY8, false, memdraw.DBlack),
		msg('y', 2, memdraw.Rect(0, 0, 4, 2), []byte{0, 0x40, 0x80, 0xFF, 0xFF, 0x80, 0x40, 0}),
		msg('d', 0, 1, 9, scr, memdraw.ZP, memdraw.ZP), // fill with blue
		msg('d', 0, 2, 9, memdraw.Rect(2, 2, 6, 4), memdraw.ZP, memdraw.ZP),
		msg('L', 0, memdraw.Pt(0, 7), memdraw.Pt(15, 7), 0, 0, 0, 1, memdraw.ZP),
		msg('v'),
	)
	got := c.read(0, memdraw.Rect(2, 2, 6, 3), 32)
	want := []byte{0, 0, 0, 0, 0x40, 0x40, 0x40, 0, 0x80, 0x80, 0x80, 0, 0xFF, 0xFF, 0xFF, 0}
	if !bytes.Equal(got, want) {
		t.Errorf("grey ramp on screen: % x", got)
	}
	if got := c.read(0, memdraw.Rect(0, 0, 1, 1), 32); !bytes.Equal(got, []byte{0xFF, 0, 0, 0}) {
		t.Errorf("blue: % x", got)
	}
	// The flush reached the platform screen.
	pix, stride, flushes := plat.scr.Snapshot()
	if flushes == 0 || stride != 64 || !bytes.Equal(pix[7*64:7*64+4], []byte{0xFF, 0, 0, 0}) ||
		!bytes.Equal(pix[2*64+3*4:2*64+4*4], []byte{0x40, 0x40, 0x40, 0}) {
		t.Errorf("flushed screen: %d flushes, stride %d, % x", flushes, stride, pix[2*64:2*64+16])
	}
	if dev.Screen().R != scr {
		t.Errorf("screen rect %v", dev.Screen().R)
	}
	// The operator applies to the next draw only.
	c.must(
		allocmsg(3, memdraw.Rect(0, 0, 1, 1), memdraw.RGBA32, true, 0x80000080),
		msg('O', byte(memdraw.S)),
		msg('d', 0, 3, 9, memdraw.Rect(0, 0, 1, 1), memdraw.ZP, memdraw.ZP),
		msg('d', 0, 3, 9, memdraw.Rect(1, 0, 2, 1), memdraw.ZP, memdraw.ZP),
	)
	if got := c.read(0, memdraw.Rect(0, 0, 2, 1), 32); !bytes.Equal(got, []byte{0, 0, 0x80, 0, 0x7F, 0, 0x80, 0}) {
		t.Errorf("op S then SoverD: % x", got)
	}
}

func TestDrawPolygonEllipseString(t *testing.T) {
	p, _, _ := newDrawProg(t, 16, 16, "k8")
	c := opendraw(t, p)
	c.must(allocmsg(1, memdraw.Rect(0, 0, 1, 1), memdraw.GREY1, true, memdraw.DWhite))
	// A filled square through 'P'. The first point is in the long form,
	// the rest are short deltas.
	pts := []memdraw.Point{memdraw.Pt(2, 2), memdraw.Pt(6, 2), memdraw.Pt(6, 6), memdraw.Pt(2, 6)}
	// Encode all points with the long form, absolute.
	var abs []byte
	prev := memdraw.ZP
	for i, q := range pts {
		for j, v := range []int{q.X, q.Y} {
			if i == 0 {
				abs = append(abs, 0x80|byte(v&0x7F), byte(v>>7), byte(v>>15))
				continue
			}
			dv := v - prev.X
			if j == 1 {
				dv = v - prev.Y
			}
			abs = append(abs, byte(dv&0x7F))
		}
		prev = q
	}
	c.must(msg('P', 0, int16(len(pts)-1), ^0, 0, 0, 1, memdraw.ZP), abs)
	img := c.read(0, memdraw.Rect(0, 0, 8, 8), 8)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			in := x >= 2 && x < 6 && y >= 2 && y < 6
			if (img[y*8+x] == 0xFF) != in {
				t.Fatalf("polygon pixel %d,%d = %x", x, y, img[y*8+x])
			}
		}
	}
	// A filled ellipse 'E' at (11,11), radius 2: its centre row spans 5.
	c.must(msg('E', 0, 1, memdraw.Pt(11, 11), 2, 2, 0, memdraw.ZP, 0, 0))
	row := c.read(0, memdraw.Rect(8, 11, 15, 12), 8)
	if !bytes.Equal(row, []byte{0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0}) {
		t.Errorf("ellipse row % x", row)
	}
	// A font of one character, then a string.
	c.must(
		allocmsg(2, memdraw.Rect(0, 0, 3, 3), memdraw.GREY1, false, memdraw.DBlack),
		msg('y', 2, memdraw.Rect(0, 0, 3, 3), []byte{0xA0, 0x40, 0xA0}), // an X
		msg('i', 2, 2, byte(2)),
		msg('l', 2, 2, int16(1), memdraw.Rect(0, 0, 3, 3), memdraw.ZP, byte(0), byte(4)),
		allocmsg(3, memdraw.Rect(0, 0, 16, 3), memdraw.GREY8, false, memdraw.DBlack),
		msg('s', 3, 1, 2, memdraw.Pt(0, 2), memdraw.Rect(0, 0, 16, 3), memdraw.ZP, int16(2), int16(1), int16(1)),
	)
	got := c.read(3, memdraw.Rect(0, 0, 8, 3), 8)
	want := []byte{
		0xFF, 0, 0xFF, 0, 0xFF, 0, 0xFF, 0,
		0, 0xFF, 0, 0, 0, 0xFF, 0, 0,
		0xFF, 0, 0xFF, 0, 0xFF, 0, 0xFF, 0,
	}
	if !bytes.Equal(got, want) {
		t.Errorf("string:\n% x\nwant\n% x", got, want)
	}
	if err := c.write(msg('s', 3, 1, 2, memdraw.Pt(0, 2), memdraw.Rect(0, 0, 16, 3), memdraw.ZP, int16(1), int16(9))); errstr(err) != eindex {
		t.Errorf("bad index: %v", err)
	}
}

func TestDrawWindows(t *testing.T) {
	p, _, _ := newDrawProg(t, 32, 16, "k8")
	c := opendraw(t, p)
	c.must(
		allocmsg(1, memdraw.Rect(0, 0, 1, 1), memdraw.GREY8, true, 0x202020FF),
		msg('A', 10, 0, 1, byte(1)), // public screen on the display, filled grey
		msg('b', 2, 10, byte(refbackup), uint32(memdraw.GREY8), byte(0), memdraw.Rect(2, 2, 12, 10), memdraw.Rect(2, 2, 12, 10), uint32(0x808080FF)),
		msg('b', 3, 10, byte(refbackup), uint32(memdraw.GREY8), byte(0), memdraw.Rect(8, 4, 20, 14), memdraw.Rect(8, 4, 20, 14), uint32(0xC0C0C0FF)),
	)
	at := func(x, y int) byte { return c.read(0, memdraw.Rect(x, y, x+1, y+1), 8)[0] }
	if at(3, 3) != 0x80 || at(9, 5) != 0xC0 {
		t.Fatalf("windows: %x %x", at(3, 3), at(9, 5))
	}
	// Window 2 to the front, then name it and look it up from another
	// client.
	c.must(msg('t', byte(1), int16(1), 2))
	if at(9, 5) != 0x80 {
		t.Errorf("to front: %x", at(9, 5))
	}
	c.must(msg('N', 2, byte(1), byte(3), "win"))
	c2 := opendraw(t, p)
	c2.must(msg('n', 7, byte(3), "win"))
	buf := make([]byte, 200)
	n, _ := p.Read(c2.ctl, buf)
	if f := strings.Fields(string(buf[:n])); f[1] != "7" || f[4] != "2" || f[6] != "12" {
		t.Errorf("named image info: %q", f)
	}
	if err := c2.write(msg('N', 7, byte(1), byte(1), "x")); errstr(err) != enamed {
		t.Errorf("rename: %v", err)
	}
	// The other client draws into the window through the name.
	c2.must(
		allocmsg(1, memdraw.Rect(0, 0, 1, 1), memdraw.GREY8, true, 0x101010FF),
		allocmsg(9, memdraw.Rect(0, 0, 1, 1), memdraw.GREY1, true, memdraw.DOpaque),
		msg('d', 7, 1, 9, memdraw.Rect(2, 2, 4, 4), memdraw.ZP, memdraw.ZP),
	)
	if at(3, 3) != 0x10 {
		t.Errorf("draw through name: %x", at(3, 3))
	}
	// Using the public screen from the second client.
	c2.must(msg('S', 10, uint32(memdraw.GREY8)))
	if err := c2.write(msg('S', 10, uint32(memdraw.RGB24))); errstr(err) != "inconsistent chan" {
		t.Errorf("S chan: %v", err)
	}
	// Move window 3; free window 2: the screen fill shows through.
	c.must(msg('o', 3, memdraw.Pt(8, 4), memdraw.Pt(20, 0)))
	if at(21, 1) != 0xC0 || at(13, 9) != 0x20 {
		t.Errorf("moved: %x %x", at(21, 1), at(13, 9))
	}
	// Window 2 goes when both clients have freed it.
	c.must(msg('f', 2))
	if at(3, 3) != 0x10 {
		t.Errorf("freed by one: %x", at(3, 3))
	}
	c2.must(msg('f', 7))
	if at(3, 3) != 0x20 {
		t.Errorf("freed: %x", at(3, 3))
	}
}

func TestDrawRefresh(t *testing.T) {
	p, _, _ := newDrawProg(t, 32, 16, "k8")
	c := opendraw(t, p)
	ref, err := p.Open("/dev/draw/"+c.info[0]+"/refresh", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	c.must(
		allocmsg(1, memdraw.Rect(0, 0, 1, 1), memdraw.GREY8, true, 0x202020FF),
		msg('A', 10, 0, 1, byte(0)),
		msg('b', 2, 10, byte(refmesg), uint32(memdraw.GREY8), byte(0), memdraw.Rect(2, 2, 12, 10), memdraw.Rect(2, 2, 12, 10), uint32(0x808080FF)),
		msg('b', 3, 10, byte(refbackup), uint32(memdraw.GREY8), byte(0), memdraw.Rect(8, 4, 20, 14), memdraw.Rect(8, 4, 20, 14), uint32(0xC0C0C0FF)),
		msg('t', byte(1), int16(1), 2), // window 2 to the front: needs a repaint
	)
	done := make(chan []byte)
	go func() {
		buf := make([]byte, 100)
		n, _ := p.Read(ref, buf)
		done <- buf[:n]
	}()
	select {
	case b := <-done:
		if len(b) != 20 || bglong(b) != 2 || bgrect(b[4:]) != memdraw.Rect(8, 4, 12, 10) {
			t.Errorf("refresh % x", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh read blocked")
	}
}

func TestDrawClose(t *testing.T) {
	p, _, _ := newDrawProg(t, 32, 16, "k8")
	c := opendraw(t, p)
	c.must(
		allocmsg(1, memdraw.Rect(0, 0, 1, 1), memdraw.GREY8, true, 0x202020FF),
		msg('A', 10, 0, 1, byte(0)),
		msg('b', 2, 10, byte(refbackup), uint32(memdraw.GREY8), byte(0), memdraw.Rect(2, 2, 12, 10), memdraw.Rect(2, 2, 12, 10), uint32(0x808080FF)),
	)
	c2 := opendraw(t, p)
	p.Close(c.data)
	p.Close(c.ctl)
	// The window is gone and the fill painted where it was.
	if got := c2.read(0, memdraw.Rect(3, 3, 4, 4), 8); got[0] != 0x20 {
		t.Errorf("after close: %x", got[0])
	}
	if got := names(t, p, "/dev/draw"); got != "new 2" {
		t.Errorf("clients after close: %q", got)
	}
}

func TestPointer(t *testing.T) {
	p, plat, _ := newDrawProg(t, 32, 16, "k8")
	fd, err := p.Open("/dev/pointer", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Open("/dev/pointer", abi.OREAD); errstr(err) != abi.Einuse {
		t.Errorf("second open: %v", err)
	}
	plat.scr.Track(platform.PointerEvent{X: 3, Y: 4, Buttons: 1})
	buf := make([]byte, 100)
	n, err := p.Read(fd, buf)
	if err != nil || n != 49 {
		t.Fatalf("read %d %v", n, err)
	}
	f := strings.Fields(string(buf[:n]))
	if f[0] != "m" || f[1] != "3" || f[2] != "4" || f[3] != "1" {
		t.Errorf("pointer %q", buf[:n])
	}
	// Writing moves the pointer, which the headless screen reports.
	if _, err := p.Write(fd, []byte("m 10 12")); err != nil {
		t.Fatal(err)
	}
	n, _ = p.Read(fd, buf)
	if f := strings.Fields(string(buf[:n])); f[1] != "10" || f[2] != "12" || f[3] != "1" {
		t.Errorf("after write %q", buf[:n])
	}
	cfd, err := p.Open("/dev/cursor", abi.OWRITE)
	if err != nil {
		t.Fatal(err)
	}
	cur := msg(0, 0, 0, 16, 16, make([]byte, 2*16))[1:]
	if _, err := p.Write(cfd, cur); err != nil {
		t.Errorf("cursor: %v", err)
	}
	if _, err := p.Write(cfd, make([]byte, 20)); errstr(err) != abi.Ebadarg {
		t.Errorf("bad cursor: %v", err)
	}
	p.Close(fd)
	if fd, err = p.Open("/dev/pointer", abi.OREAD); err != nil {
		t.Errorf("reopen: %v", err)
	}
}

func TestNoFrameBuffer(t *testing.T) {
	k := &Kernel{Platform: &testPlat{}, Progs: NewProgs(), Owner: "inferno"}
	root := NewRoot("inferno")
	root.AddDir("a")
	k.DevTab = NewDevTab(root, NewDevDraw(k))
	rc, err := k.DevTab.Attach(context.Background(), '/', "")
	if err != nil {
		t.Fatal(err)
	}
	p := NewProg(k, NewPgrp(k.DevTab, rc), "inferno")
	if _, err := p.Bind("#i", "/a", abi.MREPL); err == nil || err.Error() != enofb {
		t.Errorf("attach without a screen: %v", err)
	}
}

// TestDrawResize changes the screen's size under a public screen with
// two windows, one partly off the screen: the device keeps the pixels
// and windows, fills what is new with the screen's fill, restores the
// part of a window that comes back onto the screen from its save area,
// and gives each client a refresh record for image 0 and the new
// rectangle, which a read of ctl for image 0 also describes.
func TestDrawResize(t *testing.T) {
	p, plat, d := newDrawProg(t, 32, 16, "k8")
	c := opendraw(t, p)
	ref, err := p.Open("/dev/draw/"+c.info[0]+"/refresh", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	c.must(
		allocmsg(1, memdraw.Rect(0, 0, 1, 1), memdraw.GREY8, true, 0x202020FF),
		msg('A', 10, 0, 1, byte(1)),
		msg('b', 2, 10, byte(refbackup), uint32(memdraw.GREY8), byte(0), memdraw.Rect(2, 2, 12, 10), memdraw.Rect(2, 2, 12, 10), uint32(0x808080FF)),
		msg('b', 3, 10, byte(refbackup), uint32(memdraw.GREY8), byte(0), memdraw.Rect(20, 4, 40, 14), memdraw.Rect(20, 4, 40, 14), uint32(0xC0C0C0FF)),
	)
	resized := func(w, h int) []byte {
		t.Helper()
		plat.scr.Resize(w, h)
		done := make(chan []byte)
		go func() {
			buf := make([]byte, 100)
			n, _ := p.Read(ref, buf)
			done <- buf[:n]
		}()
		select {
		case b := <-done:
			return b
		case <-time.After(5 * time.Second):
			t.Fatal("refresh read blocked")
			return nil
		}
	}
	if b := resized(48, 20); len(b) != 20 || bglong(b) != 0 || bgrect(b[4:]) != memdraw.Rect(0, 0, 48, 20) {
		t.Fatalf("refresh % x", b)
	}
	if r := d.Screen().R; r != memdraw.Rect(0, 0, 48, 20) {
		t.Fatalf("screen %v", r)
	}
	at := func(x, y int) byte { return c.read(0, memdraw.Rect(x, y, x+1, y+1), 8)[0] }
	for _, k := range []struct {
		x, y int
		want byte
	}{{3, 3, 0x80}, {25, 5, 0xC0}, {35, 10, 0xC0}, {44, 17, 0x20}, {1, 18, 0x20}} {
		if v := at(k.x, k.y); v != k.want {
			t.Errorf("after growing, (%d,%d) = %x, want %x", k.x, k.y, v, k.want)
		}
	}
	// Draw into window 3 where it is off the screen, shrink the screen,
	// draw into it again, and grow: what was drawn while it was off the
	// screen shows.
	c.must(
		allocmsg(4, memdraw.Rect(0, 0, 1, 1), memdraw.GREY8, true, 0x404040FF),
		allocmsg(9, memdraw.Rect(0, 0, 1, 1), memdraw.GREY1, true, memdraw.DOpaque),
		msg('d', 3, 4, 9, memdraw.Rect(30, 4, 40, 14), memdraw.ZP, memdraw.ZP),
	)
	if b := resized(24, 12); bgrect(b[4:]) != memdraw.Rect(0, 0, 24, 12) {
		t.Fatalf("refresh % x", b)
	}
	c.must(
		allocmsg(5, memdraw.Rect(0, 0, 1, 1), memdraw.GREY8, true, 0x606060FF),
		msg('d', 3, 5, 9, memdraw.Rect(20, 10, 40, 14), memdraw.ZP, memdraw.ZP),
	)
	resized(48, 20)
	for _, k := range []struct {
		x, y int
		want byte
	}{{3, 3, 0x80}, {21, 5, 0xC0}, {35, 5, 0x40}, {25, 12, 0x60}, {35, 12, 0x60}, {44, 17, 0x20}} {
		if v := at(k.x, k.y); v != k.want {
			t.Errorf("after shrinking and growing, (%d,%d) = %x, want %x", k.x, k.y, v, k.want)
		}
	}
	// ctl describes the screen at its new size.
	b := binary.LittleEndian.AppendUint32(nil, 0)
	if _, err := p.Write(c.ctl, b); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 144)
	n, _ := p.Read(c.ctl, buf)
	if f := strings.Fields(string(buf[:n])); len(f) < 12 || f[6] != "48" || f[7] != "20" {
		t.Errorf("ctl %q", f)
	}
	// The headless screen got the whole new screen.
	pix, stride, _ := plat.scr.Snapshot()
	if stride != 48 || len(pix) != 48*20 || pix[17*48+44] != 0x20 {
		t.Errorf("flushed screen: stride %d, %d bytes", stride, len(pix))
	}
}
