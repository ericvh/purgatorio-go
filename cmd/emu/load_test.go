package main

import "testing"

func TestLoad(t *testing.T) {
	out, st := emu(t, "", "/bin/test/load.wasm")
	want := "echo \"hello\" <nil>\nfail it failed\nnope no such function: nope\nsame prog true\nbig 100000 <nil>\ncommand: /bin/echo.wasm: not a loadable module\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}
