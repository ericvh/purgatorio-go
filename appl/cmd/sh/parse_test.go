package main

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct{ in, out string }{
		{"echo hello world", "echo hello world"},
		{"a; b & c", "a;b&;c"},
		{"a | b | c", "a|b|c"},
		{"a |[2] b", "a|[2]b"},
		{"a |[1=0] b", "a|[1=0]b"},
		{"a && b || c", "or {and {a} {b}} {c}"},
		{"x = a b", "x=a b"},
		{"x := a", "x:=a"},
		{"(a b) = c", "(a b)=c"},
		{"x = y = z", "x=y=z"},
		{"x =", "x="},
		{"a > f >> g < h <> i", "a >f >>g <h <>i"},
		{"a >[2] f >[2=1] <[0=3]", "a >[2]f >[2=1] <[0=3]"},
		{"> f a", ">f a"},
		{"a <{b} >{c}", "a <{b} >{c}"},
		{"echo $x $#x $\"x ${x y} `{a} \"{b}", "echo $x $#x $\"x ${x y} `{a} \"{b}"},
		{"echo -$x.b", "echo -^$x^.b"},
		{"echo $x'y'$z", "echo $x^y^$z"},
		{"echo 'a'b'c'", "echo a^b^c"},
		{"echo a^b ^ c", "echo a^b^c"},
		{"echo (a\nb\n)", "echo (a b)"},
		{"{a\nb\n\nc}", "{a;b;c}"},
		{"echo *.b 'x*'", "echo *.b 'x*'"},
		{"echo 'it''s' ''", "echo 'it''s' ''"},
		{"echo a#comment", "echo a"},
		{"x:=a:b", "x:=a:b"},
		{"echo a | \n b", "echo a|b"},
		{"", ""},
		{";;", ""},
	}
	for _, c := range cases {
		n, err := parse(c.in)
		if err != "" {
			t.Errorf("parse(%q): %s", c.in, err)
			continue
		}
		if got := cmd2string(n); got != c.out {
			t.Errorf("parse(%q) = %q, want %q", c.in, got, c.out)
		}
		// The text parses back to the same tree.
		n2, err := parse(cmd2string(n))
		if err != "" || cmd2string(n2) != cmd2string(n) {
			t.Errorf("reparse(%q) = %q, %q", c.out, cmd2string(n2), err)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ in, err string }{
		{"echo )", "internal: parse error: syntax error"},
		{"echo a=b", "internal: parse error: syntax error"},
		{"echo {", "internal: parse error: syntax error"},
		{"echo 'abc", "internal: parse error: unterminated string literal"},
		{"a |[x] b", "internal: parse error: syntax error in pipe redirection"},
		{"a >[1=x] b", "internal: parse error: syntax error in redirection"},
		{"a |", "internal: parse error: syntax error"},
		{"a >", "internal: parse error: syntax error"},
		{"(>x a)", "internal: parse error: syntax error"},
	}
	for _, c := range cases {
		if _, err := parse(c.in); err != c.err {
			t.Errorf("parse(%q) error %q, want %q", c.in, err, c.err)
		}
	}
}

// A file reports the line of an error and recovers at the next line.
func TestParseFile(t *testing.T) {
	l := newFileLexer(strings.NewReader("echo a\necho )\necho (b\nc)\n'x\n"), "f", nil)
	var got []string
	for !l.eof {
		n, err := doparse(l, "", true)
		if err != "" {
			got = append(got, err)
		} else if n != nil {
			got = append(got, cmd2string(n))
		}
	}
	want := []string{"echo a", "f:2: syntax error", "echo (b c)", "f:5: unterminated string literal"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestQuote(t *testing.T) {
	val := stringlist([]string{"a", "b c", "", "it's", "x=y", "\x01*"})
	q := quoted(val, false)
	if want := "a 'b c' '' 'it''s' 'x=y' '\x01*'"; q != want {
		t.Errorf("quoted = %q, want %q", q, want)
	}
	if got := unquoted(quoted(val[:5], false)); strings.Join(got, "|") != "a|b c||it's|x=y" {
		t.Errorf("unquoted = %q", got)
	}
	if got := unquoted("  a  'b ''c'' d'e  'unbalanced"); strings.Join(got, "|") != "a|b 'c' de|unbalanced" {
		t.Errorf("unquoted = %q", got)
	}
	if q := quote(`"id":7,`, false); q != `'"id":7,'` {
		t.Errorf("quote of a double quote = %q", q)
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		pat, name string
		ok        bool
	}{
		{"*", "", true},
		{"a*", "abc", true},
		{"*c", "abc", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"[a-c]x", "bx", true},
		{"[^a-c]x", "bx", false},
		{"[^a-c]x", "dx", true},
		{"[ab", "a", false},
		{"\\*", "*", true},
		{"\\*", "a", false},
		{"a/*", "a/b", true},
		{"é?", "éx", true},
	}
	for _, c := range cases {
		if got := match(c.pat, c.name); got != c.ok {
			t.Errorf("match(%q, %q) = %v", c.pat, c.name, got)
		}
	}
	if got := patquote("\x01[~a]\x01*b?"); got != "[^a]*b\\?" {
		t.Errorf("patquote = %q", got)
	}
}

func TestExprNumbers(t *testing.T) {
	for _, c := range []struct {
		n     int64
		radix int
		s     string
	}{{255, 16, "16rff"}, {-5, 2, "-2r101"}, {0, 10, "0"}, {-9, 10, "-9"}} {
		if got := big2string(c.n, c.radix); got != c.s {
			t.Errorf("big2string(%d, %d) = %q, want %q", c.n, c.radix, got, c.s)
		}
	}
	if limboAtoi(" 12abc") != 12 || limboAtoi("-3") != -3 || limboAtoi("x") != 0 {
		t.Error("limboAtoi")
	}
}
