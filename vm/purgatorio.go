package vm

import "github.com/ericvh/purgatorio-go/abi"

// errmax is Inferno's ERRMAX: the longest error string kept.
const errmax = 128

func purgatorioFuncs() []Func {
	f := func(name, params, results string, call func(*Proc, Memory, []uint64) ([]uint64, error)) Func {
		return Func{Module: abi.Module, Name: name, Params: sig(params), Results: sig(results), Call: call}
	}
	return []Func{
		// errstr(buf, blen) -> n: copy the prog's error string out.
		f("errstr", "ii", "i", func(p *Proc, m Memory, a []uint64) ([]uint64, error) {
			s := p.Errstr
			if n := int(uint32(a[1])); len(s) > n {
				s = s[:n]
			}
			if !m.Write(uint32(a[0]), []byte(s)) {
				p.Errstr = abi.Ebadaddr
				return ret(-1)
			}
			return ret(int32(len(s)))
		}),
		// werrstr(ptr, len): set the prog's error string.
		f("werrstr", "ii", "", func(p *Proc, m Memory, a []uint64) ([]uint64, error) {
			n := uint32(a[1])
			if n > errmax {
				n = errmax
			}
			b, ok := m.Read(uint32(a[0]), n)
			if !ok {
				p.Errstr = abi.Ebadaddr
				return nil, nil
			}
			p.Errstr = string(b)
			return nil, nil
		}),
		// exits(status, slen): end the prog with an Inferno status string.
		f("exits", "ii", "", func(p *Proc, m Memory, a []uint64) ([]uint64, error) {
			b, ok := m.Read(uint32(a[0]), uint32(a[1]))
			if !ok {
				b = []byte("bad exit status")
			}
			p.Status = string(b)
			return nil, &Exit{Status: p.Status}
		}),
	}
}
