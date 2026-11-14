package main

import (
	"strings"
	"testing"
)

// TestDraw runs a guest that draws through sdk/draw on the headless
// screen and checks the pixels it reads back.
func TestDraw(t *testing.T) {
	out, st := emu(t, "", "/bin/test/draw.wasm")
	want := "screen 640 480 x8r8g8b8\nlines ok\nellipse ok\nimage ok\ntext ok\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}

// TestDrawCtl reads a new client's ctl file: the screen's description.
func TestDrawCtl(t *testing.T) {
	out, _ := emu(t, "", "cat", "#i/draw/new")
	want := "          1           0    x8r8g8b8           0           0           0         640         480           0           0         640         480 "
	if !strings.HasPrefix(out, want) {
		t.Errorf("got %q", out)
	}
}
