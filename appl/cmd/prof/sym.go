package main

import (
	"bytes"
	"debug/gosym"
	"encoding/binary"
	"errors"
)

// A Go guest is linked without a name section, but its pclntab, the
// table the Go runtime uses for tracebacks, is in its data. symtab finds
// it and maps wasm function indexes to Go functions.

// funcValueOffset is the Go linker's offset between a function's index
// among the defined functions and its PC_F (cmd/link/internal/wasm).
const funcValueOffset = 0x1000

type symtab struct {
	nimports int
	tab      *gosym.Table
}

// fn returns the Go function for wasm function index i.
func (s *symtab) fn(i uint32) *gosym.Func {
	if s == nil || int(i) < s.nimports {
		return nil
	}
	pc := uint64(funcValueOffset + int(i) - s.nimports)
	return s.tab.PCToFunc(pc)
}

type wasmreader struct {
	b   []byte
	err bool
}

func (r *wasmreader) byte() byte {
	if len(r.b) == 0 {
		r.err = true
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *wasmreader) uleb() uint64 {
	var v uint64
	for shift := 0; shift < 64; shift += 7 {
		c := r.byte()
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			break
		}
	}
	return v
}

func (r *wasmreader) bytes(n uint64) []byte {
	if uint64(len(r.b)) < n {
		r.err = true
		n = uint64(len(r.b))
	}
	b := r.b[:n]
	r.b = r.b[n:]
	return b
}

// constexpr reads an init expression, i32.const n; end, and returns n.
func (r *wasmreader) constexpr() uint64 {
	var v uint64
	for {
		switch op := r.byte(); op {
		case 0x41: // i32.const (signed leb)
			v = r.uleb()
		case 0x0b:
			return v
		default:
			if r.err {
				return 0
			}
			if op == 0x23 { // global.get
				r.uleb()
			} else {
				r.err = true
				return 0
			}
		}
	}
}

var pclnMagic = []byte{0xf1, 0xff, 0xff, 0xff, 0x00, 0x00, 0x01, 0x08}

// readsym builds the symbol table of a Go wasm module.
func readsym(code []byte) (*symtab, error) {
	if len(code) < 8 || string(code[:4]) != "\x00asm" {
		return nil, errors.New("not a wasm module")
	}
	r := &wasmreader{b: code[8:]}
	s := &symtab{}
	var mem []byte
	for len(r.b) > 0 && !r.err {
		id := r.byte()
		sec := &wasmreader{b: r.bytes(r.uleb())}
		switch id {
		case 2: // imports
			for n := sec.uleb(); n > 0 && !sec.err; n-- {
				sec.bytes(sec.uleb())
				sec.bytes(sec.uleb())
				switch sec.byte() {
				case 0:
					sec.uleb()
					s.nimports++
				case 1:
					sec.byte()
					if sec.byte()&1 != 0 {
						sec.uleb()
					}
					sec.uleb()
				case 2:
					if sec.byte()&1 != 0 {
						sec.uleb()
					}
					sec.uleb()
				case 3:
					sec.byte()
					sec.byte()
				}
			}
		case 11: // data
			for n := sec.uleb(); n > 0 && !sec.err; n-- {
				off := uint64(0)
				switch sec.uleb() {
				case 0:
					off = sec.constexpr()
				case 2:
					sec.uleb()
					off = sec.constexpr()
				}
				b := sec.bytes(sec.uleb())
				if end := off + uint64(len(b)); end <= 1<<30 {
					if uint64(len(mem)) < end {
						mem = append(mem, make([]byte, end-uint64(len(mem)))...)
					}
					copy(mem[off:], b)
				}
			}
		}
		if sec.err {
			r.err = true
		}
	}
	if r.err {
		return nil, errors.New("bad wasm module")
	}
	for i := 0; ; {
		j := bytes.Index(mem[i:], pclnMagic)
		if j < 0 {
			return nil, errors.New("no Go symbol table")
		}
		i += j
		if len(mem)-i > 64 && binary.LittleEndian.Uint64(mem[i+8:]) > 0 {
			lt := gosym.NewLineTable(mem[i:], 0)
			if tab, err := gosym.NewTable(nil, lt); err == nil && len(tab.Funcs) > 0 {
				s.tab = tab
				return s, nil
			}
		}
		i++
	}
}
