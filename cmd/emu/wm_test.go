package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
)

// TestWM runs the window manager on the headless screen with a client
// that drives it through #m/pointer and #c/keyboard: focus, a button,
// typing in an entry, dragging its window by the title bar, and a wm/sh
// window that runs what is typed in it. The client checks pixels and
// the window manager's files and prints what it found.
func TestWM(t *testing.T) {
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(filepath.Join(root, "tmp", "wmsh.out")) })
	out, st := emu(t, "", "wm/wm", "/bin/test/wm.wasm")
	want := "window 40 30 144 86\n" +
		"wmrect 0 0 640 480\n" +
		"mnt/wm 1 clone wmrect\n" +
		"focus ok\n" +
		"button ok\n" +
		"keyboard ok\n" +
		"moved 30 20\n" +
		"drag ok\n" +
		"wm/sh wrote \"hello from wm/sh\\n\"\n" +
		"clients 2\n" +
		"done\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}

// TestWMRequests checks wm's errors for requests it cannot do.
func TestWMRequests(t *testing.T) {
	out, _ := emu(t, "", "wm/wm", "sh", "-c",
		"{echo nonsense > /mnt/wm/clone} >[2=1]; {echo '!reshape . -1 0 0' > /mnt/wm/clone} >[2=1]; "+
			"{echo start telepathy > /mnt/wm/clone} >[2=1]; echo exit > /mnt/wm/clone")
	want := "echo: write error: unknown control request\n" +
		"echo: write error: bad arg count\n" +
		"echo: write error: unknown input source\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// TestWMResize resizes the screen under wm, as a host window the user
// resizes does: the draw device's refresh file tells wm, whose wmrect
// then gives the new size, and the new part of the screen is wm's
// background.
func TestWMResize(t *testing.T) {
	for _, m := range testModes() {
		var out lockedBuffer
		scr := platform.NewHeadless(640, 480, "x8r8g8b8")
		h := &host.Host{In: strings.NewReader(""), Out: &out, Screen: scr}
		sys, err := host.Boot(h, root, getEngines(t)[m])
		if err != nil {
			t.Fatal(err)
		}
		dd := sys.Kernel.DevTab.Lookup('i').(*core.DevDraw)
		go func() {
			for dd.Screen() == nil {
				time.Sleep(50 * time.Millisecond)
			}
			time.Sleep(time.Second)
			scr.Resize(800, 600)
		}()
		st, err := runWatched(t, sys, m, &out, []string{"wm/wm", "sh", "-c",
			`load std; while {~ "{cat /mnt/wm/wmrect} '0 0 640 480'} {sleep 0}; cat /mnt/wm/wmrect; echo; echo exit > /mnt/wm/clone`})
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		sys.Init.Exit(st)
		if out.String() != "0 0 800 600\n" || st != "" {
			t.Errorf("%s: got %q status %q", m, out.String(), st)
		}
		pix, stride, _ := scr.Snapshot()
		if stride != 4*800 || len(pix) != 4*800*600 {
			t.Fatalf("%s: screen stride %d", m, stride)
		}
		if p := pix[4*(599*800+799):]; p[0] != 0x77 || p[1] != 0x77 || p[2] != 0x77 {
			t.Errorf("%s: new corner % x, want wm's grey", m, p[:4])
		}
	}
}
