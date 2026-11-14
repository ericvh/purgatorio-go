package tk

import (
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

func TestGrid(t *testing.T) {
	top := toplevel(t, "")
	mustCmd(t, top, "frame .a -width 40 -height 20; frame .b -width 10 -height 30; frame .c -width 25 -height 10")
	mustCmd(t, top, "frame .d -width 60 -height 5")
	// Two in the first row, .c alone, .d spanning both columns.
	mustCmd(t, top, "grid .a .b; grid .c -sticky w; grid .d -")
	if r := top.Rect(".", Border|Required); r.Dx() != 60 || r.Dy() != 45 {
		t.Errorf(". wants %v", r)
	}
	// Columns are 40+10 wide, widened by the span to 60: 5 each.
	for _, c := range []struct {
		n string
		r draw.Rectangle
	}{
		{".a", draw.Rect(2, 5, 42, 25)}, {".b", draw.Rect(47, 0, 57, 30)},
		{".c", draw.Rect(0, 30, 25, 40)}, {".d", draw.Rect(0, 40, 60, 45)},
	} {
		if r := top.Rect(c.n, Border|Local); r != c.r {
			t.Errorf("%s at %v, want %v", c.n, r, c.r)
		}
	}
	if v := mustCmd(t, top, "grid size ."); v != "2 3" {
		t.Errorf("size %q", v)
	}
	if v := mustCmd(t, top, "grid info .d"); v != "-in . -column 0 -row 2 -columnspan 2 -rowspan 1" {
		t.Errorf("info %q", v)
	}
	if v := mustCmd(t, top, "grid slaves ."); v != ".d .c .b .a" {
		t.Errorf("slaves %q", v)
	}
	if v := mustCmd(t, top, "grid slaves . -row 0"); v != ".a .b" {
		t.Errorf("slaves -row %q", v)
	}
	if v := mustCmd(t, top, "grid bbox . 1 0"); v != "45 0 60 30" {
		t.Errorf("bbox %q", v)
	}
	if v := mustCmd(t, top, "grid location . 50 35"); v != "1 1" {
		t.Errorf("location %q", v)
	}
	if v := mustCmd(t, top, "grid cellinfo . 1 2"); v != ".d -in . -column 0 -row 2 -columnspan 2 -rowspan 1" {
		t.Errorf("cellinfo %q", v)
	}
	for _, c := range []struct{ cmd, want string }{
		{"frame .e; grid .e -row 0 -column 0", "!grid cell in use"},
		{"frame .f; pack .f", "!cannot use pack inside a grid"},
		{"grid .nosuch", "!bad window path .nosuch"},
		{"grid ^ .e", "!bad grid span"},
		{"grid .e -sticky q", "!bad value q"},
		{"grid wobble .", "!bad command wobble"},
		{"grid .e -row nowhere", "!bad index"},
	} {
		if got := top.Cmd(c.cmd); got != c.want {
			t.Errorf("%q: got %q, want %q", c.cmd, got, c.want)
		}
	}
	mustCmd(t, top, "grid forget .b")
	if v := mustCmd(t, top, "grid slaves . -row 0"); v != ".a" {
		t.Errorf("after forget %q", v)
	}
}

func TestGridWeights(t *testing.T) {
	top := toplevel(t, "")
	mustCmd(t, top, ". configure -width 200 -height 100; pack propagate . 0")
	mustCmd(t, top, "frame .f; pack .f -fill both -expand 1")
	mustCmd(t, top, "frame .f.a -width 20 -height 20; frame .f.b -width 20 -height 20; frame .f.c -width 20 -height 20")
	mustCmd(t, top, "grid .f.a .f.b -sticky nsew; grid .f.c ^ -sticky nsew")
	mustCmd(t, top, "grid columnconfigure .f 1 -weight 1; grid rowconfigure .f {0 1} -weight 1 -pad 4")
	// Column 0 keeps 20; column 1 takes the other 180. Rows share
	// 100 less padding (4+4+4) equally; .f.b spans both.
	for _, c := range []struct {
		n string
		r draw.Rectangle
	}{
		{".f.a", draw.Rect(0, 4, 20, 48)}, {".f.b", draw.Rect(20, 4, 200, 96)}, {".f.c", draw.Rect(0, 52, 20, 96)},
	} {
		if r := top.Rect(c.n, Border|Local); r != c.r {
			t.Errorf("%s at %v, want %v", c.n, r, c.r)
		}
	}
	if v := mustCmd(t, top, "grid info .f.b"); v != "-in .f -column 1 -row 0 -columnspan 1 -rowspan 2" {
		t.Errorf("info %q", v)
	}
	mustCmd(t, top, "grid rowinsert .f 0")
	if v := mustCmd(t, top, "grid info .f.a"); v != "-in .f -column 0 -row 1 -columnspan 1 -rowspan 1" {
		t.Errorf("after rowinsert %q", v)
	}
	mustCmd(t, top, "grid columndelete .f 0")
	if v := mustCmd(t, top, "grid slaves .f"); v != ".f.b" {
		t.Errorf("after columndelete %q", v)
	}
	if v := mustCmd(t, top, "grid columnindex .f end"); v != "1" {
		t.Errorf("columnindex %q", v)
	}
}
