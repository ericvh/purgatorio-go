package main

import "testing"

// TestMash covers what the corpus scripts (root/lib/test/sh/mash.sh and
// fs.sh) cannot: mash at the console, and exit statuses.
func TestMash(t *testing.T) {
	shtmp(t)
	cases := []struct {
		name, stdin string
		args        []string
		out, status string
	}{
		{"console", "echo one\nx = a \\\nb\necho $x\necho (\necho after\nexit\necho no\n", []string{"mash"},
			"mash% one\nmash% \tmash% a b\nmash% syntax error\nmash% after\nmash% ", ""},
		{"console eof", "echo one\n", []string{"mash"}, "mash% one\nmash% ", ""},
		{"prompt", "prompt\necho\nprompt '% ' '>> '\necho a \\\nb\n", []string{"mash"},
			"mash% mash% mash% \nmash% % >> a b\n% ", ""},
		{"history", "load history\necho typed\ncat /chan/history\n", []string{"mash"},
			"mash% mash% typed\nmash% load history\necho typed\nmash% ", ""},
		{"usage", "", []string{"mash", "-z"}, "usage: mash [-denx] [-c command] [src [args]]\n", "fail: mash usage"},
		{"no file", "", []string{"mash", "/nonexistent"},
			"could not open /nonexistent: file does not exist\n", "fail: mash system error"},
		{"error", "", []string{"mash", "-e", "-c", "echo $x"}, "x: undefined\n", "fail: mash error"},
		{"fail", "", []string{"mash", "-e", "-c", "cat /nonexistent; echo no"},
			"cat: cannot open /nonexistent: file does not exist\n", "fail:bad open"},
		{"no fail", "", []string{"mash", "-c", "cat /nonexistent"},
			"cat: cannot open /nonexistent: file does not exist\n", ""},
		{"make", "", []string{"mash", "-e", "-c", "load make; make x"}, "don't know how to make x\n", "fail: make error"},
		{"fs usage", "", []string{"fs"}, "", "fail:usage"},
		{"fs error", "", []string{"fs", "walk", "/nonexistent"},
			"fs: walk: cannot stat /nonexistent: file does not exist\n", "fail:error"},
		{"fs void", "", []string{"fs", "{print /lib/mashinit}"},
			"fs: walk: /lib/mashinit is not a directory\n" +
				"fs: eval: print: string cannot be converted to entries\n" +
				"fs: eval: usage: print entries -> void\n", "fail:error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emu(t, c.stdin, c.args...)
			if c.name == "fs usage" {
				out = ""
			}
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}
