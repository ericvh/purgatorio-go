package main

import "testing"

// TestWMMenu presses on wm's background, chooses Edit from the menu
// that comes up, and checks that wm/edit's window appears.
func TestWMMenu(t *testing.T) {
	out, st := emu(t, "", "wm/wm", "/bin/test/wmmenu.wasm")
	want := "menu up\nwm/edit started\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}
