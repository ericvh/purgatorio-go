package tk

import (
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// TestScale sets a scale, clicks in its trough on each side of the
// slider and drags the slider, checking the values its command gets.
func TestScale(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	cmd := make(chan string, 8)
	NameChan(top, cmd, "cmd")
	mustCmd(t, top, "scale .s -from 0 -to 10 -orient horizontal -showvalue 0 -command {send cmd v}")
	mustCmd(t, top, ".s set 3; pack .s; update")
	if v := mustCmd(t, top, ".s get"); v != "3" {
		t.Errorf("get %q", v)
	}
	if v := mustCmd(t, top, ".s set 40; .s get"); v != "10" {
		t.Errorf("set past the end gives %q", v)
	}
	mustCmd(t, top, ".s set 5; update")
	r := top.Rect(".s", 0)
	y := (r.Min.Y + r.Max.Y) / 2
	click(top, draw.Pt(r.Min.X+3, y), 0)
	if s := <-cmd; s != "v 4" {
		t.Errorf("trough1 sent %q", s)
	}
	click(top, draw.Pt(r.Max.X-3, y), 10)
	if s := <-cmd; s != "v 5" {
		t.Errorf("trough2 sent %q", s)
	}
	// Drag the slider past the right-hand end.
	p := center(top, ".s")
	top.Pointer(draw.Pointer{XY: p, Msec: 20})
	top.Pointer(draw.Pointer{Buttons: 1, XY: p, Msec: 21})
	top.Pointer(draw.Pointer{Buttons: 1, XY: draw.Pt(r.Max.X+50, y), Msec: 22})
	top.Pointer(draw.Pointer{XY: draw.Pt(r.Max.X+50, y), Msec: 23})
	var last string
	for len(cmd) > 0 {
		last = <-cmd
	}
	if last != "v 10" {
		t.Errorf("drag sent %q", last)
	}
	if v := mustCmd(t, top, ".s cget -orient"); v != "horizontal" {
		t.Errorf("orient %q", v)
	}
}

// TestPanel puts an image in a panel and checks it is drawn.
func TestPanel(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	mustCmd(t, top, "panel .p -bd 0")
	red, err := top.Display.AllocImage(draw.Rect(0, 0, 30, 20), top.Display.Image.Chan, false, 0xFF0000FF)
	if err != nil {
		t.Fatal(err)
	}
	if e := PutImage(top, ".p", red, nil); e != "" {
		t.Fatal(e)
	}
	mustCmd(t, top, "pack .p; update")
	if r := top.Rect(".p", 0); r.Dx() != 30 || r.Dy() != 20 {
		t.Errorf("panel %v", r)
	}
	if c := pixel(t, top, center(top, ".p")); c != 0xFF0000 {
		t.Errorf("panel pixel %06x", c)
	}
	mustCmd(t, top, ".p dirty; update")
}
