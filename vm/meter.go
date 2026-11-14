package vm

import (
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
)

// Meter charges one module instance's wasm memory to its prog, against
// the prog's memory limit and those of its domains (core/limits.go). The
// memory and tables a module starts with are charged before it is
// instantiated, so a module already past a limit fails then; the engine
// charges each memory.grow through Charge, which says no past a limit.
// Release returns everything when the instance is gone.
type Meter struct {
	p       *core.Prog
	mu      sync.Mutex
	held    uint64 // charged to the prog
	prepaid uint64 // initial memory charged but not yet allocated
}

// NewMeter charges the initial memory and tables of code to p. A module
// whose initial memory is past a limit fails with out of memory.
func NewMeter(p *core.Prog, code []byte) (*Meter, error) {
	mem, tab := InitialSizes(code)
	m := &Meter{p: p}
	n := mem + tab*core.TableElemBytes
	if !p.ChargeMem(n) {
		return nil, core.Error(abi.Enovmem)
	}
	m.held, m.prepaid = n, mem
	return m, nil
}

// Charge asks to grow the instance's memory by n bytes. The first
// allocations use the initial memory NewMeter charged.
func (m *Meter) Charge(n uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := min(n, m.prepaid)
	m.prepaid -= k
	n -= k
	if !m.p.ChargeMem(n) {
		return false
	}
	m.held += n
	return true
}

// Release returns what the instance was charged.
func (m *Meter) Release() {
	m.mu.Lock()
	n := m.held
	m.held, m.prepaid = 0, 0
	m.mu.Unlock()
	m.p.UnchargeMem(n)
}

// InitialSizes reads the bytes of linear memory and the table elements a
// module defines at instantiation, from its memory and table sections.
// Imported memories and tables belong to another module and are not
// counted. A malformed module gives what was read; the engine rejects it.
func InitialSizes(code []byte) (mem, elems uint64) {
	if len(code) < 8 {
		return 0, 0
	}
	r := wasmReader{b: code[8:]}
	for !r.done() {
		id := r.byte()
		size := r.uleb()
		if r.bad || size > uint64(len(r.b)) {
			return
		}
		sec := wasmReader{b: r.b[:size]}
		r.b = r.b[size:]
		switch id {
		case 4: // tables
			for n := sec.uleb(); n > 0 && !sec.bad; n-- {
				if sec.peek() == 0x40 { // a table with an initializer
					sec.byte()
					sec.byte()
				}
				sec.byte() // reference type
				elems += sec.limitsMin()
			}
		case 5: // memories
			for n := sec.uleb(); n > 0 && !sec.bad; n-- {
				mem += sec.limitsMin() * 65536
			}
		}
	}
	return
}

// wasmReader decodes the parts of the wasm binary format InitialSizes
// needs.
type wasmReader struct {
	b   []byte
	bad bool
}

func (r *wasmReader) done() bool { return r.bad || len(r.b) == 0 }

func (r *wasmReader) peek() byte {
	if len(r.b) == 0 {
		r.bad = true
		return 0
	}
	return r.b[0]
}

func (r *wasmReader) byte() byte {
	c := r.peek()
	if !r.bad {
		r.b = r.b[1:]
	}
	return c
}

func (r *wasmReader) uleb() uint64 {
	var v uint64
	for shift := 0; shift < 64; shift += 7 {
		c := r.byte()
		if r.bad {
			return 0
		}
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v
		}
	}
	r.bad = true
	return 0
}

// limitsMin reads a limits and returns its minimum.
func (r *wasmReader) limitsMin() uint64 {
	flags := r.byte()
	lo := r.uleb()
	if flags&1 != 0 {
		r.uleb() // maximum
	}
	return lo
}
