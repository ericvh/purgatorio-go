// Package tktest gives host tests of Tk programs a display: a draw
// device on a headless screen in a kernel of its own.
package tktest

import (
	"context"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/sdk/draw"
)

type plat struct{ scr *platform.Headless }

func (*plat) Now() int64                 { return 0 }
func (*plat) Entropy([]byte)             {}
func (*plat) Console() platform.Console  { return nil }
func (*plat) User() string               { return "inferno" }
func (*plat) Sysname() string            { return "test" }
func (p *plat) Display() platform.Screen { return p.scr }

type fdrw struct {
	p  *core.Prog
	fd int
}

func (f fdrw) Read(b []byte) (int, error)  { return f.p.Read(f.fd, b) }
func (f fdrw) Write(b []byte) (int, error) { return f.p.Write(f.fd, b) }

// Display returns a Display of a w by h screen, with its default font.
func Display(t testing.TB, w, h int) *draw.Display {
	t.Helper()
	k := &core.Kernel{Platform: &plat{platform.NewHeadless(w, h, "x8r8g8b8")}, Progs: core.NewProgs(), Owner: "inferno"}
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
	if _, err := d.GetDefaultFont(); err != nil {
		t.Fatal(err)
	}
	return d
}
