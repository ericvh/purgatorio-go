package main

import "testing"

func TestNet(t *testing.T) {
	out, st := emu(t, "", "/bin/test/net.wasm")
	want := "server got \"ping\"\nclient got \"pong\"\nstatus Established\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}
