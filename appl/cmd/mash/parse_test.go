package main

import (
	"strings"
	"testing"
)

// parseall parses src and returns the text of each top level command,
// as -d prints it.
func parseall(src string) []string {
	e := newEnv()
	e.stderr = -1
	e.sopen(src)
	var out []string
	p := &parser{e: e, run1: func(c *Cmd) {
		s := c.text()
		if s != "" && s[len(s)-1] != '&' {
			s += ";"
		}
		out = append(out, s)
	}}
	p.run()
	return out
}

func TestParse(t *testing.T) {
	cases := []struct{ in, out string }{
		{"echo hello world", "echo hello world;"},
		{"a; b & c", "a;|b&|c;"},
		{"a\nb", "a b;"},
		{"a | b | c", "a | b | c;"},
		{"x = a b; y := c", "x = a b;|y := c;"},
		{"a > f < h", "a > f < h;"},
		{"a <> f", "a <> f;"},
		{"echo `{a} \"{b} <{c} >{d}", "echo `{ a; } \"{ b; } <{ c; } >{ d; };"},
		{"echo $x $\"y $1", "echo $x $\"y $1;"},
		{"echo -$x.b", "echo -$x.b;"},
		{"echo a^b ^ c", "echo a ^ b ^ c;"},
		{"echo 'it\\'s' 'a b'", "echo 'it\\'s' 'a b';"},
		{"echo (hd $x :: tl $x ^ y == z)", "echo (hd $x :: tl $x ^ y == z);"},
		{"if (a) b else c", "if (a) b else c;"},
		{"if (a) b | c", "if (a) b | c;"},
		{"for (i in a b) echo $i", "for (i in a b) echo $i;"},
		{"while (! $x) x = 1", "while (!$x) x = 1;"},
		{"case $x { a => b; c :: d => e & }", "case $x { a => b; c :: d => e& };"},
		{"fn f { a; b }; fn g", "fn f { a; b; };|fn g { };"},
		{"{ a; b } > f", "{ a; b; };"},
		{"@{ a }", "@{ a; };"},
		{"a b : c d", "a b : c d ;"},
		{"a : b { c }", "a : b { c; };"},
		{"a : b : { c }", "a : b :{ c; };"},
		{"*.dis :~ $1.b { limbo $1.b }", "*.dis :~ $1.b { limbo $1.b; };"},
		{"echo in", ""},
		{"echo (; echo gone\necho after", "echo after;"},
		{"echo a; ) ; ; echo b", "echo a;"},
		{"a | b < f", ""},
		{"a > f > g", ""},
	}
	for _, c := range cases {
		got := strings.Join(parseall(c.in), "|")
		if got != c.out {
			t.Errorf("%q: got %q, want %q", c.in, got, c.out)
		}
	}
}

func TestFulltext(t *testing.T) {
	e := newEnv()
	e.stderr = -1
	e.sopen("{ a } > f")
	var c *Cmd
	p := &parser{e: e, run1: func(x *Cmd) { c = x }}
	p.run()
	if got := fulltext(c); got != "{ a; } > f" {
		t.Errorf("got %q", got)
	}
}

func TestCaret(t *testing.T) {
	cases := []struct {
		s1, s2 string
		l1, l2 []string
		s      string
		l      []string
	}{
		{"a", "b", nil, nil, "ab", nil},
		{"a", "", nil, []string{"1", "2"}, "", []string{"a1", "a2"}},
		{"", "b", []string{"1", "2"}, nil, "", []string{"1b", "2b"}},
		{"", "", []string{"a", "b", "c"}, []string{"1", "2"}, "", []string{"a1", "b2", "c"}},
		{"a", "", nil, nil, "a", nil},
		{"", "", nil, []string{"x"}, "", []string{"x"}},
	}
	for _, c := range cases {
		s, l, _ := caret(c.s1, c.l1, 0, c.s2, c.l2, 0)
		if s != c.s || strings.Join(l, " ") != strings.Join(c.l, " ") {
			t.Errorf("caret(%q %q, %q %q) = %q %q", c.s1, c.l1, c.s2, c.l2, s, l)
		}
	}
}

func TestMatchelem(t *testing.T) {
	if ok, l := matches("*.dis", "rotta.dis"); !ok || strings.Join(l, " ") != "rotta" {
		t.Errorf("matches: %v %q", ok, l)
	}
	if ok, l := matches("a*b*c", "aXbYc"); !ok || strings.Join(l, " ") != "X Y" {
		t.Errorf("matches: %v %q", ok, l)
	}
	if !matchelem("*.b", "x.b") || matchelem("*.b", "x.c") {
		t.Error("matchelem")
	}
}
