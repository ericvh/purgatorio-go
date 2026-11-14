package mcp

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	src := `{"a":[1,-2,3.5,1e3,true,false,null],"b":{"c":"d"},"e":""}`
	v, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Get(v, "a")
	if TypeName(a) != "array" {
		t.Errorf("a is %s", TypeName(a))
	}
	b, _ := Get(v, "b")
	if s, _ := Str(b, "c"); s != "d" {
		t.Errorf("b.c = %q", s)
	}
	want := `{"a":[1,-2,3.5,1000,true,false,null],"b":{"c":"d"},"e":""}`
	if got := Encode(v); got != want {
		t.Errorf("got %s", got)
	}
}

func TestIDsStayExact(t *testing.T) {
	v, _ := Parse("9007199254740993")
	if Encode(v) != "9007199254740993" {
		t.Error(Encode(v))
	}
	v, _ = Parse("-0")
	if v != int64(0) {
		t.Errorf("-0 is %#v", v)
	}
	v, _ = Parse("3.0")
	if n, ok := AsInt(v); !ok || n != 3 {
		t.Error("3.0")
	}
	v, _ = Parse("3.5")
	if _, ok := AsInt(v); ok {
		t.Error("3.5")
	}
}

func TestStrings(t *testing.T) {
	v, err := Parse(`"a\"b\\c\/d\n\té😀\u0000"`)
	if err != nil || v != "a\"b\\c/d\n\té\U0001f600\x00" {
		t.Errorf("%q %v", v, err)
	}
	out := Encode("x\ny\r\x01 \x7fé\U0001f600")
	if out != "\"x\\ny\\r\\u0001\\u2028\\u007fé\U0001f600\"" {
		t.Error(out)
	}
}

func TestRejectsBadText(t *testing.T) {
	for _, bad := range []string{"", "{", "[1,]", `{"a":1,}`, "{a:1}", "01", "1.", "-", "1e",
		"nul", `"abc`, `"\x"`, `"\ud800"`, `"\udc00"`, "\"a\nb\"", "1 2", "1e999", "'a'"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if _, err := Parse(strings.Repeat("[", 1000) + strings.Repeat("]", 1000)); err == nil {
		t.Error("deep nesting parsed")
	}
	if _, err := Parse(strings.Repeat("[", 100) + strings.Repeat("]", 100)); err != nil {
		t.Error(err)
	}
	if _, err := Parse("nope"); err == nil || err.Error() != "bad literal at byte 0" {
		t.Error(err)
	}
}

func TestDuplicatesKeepTheLast(t *testing.T) {
	v, _ := Parse(`{"a":1,"a":2}`)
	if Encode(v) != `{"a":2}` {
		t.Error(Encode(v))
	}
}

func TestBase64(t *testing.T) {
	for _, c := range [][2]string{{"", ""}, {"f", "Zg=="}, {"fo", "Zm8="}, {"foo", "Zm9v"},
		{"foob", "Zm9vYg=="}, {"fooba", "Zm9vYmE="}, {"foobar", "Zm9vYmFy"}} {
		if Base64([]byte(c[0])) != c[1] {
			t.Error(c)
		}
		if d, err := Unbase64(c[1]); err != nil || string(d) != c[0] {
			t.Error(c, err)
		}
	}
	if d, _ := Unbase64("Zm9v\nYg"); string(d) != "foob" {
		t.Error("whitespace")
	}
	for _, bad := range []string{"Zm9*", "Z", "Zg==Zg==", "Zh=="} {
		if _, err := Unbase64(bad); err == nil {
			t.Error(bad)
		}
	}
}

func TestLossy(t *testing.T) {
	for _, c := range []struct{ in, out string }{
		{"ok", "ok"},
		{"a\xffb", "a�b"},
		{"a\xe0\xa0", "a�"},
		{"\xe0\xa0x", "�x"},
		{"\xf0\x9f\x98", "�"},
		{"\xc0\x80", "��"},
	} {
		if got := Lossy([]byte(c.in)); got != c.out {
			t.Errorf("%q: %q", c.in, got)
		}
	}
	if n, inc := ValidUTF8Prefix([]byte("ab\xe2\x82")); n != 2 || !inc {
		t.Error(n, inc)
	}
	if n, inc := ValidUTF8Prefix([]byte("ab\xff")); n != 2 || inc {
		t.Error(n, inc)
	}
}
