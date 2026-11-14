package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/vm"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

var engines = []wazero.Engine{wazero.Compiler, wazero.Interpreter}

// run executes root/bin/<path>.wasm on both engines; the guests must be
// built first with go run ./tools/build.
func run(t *testing.T, path string, args ...string) (out, status string) {
	t.Helper()
	code, err := os.ReadFile(filepath.Join("..", "..", "root", "bin", path+".wasm"))
	if err != nil {
		t.Fatalf("%v (run go run ./tools/build first)", err)
	}
	var results []string
	for _, e := range engines {
		var stdout bytes.Buffer
		h := host.Host{}
		p := &vm.Proc{Args: append([]string{filepath.Base(path)}, args...),
			Stdin: bytes.NewReader(nil), Stdout: &stdout, Stderr: &stdout,
			Now: h.Now, Entropy: h.Entropy}
		st, err := wazero.Run(context.Background(), e, code, p)
		if err != nil {
			t.Fatalf("%s on %s: %v", path, e, err)
		}
		results = append(results, stdout.String(), st)
	}
	if results[0] != results[2] || results[1] != results[3] {
		t.Fatalf("engines disagree: %q", results)
	}
	return results[0], results[1]
}

func TestEcho(t *testing.T) {
	if out, st := run(t, "echo", "hello", "world"); out != "hello world\n" || st != "" {
		t.Errorf("got %q status %q", out, st)
	}
	if out, _ := run(t, "echo", "-n", "x"); out != "x" {
		t.Errorf("echo -n: got %q", out)
	}
}

func TestABI(t *testing.T) {
	if out, st := run(t, "test/abi"); out != "errstr ok\n" || st != "" {
		t.Errorf("got %q status %q", out, st)
	}
	if _, st := run(t, "test/abi", "fail:bad"); st != "fail:bad" {
		t.Errorf("status %q, want fail:bad", st)
	}
}
