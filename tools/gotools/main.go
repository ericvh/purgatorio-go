// Command gotools puts the Go toolchain inside purgatorio-go's root, so
// Go programs can be compiled and linked in the emu itself: cmd/compile
// and cmd/link built for wasip1 go to root/bin/go/{compile,link}.wasm,
// the standard library's packages, compiled for wasip1, to
// root/lib/go/pkg, with root/lib/go/importcfg naming them, and the
// sources of sdk/sys to root/lib/go/src for guests that use it.
//
//	go run ./tools/gotools [package ...]
//
// The packages default to those the self-hosting test and small programs
// need; their dependencies are included.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var defaults = []string{"fmt", "os", "strings", "strconv", "bufio", "io", "sort", "errors",
	"time", "bytes", "unicode/utf8", "path", "flag", "encoding/binary", "unsafe", "sync"}

func main() {
	pkgs := os.Args[1:]
	if len(pkgs) == 0 {
		pkgs = defaults
	}
	wasm := append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	check(os.MkdirAll("root/bin/go", 0o755))
	for _, t := range []string{"compile", "link"} {
		run(wasm, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=",
			"-o", "root/bin/go/"+t+".wasm", "cmd/"+t)
	}
	out := runOut(wasm, "go", append([]string{"list", "-export", "-deps", "-f",
		"{{if .Export}}{{.ImportPath}} {{.Export}}{{end}}"}, pkgs...)...)
	check(os.RemoveAll("root/lib/go"))
	var cfg bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(out))
	n := 0
	for sc.Scan() {
		p, file, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		dst := filepath.Join("root/lib/go/pkg", p+".a")
		check(os.MkdirAll(filepath.Dir(dst), 0o755))
		b, err := os.ReadFile(file)
		check(err)
		check(os.WriteFile(dst, b, 0o644))
		fmt.Fprintf(&cfg, "packagefile %s=/lib/go/pkg/%s.a\n", p, p)
		n++
	}
	check(os.WriteFile("root/lib/go/importcfg", cfg.Bytes(), 0o644))
	// A module of its own, so the go tool on the host leaves the copied
	// sources out of ./... patterns.
	check(os.WriteFile("root/lib/go/go.mod", []byte("module purgatorio-root-go\n"), 0o644))
	// sdk/sys, as source: a guest built in the emu compiles it there.
	files := runOut(wasm, "go", "list", "-f", `{{join .GoFiles " "}}`, "./sdk/sys")
	check(os.MkdirAll("root/lib/go/src/sdk/sys", 0o755))
	for _, f := range strings.Fields(string(files)) {
		b, err := os.ReadFile(filepath.Join("sdk/sys", f))
		check(err)
		check(os.WriteFile(filepath.Join("root/lib/go/src/sdk/sys", f), b, 0o644))
	}
	fmt.Printf("root/bin/go: compile.wasm, link.wasm; root/lib/go: %d packages, sdk/sys source\n", n)
}

func run(env []string, name string, args ...string) {
	c := exec.Command(name, args...)
	c.Env, c.Stdout, c.Stderr = env, os.Stdout, os.Stderr
	check(c.Run())
}

func runOut(env []string, name string, args ...string) []byte {
	c := exec.Command(name, args...)
	c.Env, c.Stderr = env, os.Stderr
	b, err := c.Output()
	check(err)
	return b
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "gotools:", err)
		os.Exit(1)
	}
}
