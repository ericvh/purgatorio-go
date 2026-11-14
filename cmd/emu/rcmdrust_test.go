package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
)

// rustListen runs styxlisten on a free loopback port in the Rust emu
// ($PURG_RUST_BIN/emu on $PURG_RUST_ROOT), trusting the test keys, with
// cmd serving each connection, and returns its dial string.
func rustListen(t *testing.T, bin, rroot string, cmd string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(filepath.Join(bin, "emu"), "-r", rroot, "sh", "-c",
		"bind /lib/test/keys /lib/keys; styxlisten tcp!127.0.0.1!0 "+cmd)
	c.Stdout, c.Stderr = w, w
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() { c.Process.Kill(); c.Wait(); r.Close() })
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := strings.CutPrefix(strings.TrimSpace(line), "styxlisten: ")
	if !ok {
		t.Fatalf("styxlisten said %q", line)
	}
	return "tcp!" + addr
}

// goListen boots a Go system on each test mode, runs script (which
// starts listeners in the background and prints one dial string per
// line), and returns the dial strings for each mode. The systems are
// stopped when the test ends.
func goListen(t *testing.T, script string, n int) [][]string {
	t.Helper()
	var all [][]string
	for _, m := range testModes() {
		out := &lockedBuffer{}
		h := &host.Host{In: strings.NewReader(""), Out: out}
		sys, err := host.Boot(h, root, getEngines(t)[m])
		if err != nil {
			t.Fatal(err)
		}
		go sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", []string{"emuinit", "sh", "-c", script + "\nsleep 100000"})
		t.Cleanup(func() {
			for _, pid := range sys.Kernel.Progs.Pids() {
				if p := sys.Kernel.Progs.Get(pid); p != nil {
					p.Kill()
				}
			}
		})
		deadline := time.Now().Add(time.Minute)
		for {
			lines := strings.Fields(out.String())
			if len(lines) >= n {
				all = append(all, lines[:n])
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: listeners did not start: %q", m, out.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return all
}

// TestRcmdRust runs rcmd, cpu and import between the Go and the Rust
// implementations over pgauth's classical protocol (pgrstyx2 and the
// version 3 mount handshake), both ways, when $PURG_RUST_BIN names a
// directory holding the Rust emu (cargo build -p purg-emu --bins) and
// $PURG_RUST_ROOT the Rust root with its guests built (cargo xtask
// build-root). Both ends trust the public test key.
func TestRcmdRust(t *testing.T) {
	bin, rroot := os.Getenv("PURG_RUST_BIN"), os.Getenv("PURG_RUST_ROOT")
	if bin == "" || rroot == "" {
		t.Skip("PURG_RUST_BIN and PURG_RUST_ROOT not set")
	}

	// Go clients, Rust servers.
	rr := rustListen(t, bin, rroot, "/bin/auxi/rstyxd.wasm")
	rx := rustListen(t, bin, rroot, "export /lib")
	script := `bind /lib/test/keys /lib/keys
P=` + rr + `
rcmd -p classical $P echo hello remote </dev/null
echo status $status end
echo from stdin | rcmd -p classical $P cat
rcmd -p classical $P cat /n/client/lib/hello.txt </dev/null
rcmd -p classical -x /lib $P cat /n/client/hello.txt </dev/null
rcmd -p classical -S $P echo proved </dev/null
rcmd -p classical $P 'echo to stderr >[1=2]' </dev/null
rcmd -p classical $P 'echo out; echo err >[1=2]; exit odd' </dev/null >[2=1]
echo status $status end
cpu -p classical $P cat /dev/user </dev/null > /tmp/rcmdrust.user
cmp /tmp/rcmdrust.user /dev/user
echo cpu $status end
rcmd $P echo x </dev/null
rcmd -A -p classical $P echo x </dev/null
mkdir -p /tmp/rcmdrust
import -p classical -S ` + rx + ` /tmp/rcmdrust
echo import $status end
cat /tmp/rcmdrust/test/keys/users | wc -l
unmount /tmp/rcmdrust
import ` + rx + ` /tmp/rcmdrust
`
	out, _ := emu(t, "", "sh", "-c", "{"+script+"} >[2=1]")
	// The Rust sh's exit takes a status and reports none of its own, so
	// the remote status comes back as "odd" and Inferno's sh reports a
	// status without fail: as it would any other.
	want := `hello remote
status  end
from stdin
hello from the root
hello from the root
proved
to stderr
out
err
sh: \d+ "rcmd":odd
status odd end
cpu  end
rcmd: rstyx protocol botch
rcmd: authentication required
import  end
      3
import: authentication failed: no hybrid key offered
`
	if !matchLines(out, want) {
		t.Errorf("go clients, rust servers: got\n%s\nwant\n%s", out, want)
	}

	// Rust clients, Go servers.
	for i, addrs := range goListen(t, `bind /lib/test/keys /lib/keys
listen -p classical 'tcp!127.0.0.1!0' auxi/rstyxd > /dev/null >[2] /dev/null </dev/null
listen 'tcp!127.0.0.1!0' auxi/rstyxd > /dev/null >[2] /dev/null </dev/null
styxlisten -p classical -k /lib/test/auth.key 'tcp!127.0.0.1!0' export /lib >[2] /dev/null </dev/null
echo tcp!`+"`"+`{cat /net/tcp/0/local} tcp!`+"`"+`{cat /net/tcp/1/local} tcp!`+"`"+`{cat /net/tcp/2/local}
`, 3) {
		gc, gh, gx := addrs[0], addrs[1], addrs[2]
		script := `bind /lib/test/keys /lib/keys
P=` + gc + `
rcmd $P echo hello remote </dev/null
echo status $status end
echo from stdin | rcmd $P cat
rcmd $P cat /n/client/lib/test/auth.key.pub </dev/null | wc -c
rcmd -x /lib/test $P cat /n/client/auth.key.pub </dev/null | wc -c
rcmd -S $P echo proved </dev/null
rcmd $P 'echo to stderr >[1=2]' </dev/null
rcmd $P 'echo out; nonesuch' </dev/null
echo status $status end
cpu $P cat /dev/user </dev/null > /tmp/rcmdrust.user
cmp /tmp/rcmdrust.user /dev/user
echo cpu $status end
rcmd ` + gh + ` echo x </dev/null
rcmd -A $P echo x </dev/null
mkdir -p /tmp/rcmdrust
import -S ` + gx + ` /tmp/rcmdrust
echo import $status end
cat /tmp/rcmdrust/hello.txt
`
		want := `hello remote
status  end
from stdin
     32
     32
proved
to stderr
out
sh: nonesuch: file does not exist
status fail:file does not exist end
cpu  end
rcmd: hybrid authentication required
rcmd: authentication required
import  end
hello from the root
`
		for _, vm := range []string{"wasmtime", "wasmi"} {
			b, _ := exec.Command(filepath.Join(bin, "emu"), "--vm="+vm, "-r", rroot, "sh", "-c", script).CombinedOutput()
			if !matchLines(string(b), want) {
				t.Errorf("rust clients (%s), go servers (%s): got\n%s\nwant\n%s", vm, testModes()[i], b, want)
			}
		}
	}
}

// matchLines compares out with want line by line, where a want line
// may hold \d+ for a number (a pid).
func matchLines(out, want string) bool {
	o, w := strings.Split(out, "\n"), strings.Split(want, "\n")
	if len(o) != len(w) {
		return false
	}
	for i := range o {
		pre, post, num := strings.Cut(w[i], `\d+`)
		if !num {
			if o[i] != w[i] {
				return false
			}
			continue
		}
		mid, ok := strings.CutPrefix(o[i], pre)
		if !ok {
			return false
		}
		mid, ok = strings.CutSuffix(mid, post)
		if !ok || mid == "" || strings.Trim(mid, "0123456789") != "" {
			return false
		}
	}
	return true
}
