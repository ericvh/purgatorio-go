package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/styx"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

// The text commands (ed, sed, look, fmt, freq) work in a scratch tree
// under root/tmp, which the emu sees as /tmp/cmds2a.
const aRoot = "/tmp/cmds2a"

func aDir() string { return filepath.Join(root, "tmp", "cmds2a") }

func aPath(p string) string { return aRoot + "/" + p }

// aFiles is the scratch tree each case starts from.
var aFiles = map[string]string{
	// Sorted for look -df.
	"words": "Aardvark\nabacus\napple\napple pie\napricot\nBanana\nbanana split\ncherry\n",
	// Sorted in plain byte order, with a tab-keyed line.
	"plain": "apple\napple pie\napricot\nbanana\ncherry\ndate\tfruit\n",
	"nums":  "-5\n2\n10\n10.5\n33\n",
	"text":  "one\ntwo\nthree\n",
	"para": "The quick brown fox jumps over the lazy dog. It barks!\nA second line.\n\n" +
		"    Indented text that goes on and on for a while here.\n",
	"script": "# a sed script\n/t/s/^/T:/\n2q\n",
}

func aReset(t *testing.T, extra map[string]string) {
	t.Helper()
	dir := aDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range []map[string]string{aFiles, extra} {
		for name, data := range m {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// emuA is emu, but rebuilds the scratch tree before each wazero mode.
func emuA(t *testing.T, extra map[string]string, stdin string, args ...string) (string, string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "bin", "emuinit.wasm")); err != nil {
		t.Fatalf("%v (run go run ./tools/build first)", err)
	}
	var outs, sts []string
	for _, m := range []wazero.Mode{wazero.Compiler, wazero.Interpreter} {
		aReset(t, extra)
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
		outs, sts = append(outs, out.String()), append(sts, st)
	}
	if outs[0] != outs[1] || sts[0] != sts[1] {
		t.Fatalf("modes disagree:\ncompiler: %q %q\ninterpreter: %q %q", outs[0], sts[0], outs[1], sts[1])
	}
	return outs[0], sts[0]
}

type aCase struct {
	name, stdin string
	args        []string
	out, status string
	files       map[string]string // added to the scratch tree
	after       map[string]string // scratch files expected afterwards
}

func aRun(t *testing.T, cases []aCase) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(aDir()) })
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emuA(t, c.files, c.stdin, c.args...)
			if out != c.out || st != c.status {
				t.Errorf("%q:\ngot  %q status %q\nwant %q status %q", c.args, out, st, c.out, c.status)
			}
			for name, want := range c.after {
				b, err := os.ReadFile(filepath.Join(aDir(), name))
				if err != nil {
					t.Errorf("%s: %v", name, err)
				} else if string(b) != want {
					t.Errorf("%s holds %q, want %q", name, b, want)
				}
			}
		})
	}
}

func aArgs(a ...string) []string { return a }

func TestCmds2AFreq(t *testing.T) {
	aRun(t, []aCase{
		{name: "default", args: aArgs("freq", aPath("nums")),
			out: " 10 012 0a -        5\n 45 055 2d -        1\n 46 056 2e .        1\n" +
				" 48 060 30 0        2\n 49 061 31 1        2\n 50 062 32 2        1\n" +
				" 51 063 33 3        2\n 53 065 35 5        2\n"},
		{name: "dc", args: aArgs("freq", "-dc", aPath("text")),
			out: " 10 -        3\n101 e        3\n104 h        1\n110 n        1\n111 o        2\n" +
				"114 r        1\n116 t        2\n119 w        1\n"},
		{name: "counts carry over", args: aArgs("freq", "-x", aPath("text"), aPath("text")),
			out: "0a        3\n65        3\n68        1\n6e        1\n6f        2\n72        1\n74        2\n77        1\n" +
				"0a        6\n65        6\n68        2\n6e        2\n6f        4\n72        2\n74        4\n77        2\n"},
		{name: "runes", stdin: "aé€\n", args: aArgs("freq", "-rxc"),
			out: "0a -        1\n61 a        1\ne9 é        1\n20ac €        1\n"},
		{name: "bytes", stdin: "é", args: aArgs("freq", "-o"),
			out: "251        1\n303        1\n"},
		{name: "stdin", stdin: "aa", args: aArgs("freq", "-d"), out: " 97        2\n"},
		{name: "bad option", args: aArgs("freq", "-z"),
			out: "freq: unknown option z\n", status: "fail:usage"},
		{name: "missing", args: aArgs("freq", "/nonexistent"), out: "cannot open /nonexistent\n"},
	})
}

func TestCmds2AFmt(t *testing.T) {
	aRun(t, []aCase{
		{name: "fill", args: aArgs("fmt", "-l", "30", aPath("para")),
			out: "The quick brown fox jumps over\nthe lazy dog.  It barks!  A\nsecond line.\n\n" +
				"    Indented text that goes on\n    and on for a while here.\n"},
		{name: "w", args: aArgs("fmt", "-w30", aPath("para")),
			out: "The quick brown fox jumps over\nthe lazy dog.  It barks!  A\nsecond line.\n\n" +
				"    Indented text that goes on\n    and on for a while here.\n"},
		{name: "no join", args: aArgs("fmt", "-j", "-l", "30", aPath("para")),
			out: "The quick brown fox jumps over\nthe lazy dog.  It barks!\nA second line.\n\n" +
				"    Indented text that goes on\n    and on for a while here.\n"},
		{name: "indent and files", args: aArgs("fmt", "-i", "4", "-l", "20", aPath("text"), aPath("text")),
			out: "    one two three\n\n    one two three\n"},
		{name: "tab indent", stdin: "\t\tdeep\n", args: aArgs("fmt", "-i", "2"),
			out: "\t\t  deep\n"},
		{name: "default", stdin: strings.Repeat("word ", 20) + "\n", args: aArgs("fmt"),
			out: strings.TrimSuffix(strings.Repeat("word ", 14), " ") + "\n" +
				strings.TrimSuffix(strings.Repeat("word ", 6), " ") + "\n"},
		{name: "length", args: aArgs("fmt", "-i", "30", "-l", "10"),
			out: "fmt: line length<=indentation\n", status: "fail:length"},
		{name: "missing", args: aArgs("fmt", "/nonexistent", aPath("text")),
			out: "fmt: can't open /nonexistent: file does not exist\none two three\n", status: "fail:open"},
		{name: "usage", args: aArgs("fmt", "-l"),
			out: "usage: fmt [-j] [-i indent] [-l length] [file...]\n", status: "fail:usage"},
		{name: "bad option", args: aArgs("fmt", "-q"),
			out: "usage: fmt [-j] [-i indent] [-l length] [file...]\n", status: "fail:usage"},
	})
}

func TestCmds2ALook(t *testing.T) {
	aRun(t, []aCase{
		{name: "prefix", args: aArgs("look", "-df", "ap", aPath("words")),
			out: "apple\napple pie\napricot\n"},
		{name: "fold", args: aArgs("look", "-df", "AARD", aPath("words")), out: "Aardvark\n"},
		{name: "exact", args: aArgs("look", "-dfx", "apple", aPath("words")), out: "apple\n"},
		{name: "range", args: aArgs("look", "-df", "-r", "banana", "apr", aPath("words")),
			out: "apricot\nBanana\n"},
		{name: "plain", args: aArgs("look", "ap", aPath("plain")),
			out: "apple\napple pie\napricot\n"},
		{name: "tab key", args: aArgs("look", "-x", "date", aPath("plain")), out: "date\tfruit\n"},
		{name: "no tab key", args: aArgs("look", "-x", "-tx", "date", aPath("plain")),
			status: "fail:not found"},
		{name: "numeric", args: aArgs("look", "-n", "10", aPath("nums")), out: "10\n"},
		{name: "negative", args: aArgs("look", "-n", "--", "-5", aPath("nums")), out: "-5\n"},
		{name: "interactive", stdin: "ch\nzz\nban\n", args: aArgs("look", "-i", aPath("plain")),
			out: "cherry\nbanana\n"},
		{name: "not found", args: aArgs("look", "zz", aPath("plain")), status: "fail:not found"},
		{name: "no dictionary", args: aArgs("look", "a", "/nonexistent"),
			out: "look: can't open /nonexistent\n", status: "fail:no dictionary"},
		{name: "usage", args: aArgs("look", "-q"),
			out: "usage: look -[dfinx] [-r lastkey] [-t c] [string] [file]\n", status: "fail:usage"},
	})
}

func TestCmds2ASed(t *testing.T) {
	aRun(t, []aCase{
		{name: "subst", args: aArgs("sed", "s/o/0/g", aPath("text")), out: "0ne\ntw0\nthree\n"},
		{name: "nth line", args: aArgs("sed", "-n", "2p", aPath("text")), out: "two\n"},
		{name: "dollar", args: aArgs("sed", "-n", "$p", aPath("text")), out: "three\n"},
		{name: "range", args: aArgs("sed", "/one/,/two/d", aPath("text")), out: "three\n"},
		{name: "negate", args: aArgs("sed", "-n", "/t/!p", aPath("text")), out: "one\n"},
		{name: "groups", args: aArgs("sed", `s/(t)(w|h)/[\2\1&]/`, aPath("text")),
			out: "one\n[wttw]o\n[htth]ree\n"},
		{name: "newline in rhs", args: aArgs("sed", `1s/n/\n/`, aPath("text")), out: "o\ne\ntwo\nthree\n"},
		{name: "anchors", args: aArgs("sed", "-e", "s/^/>/", "-e", "s/$/</", aPath("text")),
			out: ">one<\n>two<\n>three<\n"},
		{name: "empty match g", stdin: "ab\n", args: aArgs("sed", "s/x*/-/g"), out: "-a-b-\n"},
		{name: "g flag", stdin: "aaa\n", args: aArgs("sed", "-g", "s/a/b/"), out: "bbb\n"},
		{name: "y", stdin: "abcabc\n", args: aArgs("sed", "y/abc/bca/"), out: "bcabca\n"},
		{name: "block", args: aArgs("sed", "-n", "/t/{s/t/T/;p;}", aPath("text")), out: "Two\nThree\n"},
		{name: "branch", stdin: "a.b.c\n", args: aArgs("sed", ":x;s/\\./-/;tx"), out: "a-b-c\n"},
		{name: "hold", args: aArgs("sed", "-n", "1h;1!H;${x;p;}", aPath("text")), out: "one\ntwo\nthree\n"},
		{name: "N and P", args: aArgs("sed", "-n", "N;P", aPath("text")), out: "one\n"},
		{name: "D", args: aArgs("sed", "$!N;$!D", aPath("text")), out: "two\nthree\n"},
		{name: "append insert change", args: aArgs("sed", "-e", "1a\\\nafter", "-e", "2i\\\nbefore",
			"-e", "3c\\\nchanged", aPath("text")),
			out: "one\nafter\nbefore\ntwo\nchanged\n"},
		{name: "equals", args: aArgs("sed", "-n", "/two/=", aPath("text")), out: "2\n"},
		{name: "list", stdin: "a\tb\\\x01\n", args: aArgs("sed", "-n", "l"), out: "a\\tb\\\\u0001\n"},
		{name: "quit", args: aArgs("sed", "2q", aPath("text")), out: "one\ntwo\n"},
		{name: "script file", args: aArgs("sed", "-f", aPath("script"), aPath("text")),
			out: "one\nT:two\n"},
		{name: "read file", args: aArgs("sed", "1r "+aPath("nums"), aPath("text")),
			out: "one\n-5\n2\n10\n10.5\n33\ntwo\nthree\n"},
		{name: "write file", args: aArgs("sed", "-n", "/t/w "+aPath("out"), aPath("text")),
			after: map[string]string{"out": "two\nthree\n"}},
		{name: "s with w", args: aArgs("sed", "s/e/E/w "+aPath("out"), aPath("text")),
			out: "onE\ntwo\nthrEe\n", after: map[string]string{"out": "onE\nthrEe\n"}},
		{name: "files", args: aArgs("sed", "-n", "$p", aPath("text"), aPath("nums")), out: "33\n"},
		{name: "missing pattern", args: aArgs("sed"), out: "<stdin>:0 missing pattern\n", status: "fail:error"},
		{name: "unknown command", args: aArgs("sed", "k"),
			out: "<stdin>:2 unrecognised command: 'k' (k)\n", status: "fail:error"},
		{name: "unterminated", args: aArgs("sed", "s/x"), out: "<stdin>:2 too much text: s/x\n", status: "fail:error"},
		{name: "no rhs end", args: aArgs("sed", "s/x/y"),
			out: "<stdin>:2 no closing / in replacement text: s/x/y\n", status: "fail:error"},
		{name: "two addresses", args: aArgs("sed", "1,2="),
			out: "<stdin>:2 only one address allowed:  '1,2='\n", status: "fail:error"},
		{name: "bad regex", args: aArgs("sed", "s/(/x/"),
			out: "<stdin>:2 invalid regular expression '('\n", status: "fail:error"},
		{name: "junk", args: aArgs("sed", "p x"), out: "<stdin>:2 leftover junk:  x\n", status: "fail:error"},
		{name: "missing file", args: aArgs("sed", "p", "/nonexistent"),
			out: "<stdin>:0 can't open '/nonexistent'\n", status: "fail:error"},
		{name: "missing script", args: aArgs("sed", "-f", "/nonexistent"),
			out: "/nonexistent:0 couldn't open '/nonexistent': file does not exist\n", status: "fail:error"},
		{name: "usage", args: aArgs("sed", "-x"),
			out: "usage: sed [-ngd] [-e expr] [-f file] [expr] [file...]\n", status: "fail:usage"},
	})
}

func TestCmds2AEd(t *testing.T) {
	aRun(t, []aCase{
		{name: "print", stdin: "1,$p\n2\n$\n-\nq\n", args: aArgs("ed", aPath("text")),
			out: "14\none\ntwo\nthree\ntwo\nthree\ntwo\n"},
		{name: "number and list", stdin: "a\ntab\there \n.\n,n\n$l\nq\nQ\n", args: aArgs("ed", aPath("text")),
			out: "14\n1\tone\n2\ttwo\n3\tthree\n4\ttab\there \n" + "tab\\there \\n\n?\n"},
		{name: "substitute", stdin: "2s/w/W/p\ns/(.)(.)/\\2\\1/p\n,s/e/E/g\n,p\nw\nq\n",
			args:  aArgs("ed", aPath("text")),
			out:   "14\ntWo\nWto\nonE\nWto\nthrEE\n14\n",
			after: map[string]string{"text": "onE\nWto\nthrEE\n"}},
		{name: "nth match and split", stdin: "3s2/e/X/\n3s/h/\\\n/\n,p\nq\n", args: aArgs("ed", "-", aPath("text")),
			out: "one\ntwo\nt\nreX\n"},
		{name: "global", stdin: "g/o/s//0/\n,p\nv/0/d\n,p\nQ\n", args: aArgs("ed", "-", aPath("text")),
			out: "0ne\ntw0\nthree\n0ne\ntw0\n"},
		{name: "global delete", stdin: "g/t/d\n,p\nQ\n", args: aArgs("ed", "-", aPath("text")), out: "one\n"},
		{name: "global default print", stdin: "g/e/\nQ\n", args: aArgs("ed", "-", aPath("text")),
			out: "one\nthree\n"},
		{name: "search", stdin: "/t/\n//\n??\n?one?\n/x/\nQ\n", args: aArgs("ed", "-", aPath("text")),
			out: "two\nthree\ntwo\none\n?\n"},
		{name: "marks and moves", stdin: "3ka\n'ap\n1m$\n,p\n'a,'at0\n,p\nQ\n", args: aArgs("ed", "-", aPath("text")),
			out: "three\ntwo\nthree\none\nthree\ntwo\nthree\none\n"},
		{name: "join change insert", stdin: "1,2j\np\n2c\nTHREE\n.\n1i\nzero\n.\n,p\n=\n.=\nQ\n",
			args: aArgs("ed", "-", aPath("text")),
			out:  "onetwo\nzero\nonetwo\nTHREE\n3\n3\n"},
		{name: "undo", stdin: "2s/two/2/\nu\np\nQ\n", args: aArgs("ed", "-", aPath("text")), out: "two\n"},
		{name: "browse", stdin: "1b2\nQ\n", args: aArgs("ed", "-", aPath("text")), out: "one\ntwo\nthree\n"},
		{name: "append and write", stdin: "a\nfirst\nsecond\n.\nw " + aPath("new") + "\nf\nq\n", args: aArgs("ed"),
			out:   "13\n" + aPath("new") + "\n",
			after: map[string]string{"new": "first\nsecond\n"}},
		{name: "W appends", stdin: "W " + aPath("nums") + "\nq\n", args: aArgs("ed", aPath("text")),
			out:   "14\n14\n",
			after: map[string]string{"nums": "-5\n2\n10\n10.5\n33\none\ntwo\nthree\n"}},
		{name: "partial write keeps change", stdin: "1d\n1w " + aPath("out") + "\nq\nq\n", args: aArgs("ed", aPath("text")),
			out: "14\n4\n?\n", after: map[string]string{"out": "two\n"}},
		{name: "wq", stdin: "1d\nwq\n", args: aArgs("ed", aPath("text")),
			out: "14\n10\n", after: map[string]string{"text": "two\nthree\n"}},
		{name: "read", stdin: "r " + aPath("nums") + "\n1r " + aPath("text") + "\n,p\nQ\n", args: aArgs("ed"),
			out: "16\n14\n-5\none\ntwo\nthree\n2\n10\n10.5\n33\n"},
		{name: "no newline at end", stdin: "1p\nQ\n", args: aArgs("ed", aPath("partial")),
			files: map[string]string{"partial": "abc"},
			out:   "'\\n' appended\n4\nabc\n"},
		{name: "edit", stdin: "e " + aPath("nums") + "\n1d\ne\nE\n1p\nq\n", args: aArgs("ed", aPath("text")),
			out: "14\n16\n?\n16\n-5\n"},
		{name: "missing file", stdin: "q\n", args: aArgs("ed", "/nonexistent"), out: "?/nonexistent\n"},
		{name: "errors", stdin: "5p\nx\n0d\nk\n1,2,3p\nq\n", args: aArgs("ed", aPath("text")),
			out: "14\n?\n?\n?\n?\n?\n"},
		{name: "unsaved at end of input", stdin: "1d\n", args: aArgs("ed", aPath("text")), out: "14\n?\n"},
		{name: "o", stdin: "hello\nworld\n.\n1d\nw\nq\n", args: aArgs("ed", "-o"), out: "world\n"},
	})
}

// Scratch files for calc, units and tcs live under root/tmp/cmds2b, seen
// as /tmp/cmds2b in the emu.
func bScratch(t *testing.T, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "tmp", "cmds2b")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type bCase struct {
	name, stdin string
	args        []string
	out, status string
}

func bRun(t *testing.T, cases []bCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emu(t, c.stdin, c.args...)
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}

func TestCmds2BUnicode(t *testing.T) {
	const usage = "usage: unicode { [-t] hex hex ... | hexmin-hexmax ... | [-n] char ... }\n"
	bRun(t, []bCase{
		{"char", "", []string{"unicode", "pé"}, "0070\n00e9\n", ""},
		{"-n", "", []string{"unicode", "-n", "41"}, "0034\n0031\n", ""},
		{"hex", "", []string{"unicode", "41", "3b1"}, "A\nα\n", ""},
		{"-t", "", []string{"unicode", "-t", "41", "42"}, "AB", ""},
		{"range", "", []string{"unicode", "41-4a"},
			"0041 A\t0042 B\t0043 C\t0044 D\t0045 E\t0046 F\t0047 G\t0048 H\n0049 I\t004a J\n", ""},
		{"bad range", "", []string{"unicode", "41-3"}, "unicode: bad range 41-3\n", ""},
		{"bad value", "", []string{"unicode", "110000"}, "unicode: bad unicode value 110000\n", ""},
		{"usage", "", []string{"unicode"}, usage, ""},
	})
}

func TestCmds2BTcs(t *testing.T) {
	bScratch(t, map[string]string{
		"l1":    "caf\xe9\n",
		"l9":    "\xa4\n",
		"bad":   "x\n",
		"cs":    "x=\n\tdesc=X\n\tstob=nosuch\n",
		"quote": "x='\n",
		"u16":   "\xff\xfeh\x00i\x00",
	})
	const all = "utf-8 utf-16 utf-16be utf-16le iso-8859-1 iso-8859-15\n"
	bRun(t, []bCase{
		{"copy", "héllo\n", []string{"tcs"}, "héllo\n", ""},
		{"-l", "", []string{"tcs", "-l"}, all, ""},
		{"-lv", "", []string{"tcs", "-lv"}, "utf-8\tUnicode UTF-8\nutf-16\tUnicode UTF-16\n" +
			"utf-16be\tUnicode UTF-16 big endian\nutf-16le\tUnicode UTF-16 little endian\n" +
			"iso-8859-1\tLatin-1\niso-8859-15\tLatin-9\n", ""},
		{"aliases", "", []string{"tcs", "-l", "LATIN1"}, "iso-8859-1 latin1 l1 iso8859-1 iso_8859-1\n", ""},
		{"aliases -v", "", []string{"tcs", "-lv", "ascii"}, "Unicode UTF-8\nutf-8 utf8 ascii us-ascii\n", ""},
		{"no aliases", "", []string{"tcs", "-l", "ebcdic"}, "unknown charset ebcdic\n", ""},
		{"from latin1", "", []string{"tcs", "-f", "latin1", "/tmp/cmds2b/l1"}, "café\n", ""},
		{"bad alias", "", []string{"tcs", "-f", "8859-1", "/tmp/cmds2b/l1"},
			"tcs: 8859-1: unknown charset 8859-1\n", "fail:badarg"},
		{"latin9", "", []string{"tcs", "-f", "l9", "/tmp/cmds2b/l9", "/tmp/cmds2b/l1"}, "€\ncafé\n", ""},
		{"to latin1", "é€\n", []string{"tcs", "-t", "latin1"}, "\xe9?\n", ""},
		{"to utf16", "hi", []string{"tcs", "-t", "utf16"}, "\xfe\xff\x00h\x00i", ""},
		{"to utf16le", "hi", []string{"tcs", "-t", "utf16le"}, "h\x00i\x00", ""},
		{"from utf16", "", []string{"tcs", "-f", "utf16", "/tmp/cmds2b/u16"}, "hi", ""},
		{"bad ocs", "", []string{"tcs", "-t", "Klingon"}, "tcs: Klingon: unknown charset klingon\n", "fail:badarg"},
		{"no file", "", []string{"tcs", "/tmp/cmds2b/none"},
			"tcs: cannot open /tmp/cmds2b/none: file does not exist\n", "fail:open"},
		{"no config", "", []string{"tcs", "-C", "/tmp/cmds2b/none", "-l"},
			"convcs: bad path: file does not exist\n", "fail:init"},
		{"bad config", "", []string{"tcs", "-C", "/tmp/cmds2b/quote", "-l"},
			"convcs: /tmp/cmds2b/quote:1: missing quote\n", "fail:init"},
		{"no converter", "", []string{"tcs", "-C", "/tmp/cmds2b/cs", "-f", "x"},
			"tcs: utf8: unknown charset utf8\n", "fail:badarg"},
		{"unknown converter", "", []string{"tcs", "-C", "/tmp/cmds2b/cs", "-t", "x", "-f", "x"},
			"tcs: x: cannot load converter: nosuch: unknown converter\n", "fail:badarg"},
		{"usage", "", []string{"tcs", "-q"},
			"usage: tcs [-C configfile] [-l] [-f ics] [-t ocs] file ...\n", "fail:usage"},
	})
}

func TestCmds2BUnits(t *testing.T) {
	bScratch(t, map[string]string{
		"db":  "m\t#\nsec\t#\nfoot\t.3048 m\nfoot\t2 m\nmph\tmile/hour\n",
		"bad": "m\t#\nx\t(\nx\t(\nx\t(\nx\t(\nx\t(\nx\t(\n",
	})
	bRun(t, []bCase{
		{"inch", "inch\ncm\n", []string{"units"},
			"you have: you want: \t* 2.54\n\t/ 0.3937008\nyou have: \n", ""},
		{"man example", "15 pounds force/in²\natm\n", []string{"units"},
			"you have: you want: \t* 1.020689\n\t/ 0.9797299\nyou have: \n", ""},
		{"celsius", "100 °C\n°F\n", []string{"units"}, "you have: you want: \tis 212 °F\nyou have: \n", ""},
		{"prefix", "2 kilometers\nmiles\n", []string{"units"},
			"you have: you want: \t* 1.242742\n\t/ 0.804672\nyou have: \n", ""},
		{"dims", "km\nlb\n", []string{"units"},
			"you have: you want: \t* 2204.623 m / kg\n\t/ 0.0004535924 kg / m\nyou have: \n", ""},
		{"undefined", "frob\n", []string{"units"}, "you have: 0: frob\n\tundefined frob\nyou have: \n", ""},
		{"syntax", "(m\n", []string{"units"},
			"you have: 0: (m\n\tsyntax error, last name: m\nyou have: \n", ""},
		{"like units", "m+sec\n", []string{"units"},
			"you have: 0: m+sec\n\tadd must be like units\n0: m+sec\n\tadd must be like units\nyou have: \n", ""},
		{"file", "", []string{"units", "-v", "/tmp/cmds2b/db"},
			"m\t#\nsec\t#\nfoot\t0.3048 m\n4: foot\t2 m\n\tredefinition of foot\n" +
				"5: mph\tmile/hour\n\tundefined mile\n5: mph\tmile/hour\n\tundefined hour\nmph\t1\n" +
				"you have: \n", ""},
		{"errors", "", []string{"units", "/tmp/cmds2b/bad"},
			"2: x\t(\n\tsyntax error, last name: x\n3: x\t(\n\tsyntax error, last name: x\n" +
				"4: x\t(\n\tsyntax error, last name: x\n5: x\t(\n\tsyntax error, last name: x\n" +
				"6: x\t(\n\tsyntax error, last name: x\n7: x\t(\n\tsyntax error, last name: x\n" +
				"too many errors\n", "fail:errors"},
		{"no file", "", []string{"units", "/tmp/cmds2b/none"},
			"units: cannot open /tmp/cmds2b/none: file does not exist\n", "fail:open"},
		{"usage", "", []string{"units", "-x"}, "usage: units [-v] [file]\n", "fail:usage"},
	})
}

func TestCmds2BCalc(t *testing.T) {
	bScratch(t, map[string]string{
		"prog": "fn sq(x) { return x*x }\nprint sq(3), \"\\n\"\ninclude \"/tmp/cmds2b/inc\"\nz\n",
		"inc":  "z = 42\n",
	})
	bRun(t, []bCase{
		{"args", "", []string{"calc", "2", "+", "3", "*", "4"}, "14\n", ""},
		{"expr arg", "", []string{"calc", "1/4"}, ".25\n", ""},
		{"sigma", "sigma(i = 0, 100, 1/i!)\n", []string{"calc"}, "2.7182818284590455\n", ""},
		{"integral", "integral(x = -1.96, 1.96, exp(-0.5*x*x)/sqrt(2*Pi))\n", []string{"calc"},
			".9500042096998785\n", ""},
		{"differential", "differential(x=1, x*x+5*x+6)\n", []string{"calc"}, "7\n", ""},
		{"solve", "solve(x**2-5*x+6==0)\n", []string{"calc"}, "2 3 \n3\n", ""},
		{"no roots", "solve(x*x+1)\n", []string{"calc"}, "error: line 1: no roots found\n", ""},
		{"ack", "fn ack(a, b)\n{\n\tif(a == 0)\n\t\treturn b+1;\n\tif(b == 0)\n\t\treturn ack(a-1, 1);\n" +
			"\treturn ack(a-1, ack(a, b-1));\n}\nack(2, 3)\n", []string{"calc"}, "9\n", ""},
		{"loops", "s = 0; for(i = 1; i <= 10; i++) s += i; s\nwhile(i > 5) { i-- }\ni\ndo { i = i*2 } while(i < 100)\ni\n",
			[]string{"calc"}, "55\n5\n160\n", ""},
		{"printbase", "printbase = 16\n255\n-0.5\nprintbase = 2\n5\n", []string{"calc"}, "16rff\n-16r0.8\n2r101\n", ""},
		{"numbers", "0x1f\n017\n2r101\n1.5e3\n'a'\n1e6\n100000\n0.00001\n", []string{"calc"},
			"31\n15\n5\n1500\n97\n1e+06\n100000\n1e-05\n", ""},
		{"operators", "7 % 3\n7 // 2\n2 ** 10\n5!\n6 & 3\n6 | 3\n6 ^ 3\n1 << 4\n3 > 2 ? 10 : 20\n1 <-> 0\n",
			[]string{"calc"}, "1\n3\n1024\n120\n2\n7\n5\n16\n10\n0\n", ""},
		{"degrees", "degrees = 1\nsin 90\natan2(1, 1)\n", []string{"calc"}, "1\n45\n", ""},
		{"constants", "Pi\nInfinity\nNaN\n", []string{"calc"}, "3.141592653589793\nInfinity\nNaN\n", ""},
		{"zero divide", "7 % 0\n1/0\n", []string{"calc"}, "error: zero divide\nInfinity\n", ""},
		{"errors", "1 +\n\"str\"\n$\n", []string{"calc"},
			"error: line 1: missing expression\nerror: line 2: illegal string operation\nerror: line 3: bad character $\n", ""},
		{"args count", "sqrt(1, 2)\n", []string{"calc"}, "error: line 1: wrong number of arguments\n", ""},
		{"strict", "", []string{"calc", "-s", "y+1"}, "error: line 1: y undefined\n", ""},
		{"strict declared", "", []string{"calc", "-s", "y := 2; y+1"}, "3\n", ""},
		{"break", "break\n", []string{"calc"}, "error: line 1: break/continue not in loop\n", ""},
		{"eof", "{ 1\n", []string{"calc"}, "error: premature eof\n", ""},
		{"exit", "1\nexit\n2\n", []string{"calc"}, "1\n", ""},
		{"file", "", []string{"calc", "/tmp/cmds2b/prog"}, "9\n42\n", ""},
		{"include missing", "include \"/tmp/cmds2b/none\"\n", []string{"calc"},
			"error: line 1: cannot open /tmp/cmds2b/none\n", ""},
		{"read", "read a\n4\na*2\n", []string{"calc"}, "a ? 8\n", ""},
	})
}

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
		var out lockedBuffer
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

// These commands work in a scratch tree under root/tmp, which the emu
// sees as /tmp/cmds2d.
func dScratch() string { return filepath.Join(root, "tmp", "cmds2d") }

// dReset builds the scratch tree afresh: n holds the numbers 1 to 5, one
// per line, and d/x and d/e/y make a small tree to copy.
func dReset(t *testing.T) {
	t.Helper()
	dir := dScratch()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"n":     "1\n2\n3\n4\n5\n",
		"a":     "hello\n",
		"b":     "",
		"words": "one\ntwo\nthree\nfour\nfive\nsix\nseven\n",
		"d/x":   "xx\n",
		"d/e/y": "yy\n",
		"t/k":   "",
	}
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
}

func dRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dScratch(), name))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// emuD is emu with the scratch tree rebuilt before each wazero mode. check,
// if set, looks at the tree after each mode.
func emuD(t *testing.T, check func(t *testing.T), stdin string, args ...string) (string, string) {
	t.Helper()
	return dRun(t, check, stdin, nil, args)
}

// dRun boots a system for each wazero mode and runs one command through
// emuinit; or, given bg, starts bg in the background, waits for the file
// named by its last argument to appear, and runs each step in turn in a
// prog of its own. It returns the console output and the last status.
func dRun(t *testing.T, check func(t *testing.T), stdin string, bg []string, steps ...[]string) (string, string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "bin", "emuinit.wasm")); err != nil {
		t.Fatalf("%v (run go run ./tools/build first)", err)
	}
	var outs, sts []string
	for _, m := range []wazero.Mode{wazero.Compiler, wazero.Interpreter} {
		dReset(t)
		var out lockedBuffer
		h := &host.Host{In: strings.NewReader(stdin), Out: &out}
		sys, err := host.Boot(h, root, getEngines(t)[m])
		if err != nil {
			t.Fatal(err)
		}
		var st string
		if bg != nil {
			fds := [][2]int{{0, 0}, {1, 1}, {2, 2}}
			pid, err := sys.Machine.Spawn(sys.Init, "/bin/"+bg[0]+".wasm", bg, abi.NEWFD, fds)
			if err != nil {
				t.Fatal(err)
			}
			path := bg[len(bg)-1]
			for i := 0; ; i++ {
				if _, err := sys.Init.Stat(path); err == nil {
					break
				}
				if i == 500 {
					t.Fatalf("%s did not appear", path)
				}
				time.Sleep(10 * time.Millisecond)
			}
			for _, args := range steps {
				child, err := sys.Init.Spawn(abi.NEWFD, fds)
				if err != nil {
					t.Fatal(err)
				}
				st, err = sys.Machine.Run(child, "/bin/"+args[0]+".wasm", args)
				if err != nil {
					t.Fatalf("%s: %v", m, err)
				}
				child.Exit(st)
			}
			sys.Kernel.Progs.Kill(pid)
		} else {
			st, err = sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", append([]string{"emuinit"}, steps[0]...))
			if err != nil {
				t.Fatalf("%s: %v", m, err)
			}
		}
		sys.Init.Exit(st)
		if check != nil {
			check(t)
		}
		outs, sts = append(outs, out.String()), append(sts, st)
	}
	if outs[0] != outs[1] || sts[0] != sts[1] {
		t.Fatalf("modes disagree:\ncompiler: %q %q\ninterpreter: %q %q", outs[0], sts[0], outs[1], sts[1])
	}
	return outs[0], sts[0]
}

type dCase struct {
	name, stdin string
	args        []string
	out, status string
	check       func(t *testing.T)
}

func dRunCases(t *testing.T, cases []dCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emuD(t, c.check, c.stdin, c.args...)
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}

func dWant(name, want string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		if got := dRead(t, name); got != want {
			t.Errorf("%s holds %q, want %q", name, got, want)
		}
	}
}

func TestCmds2DXargs(t *testing.T) {
	dRunCases(t, []dCase{
		{"usage", "", []string{"xargs"},
			"Usage: xargs command [command args] <[list of last command arg]\n", "", nil},
		{"echo", "x y\n\n  z\t\nw", []string{"xargs", "echo", "a"}, "a x y\na z\na w\n", "", nil},
		{"no args", "p q\n", []string{"xargs", "echo"}, "p q\n", "", nil},
		{"missing", "x\n", []string{"xargs", "nosuch"},
			"xargs: can't load /bin/nosuch.wasm: file does not exist\n", "", nil},
		{"fails", "/nonexistent\n/lib/hello.txt\n", []string{"xargs", "cat"},
			"cat: cannot open /nonexistent: file does not exist\n", "fail:bad open", nil},
	})
}

func TestCmds2DP(t *testing.T) {
	n := "/tmp/cmds2d/n"
	dRunCases(t, []dCase{
		{"pages", "\n\n", []string{"p", "-2", n}, "1\n2\n3\n4\n5\n", "", nil},
		{"quit", "q\n", []string{"p", "-2", n}, "1\n2\n", "", nil},
		{"eof quits", "", []string{"p", "-3", n}, "1\n2\n3\n", "", nil},
		{"default", "", []string{"p", n}, "1\n2\n3\n4\n5\n", "", nil},
		{"usage", "", []string{"p", "-x", n}, "Usage: p [-number] [file...]\n", "fail:usage", nil},
		{"zero", "", []string{"p", "-0", n}, "Usage: p [-number] [file...]\n", "fail:usage", nil},
		{"missing", "", []string{"p", "/tmp/cmds2d/nosuch", n},
			"p: can't open /tmp/cmds2d/nosuch: file does not exist\n1\n2\n3\n4\n5\n", "", nil},
	})
}

func TestCmds2DStream(t *testing.T) {
	usage := "Usage: stream [-a] [-b bufsize] file1 [file2]\n"
	dRunCases(t, []dCase{
		{"file", "", []string{"stream", "/lib/hello.txt"}, "hello from the root\n", "", nil},
		{"bufsize", "", []string{"stream", "-b", "3", "/lib/hello.txt"}, "hello from the root\n", "", nil},
		{"bufsize joined", "", []string{"stream", "-b3", "/lib/hello.txt"}, "hello from the root\n", "", nil},
		{"usage", "", []string{"stream"}, usage, "fail:usage", nil},
		{"bad option", "", []string{"stream", "-x", "/lib/hello.txt"}, usage, "fail:usage", nil},
		{"missing b", "", []string{"stream", "-b"}, usage, "fail:usage", nil},
		{"missing", "", []string{"stream", "/nonexistent"},
			"stream: can't open /nonexistent: file does not exist\n", "fail:open", nil},
		{"two files", "", []string{"stream", "/tmp/cmds2d/a", "/tmp/cmds2d/b"}, "", "",
			func(t *testing.T) { dWant("b", "hello\n")(t); dWant("a", "hello\n")(t) }},
	})
}

func TestCmds2DChgrp(t *testing.T) {
	dRunCases(t, []dCase{
		{"usage", "", []string{"chgrp"}, "usage: chgrp [-uo] group file ...\n", "fail:usage", nil},
		{"bad option", "", []string{"chgrp", "-x", "g", "f"},
			"usage: chgrp [-uo] group file ...\n", "fail:usage", nil},
		{"no files", "", []string{"chgrp", "g"}, "", "", nil},
		{"missing", "", []string{"chgrp", "-u", "u", "/tmp/cmds2d/nosuch", "/tmp/cmds2d/nosuch2"},
			"chgrp: can't change /tmp/cmds2d/nosuch: file does not exist\n" +
				"chgrp: can't change /tmp/cmds2d/nosuch2: file does not exist\n", "fail:error", nil},
	})
}

func TestCmds2DMc(t *testing.T) {
	dRunCases(t, []dCase{
		{"columns", "", []string{"mc", "/tmp/cmds2d/words"},
			"one   two   three four  five  six   seven\n", "", nil},
		{"narrow", "", []string{"mc", "-c", "13", "/tmp/cmds2d/words"},
			"one   five\ntwo   six\nthree seven\nfour\n", "", nil},
		{"one column", "", []string{"mc", "-c1", "/tmp/cmds2d/n"}, "1\n2\n3\n4\n5\n", "", nil},
		{"stdin", "a\nbb\nc\n", []string{"mc"}, "a  bb c\n", "", nil},
		{"two files", "", []string{"mc", "/tmp/cmds2d/a", "/tmp/cmds2d/d/x"}, "hello xx\n", "", nil},
		{"missing", "", []string{"mc", "/tmp/cmds2d/nosuch", "/tmp/cmds2d/a"},
			"mc: can't open /tmp/cmds2d/nosuch: file does not exist\nhello\n", "", nil},
		{"usage", "", []string{"mc", "-z"}, "usage: mc [-c columns] [file ...]\n", "fail:usage", nil},
		{"no arg", "", []string{"mc", "-c"}, "usage: mc [-c columns] [file ...]\n", "fail:usage", nil},
	})
}

func TestCmds2DFcp(t *testing.T) {
	s := "/tmp/cmds2d/"
	usage := "usage: \tfcp [-r] [-R nproc] [-W nproc] src target\n\tfcp [-r] [-R nproc] [-W nproc] src ... directory\n"
	dRunCases(t, []dCase{
		{"file", "", []string{"fcp", s + "n", s + "m"}, "", "", dWant("m", "1\n2\n3\n4\n5\n")},
		{"serial", "", []string{"fcp", "-R", "1", "-W1", s + "n", s + "m"}, "", "", dWant("m", "1\n2\n3\n4\n5\n")},
		{"into dir", "", []string{"fcp", s + "n", s + "a", s + "d"}, "", "",
			func(t *testing.T) { dWant("d/n", "1\n2\n3\n4\n5\n")(t); dWant("d/a", "hello\n")(t) }},
		{"not a dir", "", []string{"fcp", s + "n", s + "a", s + "b"},
			"fcp: " + s + "b  not a directory\n", "fail:error", nil},
		{"same", "", []string{"fcp", s + "a", s + "a"},
			"fcp: " + s + "a and " + s + "a are the same file\n", "fail:error", nil},
		{"directory", "", []string{"fcp", s + "d", s + "z"},
			"fcp: " + s + "d is a directory\n", "fail:error", nil},
		{"missing", "", []string{"fcp", s + "nosuch", s + "z"},
			"fcp: " + s + "nosuch: file does not exist\n", "fail:error", nil},
		{"recursive", "", []string{"fcp", "-r", s + "d", s + "c"}, "", "",
			func(t *testing.T) { dWant("c/x", "xx\n")(t); dWant("c/e/y", "yy\n")(t) }},
		{"recursive into", "", []string{"fcp", "-r", s + "d", s + "a", s + "t"}, "", "",
			func(t *testing.T) { dWant("t/d/e/y", "yy\n")(t); dWant("t/a", "hello\n")(t) }},
		{"into itself", "", []string{"fcp", "-r", s + "d", s + "d"},
			"fcp: cannot copy " + s + "d into itself\n", "fail:error", nil},
		{"onto file", "", []string{"fcp", "-r", s + "d", s + "a"},
			"fcp: " + s + "a: destination not a directory\n", "fail:error", nil},
		{"usage", "", []string{"fcp", s + "a"}, usage, "fail:usage", nil},
		{"bad R", "", []string{"fcp", "-R", "0", s + "a", s + "z"}, usage, "fail:usage", nil},
	})
}

func TestCmds2DLogfile(t *testing.T) {
	usage := "usage: logfile [-size] file\n"
	dRunCases(t, []dCase{
		{"usage", "", []string{"logfile"}, usage, "fail: usage", nil},
		{"two", "", []string{"logfile", "a", "b"}, usage, "fail: usage", nil},
		{"zero", "", []string{"logfile", "-0", "f"}, "logfile: can't have a zero buffer size\n" + usage, "fail: usage", nil},
		{"bind", "", []string{"logfile", "/nonexistent/log"},
			"logfile: bind #s failed: file does not exist\n", "", nil},
	})
	log := "/tmp/cmds2d/log"
	t.Run("keeps the last bytes", func(t *testing.T) {
		out, st := dRun(t, nil, "q\n", []string{"logfile", "-8", log},
			[]string{"fcp", "-R1", "-W1", "/tmp/cmds2d/a", log},
			[]string{"fcp", "-R1", "-W1", "/tmp/cmds2d/d/x", log},
			[]string{"p", "-2", log})
		if want := "ello\nxx\n"; out != want || st != "" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
	t.Run("too long", func(t *testing.T) {
		out, st := dRun(t, nil, "", []string{"logfile", "-4", log},
			[]string{"fcp", "-R1", "-W1", "/tmp/cmds2d/a", log})
		if want := "fcp: error writing " + log + ": write too long for buffer\n"; out != want || st != "fail:error" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
}

// dStyxServer listens on a local TCP port and, for each of two
// connections in turn (one per wazero mode), reads
// n T-messages, then answers each: Rversion, Rattach, Rread of "hi" for
// Tread, and Rerror for the rest. It returns the port.
func dStyxServer(t *testing.T, n int) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer l.Close()
		for range 2 {
			c, err := l.Accept()
			if err != nil {
				return
			}
			dServe(c, n)
		}
	}()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

func dServe(c net.Conn, n int) {
	defer c.Close()
	var ts []*styx.Fcall
	for range n {
		var sz [4]byte
		if _, err := io.ReadFull(c, sz[:]); err != nil {
			return
		}
		b := make([]byte, binary.LittleEndian.Uint32(sz[:]))
		copy(b, sz[:])
		if _, err := io.ReadFull(c, b[4:]); err != nil {
			return
		}
		f, err := styx.Unmarshal(b, 0)
		if err != nil {
			return
		}
		ts = append(ts, f)
	}
	for _, f := range ts {
		r := &styx.Fcall{Tag: f.Tag}
		switch f.Type {
		case styx.Tversion:
			r.Type, r.Msize, r.Version = styx.Rversion, f.Msize, f.Version
		case styx.Tattach:
			r.Type, r.Qid = styx.Rattach, styx.Qid{Type: styx.QTDIR, Path: 0x10}
		case styx.Tread:
			r.Type, r.Data = styx.Rread, []byte("hi\x01")
		default:
			r.Type, r.Ename = styx.Rerror, "permission denied"
		}
		b, _ := styx.Marshal(nil, r)
		c.Write(b)
	}
}

func TestCmds2DStyxchat(t *testing.T) {
	usage := "usage: styxchat [-nsv] [-m messagesize] [dest]\n"
	dRunCases(t, []dCase{
		{"usage", "", []string{"styxchat", "-x"}, usage, "fail:usage", nil},
		{"two dests", "", []string{"styxchat", "a", "b"}, usage, "fail:usage", nil},
		{"missing", "", []string{"styxchat", "/nonexistent"},
			"styxchat: can't open /nonexistent: file does not exist\n", "fail:error", nil},
	})
	t.Run("client", func(t *testing.T) {
		in := "# a comment\n" +
			"Tversion 8192 9P2000\n" +
			"Tbogus\n" +
			"Tattach 1 nofid me ''\n" +
			"Tclunk\n" +
			"nexttag\n" +
			"Twalk 1 2 a 'b c'\n" +
			"Twrite 2 0 hello\n" +
			"Tread 2 0 16r10\n" +
			"\n"
		port := dStyxServer(t, 5)
		out, st := emu(t, in, "styxchat", "-v", "-n", "tcp!127.0.0.1!"+port)
		want := "-> Tmsg.Version(65535,8192,\"9P2000\")\n" +
			"?unknown message type\n" +
			"-> Tmsg.Attach(1,1,4294967295,\"me\",\"\")\n" +
			"?usage: Tclunk fid\n" +
			"?next tag is 2\n" +
			"-> Tmsg.Walk(2,1,2,array[] of {\"a\",\"b c\"})\n" +
			"-> Tmsg.Write(3,2,0,array[5] of byte)\n" +
			"\ttext(\"hello\")\n" +
			"-> Tmsg.Read(4,2,0,16)\n" +
			"<- Rmsg.Version(65535,8192,\"9P2000\")\n" +
			"<- Rmsg.Attach(1,Qid(16r10,0,16r80))\n" +
			"<- Rmsg.Error(2,\"permission denied\")\n" +
			"<- Rmsg.Error(3,\"permission denied\")\n" +
			"<- Rmsg.Read(4,array[3] of byte)\n" +
			"\tdump(686901)\n" +
			"styxchat: server hungup\n"
		if out != want || st != "" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
	t.Run("bad reply", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		go func() {
			for range 2 {
				c, err := l.Accept()
				if err != nil {
					return
				}
				var sz [4]byte
				io.ReadFull(c, sz[:])
				io.ReadFull(c, make([]byte, binary.LittleEndian.Uint32(sz[:])-4))
				c.Write([]byte{3, 0, 0, 0})
				defer c.Close()
			}
		}()
		port := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
		out, st := emu(t, "Tversion 8192 9P2000\n", "styxchat", "-n", "tcp!127.0.0.1!"+port)
		want := "-> Tmsg.Version(65535,8192,\"9P2000\")\n" +
			"<- Rmsg.Readerror(0,\"invalid 9P message size\")\n"
		if out != want || st != "killed" {
			t.Errorf("got %q status %q, want %q status killed", out, st, want)
		}
	})
	t.Run("server", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
		l.Close()
		in := "Rversion 65535 8192 9P2000\n" +
			"Rattach 1 16r20.3.d\n" +
			"Rwalk 2\n" +
			"Rstat 3 1.0.a 8r644 0 10 5 f u g m\n" +
			"Rbogus\n" +
			"Rwstat 4\n"
		// The client reads the four replies, then sends three requests
		// and hangs up; once for each wazero mode. A connection that
		// fails (it can reach the first run's listener as it closes) is
		// tried again.
		session := func() bool {
			c, err := net.Dial("tcp", "127.0.0.1:"+port)
			if err != nil {
				return false
			}
			defer c.Close()
			for range 4 {
				var sz [4]byte
				if _, err := io.ReadFull(c, sz[:]); err != nil {
					return false
				}
				b := make([]byte, binary.LittleEndian.Uint32(sz[:])-4)
				if _, err := io.ReadFull(c, b); err != nil {
					return false
				}
			}
			for _, f := range []*styx.Fcall{
				{Type: styx.Tversion, Tag: styx.NOTAG, Msize: 8192, Version: "9P2000"},
				{Type: styx.Topen, Tag: 1, Fid: 7, Mode: 1},
				{Type: styx.Tcreate, Tag: 2, Fid: 7, Name: "new", Perm: 0o755, Mode: 2},
			} {
				b, _ := styx.Marshal(nil, f)
				c.Write(b)
			}
			return true
		}
		done := make(chan bool, 1)
		go func() {
			for range 2 {
				for i := 0; !session(); i++ {
					if i == 1000 {
						done <- false
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			done <- true
		}()
		out, st := emu(t, in, "styxchat", "-s", "-n", "tcp!127.0.0.1!"+port)
		if !<-done {
			t.Fatal("client could not finish")
		}
		want := "-> Rmsg.Version(65535,8192,\"9P2000\")\n" +
			"-> Rmsg.Attach(1,Qid(16r20,3,16r80))\n" +
			"-> Rmsg.Walk(2,array[] of {})\n" +
			"-> Rmsg.Stat(3,Dir(\"f\",\"u\",\"g\",Qid(16r1,0,16r40),8r10000000644,0,10,5,16r0,0))\n" +
			"?unknown message type\n" +
			"?usage: Rwstat tag\n" +
			"<- Tmsg.Version(65535,8192,\"9P2000\")\n" +
			"<- Tmsg.Open(1,7,1)\n" +
			"<- Tmsg.Create(2,7,\"new\",8r755,2)\n" +
			"styxchat: clients hungup\n"
		if out != want || st != "" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
}
