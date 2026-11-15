package main

import "testing"

func TestCmdusage(t *testing.T) {
	cases := []struct{ name, types, out string }{
		{"walk", "xs-bs", "walk [-b string] string -> fs"},
		{"and", "pppp*", "and gate gate [gate...] -> gate"},
		{"exec", "vct-ns-tc-p-P", "exec [-pP] [-n string] [-t command] command entries -> void"},
		{"merge", "xxxx*-1-cm", "merge [-1] [-c selector] fs fs [fs...] -> fs"},
		{"path", "pss*-x", "path [-x] string [string...] -> gate"},
		{"eval", "as-v", "eval [-v] string -> any"},
	}
	for _, c := range cases {
		if got := cmdusage(c.name, c.types); got != c.out {
			t.Errorf("cmdusage(%q, %q) = %q, want %q", c.name, c.types, got, c.out)
		}
	}
}

func TestTypecompat(t *testing.T) {
	for _, c := range []struct {
		t0, t1 string
		ok     bool
	}{
		{"xs", "xs-bs", true},
		{"as", "as-v", true},
		{"tx", "tx", true},
		{"ps", "pp", false},
		{"vt", "xs", false},
	} {
		if got := typecompat(c.t0, c.t1); got != c.ok {
			t.Errorf("typecompat(%q, %q) = %v", c.t0, c.t1, got)
		}
	}
}

func TestGettok(t *testing.T) {
	p := &evalstate{s: []rune("void {walk 'a b''c' @ {echo '}' {x}} $0}")}
	want := []struct {
		t int
		s string
	}{
		{tWORD, "void"}, {'{', ""}, {tWORD, "walk"}, {tWORD, "a b'c"},
		{tSHCMD, "{echo '}' {x}}"}, {tVAR, "0"}, {'}', ""}, {tEOF, ""},
	}
	for _, w := range want {
		tok, s := p.gettok()
		if tok != w.t || s != w.s {
			t.Fatalf("got %d %q, want %d %q", tok, s, w.t, w.s)
		}
	}
}

func TestParsemode(t *testing.T) {
	ok, mask, mode := parsemode("u+w")
	if !ok || mask != 0o200 || mode&mask != 0o200 {
		t.Errorf("u+w: %v %o %o", ok, mask, mode)
	}
	if ok, _, _ := parsemode("q"); ok {
		t.Error("q accepted")
	}
}

func TestShquote(t *testing.T) {
	for in, out := range map[string]string{"a": "a", "": "''", "a b": "'a b'", "it's": "'it''s'", "$x": "'$x'"} {
		if got := shquote(in); got != out {
			t.Errorf("shquote(%q) = %q, want %q", in, got, out)
		}
	}
}
