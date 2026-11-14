package main

import (
	"os"
	"path/filepath"
	"testing"
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
