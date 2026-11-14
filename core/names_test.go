package core

import (
	"reflect"
	"testing"
)

func TestCleanname(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "."},
		{"/", "/"},
		{"//a//b/", "/a/b"},
		{"a/./b", "a/b"},
		{"a/b/../c", "a/c"},
		{"/..", "/"},
		{"/a/../../b", "/b"},
		{"..", ".."},
		{"../a", "../a"},
		{"a/../..", ".."},
		{"./", "."},
		{"#c", "#c"},
		{"#c/", "#c"},
		{"#c/./cons", "#c/cons"},
		{"#Uspec/a/../b", "#Uspec/b"},
		{"#c/..", "#c"},
	}
	for _, c := range cases {
		if got := Cleanname(c.in); got != c.want {
			t.Errorf("Cleanname(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitDevice(t *testing.T) {
	if p, r, ok := SplitDevice("#c/cons"); p != "#c" || r != "/cons" || !ok {
		t.Errorf("got %q %q %v", p, r, ok)
	}
	if got := Elements("#U/a/b"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Elements: %q", got)
	}
	if got := Quote("a b'c"); got != "'a b''c'" {
		t.Errorf("Quote: %q", got)
	}
}
