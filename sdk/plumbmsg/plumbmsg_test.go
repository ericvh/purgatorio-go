package plumbmsg

import (
	"bytes"
	"testing"
)

func TestPackUnpack(t *testing.T) {
	m := &Msg{Src: "plumb", Dst: "edit", Dir: "/usr/inferno", Kind: "text",
		Attr: "click=3\taction=showdata", Data: []byte("hello\nworld")}
	b := m.Pack()
	want := "plumb\nedit\n/usr/inferno\ntext\nclick=3\taction=showdata\n11\nhello\nworld"
	if string(b) != want {
		t.Fatalf("Pack = %q, want %q", b, want)
	}
	u := Unpack(b)
	if u == nil {
		t.Fatal("Unpack failed")
	}
	if u.Src != m.Src || u.Dst != m.Dst || u.Dir != m.Dir || u.Kind != m.Kind || u.Attr != m.Attr ||
		!bytes.Equal(u.Data, m.Data) {
		t.Fatalf("Unpack = %+v, want %+v", u, m)
	}
	e := (&Msg{}).Pack()
	if string(e) != "\n\n\n\n\n0\n" {
		t.Fatalf("empty Pack = %q", e)
	}
	if u := Unpack(e); u == nil || len(u.Data) != 0 {
		t.Fatalf("empty Unpack = %+v", u)
	}
}

func TestUnpackBad(t *testing.T) {
	for _, s := range []string{
		"",
		"a\nb\nc\nd\ne\n",      // no length line terminator
		"a\nb\nc\nd\ne\n5\nab", // short data
		"a\nb\nc\nd\ne\n1\nab", // long data
	} {
		if u := Unpack([]byte(s)); u != nil {
			t.Errorf("Unpack(%q) = %+v, want nil", s, u)
		}
	}
}

func TestAttrs(t *testing.T) {
	a := String2attrs("click=3\taction=showdata\t\tflag=")
	if len(a) != 3 || a[0].Name != "flag" || a[0].Val != "" || a[2].Name != "click" || a[2].Val != "3" {
		t.Fatalf("String2attrs = %v", a)
	}
	if s := Attrs2string(a); s != "flag=\taction=showdata\tclick=3" {
		t.Fatalf("Attrs2string = %q", s)
	}
	if v, ok := Lookup(a, "action"); !ok || v != "showdata" {
		t.Fatalf("Lookup action = %q %v", v, ok)
	}
	if _, ok := Lookup(a, "none"); ok {
		t.Fatal("Lookup none found")
	}
	if String2attrs("") != nil || Attrs2string(nil) != "" {
		t.Fatal("empty attrs")
	}
	if a := String2attrs("noeq=x=y\tbare"); a[0].Name != "" || a[1].Name != "noeq" || a[1].Val != "x=y" {
		t.Fatalf("String2attrs odd = %+v %+v", a[0], a[1])
	}
}
