// Command build compiles every guest for wasip1 and installs it with the
// purgatorio.abi custom section: appl/cmd/<name> as root/bin/<name>.wasm and
// appl/test/<name> as root/bin/test/<name>.wasm.
//
//	go run ./tools/build
//
// Guest builds must be byte-identical on every build host: the flags below
// strip paths, VCS stamps, build IDs and symbols.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ericvh/purgatorio-go/abi"
)

func main() {
	build("appl/cmd/*", "root/bin")
	build("appl/test/*", "root/bin/test")
}

func build(pattern, bin string) {
	dirs, err := filepath.Glob(pattern)
	check(err)
	check(os.MkdirAll(bin, 0o755))
	for _, d := range dirs {
		name := filepath.Base(d)
		out := filepath.Join(bin, name+".wasm")
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false",
			"-ldflags=-s -w -buildid=", "-o", out, "./"+d)
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		check(cmd.Run())
		b, err := os.ReadFile(out)
		check(err)
		check(os.WriteFile(out, appendCustom(b, abi.Section, []byte(abi.Version)), 0o644))
		fmt.Println(out)
	}
}

// appendCustom adds a WebAssembly custom section (id 0) to a module.
func appendCustom(mod []byte, name string, payload []byte) []byte {
	body := append(uleb(uint32(len(name))), name...)
	body = append(body, payload...)
	mod = append(mod, 0)
	mod = append(mod, uleb(uint32(len(body)))...)
	return append(mod, body...)
}

func uleb(v uint32) []byte {
	var b []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		b = append(b, c)
		if v == 0 {
			return b
		}
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
}
