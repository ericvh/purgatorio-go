package regex

import "testing"

func TestExecute(t *testing.T) {
	cases := []struct {
		re, s    string
		beg, end int
	}{
		{"[ABCb-z]+", "aAbBcCdD", 1, 7},
		{"a*b*", "bbaabb", 0, 2},
		{"(thick )*(chocolate |raspberry )?(topp|fill)ing", "thick thick raspberry filling", 0, 29},
		{"x", "abc", -1, -1},
		{"^abc$", "abc", 0, 3},
		{"^b", "abc", -1, -1},
		{"b|bc", "abcd", 1, 3},
		{"a.c", "a\nc abc", 4, 7},
		{"[^a-c]+", "abcdefabc", 3, 6},
		{"\\.", "a.b", 1, 2},
		{"é+", "caféé!", 3, 5},
		{"[à-ÿ]", "naïve", 2, 3},
		{"a+", "baaa", 1, 4},
		{"(a|ab)(c|bcd)", "abcd", 0, 4},
	}
	for _, c := range cases {
		re, err := Compile(c.re, 0)
		if re == nil {
			t.Errorf("Compile(%q): %s", c.re, err)
			continue
		}
		m := re.Execute([]rune(c.s))
		if c.beg < 0 {
			if m != nil {
				t.Errorf("%q on %q: matched %v", c.re, c.s, m)
			}
			continue
		}
		if m == nil || m[0] != (Range{c.beg, c.end}) {
			t.Errorf("%q on %q: got %v, want [%d %d]", c.re, c.s, m, c.beg, c.end)
		}
	}
}

func TestSubexpressions(t *testing.T) {
	re, _ := Compile("(a+)(b*)(x)?", 1)
	m := re.Execute([]rune("caabbd"))
	want := []Range{{1, 5}, {1, 3}, {3, 5}, {-1, -1}}
	if len(m) != len(want) {
		t.Fatalf("got %v", m)
	}
	for i := range want {
		if m[i] != want[i] {
			t.Errorf("sub %d: got %v, want %v", i, m[i], want[i])
		}
	}
}

func TestCompileErrors(t *testing.T) {
	for e, diag := range map[string]string{
		"":      "missing expression",
		"(":     "invalid regular expression",
		"a)":    "invalid regular expression",
		"[a-":   "invalid regular expression",
		"*a":    "invalid regular expression",
		"a\\":   "invalid regular expression",
		"[z-a]": "invalid regular expression",
	} {
		if re, err := Compile(e, 0); re != nil || err != diag {
			t.Errorf("Compile(%q) = %v, %q; want %q", e, re, err, diag)
		}
	}
}

func TestExecuteSE(t *testing.T) {
	re, _ := Compile("c$", 0)
	s := []rune("abc\n")
	if m := re.ExecuteSE(s, 0, len(s)-1, true, true); m == nil || m[0] != (Range{2, 3}) {
		t.Errorf("got %v", m)
	}
	if m := re.ExecuteSE(s, 0, len(s)-1, true, false); m != nil {
		t.Errorf("eol false matched %v", m)
	}
}
