package vm

import (
	"errors"
	"io"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
)

// wasiSigs is the full WASI preview 1 import surface. Every function is
// registered so any guest links; those not yet implemented return ENOSYS.
var wasiSigs = []struct{ name, params, results string }{
	{"args_get", "ii", "i"},
	{"args_sizes_get", "ii", "i"},
	{"environ_get", "ii", "i"},
	{"environ_sizes_get", "ii", "i"},
	{"clock_res_get", "ii", "i"},
	{"clock_time_get", "iIi", "i"},
	{"fd_advise", "iIIi", "i"},
	{"fd_allocate", "iII", "i"},
	{"fd_close", "i", "i"},
	{"fd_datasync", "i", "i"},
	{"fd_fdstat_get", "ii", "i"},
	{"fd_fdstat_set_flags", "ii", "i"},
	{"fd_fdstat_set_rights", "iII", "i"},
	{"fd_filestat_get", "ii", "i"},
	{"fd_filestat_set_size", "iI", "i"},
	{"fd_filestat_set_times", "iIIi", "i"},
	{"fd_pread", "iiiIi", "i"},
	{"fd_prestat_get", "ii", "i"},
	{"fd_prestat_dir_name", "iii", "i"},
	{"fd_pwrite", "iiiIi", "i"},
	{"fd_read", "iiii", "i"},
	{"fd_readdir", "iiiIi", "i"},
	{"fd_renumber", "ii", "i"},
	{"fd_seek", "iIii", "i"},
	{"fd_sync", "i", "i"},
	{"fd_tell", "ii", "i"},
	{"fd_write", "iiii", "i"},
	{"path_create_directory", "iii", "i"},
	{"path_filestat_get", "iiiii", "i"},
	{"path_filestat_set_times", "iiiiIIi", "i"},
	{"path_link", "iiiiiii", "i"},
	{"path_open", "iiiiiIIii", "i"},
	{"path_readlink", "iiiiii", "i"},
	{"path_remove_directory", "iii", "i"},
	{"path_rename", "iiiiii", "i"},
	{"path_symlink", "iiiii", "i"},
	{"path_unlink_file", "iii", "i"},
	{"poll_oneoff", "iiii", "i"},
	{"proc_exit", "i", ""},
	{"proc_raise", "i", "i"},
	{"sched_yield", "", "i"},
	{"random_get", "ii", "i"},
	{"sock_accept", "iii", "i"},
	{"sock_recv", "iiiiii", "i"},
	{"sock_send", "iiiii", "i"},
	{"sock_shutdown", "ii", "i"},
}

type hostFn = func(*Proc, Memory, []uint64) ([]uint64, error)

var wasiImpl = map[string]hostFn{
	"args_get":            func(p *Proc, m Memory, a []uint64) ([]uint64, error) { return strvGet(m, p.Args, a) },
	"args_sizes_get":      func(p *Proc, m Memory, a []uint64) ([]uint64, error) { return strvSizes(m, p.Args, a) },
	"environ_get":         func(p *Proc, m Memory, a []uint64) ([]uint64, error) { return strvGet(m, p.Env, a) },
	"environ_sizes_get":   func(p *Proc, m Memory, a []uint64) ([]uint64, error) { return strvSizes(m, p.Env, a) },
	"clock_res_get":       clockResGet,
	"clock_time_get":      clockTimeGet,
	"fd_close":            fdClose,
	"fd_fdstat_get":       fdFdstatGet,
	"fd_fdstat_set_flags": fdFdstatSetFlags,
	"fd_prestat_get":      func(*Proc, Memory, []uint64) ([]uint64, error) { return errno(abi.ErrnoBadf) },
	"fd_read":             fdRead,
	"fd_write":            fdWrite,
	"poll_oneoff":         pollOneoff,
	"proc_exit": func(p *Proc, m Memory, a []uint64) ([]uint64, error) {
		p.Status = status(uint32(a[0]))
		return nil, &Exit{Status: p.Status}
	},
	"sched_yield": func(*Proc, Memory, []uint64) ([]uint64, error) { return ok() },
	"random_get": func(p *Proc, m Memory, a []uint64) ([]uint64, error) {
		b := make([]byte, uint32(a[1]))
		p.Entropy(b)
		if !m.Write(uint32(a[0]), b) {
			return errno(abi.ErrnoFault)
		}
		return ok()
	},
}

func wasiFuncs() []Func {
	fs := make([]Func, 0, len(wasiSigs))
	for _, s := range wasiSigs {
		call, found := wasiImpl[s.name]
		if !found {
			call = func(*Proc, Memory, []uint64) ([]uint64, error) { return errno(abi.ErrnoNosys) }
		}
		fs = append(fs, Func{Module: abi.WASIModule, Name: s.name,
			Params: sig(s.params), Results: sig(s.results), Call: call})
	}
	return fs
}

func strvSizes(m Memory, v []string, a []uint64) ([]uint64, error) {
	n := 0
	for _, s := range v {
		n += len(s) + 1
	}
	if !putU32(m, uint32(a[0]), uint32(len(v))) || !putU32(m, uint32(a[1]), uint32(n)) {
		return errno(abi.ErrnoFault)
	}
	return ok()
}

func strvGet(m Memory, v []string, a []uint64) ([]uint64, error) {
	ptrs, buf := uint32(a[0]), uint32(a[1])
	for i, s := range v {
		if !putU32(m, ptrs+uint32(4*i), buf) || !m.Write(buf, append([]byte(s), 0)) {
			return errno(abi.ErrnoFault)
		}
		buf += uint32(len(s) + 1)
	}
	return ok()
}

const (
	clockRealtime  = 0
	clockMonotonic = 1
)

var boot = time.Now()

func clockResGet(p *Proc, m Memory, a []uint64) ([]uint64, error) {
	if uint32(a[0]) > clockMonotonic {
		return errno(abi.ErrnoInval)
	}
	if !putU64(m, uint32(a[1]), 1000) {
		return errno(abi.ErrnoFault)
	}
	return ok()
}

func clockTimeGet(p *Proc, m Memory, a []uint64) ([]uint64, error) {
	var t uint64
	switch uint32(a[0]) {
	case clockRealtime:
		t = uint64(p.Now())
	case clockMonotonic:
		t = uint64(time.Since(boot))
	default:
		return errno(abi.ErrnoInval)
	}
	if !putU64(m, uint32(a[2]), t) {
		return errno(abi.ErrnoFault)
	}
	return ok()
}

// Until the fd group lands, only the three standard streams exist.
func stdio(fd uint32) bool { return fd <= 2 }

func fdClose(p *Proc, m Memory, a []uint64) ([]uint64, error) {
	if !stdio(uint32(a[0])) {
		return errno(abi.ErrnoBadf)
	}
	return ok()
}

const (
	filetypeCharacterDevice = 2
	rightsAll               = ^uint64(0) >> 35 // the 29 defined rights
)

func fdFdstatGet(p *Proc, m Memory, a []uint64) ([]uint64, error) {
	if !stdio(uint32(a[0])) {
		return errno(abi.ErrnoBadf)
	}
	var st [24]byte
	st[0] = filetypeCharacterDevice
	le.PutUint64(st[8:], rightsAll)
	le.PutUint64(st[16:], rightsAll)
	if !m.Write(uint32(a[1]), st[:]) {
		return errno(abi.ErrnoFault)
	}
	return ok()
}

func fdFdstatSetFlags(p *Proc, m Memory, a []uint64) ([]uint64, error) {
	if !stdio(uint32(a[0])) {
		return errno(abi.ErrnoBadf)
	}
	return ok()
}

// iovecs walks a WASI iovec array, calling f on each buffer view.
func iovecs(m Memory, iovs, n uint32, f func([]byte) (int, bool)) (uint32, uint16) {
	var total uint32
	for i := uint32(0); i < n; i++ {
		ptr, ok1 := getU32(m, iovs+8*i)
		ln, ok2 := getU32(m, iovs+8*i+4)
		if !ok1 || !ok2 {
			return total, abi.ErrnoFault
		}
		b, ok := m.Read(ptr, ln)
		if !ok {
			return total, abi.ErrnoFault
		}
		k, more := f(b)
		total += uint32(k)
		if !more {
			break
		}
	}
	return total, abi.ErrnoSuccess
}

func fdWrite(p *Proc, m Memory, a []uint64) ([]uint64, error) {
	var w = p.Stdout
	switch uint32(a[0]) {
	case 1:
	case 2:
		w = p.Stderr
	default:
		p.Errstr = abi.Ebadfd
		return errno(abi.ErrnoBadf)
	}
	var werr error
	n, e := iovecs(m, uint32(a[1]), uint32(a[2]), func(b []byte) (int, bool) {
		k, err := w.Write(b)
		werr = err
		return k, err == nil
	})
	if e != abi.ErrnoSuccess {
		p.Errstr = abi.Ebadaddr
		return errno(e)
	}
	if werr != nil {
		p.Errstr = abi.Ehungup
		return errno(abi.ErrnoPipe)
	}
	if !putU32(m, uint32(a[3]), n) {
		p.Errstr = abi.Ebadaddr
		return errno(abi.ErrnoFault)
	}
	return ok()
}

func fdRead(p *Proc, m Memory, a []uint64) ([]uint64, error) {
	if uint32(a[0]) != 0 {
		p.Errstr = abi.Ebadfd
		return errno(abi.ErrnoBadf)
	}
	var rerr error
	// One read per call: a short read from a terminal or pipe ends it.
	n, e := iovecs(m, uint32(a[1]), uint32(a[2]), func(b []byte) (int, bool) {
		if len(b) == 0 {
			return 0, true
		}
		k, err := p.Stdin.Read(b)
		rerr = err
		return k, false
	})
	if e != abi.ErrnoSuccess {
		p.Errstr = abi.Ebadaddr
		return errno(e)
	}
	if rerr != nil && n == 0 && !errors.Is(rerr, io.EOF) {
		p.Errstr = abi.Eio
		return errno(abi.ErrnoIO)
	}
	if !putU32(m, uint32(a[3]), n) {
		p.Errstr = abi.Ebadaddr
		return errno(abi.ErrnoFault)
	}
	return ok()
}

// poll_oneoff handles clock subscriptions (sleep) and reports fd
// subscriptions on the standard streams as ready at once, since reads and
// writes on them block in the host call itself.
func pollOneoff(p *Proc, m Memory, a []uint64) ([]uint64, error) {
	in, out, nsub := uint32(a[0]), uint32(a[1]), uint32(a[2])
	if nsub == 0 {
		return errno(abi.ErrnoInval)
	}
	const subSize, evSize = 48, 32
	subs, okr := m.Read(in, nsub*subSize)
	if !okr {
		return errno(abi.ErrnoFault)
	}
	subs = append([]byte(nil), subs...)
	var wait time.Duration = -1
	ready := false
	for i := uint32(0); i < nsub; i++ {
		s := subs[i*subSize:]
		switch s[8] {
		case 0: // clock
			d := time.Duration(le.Uint64(s[24:]))
			if le.Uint16(s[40:])&1 != 0 { // absolute
				d -= time.Duration(p.Now())
			}
			if wait < 0 || d < wait {
				wait = d
			}
		default:
			ready = true
		}
	}
	if !ready && wait > 0 {
		time.Sleep(wait)
	}
	var n uint32
	for i := uint32(0); i < nsub; i++ {
		s := subs[i*subSize:]
		if ready && s[8] == 0 {
			continue
		}
		var ev [evSize]byte
		copy(ev[0:8], s[0:8])
		ev[10] = s[8]
		if s[8] != 0 && !stdio(le.Uint32(s[16:])) {
			le.PutUint16(ev[8:], abi.ErrnoBadf)
		}
		if !m.Write(out+n*evSize, ev[:]) {
			return errno(abi.ErrnoFault)
		}
		n++
	}
	if !putU32(m, uint32(a[3]), n) {
		return errno(abi.ErrnoFault)
	}
	return ok()
}
