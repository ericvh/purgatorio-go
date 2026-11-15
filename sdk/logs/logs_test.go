package logs

import (
	"bufio"
	"strings"
	"testing"
)

func TestEntry(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("1064889121 4 a 'a file' - 664 sys sys 1064887847 3 abcd\n" +
		"1064889122 0 m 'a file' srv/f 644 - - 1064887848 3\n" +
		"1064889123 0 x f - 664 a b 1 1\n"))
	e, err := Read(in)
	if err != "" {
		t.Fatal(err)
	}
	if e.Seq != 1064889121<<32|4 || e.Action != 'a' || e.Path != "a file" || e.Serverpath != "a file" ||
		e.D.Mode != 0o664 || e.D.UID != "sys" || e.D.Mtime != 1064887847 || e.D.Length != 3 ||
		len(e.Contents) != 1 || e.Contents[0] != "abcd" {
		t.Errorf("read %+v", e)
	}
	if got, want := e.Logtext(), "1064889121 4 a 'a file' - 664 sys sys 1064887847 3 abcd"; got != want {
		t.Errorf("logtext %q, want %q", got, want)
	}
	db := New("test")
	s := db.Entry(e.Seq, e.Path, e.D)
	s.Update(e)
	m, _ := Read(in)
	s.Update(m)
	if s.Action != 'a' || s.D.Mode != 0o644 || s.Serverpath != "srv/f" || s.Seq != m.Seq || db.Look("a file") != s {
		t.Errorf("updated %+v", s)
	}
	if got, want := s.Sumtext(), "a 'a file' 644 '' '' 1064887848"; got != want {
		t.Errorf("sumtext %q, want %q", got, want)
	}
	if _, err := Read(in); err != "bad log entry: '1064889123 0 x f - 664 a b 1 1'" {
		t.Errorf("bad entry: %q", err)
	}
	if e, err := Read(in); e != nil || err != "" {
		t.Errorf("at end: %v %q", e, err)
	}
	if Mkpath("/a", "b") != "/a/b" || Mkpath("/a/", "b") != "/a/b" || Mkpath("", "b") != "b" {
		t.Error("mkpath")
	}
}
