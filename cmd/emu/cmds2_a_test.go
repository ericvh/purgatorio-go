package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
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
		var out bytes.Buffer
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
