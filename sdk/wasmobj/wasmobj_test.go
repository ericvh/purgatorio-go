package wasmobj

import (
	"os"
	"testing"
)

// mod imports one function, defines two with a name section, a memory
// with limits, a mutable global and a data segment.
var mod = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
	0x01, 0x09, 0x02, 0x60, 0x01, 0x7f, 0x00, 0x60, 0x00, 0x01, 0x7e, // types: (i32), () -> i64
	0x02, 0x07, 0x01, 0x01, 'm', 0x01, 'f', 0x00, 0x00, // import m.f type 0
	0x03, 0x03, 0x02, 0x01, 0x00, // funcs 1 and 2
	0x05, 0x04, 0x01, 0x01, 0x01, 0x02, // memory min 1 max 2
	0x06, 0x06, 0x01, 0x7f, 0x01, 0x41, 0x7f, 0x0b, // global mut i32 = -1
	0x07, 0x05, 0x01, 0x01, 'g', 0x00, 0x02, // export g = func 2
	0x0a, 0x09, 0x02, 0x04, 0x00, 0x42, 0x00, 0x0b, 0x02, 0x00, 0x0b, // bodies
	0x0b, 0x08, 0x01, 0x00, 0x41, 0x04, 0x0b, 0x02, 'h', 'i', // data at 4
	0x00, 0x0d, 0x04, 'n', 'a', 'm', 'e', 0x01, 0x06, 0x01, 0x01, 0x03, 'o', 'n', 'e', // name 1 = one
}

func TestParse(t *testing.T) {
	m, err := Parse(mod)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Types) != 2 || m.Types[1].String() != "() -> i64" || m.Types[0].String() != "(i32)" {
		t.Errorf("types %v", m.Types)
	}
	if len(m.Imports) != 1 || m.Imports[0].Module != "m" || m.Imports[0].Name != "f" || m.Imported(KFunc) != 1 {
		t.Errorf("imports %v", m.Imports)
	}
	if t2, ok := m.FuncType(1); !ok || t2.String() != "() -> i64" {
		t.Errorf("FuncType(1) = %v %v", t2, ok)
	}
	if len(m.Mems) != 1 || m.Mems[0].String() != "min 1 max 2" {
		t.Errorf("mems %v", m.Mems)
	}
	if len(m.Globals) != 1 || m.Globals[0].String() != "mut i32" || m.Globals[0].Init != -1 || !m.Globals[0].HasInit {
		t.Errorf("globals %v", m.Globals)
	}
	if len(m.Bodies) != 2 || m.Bodies[0] != 4 || m.Bodies[1] != 2 {
		t.Errorf("bodies %v", m.Bodies)
	}
	if mem := m.Memory(); string(mem) != "\x00\x00\x00\x00hi" {
		t.Errorf("memory %q", mem)
	}
	if last := m.Sections[len(m.Sections)-1]; last.Kind() != "custom" || last.Name != "name" || last.Offset != len(mod)-15 {
		t.Errorf("last section %+v", last)
	}
	s := m.Syms()
	for i, want := range []string{"m.f", "one", "func2"} {
		if f := s.Lookup(uint32(i)); f == nil || f.Name != want {
			t.Errorf("Lookup(%d) = %v, want %s", i, f, want)
		}
	}
	if s.IsGo() || s.Lookup(3) != nil {
		t.Error("bad symbols")
	}
	for _, bad := range [][]byte{nil, []byte("\x00asm\x02\x00\x00\x00"), mod[:20]} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) worked", bad)
		}
	}
	if _, err := Parse(mod[:20]); err == nil || err.Error() != "unexpected end at offset 20" {
		t.Errorf("short module: %v", err)
	}
}

func TestParseStack(t *testing.T) {
	fs := ParseStack("00000001 00000016 00000000 00000000 1 $purgatorio\n" +
		"00000000 0000000a 00001234 00000000 1 /bin/a b.wasm\nshort\n")
	if len(fs) != 2 || !fs[0].Host() || fs[0].Func != 0x16 || fs[1].Host() ||
		fs[1].Offset != 0x1234 || fs[1].Path != "/bin/a b.wasm" || fs[1].N != 0 {
		t.Errorf("frames %+v", fs)
	}
	var n Namer
	if l := n.Line(&fs[0]); l != "unknown fn() Module $purgatorio PC 22" {
		t.Errorf("host line %q", l)
	}
}

// A Go guest's functions are named from its pclntab.
func TestGoSyms(t *testing.T) {
	b, err := os.ReadFile("../../root/bin/echo.wasm")
	if err != nil {
		t.Skip("guests not built")
	}
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Syms()
	if !s.IsGo() {
		t.Fatal("no Go symbols")
	}
	found := false
	for _, e := range m.Exports {
		if e.Name == "_start" {
			f := s.Lookup(e.Index)
			found = f != nil && f.Name == "_rt0_wasm_wasip1" && f.Line > 0
		}
	}
	if !found {
		t.Error("_start is not _rt0_wasm_wasip1")
	}
}
