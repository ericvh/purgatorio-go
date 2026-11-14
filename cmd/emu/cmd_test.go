package main

import "testing"

func TestOs(t *testing.T) {
	out, st := emu(t, "", "os", "echo", "hello from the host")
	if out != "hello from the host\n" || st != "" {
		t.Errorf("os echo: %q %q", out, st)
	}
	out, st = emu(t, "", "os", "sh", "-c", "echo oops >&2; exit 3")
	if out != "oops\n" || st != "fail:host: exit: 3" {
		t.Errorf("os exit 3: %q %q", out, st)
	}
	out, st = emu(t, "piped\n", "os", "cat")
	if out != "piped\n" || st != "" {
		t.Errorf("os cat: %q %q", out, st)
	}
}
