package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/platform"
)

// rioFiles are the files in /tmp the rio tests make.
var rioFiles = []string{"rio.wctl", "rio.text", "rio.ok", "riodraw.ptr", "rio.after", "rio.ready",
	"rio.scaled", "rio.sent", "rio.snarf"}

func rioTmp(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, n := range rioFiles {
			os.Remove(filepath.Join(root, "tmp", n))
		}
	})
}

// TestRio runs rio on the headless screen beside a client that drives
// it through #m/pointer and #c/keyboard: a window swept with New, a
// command typed into its shell, the window's files read back from it,
// a program drawing through the SDK in the window, a click it gets, a
// resize it follows, and Exit.
func TestRio(t *testing.T) {
	rioTmp(t)
	out, st := emu(t, "", "sh", "-c", "rio & /bin/test/rio.wasm")
	want := "rio up\n" +
		"window made\n" +
		`wctl "          1          60          60         460         300     visible     current window 1/dev/wsys/1\n"` + "\n" +
		`text "; echo hello world\nhello world\n; cat /dev/winid /dev/wctl /dev/label >/tmp/rio.wctl; ls /dev/wsys >>/tmp/rio.wctl; cat /dev/text >/tmp/rio.text; echo >/tmp/rio.ok\n"` + "\n" +
		"riodraw in the window\n" +
		`riodraw got "260 180 1\n"` + "\n" +
		"resized\n" +
		`shell still runs: "after\n"` + "\n" +
		"done\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}

// TestRioEdit checks a text window's selection and button 2 menu
// (snarf, send), Hide and the hidden window in the menu, a corner
// dragged with button 1, Move, a window made through wctl and Delete.
func TestRioEdit(t *testing.T) {
	rioTmp(t)
	out, st := emu(t, "", "sh", "-c", "rio & /bin/test/rio.wasm -edit")
	want := "rio up\n" +
		"window made\n" +
		`send ran "sent\n"` + "\n" +
		`snarf "echo sent >/tmp/rio.sent\n"` + "\n" +
		"hide and show\n" +
		"corner dragged\n" +
		"moved\n" +
		`wctl ["110" "90" "552" "382" "visible" "current"]` + "\n" +
		"done\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}

// TestRioResize resizes the screen under rio, as a host window the user
// resizes does: rio scales its window to the new screen.
func TestRioResize(t *testing.T) {
	rioTmp(t)
	ready := filepath.Join(root, "tmp", "rio.ready")
	for _, m := range testModes() {
		os.Remove(ready)
		var out lockedBuffer
		scr := platform.NewHeadless(640, 480, "x8r8g8b8")
		h := &host.Host{In: strings.NewReader(""), Out: &out, Screen: scr}
		sys, err := host.Boot(h, root, getEngines(t)[m])
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for i := 0; i < 600; i++ {
				if _, err := os.Stat(ready); err == nil {
					scr.Resize(800, 600)
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
		st, err := runWatched(t, sys, m, &out, []string{"sh", "-c", "rio & /bin/test/rio.wasm -resize"})
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		sys.Init.Exit(st)
		want := "rio up\nwindow made\n" +
			`scaled ["75" "75" "575" "375" "visible" "current"]` + "\n" +
			"done\n"
		if out.String() != want || st != "" {
			t.Errorf("%s: got %q status %q, want %q", m, out.String(), st, want)
		}
	}
}
