package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSelfHost builds one of the commands, echo, and the sdk/sys package
// it uses from source inside the emu, with Go's own compiler and linker
// running as guests, then runs what it built. It needs
// go run ./tools/gotools first; without it the test is skipped.
func TestSelfHost(t *testing.T) {
	if _, err := os.Stat(filepath.Join(root, "bin", "go", "compile.wasm")); err != nil {
		t.Skip("no Go toolchain in root (go run ./tools/gotools)")
	}
	dir := filepath.Join(root, "tmp", "selfhost")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	src, err := os.ReadFile("../../appl/cmd/echo/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "echo.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	out, st := emu(t, "", "sh", "-c",
		"sh /lib/sh/gobuild /tmp/selfhost/echo.wasm /tmp/selfhost; /tmp/selfhost/echo.wasm built inside purgatorio-go")
	if out != "built inside purgatorio-go\n" || st != "" {
		t.Errorf("got %q status %q", out, st)
	}
}
