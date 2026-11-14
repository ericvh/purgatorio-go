// Package vm holds the guest ABI host functions, written once against a
// small Memory interface so any WebAssembly engine can carry them: WASI
// preview 1 implemented natively (no host WASI layer), plus the purgatorio
// extension module.
//
// It is a portable package. Engine adapters live in subpackages
// (vm/wazero).
package vm

import (
	"encoding/binary"
	"io"
	"strconv"
)

var le = binary.LittleEndian

// Memory is a guest's linear memory. Read returns a view of n bytes at off,
// or false if any of them is out of bounds.
type Memory interface {
	Read(off, n uint32) ([]byte, bool)
	Write(off uint32, b []byte) bool
}

// Exit stops the guest. It is returned by a host function, never by a guest.
type Exit struct {
	Status string // Inferno exit status; empty means success
}

func (e *Exit) Error() string { return "exit: " + e.Status }

// Code is the host process exit code for the status.
func (e *Exit) Code() uint32 {
	if e.Status == "" {
		return 0
	}
	return 1
}

// Proc is the prog-side state the host functions act on. In M1 it gains a
// core.Prog with its namespace and fd group; for now the standard streams
// are plain readers and writers.
type Proc struct {
	Args    []string
	Env     []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Now     func() int64 // ns since the epoch
	Entropy func([]byte)

	Errstr string
	Status string
}

// Type is a WebAssembly value type.
type Type byte

const (
	I32 Type = 'i'
	I64 Type = 'I'
)

// Func is one host function.
type Func struct {
	Module  string
	Name    string
	Params  []Type
	Results []Type
	// Call reads its arguments from args and returns its results. A
	// non-nil error (an *Exit) stops the guest.
	Call func(p *Proc, m Memory, args []uint64) ([]uint64, error)
}

func sig(s string) []Type {
	t := make([]Type, len(s))
	for i := range s {
		t[i] = Type(s[i])
	}
	return t
}

func ok() ([]uint64, error)            { return []uint64{0}, nil }
func errno(e uint16) ([]uint64, error) { return []uint64{uint64(e)}, nil }
func ret(v int32) ([]uint64, error)    { return []uint64{uint64(uint32(v))}, nil }
func status(code uint32) string {
	if code == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(code), 10)
}

func putU32(m Memory, off, v uint32) bool {
	var b [4]byte
	le.PutUint32(b[:], v)
	return m.Write(off, b[:])
}

func putU64(m Memory, off uint32, v uint64) bool {
	var b [8]byte
	le.PutUint64(b[:], v)
	return m.Write(off, b[:])
}

func getU32(m Memory, off uint32) (uint32, bool) {
	b, ok := m.Read(off, 4)
	if !ok {
		return 0, false
	}
	return le.Uint32(b), true
}

// HostFuncs returns every host function, in both modules.
func HostFuncs() []Func {
	return append(wasiFuncs(), purgatorioFuncs()...)
}
