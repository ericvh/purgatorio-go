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
