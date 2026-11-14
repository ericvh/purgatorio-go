package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var shupdate = flag.Bool("shupdate", false, "rewrite the sh corpus .out files")

// shtmp makes /mnt/shtest, the scratch directory sh tests write in.
func shtmp(t *testing.T) {
	t.Helper()
	d := filepath.Join(root, "mnt", "shtest")
	if err := os.RemoveAll(d); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
}

func TestSh(t *testing.T) {
	shtmp(t)
	cases := []struct {
		name, stdin string
		args        []string
		out, status string
	}{
		{"echo", "", []string{"sh", "-c", "echo hi"}, "hi\n", ""},
		{"pipe", "", []string{"sh", "-c", "echo hi | cat"}, "hi\n", ""},
		{"pipe3", "", []string{"sh", "-c", "echo a b | cat | cat | cat"}, "a b\n", ""},
		{"seq", "", []string{"sh", "-c", "echo a; echo b"}, "a\nb\n", ""},
		{"args", "", []string{"sh", "-c", "echo $0 $*", "x", "y"}, "{echo $0 $*} x y\n", ""},
		{"emuinit", "", []string{"echo", "via", "emuinit"}, "via emuinit\n", ""},
		{"status", "", []string{"sh", "-c", "cat /nonexistent"},
			"cat: cannot open /nonexistent: file does not exist\n", "fail:bad open"},
		{"status ok", "", []string{"sh", "-c", "cat /nonexistent; echo"},
			"cat: cannot open /nonexistent: file does not exist\n\n", ""},
		{"not found", "", []string{"sh", "-c", "nosuch"},
			"sh: nosuch: file does not exist\n", "fail:file does not exist"},
		{"pipe status", "", []string{"sh", "-c", "cat /nonexistent | cat"},
			"cat: cannot open /nonexistent: file does not exist\n", "fail:bad open|"},
		{"parse error", "", []string{"sh", "-c", "echo )"},
			"sh: internal: parse error: syntax error\n", "fail:parse error"},
		{"usage", "", []string{"sh", "-c"},
			"usage: sh [-ilexn] [-c command] [file [arg...]]\n", "fail:usage"},
		{"no script", "", []string{"sh", "/nonexistent"},
			"sh: cannot open /nonexistent: file does not exist\n", "fail:bad script path"},
		{"bad module", "", []string{"sh", "-c", "load nosuch; echo no"},
			"sh: load: cannot load /dis/sh/nosuch.dis: file does not exist\n", "fail:bad module"},
		{"raise", "", []string{"sh", "-c", "load std; raise oops; echo no"}, "", "fail:oops"},
		{"exit", "", []string{"sh", "-c", "exit; echo no"}, "", ""},
		{"block pipe", "", []string{"sh", "-c", "load std; for i in 1 2 3 {echo $i} | cat"}, "1\n2\n3\n", ""},
		{"fn pipe", "", []string{"sh", "-c", "load std; fn f {echo f $*; cat}; f a | f b | cat"}, "f b\nf a\n", ""},
		{"export", "", []string{"sh", "-c", "x=(a 'b c'); sh -c 'echo $#x $x'"}, "2 a b c\n", ""},
		{"redirect", "", []string{"sh", "-c", "echo a > /mnt/shtest/f; echo b >> /mnt/shtest/f; cat < /mnt/shtest/f"},
			"a\nb\n", ""},
		{"bq", "", []string{"sh", "-c", "x=`{echo a b c}; echo $#x"}, "3\n", ""},
		{"expr", "", []string{"sh", "-c", "load expr; echo ${expr 6 7 x}"}, "42\n", ""},
		{"string", "", []string{"sh", "-c", "load string; echo ${toupper abc}"}, "ABC\n", ""},
		{"stdin script", "echo from stdin\necho $status\n", []string{"sh", "-c", "cat | sh"},
			"from stdin\n\n", ""},
		{"interactive", "echo one\ncat /nonexistent\necho $status\necho (\nthis\n)\n", []string{"sh"},
			"; one\n; cat: cannot open /nonexistent: file does not exist\n; bad open\n; this\n; ", ""},
		{"interactive error", "echo )\necho after\n", []string{"sh", "-i"},
			"; sh: stdin: parse error: syntax error\n; after\n; ", ""},
		{"prompt", "echo (a\nb)\n", []string{"sh", "-c", "prompt=('% ' '> '); sh -i < /dev/cons"},
			"% > a b\n% ", ""},
		{"interactive exception", "load std; raise x\necho $status\n", []string{"sh"}, "; ; x\n; ", ""},
		{"login", "echo hi\n", nil, "; hi\n; ", ""},
		{"hashpling", "", []string{"sh", "-c", "/mnt/shtest/script a b"}, "script /mnt/shtest/script a b\n", ""},
		{"bad header", "", []string{"sh", "-c", "/mnt/shtest/noheader"},
			"sh: bad script header on /mnt/shtest/noheader\n", "fail:bad header"},
	}
	scripts := map[string]string{"script": "#!/bin/sh\necho script $0 $*\n", "noheader": "echo\n"}
	for name, text := range scripts {
		if err := os.WriteFile(filepath.Join(root, "mnt", "shtest", name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
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

// TestShCorpus runs each script in root/lib/test/sh and compares its
// output with the .out file beside it.
func TestShCorpus(t *testing.T) {
	scripts, err := filepath.Glob(filepath.Join(root, "lib", "test", "sh", "*.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) == 0 {
		t.Fatal("no sh corpus")
	}
	for _, s := range scripts {
		name := strings.TrimSuffix(filepath.Base(s), ".sh")
		t.Run(name, func(t *testing.T) {
			shtmp(t)
			out, _ := emu(t, "", "sh", "/lib/test/sh/"+name+".sh")
			golden := strings.TrimSuffix(s, ".sh") + ".out"
			if *shupdate {
				if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if out != string(want) {
				t.Errorf("%s: output differs\ngot:\n%s\nwant:\n%s", name, out, want)
			}
		})
	}
}
