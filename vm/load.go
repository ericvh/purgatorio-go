package vm

import (
	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
)

// Module loading (M5), the counterpart of Limbo's load: a reactor module
// built with sdk/module is instantiated in the calling prog, sharing its
// fds and name space, and its functions are called with byte arguments.
//
//	load(path, plen) -> handle
//	call(h, name, nlen, in, ilen, out, olen) -> n: the result length; when
//	    n > olen nothing is written and call_result fetches it
//	call_result(out, olen) -> n
//	unload(h) -> 0

func loadFuncs() []Func {
	f := func(name, params, results string, call hostFn) Func {
		return Func{Module: abi.Module, Name: name, Params: sig(params), Results: sig(results), Call: call}
	}
	return []Func{
		f("load", "ii", "i", pLoad),
		f("call", "iiiiiii", "i", pCall),
		f("call_result", "ii", "i", pCallResult),
		f("unload", "i", "i", pUnload),
	}
}

func pLoad(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	path, okp := in.str(m, a[0], a[1])
	if !okp {
		return ret(-1)
	}
	code, rec, err := in.M.LoadChecked(in.Prog, path)
	if err != nil {
		return in.pfail(err)
	}
	in.Prog.AddModule(rec, false)
	l, err := in.M.Engine.Load(in.Prog.Ctx, path, code, in)
	if err != nil {
		return in.pfail(core.Error(path + ": " + err.Error()))
	}
	in.mu.Lock()
	if in.mods == nil {
		in.mods = map[int]Loaded{}
	}
	in.modNext++
	h := in.modNext
	in.mods[h] = l
	in.mu.Unlock()
	return ret(int64(h))
}

func (in *Instance) mod(h uint64) (Loaded, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if l, ok := in.mods[int(i32(h))]; ok {
		return l, nil
	}
	return nil, core.Error(abi.Ebadarg)
}

func pCall(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	l, err := in.mod(a[0])
	if err != nil {
		return in.pfail(err)
	}
	name, ok1 := in.str(m, a[1], a[2])
	arg, ok2 := m.Read(uint32(a[3]), uint32(a[4]))
	if !ok1 || !ok2 {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	res, err := l.Call(in.Prog.Ctx, name, append([]byte(nil), arg...))
	if err != nil {
		return in.pfail(err)
	}
	if len(res) > int(uint32(a[6])) {
		in.mu.Lock()
		in.pending = res
		in.mu.Unlock()
		return ret(int64(len(res)))
	}
	if !m.Write(uint32(a[5]), res) {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	return ret(int64(len(res)))
}

func pCallResult(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	in.mu.Lock()
	res := in.pending
	in.pending = nil
	in.mu.Unlock()
	if len(res) > int(uint32(a[1])) {
		in.Prog.SetErrstr(abi.Etoosmall)
		return ret(-1)
	}
	if !m.Write(uint32(a[0]), res) {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	return ret(int64(len(res)))
}

func pUnload(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	in.mu.Lock()
	l, ok := in.mods[int(i32(a[0]))]
	delete(in.mods, int(i32(a[0])))
	in.mu.Unlock()
	if !ok {
		return in.pfail(core.Error(abi.Ebadarg))
	}
	l.Close()
	return ret(0)
}

// closeMods unloads every module when the instance ends.
func (in *Instance) closeMods() {
	in.mu.Lock()
	mods := in.mods
	in.mods = nil
	in.mu.Unlock()
	for _, l := range mods {
		l.Close()
	}
}
