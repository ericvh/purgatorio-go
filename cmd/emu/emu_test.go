package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

var (
	enginesOnce sync.Once
	engines     map[wazero.Mode]*wazero.Engine
)

func getEngines(t *testing.T) map[wazero.Mode]*wazero.Engine {
	enginesOnce.Do(func() {
		engines = map[wazero.Mode]*wazero.Engine{}
		for _, m := range testModes() {
			e, err := wazero.NewCached(m, testCacheDir())
			if err != nil {
				t.Fatal(err)
			}
			engines[m] = e
		}
	})
	return engines
}

const root = "../../root"

// emu boots a system on root and runs args through emuinit, on both
// wazero modes, and returns the console output and exit status, which
// must agree.
func emu(t *testing.T, stdin string, args ...string) (string, string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "bin", "emuinit.wasm")); err != nil {
		t.Fatalf("%v (run go run ./tools/build first)", err)
	}
	var outs [2]string
	var sts [2]string
	i := 0
	for _, m := range testModes() {
		var out lockedBuffer
		h := &host.Host{In: strings.NewReader(stdin), Out: &out}
		sys, err := host.Boot(h, root, getEngines(t)[m])
		if err != nil {
			t.Fatal(err)
		}
		st, err := sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", append([]string{"emuinit"}, args...))
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		sys.Init.Exit(st)
		outs[i], sts[i] = out.String(), st
		i++
	}
	if i > 1 && (outs[0] != outs[1] || sts[0] != sts[1]) {
		t.Fatalf("modes disagree:\ncompiler: %q %q\ninterpreter: %q %q", outs[0], sts[0], outs[1], sts[1])
	}
	return outs[0], sts[0]
}

// testCacheDir keeps compiled guests between test runs, in the user's
// cache directory (not /tmp, which is often a small tmpfs).
func testCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "purgatorio-go", "wazero-test")
}

// testModes are the wazero modes the acceptance tests run on: both, or
// those named in $PURG_EMU_MODES (comma separated). CI splits them across
// runners to halve each runner's time.
func testModes() []wazero.Mode {
	v := os.Getenv("PURG_EMU_MODES")
	if v == "" {
		return []wazero.Mode{wazero.Compiler, wazero.Interpreter}
	}
	var out []wazero.Mode
	for _, m := range strings.Split(v, ",") {
		out = append(out, wazero.Mode(strings.TrimSpace(m)))
	}
	return out
}

func TestM1(t *testing.T) {
	cases := []struct {
		name, stdin string
		args        []string
		out, status string
	}{
		{"echo", "", []string{"echo", "hello", "world"}, "hello world\n", ""},
		{"echo -n", "", []string{"echo", "-n", "x"}, "x", ""},
		{"cat file", "", []string{"cat", "/lib/hello.txt"}, "hello from the root\n", ""},
		{"cat stdin", "hi\nthere\n", []string{"cat"}, "hi\nthere\n", ""},
		{"cat missing", "", []string{"cat", "/nonexistent"},
			"cat: cannot open /nonexistent: file does not exist\n", "fail:bad open"},
		{"sysname", "", []string{"cat", "/dev/sysname"}, (&host.Host{}).Sysname(), ""},
		{"abi", "", []string{"/bin/test/abi.wasm"}, "errstr ok\n", ""},
		{"abi status", "", []string{"/bin/test/abi.wasm", "fail:bad"}, "errstr ok\n", "fail:bad"},
		{"case", "", []string{"cat", "/LIB/hello.txt"},
			"cat: cannot open /LIB/hello.txt: file does not exist\n", "fail:bad open"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emu(t, c.stdin, c.args...)
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}

// lockedBuffer is the test console: progs write to it from their own
// goroutines at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
