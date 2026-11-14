package wasmobj

import (
	"bytes"
	"debug/gosym"
	"encoding/binary"
	"strconv"
)

// Func is a function as the symbol table names it. For a Go guest one
// Func covers the wasm functions the Go linker made of one Go function.
type Func struct {
	Name  string
	File  string // "" when unknown
	Line  int    // the first line, or 0
	Entry uint64 // the Go entry pc, or the wasm function index
}

// Syms names a module's functions by wasm function index (imports
// first). A Go guest is linked without a name section, but its pclntab,
// the table the Go runtime uses for tracebacks, is in its data; Syms
// finds it, as Inferno's tools find a .sbl beside a .dis. Other modules
// are named from their name section, else from their exports and
// imports.
type Syms struct {
	nimports int
	tab      *gosym.Table
	funcs    []Func
	byentry  map[uint64]int
}

// funcValueOffset is the Go linker's offset between a function's index
// among the defined functions and its PC_F (cmd/link/internal/wasm).
const funcValueOffset = 0x1000

var pclnMagic = []byte{0xf1, 0xff, 0xff, 0xff, 0x00, 0x00, 0x01, 0x08}

// IsGo reports whether the names come from a Go symbol table.
func (s *Syms) IsGo() bool { return s.tab != nil }

// Funcs lists the functions, in the symbol table's order.
func (s *Syms) Funcs() []Func { return s.funcs }

// Lookup returns the function wasm function index i belongs to, or nil.
func (s *Syms) Lookup(i uint32) *Func {
	if s == nil {
		return nil
	}
	if s.tab != nil {
		if int(i) < s.nimports {
			return nil
		}
		f := s.tab.PCToFunc(uint64(funcValueOffset + int(i) - s.nimports))
		if f == nil {
			return nil
		}
		return &s.funcs[s.byentry[f.Entry]]
	}
	if j, ok := s.byentry[uint64(i)]; ok {
		return &s.funcs[j]
	}
	return nil
}

// Syms builds the module's symbol table.
func (m *Module) Syms() *Syms {
	s := &Syms{nimports: m.Imported(KFunc), byentry: map[uint64]int{}}
	if tab := gotable(m.Memory()); tab != nil {
		s.tab = tab
		for i := range tab.Funcs {
			f := &tab.Funcs[i]
			file, line, _ := tab.PCToLine(f.Entry)
			s.byentry[f.Entry] = len(s.funcs)
			s.funcs = append(s.funcs, Func{Name: f.Name, File: file, Line: line, Entry: f.Entry})
		}
		return s
	}
	var names map[uint32]string
	if b, ok := m.Custom("name"); ok {
		names, _ = NameSection(b)
	}
	if len(names) == 0 {
		names = map[uint32]string{}
		for _, e := range m.Exports {
			if e.Kind == KFunc {
				names[e.Index] = e.Name
			}
		}
	}
	i := 0
	for _, im := range m.Imports {
		if im.Kind == KFunc {
			if _, ok := names[uint32(i)]; !ok {
				names[uint32(i)] = im.Module + "." + im.Name
			}
			i++
		}
	}
	for i := 0; i < s.nimports+len(m.Funcs); i++ {
		name, ok := names[uint32(i)]
		if !ok {
			name = "func" + strconv.Itoa(i)
		}
		s.byentry[uint64(i)] = len(s.funcs)
		s.funcs = append(s.funcs, Func{Name: name, Entry: uint64(i)})
	}
	return s
}

// gotable finds a Go pclntab in a module's initial memory.
func gotable(mem []byte) *gosym.Table {
	for i := 0; ; {
		j := bytes.Index(mem[i:], pclnMagic)
		if j < 0 {
			return nil
		}
		i += j
		if len(mem)-i > 64 && binary.LittleEndian.Uint64(mem[i+8:]) > 0 {
			lt := gosym.NewLineTable(mem[i:], 0)
			if tab, err := gosym.NewTable(nil, lt); err == nil && len(tab.Funcs) > 0 {
				return tab
			}
		}
		i++
	}
}
