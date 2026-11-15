package draw_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// plat is a platform with a headless screen.
type plat struct{ scr *platform.Headless }

func (*plat) Now() int64                 { return 0 }
func (*plat) Entropy([]byte)             {}
func (*plat) Console() platform.Console  { return nil }
func (*plat) User() string               { return "inferno" }
func (*plat) Sysname() string            { return "test" }
func (p *plat) Display() platform.Screen { return p.scr }

// fdrw reads and writes a prog's file descriptor.
type fdrw struct {
	p  *core.Prog
	fd int
}

func (f fdrw) Read(b []byte) (int, error)  { return f.p.Read(f.fd, b) }
func (f fdrw) Write(b []byte) (int, error) { return f.p.Write(f.fd, b) }

// display connects a Display to a draw device in a kernel of its own.
func display(t *testing.T, w, h int, chans string) *draw.Display {
	t.Helper()
	k := &core.Kernel{Platform: &plat{platform.NewHeadless(w, h, chans)}, Progs: core.NewProgs(), Owner: "inferno"}
	root := core.NewRoot("inferno")
	root.AddDir("dev")
	k.DevTab = core.NewDevTab(root, core.NewDevDraw(k))
	rc, err := k.DevTab.Attach(context.Background(), '/', "")
	if err != nil {
		t.Fatal(err)
	}
	p := core.NewProg(k, core.NewPgrp(k.DevTab, rc), "inferno")
	if _, err := p.Bind("#i", "/dev", abi.MBEFORE); err != nil {
		t.Fatal(err)
	}
	ctl, err := p.Open("/dev/draw/new", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	info := make([]byte, 144)
	if _, err := p.Read(ctl, info); err != nil {
		t.Fatal(err)
	}
	data, err := p.Open("/dev/draw/1/data", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	d, err := draw.NewDisplay(fdrw{p, ctl}, fdrw{p, data}, info)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDisplay(t *testing.T) {
	d := display(t, 40, 20, "x8r8g8b8")
	if d.Image.R != draw.Rect(0, 0, 40, 20) || d.Chan != draw.XRGB32 {
		t.Fatalf("screen %v %v", d.Image.R, d.Chan)
	}
	scr := d.Image
	red, err := d.Color(draw.Red)
	if err != nil {
		t.Fatal(err)
	}
	if err := scr.Draw(scr.R, d.White, nil, draw.ZP); err != nil {
		t.Fatal(err)
	}
	scr.Line(draw.Pt(0, 0), draw.Pt(9, 0), draw.Endsquare, draw.Endsquare, 0, red, draw.ZP)
	scr.Border(draw.Rect(10, 10, 14, 14), 1, d.Black, draw.ZP)
	if err := d.Flush(true); err != nil {
		t.Fatal(err)
	}
	b, err := scr.Unload(draw.Rect(9, 0, 11, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, []byte{0, 0, 0xFF, 0, 0xFF, 0xFF, 0xFF, 0}) {
		t.Errorf("line end: % x", b)
	}
	b, _ = scr.Unload(draw.Rect(10, 11, 13, 12))
	if !bytes.Equal(b, []byte{0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0, 0xFF, 0xFF, 0xFF, 0}) {
		t.Errorf("border: % x", b)
	}
}

func TestImages(t *testing.T) {
	d := display(t, 20, 20, "k8")
	img, err := d.AllocImage(draw.Rect(0, 0, 3, 2), draw.GREY8, false, draw.Black)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := img.Load(img.R, []byte{1, 2, 3, 4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	d.Image.Draw(draw.Rect(5, 5, 8, 7), img, nil, draw.ZP)
	b, err := d.Image.Unload(draw.Rect(5, 5, 8, 7))
	if err != nil || !bytes.Equal(b, []byte{1, 2, 3, 4, 5, 6}) {
		t.Errorf("image: % x %v", b, err)
	}
	// A large load is split across messages.
	big, _ := d.AllocImage(draw.Rect(0, 0, 200, 100), draw.GREY8, false, draw.Black)
	data := make([]byte, 200*100)
	for k := range data {
		data[k] = byte(k)
	}
	if _, err := big.Load(big.R, data); err != nil {
		t.Fatal(err)
	}
	got, err := big.Unload(big.R)
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("big image: %v", err)
	}
}

func TestString(t *testing.T) {
	d := display(t, 40, 10, "k8")
	f, err := d.GetDefaultFont()
	if err != nil {
		t.Fatal(err)
	}
	if f.Width("Hi") != 10 || f.Height != 8 {
		t.Errorf("width %d height %d", f.Width("Hi"), f.Height)
	}
	d.Image.Draw(d.Image.R, d.Black, nil, draw.ZP)
	p, err := d.Image.String(draw.Pt(1, 1), d.White, draw.ZP, f, "Hi")
	if err != nil || p != draw.Pt(11, 1) {
		t.Fatalf("string end %v %v", p, err)
	}
	d.Flush(true)
	// 'H' has a full-height left stem in column 1, rows 2 to 7.
	b, _ := d.Image.Unload(draw.Rect(1, 0, 2, 10))
	if !bytes.Equal(b, []byte{0, 0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0, 0}) {
		t.Errorf("H stem % x", b)
	}
	// A font file whose subfont cannot be read falls back to the default.
	ff, err := d.BuildFont("8 7\n0 0x7F nonexistent.subfont\n", "/fonts/x.font")
	if err != nil {
		t.Fatal(err)
	}
	if ff.Width("ab") != 10 {
		t.Errorf("fallback width %d", ff.Width("ab"))
	}
}

func TestWindows(t *testing.T) {
	d := display(t, 40, 20, "k8")
	grey, _ := d.Color(0x404040FF)
	s, err := d.Image.AllocScreen(grey, false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.NewWindow(draw.Rect(5, 5, 15, 15), draw.Refbackup, draw.White)
	if err != nil {
		t.Fatal(err)
	}
	w.Draw(draw.Rect(5, 5, 7, 7), d.Black, nil, draw.ZP)
	if err := w.Origin(draw.Pt(0, 0), draw.Pt(20, 2)); err != nil {
		t.Fatal(err)
	}
	b, _ := d.Image.Unload(draw.Rect(20, 2, 23, 3))
	if !bytes.Equal(b, []byte{0, 0, 0xFF}) {
		t.Errorf("moved window: % x", b)
	}
	if w.R != draw.Rect(0, 0, 10, 10) {
		t.Errorf("window r %v", w.R)
	}
}
