package wrap

import (
	"bytes"
	"testing"
)

func TestNow2string(t *testing.T) {
	for _, n := range []int{0, 1000000, 99999999, 100000000, 999999999, 1000000000, 2100000000} {
		s := Now2string(n, true)
		if len(s) != 9 || String2now(s, true) != n {
			t.Errorf("%d: %q back %d", n, s, String2now(s, true))
		}
		if String2now(Now2string(n, false), false) != n {
			t.Errorf("%d: decimal %q", n, Now2string(n, false))
		}
	}
	if s := Now2string(1000000000, true); s != "A00000000" {
		t.Errorf("10^9: %q", s)
	}
	if String2now("A00000000", false) != 1000000000 {
		t.Error("A form without flag")
	}
}

func TestMatch(t *testing.T) {
	pre := []string{"/dis/", "/lib"}
	if !Match("/dis/ls.dis", pre) || Match("/appl/x", pre) || !Match("/wrap/x", pre) || !Match("/x", nil) {
		t.Error("match")
	}
	if Notmatch("/dis/ls.dis", pre) || !Notmatch("/appl/x", pre) || !Notmatch("/wrap", pre) {
		t.Error("notmatch")
	}
	if Pathcat("/a/", "/b") != "/a/b" || Pathcat("a", "b") != "a/b" || Pathcat("", "b") != "b" {
		t.Error("pathcat")
	}
}

func TestPutwrap(t *testing.T) {
	var b bytes.Buffer
	Putwrap(&b, "pkg", 1000, "desc", 900, false, "u", "g")
	want := "/wrap 20000000775 u g 1000 0\n" +
		"/wrap/pkg 20000000775 u g 1000 0\n" +
		"/wrap/pkg/1000 20000000775 u g 1000 0\n" +
		"/wrap/pkg/1000/update 664 u g 1000 23\n" +
		"                   900\n" +
		"/wrap/pkg/1000/desc 664 u g 1000 5\n" +
		"desc\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}
