package main

import (
	"os"
	"testing"
)

func TestM3(t *testing.T) {
	if err := os.MkdirAll(root+"/tmp", 0o777); err != nil {
		t.Fatal(err)
	}
	out, st := emu(t, "", "/bin/test/m3.wasm")
	want := "ramfile changed\nmounted hello from the root\nchans ok\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}
