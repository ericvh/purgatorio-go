package core

import "testing"

func TestCleanname(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "."},
		{"/", "/"},
		{"//a//b/", "/a/b"},
		{"a/./b", "a/b"},
		{"a/../b", "b"},
		{"/..", "/"},
		{"/a/../..", "/"},
		{"..", ".."},
		{"../..", "../.."},
		{"a/../..", ".."},
		{".", "."},
		{"./", "."},
		{"#c", "#c"},
		{"#c/", "#c"},
		{"#c/cons", "#c/cons"},
		{"#U/a/../..", "#U"},
		{"#U/./lib//x", "#U/lib/x"},
	}
	for _, c := range cases {
		if got := Cleanname(c.in); got != c.want {
			t.Errorf("Cleanname(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
