package main

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/lethe"
)

// letheTree makes a host tree for lethed: small files, a directory, a
// file larger than several Lethe blocks, and a file nobody may read.
func letheTree(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "lib", "d"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "hello"), []byte("hello, lethe\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "lib", "d", "e"), []byte("deep\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "secret"), []byte("no\n"), 0o600))
	var big strings.Builder
	for i := 0; i < 40000; i++ {
		fmt.Fprintf(&big, "line %d\n", i)
	}
	must(os.WriteFile(filepath.Join(dir, "big"), []byte(big.String()), 0o644))
	for i := 0; i < 300; i++ {
		must(os.WriteFile(filepath.Join(dir, "lib", fmt.Sprintf("f%03d", i)), nil, 0o644))
	}
	return dir, fmt.Sprintf("%x", md5.Sum([]byte(big.String())))
}

// startLethed runs a lethed in the test on addr (port 0 picks one).
func startLethed(t *testing.T, root, addr string, clear bool) *host.LethedServer {
	t.Helper()
	o := &host.LetheOptions{Addr: addr, Root: root, Clear: clear, Config: lethe.DefaultConfig(), Owner: "sys"}
	var l *host.LethedServer
	var err error
	// A restart on the same port may find it in use for a moment.
	for i := 0; i < 50; i++ {
		if l, err = host.NewLethed(o); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	go l.Serve()
	t.Cleanup(func() { l.Close() })
	return l
}

func dialString(addr string) string {
	h, p, _ := strings.Cut(addr, ":")
	return "tcp!" + h + "!" + p
}

func TestLetheClear(t *testing.T) {
	dir, sum := letheTree(t)
	l := startLethed(t, dir, "127.0.0.1:0", true)
	a := dialString(l.Addr())
	script := strings.Join([]string{
		"mkdir -p /tmp/lt",
		"lethe -A " + a + " /tmp/lt",
		"ls /tmp/lt",
		"cat /tmp/lt/hello /tmp/lt/lib/d/e",
		"md5sum /tmp/lt/big",
		"ls /tmp/lt/lib | wc -l",
		"ls -l /tmp/lt/hello",
		"cat /tmp/lt/secret",
		"cat /tmp/lt/nope",
		"lethe -A " + a + " /tmp/lt2",
		"mkdir -p /tmp/lt3",
		"lethe -A " + a + " /tmp/lt3 /lib/d",
		"cat /tmp/lt3/e",
		"lethe -A " + a + " /tmp/lt3 /hello",
		"lethe -A -t 0 " + a + " /tmp/lt3 /lib",
		"cat /tmp/lt3/d/e",
		"echo x > /tmp/lt/hello",
	}, "\n")
	out, _ := emu(t, "", "sh", "-c", script)
	want := strings.Join([]string{
		"/tmp/lt/big", "/tmp/lt/hello", "/tmp/lt/lib", "/tmp/lt/secret",
		"hello, lethe", "deep",
		sum + "\t/tmp/lt/big",
		"    301",
		"--rw-r--r-- λ 1 sys sys 13 ",
		"cat: error reading /tmp/lt/secret: permission denied",
		"cat: cannot open /tmp/lt/nope: file does not exist",
		"lethe: cannot bind #λ2 on /tmp/lt2: file does not exist",
		"deep",
		"lethe: not a directory",
		"deep",
		"sh: cannot open /tmp/lt/hello: permission denied", "",
	}, "\n")
	// The time in ls -l is the host's: compare up to it.
	lines, wlines := strings.Split(out, "\n"), strings.Split(want, "\n")
	if len(lines) != len(wlines) {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	for i := range lines {
		if !strings.HasPrefix(lines[i], wlines[i]) || (wlines[i] == "" && lines[i] != "") {
			t.Errorf("line %d: got %q, want %q", i, lines[i], wlines[i])
		}
	}
}

// A clear client and a server that wants tickets, and a server that
// goes away and comes back.
func TestLetheRefusalsAndRestart(t *testing.T) {
	dir, sum := letheTree(t)
	sealed := startLethed(t, dir, "127.0.0.1:0", false)
	l := startLethed(t, dir, "127.0.0.1:0", true)
	addr := l.Addr()
	a := dialString(addr)
	restarted := make(chan bool)
	go func() {
		// Stop the server while the guest waits, then start it again on
		// the same port.
		time.Sleep(time.Second)
		l.Close()
		time.Sleep(300 * time.Millisecond)
		startLethed(t, dir, addr, true)
		close(restarted)
	}()
	script := strings.Join([]string{
		"mkdir -p /tmp/lt",
		"lethe -A " + dialString(sealed.Addr()) + " /tmp/lt",
		"lethe -A -t 0 " + a + " /tmp/lt",
		"cat /tmp/lt/hello",
		"sleep 2",
		"cat /tmp/lt/hello",
		"md5sum /tmp/lt/big",
	}, "\n")
	out, _ := emu(t, "", "sh", "-c", script)
	<-restarted
	want := "lethe: authentication required\nhello, lethe\nhello, lethe\n" + sum + "\t/tmp/lt/big\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// TestLetheRust checks the Go and Rust implementations against each
// other, when $PURG_RUST_BIN names a directory holding the Rust lethed
// and emu (cargo build -p purg-emu --bins) and $PURG_RUST_ROOT the Rust
// root they run on (cargo xtask build-root): the Go client reads from
// the Rust lethed, and the Rust client from the Go lethed.
func TestLetheRust(t *testing.T) {
	bin, rroot := os.Getenv("PURG_RUST_BIN"), os.Getenv("PURG_RUST_ROOT")
	if bin == "" || rroot == "" {
		t.Skip("PURG_RUST_BIN and PURG_RUST_ROOT not set")
	}
	dir, sum := letheTree(t)
	cmd := exec.Command(filepath.Join(bin, "lethed"), "-A", "-u", "sys", "-a", "127.0.0.1:0", dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	raddr, ok := strings.CutPrefix(strings.TrimSpace(line), "lethed: listening on ")
	if !ok {
		t.Fatalf("lethed said %q", line)
	}
	// The two systems' commands differ in their spacing and in how cat
	// words a read error, so compare the fields.
	check := func(who, out string) {
		t.Helper()
		got := strings.Fields(strings.ReplaceAll(out, "cat: error reading", "cat:"))
		want := strings.Fields("hello, lethe deep " + sum + " /tmp/lt/big 301 cat: /tmp/lt/secret: permission denied")
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: got %q", who, out)
		}
	}
	script := func(addr string) string {
		return strings.Join([]string{
			"mkdir -p /tmp/lt",
			"lethe -A " + dialString(addr) + " /tmp/lt",
			"cat /tmp/lt/hello /tmp/lt/lib/d/e",
			"md5sum /tmp/lt/big",
			"ls /tmp/lt/lib | wc -l",
			"cat /tmp/lt/secret",
		}, "\n")
	}
	out, _ := emu(t, "", "sh", "-c", script(raddr))
	check("go client, rust lethed", out)

	l := startLethed(t, dir, "127.0.0.1:0", true)
	for _, vm := range []string{"wasmtime", "wasmi"} {
		c := exec.Command(filepath.Join(bin, "emu"), "--vm="+vm, "-r", rroot, "sh", "-c", script(l.Addr()))
		b, _ := c.CombinedOutput()
		check("rust client ("+vm+"), go lethed", string(b))
	}
}
