package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

// trapWasm is a module whose _start executes unreachable.
var trapWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
	0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type: () -> ()
	0x03, 0x02, 0x01, 0x00, // func 0 has type 0
	0x07, 0x0a, 0x01, 0x06, '_', 's', 't', 'a', 'r', 't', 0x00, 0x00, // export _start
	0x0a, 0x05, 0x01, 0x03, 0x00, 0x00, 0x0b, // code: unreachable
}

func progBoot(t *testing.T, m wazero.Mode) *host.System {
	t.Helper()
	emu(t, "", "date", "0") // checks that the guests are built
	var out lockedBuffer
	sys, err := host.Boot(&host.Host{In: strings.NewReader(""), Out: &out}, root, getEngines(t)[m])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, pid := range sys.Kernel.Progs.Pids() {
			if p := sys.Kernel.Progs.Get(pid); p != nil && p != sys.Init {
				p.Kill()
			}
		}
	})
	return sys
}

func readFile(t *testing.T, p *core.Prog, path string) (string, error) {
	t.Helper()
	fd, err := p.Open(path, abi.OREAD)
	if err != nil {
		return "", err
	}
	defer p.Close(fd)
	var s strings.Builder
	buf := make([]byte, 8192)
	for {
		n, err := p.Read(fd, buf)
		if err != nil {
			return s.String(), err
		}
		if n == 0 {
			return s.String(), nil
		}
		s.Write(buf[:n])
	}
}

func writeFd(p *core.Prog, fd int, s string) string {
	if _, err := p.Write(fd, []byte(s)); err != nil {
		return core.Errstr(err)
	}
	return ""
}

func waitState(t *testing.T, p *core.Prog, pid int, state string) {
	t.Helper()
	for i := 0; i < 500; i++ {
		s, _ := readFile(t, p, "/prog/"+strconv.Itoa(pid)+"/status")
		if f := strings.Fields(s); len(f) > 4 && f[4] == state {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("prog %d never reached state %s", pid, state)
}

// TestProgDebug covers the debugging files of #p: stack, text,
// exception, heap and dbgctl, and the ctl messages' errors.
func TestProgDebug(t *testing.T) {
	for _, m := range testModes() {
		t.Run(string(m), func(t *testing.T) {
			sys := progBoot(t, m)
			init := sys.Init
			in, err := init.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			out, err := init.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			pid, err := sys.Machine.Spawn(init, "/bin/cat.wasm", []string{"cat"}, abi.NEWFD,
				[][2]int{{0, in[1]}, {1, out[0]}, {2, 2}})
			if err != nil {
				t.Fatal(err)
			}
			dir := "/prog/" + strconv.Itoa(pid) + "/"
			waitState(t, init, pid, "release")

			if s, err := readFile(t, init, dir+"text"); err != nil || s != "/bin/cat.wasm" {
				t.Errorf("text = %q, %v", s, err)
			}
			s, err := readFile(t, init, dir+"stack")
			if err != nil {
				t.Fatalf("stack: %v", err)
			}
			lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
			frame := regexp.MustCompile(`^[0-9a-f]{8} [0-9a-f]{8} [0-9a-f]{8} 00000000 1 (\$wasi_snapshot_preview1|/bin/cat\.wasm)$`)
			for _, l := range lines {
				if !frame.MatchString(l) {
					t.Errorf("stack line %q", l)
				}
			}
			if len(lines) < 2 || !strings.HasSuffix(lines[0], " $wasi_snapshot_preview1") ||
				!strings.HasPrefix(lines[len(lines)-1], "00000000 ") {
				t.Errorf("stack:\n%s", s)
			}
			if s, err := readFile(t, init, dir+"exception"); err != nil || s != "" {
				t.Errorf("exception = %q, %v", s, err)
			}

			// heap opens only for reading and writing.
			if _, err := init.Open(dir+"heap", abi.OREAD); err == nil || core.Errstr(err) != abi.Eperm {
				t.Errorf("heap OREAD: %v", err)
			}
			hfd, err := init.Open(dir+"heap", abi.ORDWR)
			if err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 512)
			n, _ := init.Pread(hfd, buf, 0)
			if !regexp.MustCompile(`^ +[0-9]+ +[0-9]+ +65536 memory\n$`).Match(buf[:n]) {
				t.Errorf("heap = %q", buf[:n])
			}
			if e := writeFd(init, hfd, "1.W1"); e != "" {
				t.Errorf("heap query: %s", e)
			}
			if _, err := init.Pread(hfd, buf, 0); core.Errstr(err) != "misaligned address" {
				t.Errorf("misaligned heap read: %v", err)
			}
			if e := writeFd(init, hfd, "4096.B2"); e != "" {
				t.Errorf("heap query: %s", e)
			}
			if n, err := init.Pread(hfd, buf, 0); err != nil || !regexp.MustCompile(`^[0-9]+\n[0-9]+\n$`).Match(buf[:n]) {
				t.Errorf("heap B2 = %q, %v", buf[:n], err)
			}
			if e := writeFd(init, hfd, "8.L1"); e != abi.Ebadctl {
				t.Errorf("heap L query: %q", e)
			}
			init.Close(hfd)

			// ctl's errors are Inferno's.
			cfd, err := init.Open(dir+"ctl", abi.OWRITE)
			if err != nil {
				t.Fatal(err)
			}
			for msg, want := range map[string]string{
				"bogus":                `unknown control message "bogus"`,
				"kill now":             `wrong #args in control message "kill now"`,
				"exceptions":           `wrong #args in control message "exceptions"`,
				"exceptions propagate": abi.Eperm, // cat leads no group
				"restricted":           "",
			} {
				if e := writeFd(init, cfd, msg); e != want {
					t.Errorf("ctl %q: %q, want %q", msg, e, want)
				}
			}
			init.Close(cfd)

			// dbgctl: stop then cont attaches and stops at once; the prog
			// halts at its next host call, and cont runs it to its end.
			if _, err := init.Open(dir+"dbgctl", abi.OWRITE); core.Errstr(err) != abi.Eperm {
				t.Errorf("dbgctl OWRITE: %v", err)
			}
			dfd, err := init.Open(dir+"dbgctl", abi.ORDWR)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range []struct{ msg, want string }{
				{"toret", abi.Ebadctl},
				{"bpt set x 3", abi.Ebadctl},
				{"bpt set x", `wrong #args in control message "bpt set x"`},
				{"what", `unknown control message "what"`},
				{"stop", ""},
				{"cont", "child: stopped"},
			} {
				if e := writeFd(init, dfd, c.msg); e != c.want {
					t.Errorf("dbgctl %q: %q, want %q", c.msg, e, c.want)
				}
			}
			if e := writeFd(init, in[0], "hello\n"); e != "" {
				t.Fatal(e)
			}
			waitState(t, init, pid, "debug")
			if s, err := readFile(t, init, dir+"stack"); err != nil || !strings.Contains(s, "/bin/cat.wasm") {
				t.Errorf("stack of a halted prog: %q, %v", s, err)
			}
			if e := writeFd(init, dfd, "unstop"); e != "" {
				t.Fatal(e)
			}
			// step runs the prog to its next host call: cat writes the
			// line and is held at its next read.
			if e := writeFd(init, dfd, "step"); e != "" {
				t.Fatalf("step: %q", e)
			}
			waitState(t, init, pid, "debug")
			done := make(chan string, 1)
			go func() { done <- writeFd(init, dfd, "cont") }()
			if n, err := init.Read(out[1], buf); err != nil || string(buf[:n]) != "hello\n" {
				t.Errorf("cat read %q, %v", buf[:n], err)
			}
			init.Close(in[0])
			select {
			case e := <-done:
				if e != "child: exited" {
					t.Errorf("cont: %q", e)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("cont did not return")
			}
			if n, err := init.Read(dfd, buf); err != nil || string(buf[:n]) != "exited" {
				t.Errorf("dbgctl read %q, %v", buf[:n], err)
			}
			init.Close(dfd)

			// A trap is the prog's exception: pc, module and error.
			child, err := init.Spawn(abi.NEWFD, [][2]int{{0, 0}, {1, 1}, {2, 2}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sys.Machine.RunCode(child, "/tmp/trap.wasm", trapWasm, []string{"trap"}); err == nil {
				t.Error("trap ran")
			}
			if s, err := readFile(t, init, "/prog/"+strconv.Itoa(child.Pid)+"/exception"); err != nil || s != "0 trap unreachable" {
				t.Errorf("exception = %q, %v", s, err)
			}
			child.Exit("")

			// private closes heap and dbgctl.
			cfd, _ = init.Open("/prog/1/ctl", abi.OWRITE)
			if e := writeFd(init, cfd, "exceptions propagate"); e != "" {
				t.Error(e)
			}
			if e := writeFd(init, cfd, "exceptions maybe"); e != abi.Ebadctl {
				t.Errorf("exceptions maybe: %q", e)
			}
			if e := writeFd(init, cfd, "private"); e != "" {
				t.Error(e)
			}
			init.Close(cfd)
			if _, err := init.Open("/prog/1/heap", abi.ORDWR); core.Errstr(err) != abi.Eperm {
				t.Errorf("heap of a private prog: %v", err)
			}
		})
	}
}

// TestProf runs prof(1) over a command on #P and checks the device's
// files and errors directly.
func TestProf(t *testing.T) {
	out, st := emu(t, "", "prof")
	if out != "prof: no profile information: \n" || st != "fail:error" {
		t.Errorf("prof: %q %q", out, st)
	}
	re := regexp.MustCompile(`^[0-9a-f]{32}\t/bin/sh.wasm\n(?:[0-9a-f]{32}\t/bin/sh.wasm\n)*` +
		`\nModule: md5sum\(/bin/md5sum.wasm\)\n\n(?:[0-9]+\t[0-9]+\.[0-9]{2}\t.+\(\)\n)+` +
		`\n\*\*\*\* module sampling points [0-9]+ \*\*\*\*\n\n$`)
	outs, sts := emuModes(t, "", "prof", "-f", "-s", "1", "md5sum", "/bin/sh.wasm", "/bin/sh.wasm", "/bin/sh.wasm", "/bin/sh.wasm")
	for i := range outs {
		if !re.MatchString(outs[i]) || sts[i] != "" {
			t.Errorf("prof md5sum: %q %q", outs[i], sts[i])
		}
	}

	for _, m := range testModes() {
		t.Run(string(m), func(t *testing.T) {
			sys := progBoot(t, m)
			init := sys.Init
			ctl, err := init.Open("#P/ctl", abi.OWRITE)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := init.Open("#P/ctl", abi.ORDWR); core.Errstr(err) != abi.Eperm {
				t.Errorf("ctl ORDWR: %v", err)
			}
			for _, c := range []struct{ msg, want string }{
				{"bogus", abi.Ebadarg},
				{"interval", abi.Ebadarg},
				{"startmp1", ""},
				{"stop", ""},
				{"a b c", abi.Ebadarg},
				{"module md5sum", ""},
				{"interval 1", ""},
				{"start", ""},
			} {
				if e := writeFd(init, ctl, c.msg); e != c.want {
					t.Errorf("ctl %q: %q, want %q", c.msg, e, c.want)
				}
			}
			wait := waitOpen(t, init)
			pid, err := sys.Machine.Spawn(init, "/bin/md5sum.wasm",
				[]string{"md5sum", "/bin/sh.wasm", "/bin/sh.wasm", "/bin/sh.wasm", "/bin/sh.wasm"}, abi.NEWFD,
				[][2]int{{0, 0}, {1, 1}, {2, 2}})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, init, wait, pid)
			if e := writeFd(init, ctl, "stop"); e != "" {
				t.Fatal(e)
			}
			fd, err := init.Open("#P", abi.OREAD)
			if err != nil {
				t.Fatal(err)
			}
			ds, err := init.Dirents(fd)
			init.Close(fd)
			if err != nil || len(ds) != 2 || ds[0].Name != "ctl" || ds[1].Name != "00000001" {
				t.Fatalf("#P: %v %v", ds, err)
			}
			if s, _ := readFile(t, init, "#P/00000001/name"); s != "md5sum" {
				t.Errorf("name %q", s)
			}
			if s, _ := readFile(t, init, "#P/00000001/path"); s != "/bin/md5sum.wasm" {
				t.Errorf("path %q", s)
			}
			if _, err := init.Open("#P/00000001/pctl", abi.OREAD); core.Errstr(err) != abi.Eperm {
				t.Errorf("pctl OREAD: %v", err)
			}
			h, err := init.Open("#P/00000001/histogram", abi.OREAD)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := init.Read(h, make([]byte, 10)); core.Errstr(err) != abi.Etoosmall {
				t.Errorf("short histogram read: %v", err)
			}
			init.Close(h)
			h, _ = init.Open("#P/00000001/histogram", abi.OREAD)
			buf := make([]byte, 32)
			last, total := -1, 0
			pair := regexp.MustCompile(`^([0-9]+) ([1-9][0-9]*)$`)
			for {
				n, err := init.Read(h, buf)
				if err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					break
				}
				f := pair.FindStringSubmatch(string(buf[:n]))
				if f == nil {
					t.Fatalf("histogram %q", buf[:n])
				}
				pc, _ := strconv.Atoi(f[1])
				c, _ := strconv.Atoi(f[2])
				if pc <= last {
					t.Errorf("histogram out of order: %d after %d", pc, last)
				}
				last, total = pc, total+c
			}
			init.Close(h)
			if total == 0 {
				t.Error("no samples")
			}
			if e := writeFd(init, ctl, "end"); e != "" {
				t.Fatal(e)
			}
			if _, err := init.Open("#P/00000001/name", abi.OREAD); err == nil {
				t.Error("record outlived end")
			}
			init.Close(ctl)
		})
	}
}

func waitOpen(t *testing.T, p *core.Prog) int {
	t.Helper()
	fd, err := p.Open("/prog/"+strconv.Itoa(p.Pid)+"/wait", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func waitFor(t *testing.T, p *core.Prog, fd, pid int) {
	t.Helper()
	buf := make([]byte, 256)
	for {
		n, err := p.Read(fd, buf)
		if err != nil || n == 0 {
			t.Fatalf("wait: %v", err)
		}
		if w, _ := strconv.Atoi(strings.Fields(string(buf[:n]))[0]); w == pid {
			return
		}
	}
}
