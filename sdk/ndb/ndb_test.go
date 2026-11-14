package ndb

import (
	"bytes"
	"reflect"
	"testing"
)

func TestParseLine(t *testing.T) {
	cases := []struct {
		in   string
		want []Attr
		err  string
	}{
		{"", nil, ""},
		{"   # comment", nil, ""},
		{"a=b c=d\n", []Attr{{"a", "b"}, {"c", "d"}}, ""},
		{"\tlone x=1 other", []Attr{{"lone", ""}, {"x", "1"}, {"other", ""}}, ""},
		{"a= b=2", []Attr{{"b", "2"}}, ""},
		{"a='x y' b=\"q\"\"r\"", []Attr{{"a", "x y"}, {"b", "q\"r"}}, ""},
		{"database=", []Attr{{"database", ""}}, ""},
		{"a=b # c=d", []Attr{{"a", "b"}}, ""},
		{"=x", nil, "empty name"},
		{"a='x", nil, "missing quote"},
	}
	for _, c := range cases {
		got, err := ParseLine(c.in)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("%q: error %v, want %q", c.in, err, c.err)
			}
			continue
		}
		var attrs []Attr
		if got != nil {
			attrs = got.Pairs
		}
		if err != nil || !reflect.DeepEqual(attrs, c.want) {
			t.Errorf("%q: got %v %v, want %v", c.in, attrs, err, c.want)
		}
	}
}

const testdb = "#c\na=x b=1\n\tb=2\n# inside\na=y\n"

func TestReadEntry(t *testing.T) {
	f := OpenString(testdb)
	e, m, next := f.ReadEntry(0, "", "", false)
	if !m || len(e.Lines) != 2 || next != 25 {
		t.Fatalf("first entry: %v %v %d", e, m, next)
	}
	e, m, next = f.ReadEntry(next, "", "", false)
	if !m || e.FindFirst("a") != "y" || next != 29 {
		t.Fatalf("second entry: %v %v %d", e, m, next)
	}
	if e, _, _ = f.ReadEntry(next, "", "", false); e != nil {
		t.Fatalf("past the end: %v", e)
	}
	e, m, _ = f.ReadEntry(0, "a", "y", true)
	if !m || e.FindFirst("a") != "y" {
		t.Fatalf("findpair: %v %v", e, m)
	}
}

func TestDB(t *testing.T) {
	db := &DB{[]*File{OpenString("x=1 y=a\n\ty=b z=1\n\ty=c\n"), OpenString("x=2 y=d\n")}}
	e, p := db.FindByAttr(nil, "z", "1", "y")
	if e == nil {
		t.Fatal("no entry")
	}
	var vals []string
	for _, m := range e.FindByAttr("z", "1", "y") {
		for _, a := range m.Attrs {
			vals = append(vals, a.Val)
		}
	}
	if !reflect.DeepEqual(vals, []string{"b", "a", "c"}) {
		t.Errorf("findbyattr order: %q", vals)
	}
	if e, _ = db.FindByAttr(p, "z", "1", "y"); e != nil {
		t.Errorf("second match: %v", e)
	}
	var xs []string
	for e, p := db.Find(nil, "x"); e != nil; e, p = db.Find(p, "x") {
		xs = append(xs, e.FindFirst("x"))
	}
	if !reflect.DeepEqual(xs, []string{"1", "2"}) {
		t.Errorf("find across files: %q", xs)
	}
}

func TestHash(t *testing.T) {
	for _, c := range []struct {
		s    string
		hlen int
		want int
	}{
		{"", 7, 0},
		{"a", 5, 0},
		{"f", 5, 0},
		{"1", 5, 3},
		{"2", 5, 4},
		{"ab", 100, 1},
	} {
		if got := Hash(c.s, c.hlen); got != c.want {
			t.Errorf("Hash(%q, %d) = %d, want %d", c.s, c.hlen, got, c.want)
		}
	}
}

func TestMkhash(t *testing.T) {
	f := OpenString(testdb)
	f.Mtime = 0x01020304
	got := Mkhash(f, "b")
	want := []byte{4, 3, 2, 1, 5, 0, 0, 0,
		1, 0, 0x80, 1, 0, 0x80, 1, 0, 0x80, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(got, want) {
		t.Errorf("b: got % x, want % x", got, want)
	}
	// "a" and "f" collide in a table of 5: the slot points at a chain
	// link holding the first offset and the second.
	f = OpenString("a=a\na=f\n")
	got = Mkhash(f, "a")
	want = []byte{0, 0, 0, 0, 5, 0, 0, 0,
		15, 0, 0x80, 1, 0, 0x80, 1, 0, 0x80, 1, 0, 0x80, 1, 0, 0x80,
		0, 0, 0, 4, 0, 0}
	if !bytes.Equal(got, want) {
		t.Errorf("a: got % x, want % x", got, want)
	}
}
