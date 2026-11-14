package quote

import "testing"

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"":        "''",
		"abc":     "abc",
		"a b":     "'a b'",
		"it's":    "'it''s'",
		"tab\tx":  "'tab\tx'",
		"/a/b.c":  "/a/b.c",
		"ünïcode": "ünïcode",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnquoted(t *testing.T) {
	got := Unquoted("!reshape . -1 'a b' 'it''s' '' x")
	want := []string{"!reshape", ".", "-1", "a b", "it's", "", "x"}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("word %d: %q, want %q", i, got[i], want[i])
		}
	}
	for _, s := range []string{"", "a b", "it's", "x"} {
		if w := Unquoted(Quote(s)); len(w) != 1 || w[0] != s {
			t.Errorf("round trip %q: %q", s, w)
		}
	}
}
