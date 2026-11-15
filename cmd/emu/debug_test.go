package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
)

// The debugging tools of docs/debug.md: stack, broke, trace, mdb, cprof,
// mprof, wasmdump and wasmdep.

func debugDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(root, "tmp", "debugtest")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "trap.wasm"), trapWasm, 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestStack(t *testing.T) {
	frame := regexp.MustCompile(`^(unknown fn\(\) Module \$(wasi_snapshot_preview1|purgatorio) PC [0-9]+|[^ ]+\(\) [^ ]+:[0-9]+)$`)
	// sleep is in its host call for most of 100 seconds.
	outs, sts := emuModes(t, "", "sh", "-c", "sleep 100 & pid=$apid; sleep 1; stack $pid; echo; stack -v $pid; kill $pid")
	for i := range outs {
		s, v, _ := strings.Cut(outs[i], "\n\n")
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		if sts[i] != "" || len(lines) < 4 || !strings.HasPrefix(lines[0], "unknown fn() Module $purgatorio PC ") ||
			lines[1] != "github.com/ericvh/purgatorio-go/sdk/sys.sleep() github.com/ericvh/purgatorio-go/sdk/sys/sys_wasip1.go:80" ||
			lines[len(lines)-1] != "_rt0_wasm_wasip1() runtime/rt0_wasip1_wasm.s:16" ||
			!strings.Contains(s, "\nmain.main() github.com/ericvh/purgatorio-go/appl/cmd/sleep/main.go:") {
			t.Errorf("stack: %q %q", outs[i], sts[i])
		}
		for _, l := range lines {
			if !frame.MatchString(l) {
				t.Errorf("stack line %q", l)
			}
		}
		if !regexp.MustCompile(`\n\tpc=[0-9]+ offset=[0-9]+ module=/bin/sleep.wasm\n`).MatchString(v) {
			t.Errorf("stack -v: %q", v)
		}
	}
	for _, c := range []struct{ args, out, st string }{
		{"stack 99999", "stack: can't open heap file: file does not exist\n", ""},
		{"stack", "usage: stack [-v] pid\n", "fail:usage"},
		// No prog is broken, so broke kills nothing.
		{"broke", "", ""},
	} {
		if out, st := emu(t, "", strings.Fields(c.args)...); out != c.out || st != c.st {
			t.Errorf("%s: %q %q", c.args, out, st)
		}
	}
}

// A trapping guest is left broken, as in Inferno: ps shows it, and broke
// kills it and prints its pid. sh hears of the kill too, as Inferno's
// does, and may say so before or after broke prints.
func TestBroke(t *testing.T) {
	debugDir(t)
	outs, sts := emuModes(t, "", "sh", "-c", "/tmp/debugtest/trap.wasm >[2] /dev/null; ps | grep -s ' broken .* trap$'; echo ps $status; broke; broke; ps | grep -s trap; echo gone $status")
	for i := range outs {
		lines := strings.Split(strings.TrimSuffix(outs[i], "\n"), "\n")
		slices.Sort(lines)
		if want := []string{" 2", "gone |no matches", "ps ", `sh: 2 "trap":killed`}; sts[i] != "" || !slices.Equal(lines, want) {
			t.Errorf("broke: %q %q", outs[i], sts[i])
		}
	}
}

func TestTrace(t *testing.T) {
	debugDir(t)
	outs, sts := emuModes(t, "", "trace", "echo", "hi")
	call := regexp.MustCompile(`^[0-9]+: [^ ]+\(\) [^ ]+:[0-9]+$`)
	for i := range outs {
		if sts[i] != "" || !regexp.MustCompile(`\n[0-9]+: syscall.fd_write\(\) syscall/fs_wasip1.go:[0-9]+\nhi\n$`).MatchString(outs[i]) {
			t.Errorf("trace echo: %q %q", outs[i], sts[i])
		}
		for _, l := range strings.Split(strings.TrimSuffix(outs[i], "hi\n"), "\n") {
			if l != "" && !call.MatchString(l) {
				t.Errorf("trace line %q", l)
			}
		}
	}
	outs, sts = emuModes(t, "", "trace", "-v", "cat", "/dev/null")
	for i := range outs {
		if sts[i] != "" || !strings.Contains(outs[i], ": syscall.path_open() ") ||
			!strings.Contains(outs[i], "\n\tmain.main() github.com/ericvh/purgatorio-go/appl/cmd/cat/main.go:") {
			t.Errorf("trace -v cat: %q %q", outs[i], sts[i])
		}
	}
	outs, sts = emuModes(t, "", "trace", "/tmp/debugtest/trap.wasm")
	for i := range outs {
		if !regexp.MustCompile(`\ntrace: [0-9]+ "trap": broken: [^\n]*unreachable\n`).MatchString(outs[i]) ||
			!strings.Contains(sts[i], "unreachable") {
			t.Errorf("trace trap: %q %q", outs[i], sts[i])
		}
	}
	for _, c := range []struct{ args, out, st string }{
		{"trace", "usage: trace [-v] command [arg ...]\n", "fail:usage"},
		{"trace nosuch", "trace: nosuch: file does not exist\n", "fail:exec"},
	} {
		if out, st := emu(t, "", strings.Fields(c.args)...); out != c.out || st != c.st {
			t.Errorf("%s: %q %q", c.args, out, st)
		}
	}
}

func TestMdb(t *testing.T) {
	d := debugDir(t)
	if err := os.WriteFile(filepath.Join(d, "f"), []byte("hello, world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := "/tmp/debugtest/f"
	if out, st := emu(t, "0,4/X\n0/cccccC\n=D\n0x10=X\n5,2/b\n(1+2)*2=d\n/n\nx\n", "mdb", f); st != "" ||
		out != "0x6c6c6568 0x77202c6f 0x646c726f 0x0000000a \nhello,\n6        \n0x00000010 \n0x2c 0x20 \n6    \n\n\ninvalid cmd: x\n" {
		t.Errorf("mdb: %q %q", out, st)
	}
	if out, st := emu(t, "", "mdb", "-w", f, "0/w 0x4948 0x4b4a"); out != "" || st != "" {
		t.Errorf("mdb -w: %q %q", out, st)
	}
	if out, _ := emu(t, "", "mdb", f, "0,13/C"); out != `HIJKo, world\n`+"\n" {
		t.Errorf("mdb after write: %q", out)
	}
	if out, st := emu(t, "", "mdb", f, "0/O"); out != "011322444510 \n" || st != "" {
		t.Errorf("mdb /O: %q %q", out, st)
	}
	trap := "/tmp/debugtest/trap.wasm"
	for _, c := range []struct{ cmd, out string }{
		{"$h", "6d736100 Version 1 wasm module\n       1 types\n       0 imports\n       1 functions\n" +
			"       1 exports\n       0 data segments\n      -1 start function\n\nModule is Insecure\n"},
		{"$l", "\tlink func 0, \"_start\"\n"},
		{"$D", "\ttype $0, ()\n"},
		{"$i", ""},
		{"0?i", "#0\n\t_start ()\n"},
	} {
		if out, st := emu(t, "", "mdb", trap, c.cmd); out != c.out || st != "" {
			t.Errorf("mdb %s: %q %q", c.cmd, out, st)
		}
	}
	for _, c := range []struct{ args, out, st string }{
		{"mdb", "usage: mdb [-w] file [command]\n", "fail:usage"},
		{"mdb -x f", "usage: mdb [-w] file [command]\n", "fail:usage"},
		{"mdb /nonexist", "mdb: cannot open /nonexist: file does not exist\n", "fail:cannot open"},
	} {
		if out, st := emu(t, "", strings.Fields(c.args)...); out != c.out || st != c.st {
			t.Errorf("%s: %q %q", c.args, out, st)
		}
	}
}

func TestWasmdump(t *testing.T) {
	debugDir(t)
	trap := "/tmp/debugtest/trap.wasm"
	want := "module " + trap + "\nsize 37\n" +
		"digest 187e8d9bf0a3454a6765f0eb83f1398f350a5c053bd3907b430dd05f4c91134f\nabi -\n" +
		"sections 4\n\ttype offset 8 size 4\n\tfunction offset 14 size 2\n\texport offset 18 size 10\n\tcode offset 30 size 5\n" +
		"imports 0\nexports 1\n\tfunc 0 _start ()\n" +
		"functions 1 imported 0\nstart -\nelements 0\ndata 0\n"
	if out, st := emu(t, "", "wasmdump", trap); out != want || st != "" {
		t.Errorf("wasmdump: %q %q", out, st)
	}
	if out, st := emu(t, "", "wasmdump", "-f", trap); out != "module "+trap+"\nfunc 0 3 () _start\n" || st != "" {
		t.Errorf("wasmdump -f: %q %q", out, st)
	}
	out, st := emu(t, "", "wasmdump", "-hic", "/bin/echo.wasm")
	if st != "" || !strings.HasPrefix(out, "module /bin/echo.wasm\nsize ") ||
		!strings.Contains(out, "\nabi "+abi.Version+"\nimports ") ||
		!strings.Contains(out, "\n\twasi_snapshot_preview1\n\t\tfunc 0 ") ||
		!strings.Contains(out, "\n\tpurgatorio\n\t\tfunc ") ||
		!strings.HasSuffix(out, "\ncustom purgatorio.abi "+abi.Version+"\n") {
		t.Errorf("wasmdump -hic: %q %q", out, st)
	}
	out, _ = emu(t, "", "wasmdump", "-f", "/bin/echo.wasm")
	if !strings.Contains(out, " () -> i32 wasi_snapshot_preview1.sched_yield\n") || !strings.Contains(out, " main.main\n") {
		t.Errorf("wasmdump -f echo: %q", out)
	}
	if out, st := emu(t, "", "wasmdump", "/dev/null", trap); out != "wasmdump: /dev/null: not a wasm module\n"+want || st != "fail:errors" {
		t.Errorf("wasmdump /dev/null: %q %q", out, st)
	}
	if out, st := emu(t, "", "wasmdump"); out != "usage: wasmdump [-hsiemcf] file ...\n" || st != "fail:usage" {
		t.Errorf("wasmdump: %q %q", out, st)
	}

	if out, st := emu(t, "", "wasmdep", trap); out != "abi -\n" || st != "" {
		t.Errorf("wasmdep trap: %q %q", out, st)
	}
	out, st = emu(t, "", "wasmdep", trap, "/bin/sh.wasm", "/nonexist")
	if st != "fail:errors" || !strings.HasPrefix(out, "module "+trap+"\nabi -\nmodule /bin/sh.wasm\nabi "+abi.Version+"\nimport wasi_snapshot_preview1 ") ||
		!strings.Contains(out, "\nimport purgatorio spawn_prog\n") ||
		!strings.HasSuffix(out, "\nmodule /nonexist\nwasmdep: /nonexist: file does not exist\n") {
		t.Errorf("wasmdep: %q %q", out, st)
	}
}

func TestCprof(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{32}\t/bin/sh.wasm\n` +
		`\nModule: md5sum\(/bin/md5sum.wasm\)\t[0-9]+% coverage\n\n(?:[0-9]+\t[-+]\t.+\(\)\n)+` +
		`\n\*\*\*\* module function calls [0-9]+ \*\*\*\*\n\n$`)
	outs, sts := emuModes(t, "", "cprof", "-e", "md5sum", "/bin/sh.wasm")
	for i := range outs {
		if !re.MatchString(outs[i]) || sts[i] != "" || !strings.Contains(outs[i], "\t+\tmain.main()\n") ||
			!strings.Contains(outs[i], "\t-\t") {
			t.Errorf("cprof: %q %q", outs[i], sts[i])
		}
	}
	outs, _ = emuModes(t, "", "cprof", "-f", "-n", "-m", "md5sum", "md5sum", "/bin/sh.wasm")
	for i := range outs {
		if !regexp.MustCompile(`\ngithub.com/ericvh/purgatorio-go/appl/cmd/md5sum/main.go:[0-9]+\t[1-9][0-9]*\tmain.main\(\)\n`).MatchString(outs[i]) ||
			!strings.Contains(outs[i], "\t0\t") {
			t.Errorf("cprof -f: %q", outs[i])
		}
	}
	if out, st := emu(t, "", "cprof"); out != "cprof: no profile information: \n" || st != "fail:error" {
		t.Errorf("cprof: %q %q", out, st)
	}
	if out, st := emu(t, "", "cprof", "-z"); out != "cprof: unknown option -z\nusage: cprof [-fne] [-m modname]... cmd [arg ... ]\n" || st != "fail:usage" {
		t.Errorf("cprof -z: %q %q", out, st)
	}
}

func TestMprof(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{32}\t/bin/sh.wasm\n` +
		`\nModule: md5sum\(/bin/md5sum.wasm\)\n\n(?:[0-9]+\t([0-9]+)\t([0-9]+)\t.+\(\)\n)+` +
		`\nModule totals\t([1-9][0-9]*)\t([1-9][0-9]*)\n\n$`)
	outs, sts := emuModes(t, "", "mprof", "-e", "md5sum", "/bin/sh.wasm")
	for i := range outs {
		m := re.FindStringSubmatch(outs[i])
		if m == nil || sts[i] != "" || m[3] != m[4] || !strings.Contains(outs[i], "\truntime.growMemory()\n") {
			t.Errorf("mprof: %q %q", outs[i], sts[i])
		}
	}
	outs, _ = emuModes(t, "", "mprof", "-M", "-m", "md5sum", "md5sum", "/bin/sh.wasm")
	for i := range outs {
		if !regexp.MustCompile(`\nModule: md5sum\(/bin/md5sum.wasm\)\n\nModule totals\t[1-9][0-9]*\t[1-9][0-9]*\n\n$`).MatchString(outs[i]) {
			t.Errorf("mprof -M: %q", outs[i])
		}
	}
	for _, c := range []struct{ args, out, st string }{
		{"mprof", "mprof: no profile information: \n", "fail:error"},
		{"mprof -b", "", ""},
		{"mprof -c", "", ""},
		{"mprof -bc", "mprof: warning: -e option ignored\n", ""},
	} {
		if out, st := emu(t, "", strings.Fields(c.args)...); out != c.out || st != c.st {
			t.Errorf("%s: %q %q", c.args, out, st)
		}
	}
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}
