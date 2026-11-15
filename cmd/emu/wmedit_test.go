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

// TestWMCoffee starts wm/coffee under wm and checks that its cups are
// drawn and move, and that Stop and Go stop and start them.
func TestWMCoffee(t *testing.T) {
	out, st := emu(t, "", "wm/wm", "/bin/test/wmcoffee.wasm")
	want := "wm/coffee started\npanel 400x300\ncups drawn\ncups move\nstopped\ngoing\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}
