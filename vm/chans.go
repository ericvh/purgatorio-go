package vm

import (
	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
)

// Name space and channel calls of the purgatorio module (M3). See
// docs/abi.md for the layouts.

func chanFuncs() []Func {
	f := func(name, params, results string, call hostFn) Func {
		return Func{Module: abi.Module, Name: name, Params: sig(params), Results: sig(results), Call: call}
	}
	return []Func{
		f("mount", "iiiiiii", "i", pMount),
		f("fauth", "iii", "i", pFauth),
		f("fversion", "iiiiii", "i", pFversion),
		f("export", "iiii", "i", pExport),
		f("file2chan", "iiiii", "i", pFile2chan),
		f("chan_new", "i", "i", pChanNew),
		f("chan_close", "i", "i", pChanClose),
		f("chan_send", "iiiii", "i", pChanSend),
		f("chan_recv", "iiiiii", "i", pChanRecv),
		f("alt", "iii", "i", pAlt),
		f("wait", "iii", "i", pWait),
	}
}

func pMount(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	old, ok1 := in.str(m, a[2], a[3])
	spec, ok2 := in.str(m, a[5], a[6])
	if !ok1 || !ok2 {
		return ret(-1)
	}
	if err := in.Prog.Mount(int(i32(a[0])), int(i32(a[1])), old, uint32(a[4]), spec); err != nil {
		return in.pfail(err)
	}
	return ret(0)
}

func pFauth(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	spec, okp := in.str(m, a[1], a[2])
	if !okp {
		return ret(-1)
	}
	fd, err := in.Prog.Fauth(int(i32(a[0])), spec)
	if err != nil {
		return in.pfail(err)
	}
	return ret(int64(fd))
}

// fversion(fd, msize, ver, vlen, buf, blen) -> n: the agreed version is
// written to buf.
func pFversion(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	ver, okp := in.str(m, a[2], a[3])
	if !okp {
		return ret(-1)
	}
	v, err := in.Prog.Fversion(int(i32(a[0])), uint32(a[1]), ver)
	if err != nil {
		return in.pfail(err)
	}
	return putStr(in, m, a[4], a[5], v)
}

// export(fd, dir, dlen, flags): flags 1 (EXPASYNC) returns at once;
// 0x40000000 (EXPRDONLY) serves the tree read-only.
func pExport(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	dir, okp := in.str(m, a[1], a[2])
	if !okp {
		return ret(-1)
	}
	if err := in.Prog.Export(int(i32(a[0])), dir, uint32(a[3])&(abi.EXPASYNC|abi.EXPRDONLY)); err != nil {
		return in.pfail(err)
	}
	return ret(0)
}

// file2chan(dir, dlen, name, nlen, handles) writes the read and write
// channel handles as two i32s.
func pFile2chan(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	dir, ok1 := in.str(m, a[0], a[1])
	name, ok2 := in.str(m, a[2], a[3])
	if !ok1 || !ok2 {
		return ret(-1)
	}
	r, w, err := in.Prog.File2chan(dir, name)
	if err != nil {
		return in.pfail(err)
	}
	if !putU32(m, uint32(a[4]), uint32(r)) || !putU32(m, uint32(a[4])+4, uint32(w)) {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	return ret(0)
}

func pChanNew(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	return ret(int64(in.Prog.Kgrp().Alloc(core.NewKChan(int(i32(a[0]))))))
}

func pChanClose(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	k, err := in.Prog.Kgrp().Drop(int(i32(a[0])))
	if err != nil {
		return in.pfail(err)
	}
	k.Close()
	return ret(0)
}

// takeCaps reads n handles at ptr and removes them from the sender: a
// sent channel moves to the receiver.
func (in *Instance) caps(m Memory, ptr, n uint64) ([]core.Cap, []int, bool) {
	hs, okh := getI32s(m, ptr, n)
	if !okh {
		return nil, nil, false
	}
	var caps []core.Cap
	for _, h := range hs {
		k, err := in.Prog.Kgrp().Get(h)
		if err != nil {
			return nil, nil, false
		}
		caps = append(caps, core.Cap{Chan: k})
	}
	return caps, hs, true
}

func (in *Instance) dropCaps(hs []int) {
	for _, h := range hs {
		_, _ = in.Prog.Kgrp().Drop(h)
	}
}

// installCaps gives received capabilities handles (channels) or fds.
func (in *Instance) installCaps(caps []core.Cap) []int {
	var out []int
	for _, c := range caps {
		switch {
		case c.Chan != nil:
			out = append(out, in.Prog.Kgrp().Alloc(c.Chan))
		case c.Fd != nil:
			fd, err := in.Prog.Fgrp().Alloc(c.Fd)
			if err != nil {
				c.Fd.DecRef()
				fd = -1
			}
			out = append(out, fd)
		}
	}
	return out
}

func pChanSend(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	k, err := in.Prog.Kgrp().Get(int(i32(a[0])))
	if err != nil {
		return in.pfail(err)
	}
	b, okb := m.Read(uint32(a[1]), uint32(a[2]))
	caps, hs, okc := in.caps(m, a[3], a[4])
	if !okb || !okc {
		in.Prog.SetErrstr(abi.Ebadarg)
		return ret(-1)
	}
	if err := k.Send(in.Prog.Ctx, core.KMsg{Bytes: append([]byte(nil), b...), Caps: caps}); err != nil {
		return in.pfail(err)
	}
	in.dropCaps(hs)
	return ret(0)
}

// putMsg writes a received message: its bytes (truncated to blen) at buf,
// its capability handles at caps (at most maxcaps), and the number of
// handles at ncaps_ptr. It returns the byte count.
func (in *Instance) putMsg(m Memory, msg core.KMsg, buf, blen, caps, maxcaps, ncapsPtr uint64) ([]uint64, error) {
	b := msg.Bytes
	if len(b) > int(uint32(blen)) {
		b = b[:uint32(blen)]
	}
	hs := in.installCaps(msg.Caps)
	if len(hs) > int(uint32(maxcaps)) {
		hs = hs[:uint32(maxcaps)]
	}
	ok := m.Write(uint32(buf), b)
	for i, h := range hs {
		ok = ok && putU32(m, uint32(caps)+uint32(4*i), uint32(h))
	}
	if ncapsPtr != 0 {
		ok = ok && putU32(m, uint32(ncapsPtr), uint32(len(hs)))
	}
	if !ok {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	return ret(int64(len(b)))
}

// chan_recv(h, buf, blen, caps, maxcaps, ncaps_ptr) -> n. A closed, empty
// channel is an error, "i/o on hungup channel".
func pChanRecv(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	k, err := in.Prog.Kgrp().Get(int(i32(a[0])))
	if err != nil {
		return in.pfail(err)
	}
	msg, ok, err := k.Recv(in.Prog.Ctx)
	if err != nil {
		return in.pfail(err)
	}
	if !ok {
		return in.pfail(core.Error(abi.Ehungup))
	}
	return in.putMsg(m, msg, a[1], a[2], a[3], a[4], a[5])
}

// alt(ops, nops, block) -> index. Each op is six i32s: handle, kind (0
// receive, 1 send), buf, len, caps, ncaps. A send sends buf[:len] with the
// handles at caps; a receive fills buf and caps and rewrites len and ncaps
// with what arrived. Without block, -1 means no arm was ready; a closed
// channel's receive completes with len -1.
func pAlt(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	n := uint32(a[1])
	raw, okr := m.Read(uint32(a[0]), n*24)
	if !okr {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	raw = append([]byte(nil), raw...)
	ops := make([]core.AltOp, n)
	moved := make([][]int, n)
	for i := uint32(0); i < n; i++ {
		f := raw[i*24:]
		k, err := in.Prog.Kgrp().Get(int(int32(le.Uint32(f[0:]))))
		if err != nil {
			return in.pfail(err)
		}
		ops[i].Chan = k
		if le.Uint32(f[4:]) == 1 {
			b, okb := m.Read(le.Uint32(f[8:]), le.Uint32(f[12:]))
			caps, hs, okc := in.caps(m, uint64(le.Uint32(f[16:])), uint64(le.Uint32(f[20:])))
			if !okb || !okc {
				in.Prog.SetErrstr(abi.Ebadarg)
				return ret(-1)
			}
			ops[i].Send = &core.KMsg{Bytes: append([]byte(nil), b...), Caps: caps}
			moved[i] = hs
		}
	}
	idx, err := core.Alt(in.Prog.Ctx, ops, a[2] != 0)
	if err != nil {
		return in.pfail(err)
	}
	if idx < 0 {
		return ret(-1)
	}
	f := raw[idx*24:]
	if ops[idx].Send != nil {
		in.dropCaps(moved[idx])
		return ret(int64(idx))
	}
	base := uint32(a[0]) + uint32(idx)*24
	if !ops[idx].Ok {
		putU32(m, base+12, ^uint32(0))
		return ret(int64(idx))
	}
	nb, err := in.putMsg(m, ops[idx].Got, uint64(le.Uint32(f[8:])), uint64(le.Uint32(f[12:])),
		uint64(le.Uint32(f[16:])), uint64(le.Uint32(f[20:])), uint64(base+20))
	if err != nil || i32(nb[0]) < 0 {
		return nb, err
	}
	putU32(m, base+12, uint32(nb[0]))
	return ret(int64(idx))
}

// wait(ops, nops, timeout_ms) -> index: ops are pairs of i32 (kind, id),
// kind 0 fd readable, 1 fd writable, 2 channel receivable, 3 channel
// sendable. It returns the first ready op, or -1 after the timeout
// (timeout < 0 waits for ever).
func pWait(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	pairs, okp := getI32s(m, a[0], 2*a[1])
	if !okp {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	type op struct {
		kind int
		c    *core.Chan
		k    *core.KChan
	}
	var ops []op
	for i := 0; i+1 < len(pairs); i += 2 {
		o := op{kind: pairs[i]}
		var err error
		switch o.kind {
		case 0, 1:
			o.c, err = in.Prog.Chan(pairs[i+1])
		case 2, 3:
			o.k, err = in.Prog.Kgrp().Get(pairs[i+1])
		default:
			err = core.Error(abi.Ebadarg)
		}
		if err != nil {
			return in.pfail(err)
		}
		ops = append(ops, o)
	}
	ready := func(o op) bool {
		switch o.kind {
		case 0, 1:
			if w, ok := o.c.Dev.(core.Waiter); ok {
				return w.Ready(o.c, o.kind == 1)
			}
			return true
		case 2:
			return o.k.CanRecv()
		}
		return o.k.CanSend()
	}
	first := func() int {
		for i, o := range ops {
			if ready(o) {
				return i
			}
		}
		return -1
	}
	var r core.Rendez
	for _, o := range ops {
		if o.c != nil {
			if w, ok := o.c.Dev.(core.Waiter); ok {
				w.Watch(o.c, o.kind == 1, &r)
			}
		} else {
			o.k.Watch(&r)
		}
	}
	_, err := core.ParkTimeout(in.Prog.Ctx, &r, func() bool { return first() >= 0 }, int(i32(a[2])))
	for _, o := range ops {
		if o.c != nil {
			if w, ok := o.c.Dev.(core.Waiter); ok {
				w.Unwatch(o.c, &r)
			}
		} else {
			o.k.Unwatch(&r)
		}
	}
	if err != nil {
		return in.pfail(err)
	}
	return ret(int64(first()))
}
