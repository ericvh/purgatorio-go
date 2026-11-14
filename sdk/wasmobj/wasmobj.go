// Package wasmobj reads a WebAssembly module, as Inferno's Dis module
// (appl/lib/dis.b) reads a Dis file, for the commands that inspect
// modules and progs: wasmdump, wasmdep, mdb, stack, prof, cprof, mprof
// and trace. It reads every section but the function bodies, which it
// only measures, and names functions from a Go guest's own symbol table
// (gosym.go), a name section, or the exports.
package wasmobj

import (
	"errors"
	"fmt"
	"strings"
)

// Section ids.
const (
	SecCustom = iota
	SecType
	SecImport
	SecFunction
	SecTable
	SecMemory
	SecGlobal
	SecExport
	SecStart
	SecElement
	SecCode
	SecData
	SecDataCount
	SecTag
)

var secnames = []string{"custom", "type", "import", "function", "table", "memory",
	"global", "export", "start", "element", "code", "data", "datacount", "tag"}

// Kinds of import and export.
const (
	KFunc = iota
	KTable
	KMemory
	KGlobal
	KTag
)

var kindnames = []string{"func", "table", "memory", "global", "tag"}

// KindName is the name of an import or export kind.
func KindName(k byte) string {
	if int(k) < len(kindnames) {
		return kindnames[k]
	}
	return fmt.Sprintf("kind%d", k)
}

// Section is one section of the module, in file order.
type Section struct {
	ID     byte
	Name   string // a custom section's name
	Offset int    // of the section's id byte
	Size   int    // of the contents
	Data   []byte // the contents, after a custom section's name
}

// Kind is the section's kind, as its name.
func (s *Section) Kind() string {
	if int(s.ID) < len(secnames) {
		return secnames[s.ID]
	}
	return fmt.Sprintf("section%d", s.ID)
}

// FuncType is a function signature.
type FuncType struct {
	Params, Results []byte
}

func (t FuncType) String() string {
	vs := func(v []byte) string {
		s := make([]string, len(v))
		for i, b := range v {
			s[i] = ValType(b)
		}
		return strings.Join(s, " ")
	}
	r := "(" + vs(t.Params) + ")"
	switch len(t.Results) {
	case 0:
	case 1:
		r += " -> " + vs(t.Results)
	default:
		r += " -> (" + vs(t.Results) + ")"
	}
	return r
}

// ValType names a value type.
func ValType(b byte) string {
	switch b {
	case 0x7f:
		return "i32"
	case 0x7e:
		return "i64"
	case 0x7d:
		return "f32"
	case 0x7c:
		return "f64"
	case 0x7b:
		return "v128"
	case 0x70:
		return "funcref"
	case 0x6f:
		return "externref"
	}
	return fmt.Sprintf("type%#x", b)
}

// Limits are a table's or a memory's.
type Limits struct {
	Min    uint64
	Max    uint64
	HasMax bool
}

func (l Limits) String() string {
	if !l.HasMax {
		return fmt.Sprintf("min %d max -", l.Min)
	}
	return fmt.Sprintf("min %d max %d", l.Min, l.Max)
}

// Table is a table's type.
type Table struct {
	Elem byte
	Limits
}

// Global is a global's type and its initial value when constant.
type Global struct {
	Type    byte
	Mut     bool
	Init    int64
	HasInit bool
}

func (g Global) String() string {
	s := ValType(g.Type)
	if g.Mut {
		s = "mut " + s
	}
	return s
}

// Import is one import.
type Import struct {
	Module, Name string
	Kind         byte
	Type         uint32 // function or tag type index
	Table        Table
	Mem          Limits
	Global       Global
}

// Export is one export.
type Export struct {
	Name  string
	Kind  byte
	Index uint32
}

// Data is one data segment.
type Data struct {
	Active bool
	Offset uint64 // in memory, for an active segment with a constant offset
	Bytes  []byte
}

// Module is a parsed module.
type Module struct {
	Sections []Section
	Types    []FuncType
	Imports  []Import
	Funcs    []uint32 // the type of each defined function
	Tables   []Table
	Mems     []Limits
	Globals  []Global
	Exports  []Export
	Start    int64 // -1 for none
	Elements int
	Datas    []Data
	Bodies   []int // the size of each function body
}

// Error is a fault in a module, with its offset.
type Error struct {
	Off int
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("%s at offset %d", e.Msg, e.Off) }

type reader struct {
	b    []byte
	base int // file offset of b[0]
	pos  int
	err  *Error
}

func (r *reader) fail(msg string) {
	if r.err == nil {
		r.err = &Error{r.base + r.pos, msg}
	}
	r.pos = len(r.b)
}

func (r *reader) byte() byte {
	if r.pos >= len(r.b) {
		r.fail("unexpected end")
		return 0
	}
	c := r.b[r.pos]
	r.pos++
	return c
}

func (r *reader) uleb() uint64 {
	var v uint64
	for shift := 0; ; shift += 7 {
		c := r.byte()
		if shift >= 64 {
			r.fail("bad integer")
			return 0
		}
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 || r.err != nil {
			return v
		}
	}
}

func (r *reader) sleb() int64 {
	var v int64
	shift := 0
	for {
		c := r.byte()
		if shift >= 64 {
			r.fail("bad integer")
			return 0
		}
		v |= int64(c&0x7f) << shift
		shift += 7
		if c&0x80 == 0 || r.err != nil {
			if shift < 64 && c&0x40 != 0 {
				v |= -1 << shift
			}
			return v
		}
	}
}

func (r *reader) bytes(n uint64) []byte {
	if uint64(len(r.b)-r.pos) < n {
		r.fail("unexpected end")
		return nil
	}
	b := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return b
}

func (r *reader) name() string { return string(r.bytes(r.uleb())) }

func (r *reader) limits() Limits {
	var l Limits
	f := r.byte()
	l.Min = r.uleb()
	if f&1 != 0 {
		l.Max, l.HasMax = r.uleb(), true
	}
	return l
}

func (r *reader) table() Table {
	e := r.byte()
	return Table{Elem: e, Limits: r.limits()}
}

func (r *reader) global() Global {
	t := r.byte()
	return Global{Type: t, Mut: r.byte() == 1}
}

// constexpr reads an initialiser and returns its value when it is a
// single constant.
func (r *reader) constexpr() (int64, bool) {
	var v int64
	ok, n := false, 0
	for {
		op := r.byte()
		if r.err != nil {
			return 0, false
		}
		switch op {
		case 0x0b:
			return v, ok && n == 1
		case 0x41, 0x42: // i32.const, i64.const
			v, ok = r.sleb(), true
		case 0x43:
			r.bytes(4)
		case 0x44:
			r.bytes(8)
		case 0x23, 0xd2: // global.get, ref.func
			r.uleb()
		case 0xd0: // ref.null
			r.byte()
		default:
			// An extended constant expression: keep reading to the end.
		}
		n++
	}
}

var errNotWasm = errors.New("not a wasm module")

// Parse reads a module.
func Parse(b []byte) (*Module, error) {
	if len(b) < 8 || string(b[:4]) != "\x00asm" {
		return nil, errNotWasm
	}
	if b[4] != 1 || b[5] != 0 || b[6] != 0 || b[7] != 0 {
		return nil, &Error{4, "unknown version"}
	}
	m := &Module{Start: -1}
	r := &reader{b: b, pos: 8}
	for r.pos < len(b) && r.err == nil {
		off := r.pos
		id := r.byte()
		size := r.uleb()
		start := r.pos
		body := r.bytes(size)
		if r.err != nil {
			break
		}
		s := Section{ID: id, Offset: off, Size: int(size), Data: body}
		sr := &reader{b: body, base: start}
		if id == SecCustom {
			s.Name = sr.name()
			s.Data = body[sr.pos:]
		} else {
			m.section(id, sr)
			if id <= SecData && id != SecElement && sr.pos != len(body) {
				sr.fail("section size mismatch")
			}
		}
		if sr.err != nil {
			return nil, sr.err
		}
		m.Sections = append(m.Sections, s)
	}
	if r.err != nil {
		return nil, r.err
	}
	return m, nil
}

func (m *Module) section(id byte, r *reader) {
	switch id {
	case SecType:
		for n := r.uleb(); n > 0 && r.err == nil; n-- {
			if r.byte() != 0x60 {
				r.fail("unknown type form")
				return
			}
			var t FuncType
			t.Params = r.bytes(r.uleb())
			t.Results = r.bytes(r.uleb())
			m.Types = append(m.Types, t)
		}
	case SecImport:
		for n := r.uleb(); n > 0 && r.err == nil; n-- {
			im := Import{Module: r.name(), Name: r.name(), Kind: r.byte()}
			switch im.Kind {
			case KFunc:
				im.Type = uint32(r.uleb())
			case KTable:
				im.Table = r.table()
			case KMemory:
				im.Mem = r.limits()
			case KGlobal:
				im.Global = r.global()
			case KTag:
				r.byte()
				im.Type = uint32(r.uleb())
			default:
				r.fail("unknown import kind")
				return
			}
			m.Imports = append(m.Imports, im)
		}
	case SecFunction:
		for n := r.uleb(); n > 0 && r.err == nil; n-- {
			m.Funcs = append(m.Funcs, uint32(r.uleb()))
		}
	case SecTable:
		for n := r.uleb(); n > 0 && r.err == nil; n-- {
			m.Tables = append(m.Tables, r.table())
		}
	case SecMemory:
		for n := r.uleb(); n > 0 && r.err == nil; n-- {
			m.Mems = append(m.Mems, r.limits())
		}
	case SecGlobal:
		for n := r.uleb(); n > 0 && r.err == nil; n-- {
			g := r.global()
			g.Init, g.HasInit = r.constexpr()
			m.Globals = append(m.Globals, g)
		}
	case SecExport:
		for n := r.uleb(); n > 0 && r.err == nil; n-- {
			m.Exports = append(m.Exports, Export{Name: r.name(), Kind: r.byte(), Index: uint32(r.uleb())})
		}
	case SecStart:
		m.Start = int64(r.uleb())
	case SecElement:
		m.Elements = int(r.uleb())
	case SecCode:
		for n := r.uleb(); n > 0 && r.err == nil; n-- {
			size := r.uleb()
			r.bytes(size)
			m.Bodies = append(m.Bodies, int(size))
		}
	case SecData:
		for n := r.uleb(); n > 0 && r.err == nil; n-- {
			var d Data
			switch r.uleb() {
			case 0:
				v, _ := r.constexpr()
				d.Active, d.Offset = true, uint64(uint32(v))
			case 1:
			case 2:
				r.uleb()
				v, _ := r.constexpr()
				d.Active, d.Offset = true, uint64(uint32(v))
			default:
				r.fail("unknown data segment")
				return
			}
			d.Bytes = r.bytes(r.uleb())
			m.Datas = append(m.Datas, d)
		}
	}
}

// Imported is the number of imports of a kind.
func (m *Module) Imported(kind byte) int {
	n := 0
	for _, im := range m.Imports {
		if im.Kind == kind {
			n++
		}
	}
	return n
}

// FuncType is the type of function index i, imports first.
func (m *Module) FuncType(i uint32) (FuncType, bool) {
	n := uint32(0)
	for _, im := range m.Imports {
		if im.Kind == KFunc {
			if n == i {
				if int(im.Type) < len(m.Types) {
					return m.Types[im.Type], true
				}
				return FuncType{}, false
			}
			n++
		}
	}
	if j := i - n; i >= n && int(j) < len(m.Funcs) && int(m.Funcs[j]) < len(m.Types) {
		return m.Types[m.Funcs[j]], true
	}
	return FuncType{}, false
}

// Custom is the contents of the first custom section with the name.
func (m *Module) Custom(name string) ([]byte, bool) {
	for _, s := range m.Sections {
		if s.ID == SecCustom && s.Name == name {
			return s.Data, true
		}
	}
	return nil, false
}

// Memory is the initial contents of linear memory: the active data
// segments with constant offsets, up to 1 GiB.
func (m *Module) Memory() []byte {
	var mem []byte
	for _, d := range m.Datas {
		if !d.Active {
			continue
		}
		end := d.Offset + uint64(len(d.Bytes))
		if end > 1<<30 {
			continue
		}
		if uint64(len(mem)) < end {
			mem = append(mem, make([]byte, end-uint64(len(mem)))...)
		}
		copy(mem[d.Offset:], d.Bytes)
	}
	return mem
}

// NameSection reads the function names of a name section, by index.
func NameSection(b []byte) (map[uint32]string, bool) {
	r := &reader{b: b}
	for r.pos < len(b) && r.err == nil {
		id := r.byte()
		sub := r.bytes(r.uleb())
		if id != 1 {
			continue
		}
		sr := &reader{b: sub}
		names := map[uint32]string{}
		for n := sr.uleb(); n > 0 && sr.err == nil; n-- {
			i := uint32(sr.uleb())
			names[i] = sr.name()
		}
		return names, sr.err == nil
	}
	return nil, r.err == nil
}
