package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

// Group C works in its own scratch tree, root/tmp/cmds2c, seen in the
// emu as /tmp/cmds2c.
const cScratch = "/tmp/cmds2c"

func cScratchDir() string { return filepath.Join(root, "tmp", "cmds2c") }

// cTime is the mtime of every scratch file.
const cTime = 1000000

// cRun describes one command run in the scratch tree.
type cRun struct {
	stdin string
	// stdinFile and stdoutFile, scratch-relative, replace fd 0 and 1.
	stdinFile, stdoutFile string
	// dir, scratch-relative, is the prog's dot.
	dir string
	// files are written into the scratch tree before each mode; a name
	// ending in / is a directory.
	files map[string]string
	// check inspects the scratch tree after each mode.
	check func(t *testing.T, dir string)
}

// cReset builds the scratch tree afresh with files.
func cReset(t *testing.T, files map[string]string) {
	t.Helper()
	dir := cScratchDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		p := filepath.Join(dir, name)
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(cTime, 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
}

// emuCModes runs args on both wazero modes in a fresh scratch tree and
// returns each mode's console output and status.
func emuCModes(t *testing.T, r cRun, args ...string) (outs, sts [2]string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "bin", "emuinit.wasm")); err != nil {
		t.Fatalf("%v (run go run ./tools/build first)", err)
	}
	for i, m := range []wazero.Mode{wazero.Compiler, wazero.Interpreter} {
		cReset(t, r.files)
		var out bytes.Buffer
		h := &host.Host{In: strings.NewReader(r.stdin), Out: &out}
		sys, err := host.Boot(h, root, getEngines(t)[m])
		if err != nil {
			t.Fatal(err)
		}
		p := sys.Init
		if r.stdinFile != "" {
			p.Close(0)
			if fd, err := p.Open(cScratch+"/"+r.stdinFile, abi.OREAD); err != nil || fd != 0 {
				t.Fatalf("stdin %s: fd %d %v", r.stdinFile, fd, err)
			}
		}
		if r.stdoutFile != "" {
			p.Close(1)
			if fd, err := p.Create(cScratch+"/"+r.stdoutFile, abi.OWRITE, 0o644); err != nil || fd != 1 {
				t.Fatalf("stdout %s: fd %d %v", r.stdoutFile, fd, err)
			}
		}
		if r.dir != "" {
			if err := p.Chdir(cScratch + "/" + r.dir); err != nil {
				t.Fatal(err)
			}
		}
		st, err := sys.Machine.Run(p, "/bin/emuinit.wasm", append([]string{"emuinit"}, args...))
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		p.Exit(st)
		outs[i], sts[i] = out.String(), st
		if r.check != nil {
			r.check(t, cScratchDir())
		}
	}
	return outs, sts
}

// emuC is emuCModes for output that must agree on both modes.
func emuC(t *testing.T, r cRun, args ...string) (string, string) {
	t.Helper()
	outs, sts := emuCModes(t, r, args...)
	if outs[0] != outs[1] || sts[0] != sts[1] {
		t.Fatalf("modes disagree:\ncompiler: %q %q\ninterpreter: %q %q", outs[0], sts[0], outs[1], sts[1])
	}
	return outs[0], sts[0]
}

type cCase struct {
	name        string
	r           cRun
	args        []string
	out, status string
}

func cRunCases(t *testing.T, cases []cCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emuC(t, c.r, c.args...)
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}

func cReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cMode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func cMissing(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err == nil {
		t.Errorf("%s still exists", p)
	}
}

func TestCmds2CTsort(t *testing.T) {
	cRunCases(t, []cCase{
		{"chain", cRun{stdin: "a b\nb c\n"}, []string{"tsort"}, "a\nb\nc\n", ""},
		{"partial", cRun{stdin: "x y z\nw y\n"}, []string{"tsort"}, "w\nx\ny\nz\n", ""},
		{"cycle", cRun{stdin: "a b\nb a\n"}, []string{"tsort"},
			"tsort: cycle in input\ntsort: a\ntsort: b\na\nb\n", ""},
	})
}

func TestCmds2CBytes(t *testing.T) {
	files := map[string]string{"bin": "a\\b\xffc\xe2\x82", "t": "hello\n"}
	cRunCases(t, []cCase{
		{"range", cRun{files: files, stdinFile: "t"}, []string{"bytes", "1", "4"}, "ell", ""},
		{"to end", cRun{files: files, stdinFile: "t"}, []string{"bytes", "3", "end"}, "lo\n", ""},
		{"escapes", cRun{files: files, stdinFile: "bin"}, []string{"bytes", "0", "end"},
			"a\\\\b\\FFc\\E2\\82", ""},
		{"set", cRun{files: files, stdinFile: "t"}, []string{"bytes", "1", "3", `X\tY`}, "hX\tYlo\n", ""},
		{"set hex", cRun{files: files, stdinFile: "t"}, []string{"bytes", "0", "1", `\41`}, "Aello\n", ""},
		{"append", cRun{files: files, stdinFile: "t"}, []string{"bytes", "end", "end", "!"}, "hello\n!", ""},
		{"truncate", cRun{files: files, stdinFile: "t"}, []string{"bytes", "2", "end", "y"}, "hey", ""},
		{"order", cRun{files: files, stdinFile: "t"}, []string{"bytes", "3", "1"},
			"bytes: out of order range\n", "fail:bad range"},
		{"bad escape", cRun{files: files, stdinFile: "t"}, []string{"bytes", "0", "1", `\q`},
			"bytes: invalid backslash sequence\n", "fail:bad args"},
		{"usage", cRun{}, []string{"bytes", "1"}, "usage: bytes start end [bytes]\n", "fail:usage"},
		{"usage extra", cRun{files: files, stdinFile: "t"}, []string{"bytes", "1", "2", "a", "b"},
			"usage: bytes start end [bytes]\n", "fail:usage"},
	})
}

func TestCmds2CDd(t *testing.T) {
	files := map[string]string{"a": "hello\n", "l": "ab\ncdefg\n", "e": "hi\n"}
	cRunCases(t, []cCase{
		{"copy", cRun{files: files}, []string{"dd", "-if", cScratch + "/a"},
			"hello\n0+1 records in\n0+1 records out\n", ""},
		{"bs", cRun{files: files}, []string{"dd", "-if", cScratch + "/a", "-bs", "2"},
			"hello\n3+0 records in\n3+0 records out\n", ""},
		{"count skip", cRun{files: files}, []string{"dd", "-if", cScratch + "/a", "-bs", "2", "-skip", "1", "-count", "1"},
			"ll1+0 records in\n1+0 records out\n", ""},
		{"iseek", cRun{files: files}, []string{"dd", "-if", cScratch + "/a", "-ibs", "1", "-iseek", "4"},
			"o\n2+0 records in\n0+1 records out\n", ""},
		{"ucase", cRun{files: files}, []string{"dd", "-if", cScratch + "/a", "-conv", "ucase"},
			"HELLO\n0+1 records in\n0+1 records out\n", ""},
		{"block", cRun{files: files}, []string{"dd", "-if", cScratch + "/l", "-conv", "block", "-cbs", "4"},
			"ab  cdef0+1 records in\n0+1 records out\n1 truncated records\n", ""},
		{"unblock", cRun{files: map[string]string{"r": "ab  cd  "}}, []string{"dd", "-if", cScratch + "/r", "-conv", "unblock", "-cbs", "4"},
			"ab\ncd\n0+1 records in\n0+1 records out\n", ""},
		{"stdin", cRun{stdin: "xyz\n"}, []string{"dd", "-conv", "lcase,swab"},
			"yx\nz0+1 records in\n0+1 records out\n", ""},
		{"multiplier", cRun{files: files}, []string{"dd", "-if", cScratch + "/a", "-bs", "1x3"},
			"hello\n2+0 records in\n2+0 records out\n", ""},
		{"bad arg", cRun{}, []string{"dd", "-foo", "1"}, "dd: bad arg: -foo\n", "fail:arg"},
		{"needs value", cRun{}, []string{"dd", "-if"}, "dd: arg -if needs a value\n", "fail:arg"},
		{"zero", cRun{}, []string{"dd", "-ibs", "0"}, "dd: counts: cannot be zero\n", "fail:counts"},
		{"range", cRun{}, []string{"dd", "-bs", "4096x1024x1024"}, "dd: argument out of range\n", "fail:range"},
		{"open", cRun{}, []string{"dd", "-if", "/nonexistent"}, "dd: open /nonexistent: file does not exist\n", "fail:open"},
		{"create", cRun{}, []string{"dd", "-of", "/nonexistent/x"},
			"dd: create /nonexistent/x: file does not exist\n", "fail:create"},
		{"ebcdic", cRun{files: files, check: func(t *testing.T, dir string) {
			if got := cReadFile(t, filepath.Join(dir, "o")); got != "\x88\x89\x25" {
				t.Errorf("ebcdic %q", got)
			}
		}}, []string{"dd", "-if", cScratch + "/e", "-of", cScratch + "/o", "-conv", "ebcdic"},
			"0+1 records in\n0+1 records out\n", ""},
		{"ascii", cRun{files: map[string]string{"e": "\x88\x89\x25"}}, []string{"dd", "-if", cScratch + "/e", "-conv", "ascii"},
			"hi\n0+1 records in\n0+1 records out\n", ""},
	})
}

func TestCmds2CFc(t *testing.T) {
	cRunCases(t, []cCase{
		{"add", cRun{}, []string{"fc", "1", "2", "+"}, "3\n", ""},
		{"divide", cRun{}, []string{"fc", "10", "3", "/"}, "3.33333\n", ""},
		{"exp", cRun{}, []string{"fc", "1e6", "1e-5"}, "1e+06\n1e-05\n", ""},
		{"pow", cRun{}, []string{"fc", "2", "10", "pow"}, "1024\n", ""},
		{"bases in", cRun{}, []string{"fc", "0x10", "0b11", "017", "2r101", "16rff", "sum"}, "294\n", ""},
		{"char in", cRun{}, []string{"fc", "@A"}, "65\n", ""},
		{"hex", cRun{}, []string{"fc", "-x", "255", "-1", "0"}, "0xff\n-0x1\n0x0\n", ""},
		{"octal", cRun{}, []string{"fc", "-o", "8", "0"}, "010\n0\n", ""},
		{"binary", cRun{}, []string{"fc", "-b", "5", "256"}, "0b00000101\n0b0000000100000000\n", ""},
		{"annotated", cRun{}, []string{"fc", "-B", "5"}, "0b00000101\n  76543210\n", ""},
		{"annotated wide", cRun{}, []string{"fc", "-B", "256"},
			"0b0000000100000000\n  5432109876543210\n       1\n", ""},
		{"radix", cRun{}, []string{"fc", "-r", "16", "255"}, "16rff\n", ""},
		{"radix joined", cRun{}, []string{"fc", "-r2", "-5"}, "-2r101\n", ""},
		{"char", cRun{}, []string{"fc", "-c", "65"}, "@A\n", ""},
		{"ints", cRun{}, []string{"fc", "7", "2", "div", "7", "2", "%", "1", "4", "shl", "6", "3", "xor"}, "3\n1\n16\n5\n", ""},
		{"factorial", cRun{}, []string{"fc", "5", "!"}, "120\n", ""},
		{"rep", cRun{}, []string{"fc", "1", "2", "3", "4", "+", "rep"}, "10\n", ""},
		{"swap dup p", cRun{}, []string{"fc", "3", "4", "swap", "dup", "p"}, "3\n4\n3\n", ""},
		{"sqrt pi", cRun{}, []string{"fc", "2", "sqrt", "pi"}, "1.41421\n3.14159\n", ""},
		{"modf", cRun{}, []string{"fc", "2.5", "modf"}, "2\n0.5\n", ""},
		{"nan", cRun{}, []string{"fc", "nan", "infinity", "-∞"}, "NaN\n+Inf\n-Inf\n", ""},
		{"negative number", cRun{}, []string{"fc", "-5", "_"}, "5\n", ""},
		{"empty", cRun{}, []string{"fc"}, "", ""},
		{"small stack", cRun{}, []string{"fc", "1", "+"}, "fc: stack too small for op '+'\n", "fail:error"},
		{"bad op", cRun{}, []string{"fc", "1", "foo"}, "fc: invalid operator 'foo'\n", "fail:error"},
		{"bad rep", cRun{}, []string{"fc", "1", "2", "rep"}, "fc: invalid operator '' for rep\n", "fail:error"},
	})
	t.Run("usage", func(t *testing.T) {
		out, st := emuC(t, cRun{}, "fc", "-z")
		if !strings.HasPrefix(out, "fc: unknown option -z\nusage: fc [-xdbB] [-r radix] <postfix expression>\n") ||
			!strings.Contains(out, "operators are:\nswap dup rep sum p x × pow") || st != "fail:usage" {
			t.Errorf("got %q status %q", out, st)
		}
		out, st = emuC(t, cRun{}, "fc", "-r", "40", "1")
		if !strings.HasPrefix(out, "usage: fc ") || st != "fail:usage" {
			t.Errorf("radix 40: got %q status %q", out, st)
		}
	})
}

func TestCmds2CTimestamp(t *testing.T) {
	outs, sts := emuCModes(t, cRun{stdin: "one\ntwo\n"}, "timestamp", "note")
	re := regexp.MustCompile(`^\d{10} note start \d+\n\d{10} note one\n\d{10} note two\n$`)
	for i := range outs {
		if !re.MatchString(outs[i]) || sts[i] != "" {
			t.Errorf("got %q status %q", outs[i], sts[i])
		}
	}
}

func TestCmds2CRandpass(t *testing.T) {
	for _, c := range []struct {
		args []string
		n    int
	}{{nil, 16}, {[]string{"40"}, 40}} {
		outs, sts := emuCModes(t, cRun{}, append([]string{"randpass"}, c.args...)...)
		re := regexp.MustCompile(`^[A-Za-z0-9+/]{` + itoa(c.n) + `}\n$`)
		for i := range outs {
			if !re.MatchString(outs[i]) || sts[i] != "" {
				t.Errorf("%v: got %q status %q", c.args, outs[i], sts[i])
			}
		}
	}
	const usage = "Usage: randpass [password-length(<256, default=16)]\n"
	cRunCases(t, []cCase{
		{"short", cRun{}, []string{"randpass", "8"}, usage, "fail:usage"},
		{"long", cRun{}, []string{"randpass", "257"}, usage, "fail:usage"},
		{"word", cRun{}, []string{"randpass", "x"}, usage, "fail:usage"},
	})
}

func itoa(n int) string {
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestCmds2CUuencode(t *testing.T) {
	enc := "begin 644 rem\n&:&5L;&\\*\n \nend\n"
	cRunCases(t, []cCase{
		{"file", cRun{files: map[string]string{"a": "hello\n"}}, []string{"uuencode", cScratch + "/a", "rem"}, enc, ""},
		{"stdin", cRun{stdin: "hello\n"}, []string{"uuencode", "rem"}, strings.Replace(enc, "644", "666", 1), ""},
		{"usage", cRun{}, []string{"uuencode"}, "usage: uuencode [ sourcefile ] remotefile\n", ""},
		{"missing", cRun{}, []string{"uuencode", "/nonexistent", "x"}, "cannot open /nonexistent\n", ""},
		{"dir", cRun{files: map[string]string{"d/": ""}}, []string{"uuencode", cScratch + "/d", "x"},
			"cannot uuencode a directory\n", ""},
		{"decode -p", cRun{stdin: enc}, []string{"uudecode", "-p"}, "hello\n", ""},
		{"decode file", cRun{files: map[string]string{
			"u": "junk\nbegin 600 " + cScratch + "/out\n&:&5L;&\\*\n \nend\n"},
			check: func(t *testing.T, dir string) {
				p := filepath.Join(dir, "out")
				if got := cReadFile(t, p); got != "hello\n" {
					t.Errorf("out %q", got)
				}
				if m := cMode(t, p); m != 0o600 {
					t.Errorf("mode %o", m)
				}
			}}, []string{"uudecode", cScratch + "/u"}, "", ""},
		{"no begin", cRun{stdin: "x\n"}, []string{"uudecode"}, "no begin line\n", ""},
		{"no end", cRun{stdin: "begin 644 x\n \n"}, []string{"uudecode", "-p"}, "missing end line\n", ""},
		{"bad count", cRun{stdin: "begin 644 x\n&:&5\n"}, []string{"uudecode", "-p"}, "bad uuencode count\n", ""},
		{"bad 0", cRun{stdin: "begin 644 x\n x\n"}, []string{"uudecode", "-p"}, "bad 0 count line\n", ""},
		{"cannot open", cRun{}, []string{"uudecode", "/nonexistent"}, "cannot open /nonexistent\n", ""},
	})
}

const cText = "the quick brown fox jumps over the lazy dog\nthe quick brown fox jumps over the lazy dog\n"

func cGzip(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Name = "orig"
	w.Write([]byte(s))
	w.Close()
	return b.String()
}

func cGunzip(t *testing.T, s string) string {
	t.Helper()
	r, err := gzip.NewReader(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	r.Multistream(false)
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if r.Header.OS != 3 || r.Header.Name != "" || !r.Header.ModTime.IsZero() {
		t.Errorf("header %+v", r.Header)
	}
	return string(b)
}

func TestCmds2CGzip(t *testing.T) {
	files := map[string]string{"a": cText, "sub/b": "b\n"}
	cRunCases(t, []cCase{
		{"files", cRun{files: files, dir: ".", check: func(t *testing.T, dir string) {
			cMissing(t, filepath.Join(dir, "a"))
			cMissing(t, filepath.Join(dir, "sub", "b"))
			if got := cGunzip(t, cReadFile(t, filepath.Join(dir, "a.gz"))); got != cText {
				t.Errorf("a.gz holds %q", got)
			}
			// The output goes in the current directory.
			if got := cGunzip(t, cReadFile(t, filepath.Join(dir, "b.gz"))); got != "b\n" {
				t.Errorf("b.gz holds %q", got)
			}
		}}, []string{"gzip", "-9", cScratch + "/a", cScratch + "/sub/b"}, "", ""},
		{"stdin", cRun{files: files, stdinFile: "a", stdoutFile: "o.gz", check: func(t *testing.T, dir string) {
			if got := cGunzip(t, cReadFile(t, filepath.Join(dir, "o.gz"))); got != cText {
				t.Errorf("o.gz holds %q", got)
			}
		}}, []string{"gzip", "-1"}, "", ""},
		{"missing", cRun{files: files}, []string{"gzip", "/nonexistent"},
			"gzip: can't open /nonexistent: file does not exist\n", ""},
		{"usage", cRun{}, []string{"gzip", "-x"}, "usage: gzip [-vD1-9] [file ...]\n", "fail:usage"},
	})
	t.Run("verbose", func(t *testing.T) {
		out, st := emuC(t, cRun{files: files, dir: "."}, "gzip", "-v", "a")
		if !regexp.MustCompile(`^a: [ -]*\d+\.\d\d%\n$`).MatchString(out) || st != "" {
			t.Errorf("got %q status %q", out, st)
		}
	})
}

func TestCmds2CGunzip(t *testing.T) {
	gz := cGzip(t, cText)
	files := map[string]string{"a.gz": gz, "bad.gz": "not gzip at all", "x": "x"}
	trunc := map[string]string{"t.gz": gz[:len(gz)-6]}
	badcrc := []byte(gz)
	badcrc[len(badcrc)-8] ^= 1
	cRunCases(t, []cCase{
		{"file", cRun{files: files, dir: ".", check: func(t *testing.T, dir string) {
			cMissing(t, filepath.Join(dir, "a.gz"))
			if got := cReadFile(t, filepath.Join(dir, "a")); got != cText {
				t.Errorf("a holds %q", got)
			}
		}}, []string{"gunzip", "a.gz"}, "", ""},
		{"stdin", cRun{files: files, stdinFile: "a.gz"}, []string{"gunzip"}, cText, ""},
		{"not gzip", cRun{files: files, dir: "."}, []string{"gunzip", "bad.gz"},
			"gunzip: bad.gz: inflate error: not a gzip file\n", "fail:errors"},
		{"extension", cRun{files: files, dir: "."}, []string{"gunzip", "x", "a.gz"},
			"gunzip: .gz extension required: x\n", "fail:errors"},
		{"missing", cRun{}, []string{"gunzip", "/nonexistent.gz"},
			"gunzip: can't open /nonexistent.gz: file does not exist\n", "fail:errors"},
		{"truncated", cRun{files: trunc, stdinFile: "t.gz"}, []string{"gunzip"},
			"gunzip: stdin: inflate error: premature end of stream\n" + cText, "fail:errors"},
		{"crc", cRun{files: map[string]string{"c.gz": string(badcrc)}, stdinFile: "c.gz"}, []string{"gunzip"},
			"gunzip: stdin: inflate error: crc mismatch: computed " + crcHex(cText) + ", expected " + crcHex(cText, 1) + "\n" + cText, "fail:errors"},
	})
}

// crcHex is the CRC-32 of s in hex, with flip xored into its low byte.
func crcHex(s string, flip ...byte) string {
	gz := cGzipRaw(s)
	tr := gz[len(gz)-8:]
	v := uint32(tr[0]) | uint32(tr[1])<<8 | uint32(tr[2])<<16 | uint32(tr[3])<<24
	if len(flip) > 0 {
		v ^= uint32(flip[0])
	}
	const hex = "0123456789abcdef"
	out := ""
	for ; v > 0; v >>= 4 {
		out = string(hex[v&15]) + out
	}
	if out == "" {
		out = "0"
	}
	return out
}

func cGzipRaw(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

// cTar builds a ustar archive on the host.
func cTar(t *testing.T, ents ...tar.Header) string {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, h := range ents {
		h.Format = tar.FormatUSTAR
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(h.Linkname))
		}
		data := h.Linkname
		if h.Typeflag == tar.TypeReg {
			h.Linkname = ""
		}
		if err := w.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			w.Write([]byte(data))
		}
	}
	w.Close()
	return b.String()
}

func TestCmds2CTar(t *testing.T) {
	mt := time.Unix(2000000, 0)
	// Linkname carries a regular file's data in cTar.
	arch := cTar(t,
		tar.Header{Name: "d/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mt},
		tar.Header{Name: "d/f", Mode: 0o640, ModTime: mt, Linkname: "file f\n"},
		tar.Header{Name: "g", Mode: 0o600, ModTime: mt, Linkname: strings.Repeat("g", 700)},
		tar.Header{Name: "s", Typeflag: tar.TypeSymlink, Linkname: "g", ModTime: mt},
		tar.Header{Name: "/abs/h", Mode: 0o644, ModTime: mt, Linkname: "h\n"},
	)
	files := map[string]string{"t.tar": arch, "x/": ""}
	t.Run("lstar", func(t *testing.T) {
		out, st := emuC(t, cRun{files: files, stdinFile: "t.tar"}, "lstar")
		want := "d/ 2000000 0 0\nd/f 2000000 7 0\ng 2000000 700 0\nskipping symlink s\ns 0 0 0\n/abs/h 2000000 2 0\n"
		if out != want || st != "" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
	t.Run("gettar", func(t *testing.T) {
		out, st := emuC(t, cRun{files: files, stdinFile: "t.tar", dir: "x", check: func(t *testing.T, dir string) {
			x := filepath.Join(dir, "x")
			if got := cReadFile(t, filepath.Join(x, "d", "f")); got != "file f\n" {
				t.Errorf("d/f %q", got)
			}
			if m := cMode(t, filepath.Join(x, "d", "f")); m != 0o640 {
				t.Errorf("d/f mode %o", m)
			}
			fi, err := os.Stat(filepath.Join(x, "g"))
			if err != nil || fi.Size() != 700 || fi.ModTime().Unix() != 2000000 {
				t.Errorf("g: %v %v", fi, err)
			}
			if got := cReadFile(t, filepath.Join(x, "abs", "h")); got != "h\n" {
				t.Errorf("abs/h %q", got)
			}
		}}, "gettar", "-v")
		want := "d/f\ng\ngettar: skipping symlink s\n./abs/h\n"
		if out != want || st != "" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
	t.Run("gettar names keep", func(t *testing.T) {
		fs := map[string]string{"t.tar": arch, "x/g": "old\n"}
		out, st := emuC(t, cRun{files: fs, stdinFile: "t.tar", dir: "x", check: func(t *testing.T, dir string) {
			x := filepath.Join(dir, "x")
			if got := cReadFile(t, filepath.Join(x, "g")); got != "old\n" {
				t.Errorf("g %q", got)
			}
			cMissing(t, filepath.Join(x, "abs"))
			if got := cReadFile(t, filepath.Join(x, "d", "f")); got != "file f\n" {
				t.Errorf("d/f %q", got)
			}
		}}, "gettar", "-kv", "d", "g")
		want := "d/f\ngettar: skipping symlink s\n"
		if out != want || st != "" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
	bad := []byte(arch)
	bad[0] = 'e' // the checksum no longer matches
	cRunCases(t, []cCase{
		{"checksum", cRun{files: map[string]string{"t.tar": string(bad), "x/": ""}, stdinFile: "t.tar", dir: "x"},
			[]string{"gettar"}, "gettar: directory checksum error e/\n", "fail:error"},
		{"lstar checksum", cRun{files: map[string]string{"t.tar": string(bad)}, stdinFile: "t.tar"},
			[]string{"lstar"}, "lstar: directory checksum error e/: \n", ""},
		{"blocksize", cRun{files: map[string]string{"t.tar": arch[:700], "x/": ""}, stdinFile: "t.tar", dir: "x"},
			[]string{"gettar"}, "gettar: blocksize error\n", "fail:error"},
		{"usage", cRun{}, []string{"gettar", "-z"}, "usage: gettar [-kTRv] [file ...]\n", "fail:usage"},
		{"empty", cRun{files: map[string]string{"e": ""}, stdinFile: "e"}, []string{"lstar"}, "", ""},
	})
}

func TestCmds2CPuttar(t *testing.T) {
	files := map[string]string{"d/x": "xx\n", "d/y": strings.Repeat("y", 600), "n": "n\n", "list": "n\nd\nmissing\n"}
	var arch string
	t.Run("tree", func(t *testing.T) {
		// The modes' archives differ in the directory's qid and mtime.
		outs, sts := emuCModes(t, cRun{files: files, dir: "."}, "puttar", "d")
		for i, out := range outs {
			if sts[i] != "" || len(out)%10240 != 0 {
				t.Fatalf("status %q, %d bytes", sts[i], len(out))
			}
		}
		arch = outs[1]
		r := tar.NewReader(strings.NewReader(arch))
		var got []string
		for {
			h, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r)
			got = append(got, h.Name+" "+string(rune(h.Typeflag))+" "+itoa0(int(h.Mode))+" "+itoa0(len(b)))
			if h.Name == "d/x" && string(b) != "xx\n" {
				t.Errorf("d/x holds %q", b)
			}
			if h.Typeflag == tar.TypeReg && h.ModTime.Unix() != cTime {
				t.Errorf("%s mtime %v", h.Name, h.ModTime)
			}
		}
		want := []string{"d 5 493 0", "d/x 0 420 3", "d/y 0 420 600"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("entries %q, want %q", got, want)
		}
	})
	t.Run("list", func(t *testing.T) {
		outs, sts := emuCModes(t, cRun{files: files, dir: ".", stdinFile: "list", stdoutFile: "o.tar", check: func(t *testing.T, dir string) {
			r := tar.NewReader(strings.NewReader(cReadFile(t, filepath.Join(dir, "o.tar"))))
			var names []string
			for {
				h, err := r.Next()
				if err != nil {
					break
				}
				names = append(names, h.Name)
			}
			if strings.Join(names, ",") != "n,d" {
				t.Errorf("names %q", names)
			}
		}}, "puttar")
		for i := range outs {
			if outs[i] != "warning: puttar: cannot stat missing: file does not exist\n" || sts[i] != "" {
				t.Errorf("got %q status %q", outs[i], sts[i])
			}
		}
	})
	t.Run("roundtrip", func(t *testing.T) {
		if arch == "" {
			t.Skip("no archive")
		}
		out, st := emuC(t, cRun{files: map[string]string{"t.tar": arch, "y/": ""}, stdinFile: "t.tar", dir: "y",
			check: func(t *testing.T, dir string) {
				if got := cReadFile(t, filepath.Join(dir, "y", "d", "y")); got != strings.Repeat("y", 600) {
					t.Errorf("d/y %d bytes", len(got))
				}
			}}, "gettar")
		if out != "" || st != "" {
			t.Errorf("got %q status %q", out, st)
		}
		out, _ = emuC(t, cRun{files: map[string]string{"t.tar": arch}, stdinFile: "t.tar"}, "lstar")
		if want := "d 2000000 0 0\n"; !strings.HasPrefix(out, "d ") || !strings.Contains(out, "d/x 1000000 3 0\nd/y 1000000 600 0\n") {
			t.Errorf("lstar %q, want like %q", out, want)
		}
	})
}

func itoa0(n int) string {
	if n == 0 {
		return "0"
	}
	return itoa(n)
}
