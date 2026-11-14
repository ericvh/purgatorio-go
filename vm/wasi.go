package vm

import (
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/styx"
)

// wasiSigs is the full WASI preview 1 import surface. Every function is
// registered so any guest links; those Inferno has no meaning for return
// ENOTSUP or ENOSYS.
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

var wasiImpl = map[string]hostFn{
	"args_get":              func(in *Instance, m Memory, a []uint64) ([]uint64, error) { return strvGet(in, m, in.Args, a) },
	"args_sizes_get":        func(in *Instance, m Memory, a []uint64) ([]uint64, error) { return strvSizes(in, m, in.Args, a) },
	"environ_get":           func(in *Instance, m Memory, a []uint64) ([]uint64, error) { return strvGet(in, m, in.environ(), a) },
	"environ_sizes_get":     func(in *Instance, m Memory, a []uint64) ([]uint64, error) { return strvSizes(in, m, in.environ(), a) },
	"clock_res_get":         clockResGet,
	"clock_time_get":        clockTimeGet,
	"fd_advise":             fdCheck,
	"fd_allocate":           fdAllocate,
	"fd_close":              fdClose,
	"fd_datasync":           fdSync,
	"fd_sync":               fdSync,
	"fd_fdstat_get":         fdFdstatGet,
	"fd_fdstat_set_flags":   fdCheck2,
	"fd_fdstat_set_rights":  fdCheck,
	"fd_filestat_get":       fdFilestatGet,
	"fd_filestat_set_size":  fdFilestatSetSize,
	"fd_filestat_set_times": fdFilestatSetTimes,
	"fd_pread":              fdPread,
	"fd_pwrite":             fdPwrite,
	"fd_prestat_get":        fdPrestatGet,
	"fd_prestat_dir_name":   fdPrestatDirName,
	"fd_read":               fdRead,
	"fd_readdir":            fdReaddir,
	"fd_renumber":           fdRenumber,
	"fd_seek":               fdSeek,
	"fd_tell":               fdTell,
	"fd_write":              fdWrite,
	"path_create_directory": pathCreateDirectory,
	"path_filestat_get":     pathFilestatGet,
	"path_filestat_set_times": func(in *Instance, m Memory, a []uint64) ([]uint64, error) {
		p, ok := in.atPath(m, a[0], a[2], a[3])
		if !ok {
			return in.fault()
		}
		d := timesDir(in, a[5], a[6])
		if err := in.Prog.Wstat(p, &d); err != nil {
			return in.fail(err)
		}
		return ok2()
	},
	"path_link":             notsup,
	"path_symlink":          notsup,
	"path_readlink":         notsup,
	"path_open":             pathOpen,
	"path_remove_directory": func(in *Instance, m Memory, a []uint64) ([]uint64, error) { return removeKind(in, m, a, true) },
	"path_unlink_file":      func(in *Instance, m Memory, a []uint64) ([]uint64, error) { return removeKind(in, m, a, false) },
	"path_rename":           pathRename,
	"poll_oneoff":           pollOneoff,
	"proc_exit": func(in *Instance, m Memory, a []uint64) ([]uint64, error) {
		in.Status = status(uint32(a[0]))
		return nil, &Exit{Status: in.Status}
	},
	"proc_raise":  notsup,
	"sched_yield": func(*Instance, Memory, []uint64) ([]uint64, error) { return ok() },
	"random_get": func(in *Instance, m Memory, a []uint64) ([]uint64, error) {
		b := make([]byte, uint32(a[1]))
		in.M.Kernel.Platform.Entropy(b)
		if !m.Write(uint32(a[0]), b) {
			return in.fault()
		}
		return ok()
	},
	"sock_accept":   notsock,
	"sock_recv":     notsock,
	"sock_send":     notsock,
	"sock_shutdown": notsock,
}

func ok2() ([]uint64, error) { return ok() }

func notsup(*Instance, Memory, []uint64) ([]uint64, error)  { return errno(abi.ErrnoNotsup) }
func notsock(*Instance, Memory, []uint64) ([]uint64, error) { return errno(abi.ErrnoNotsock) }

func wasiFuncs() []Func {
	fs := make([]Func, 0, len(wasiSigs))
	for _, s := range wasiSigs {
		call, found := wasiImpl[s.name]
		if !found {
			call = func(*Instance, Memory, []uint64) ([]uint64, error) { return errno(abi.ErrnoNosys) }
		}
		fs = append(fs, Func{Module: abi.WASIModule, Name: s.name,
			Params: sig(s.params), Results: sig(s.results), Call: call})
	}
	return fs
}

// environ is the environment the guest sees: the prog's environment
// group, plus PWD so the Go runtime starts in the prog's current
// directory. It is taken once per instance, so environ_sizes_get and
// environ_get agree even while other progs sharing the group change it.
func (in *Instance) environ() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.env == nil {
		in.env = append(in.Prog.Egrp().Strings(), "PWD="+in.Prog.Dot())
	}
	return in.env
}

func strvSizes(in *Instance, m Memory, v []string, a []uint64) ([]uint64, error) {
	n := 0
	for _, s := range v {
		n += len(s) + 1
	}
	if !putU32(m, uint32(a[0]), uint32(len(v))) || !putU32(m, uint32(a[1]), uint32(n)) {
		return in.fault()
	}
	return ok()
}

func strvGet(in *Instance, m Memory, v []string, a []uint64) ([]uint64, error) {
	ptrs, buf := uint32(a[0]), uint32(a[1])
	for i, s := range v {
		if !putU32(m, ptrs+uint32(4*i), buf) || !m.Write(buf, append([]byte(s), 0)) {
			return in.fault()
		}
		buf += uint32(len(s) + 1)
	}
	return ok()
}

var boot = time.Now()

func clockResGet(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	if uint32(a[0]) > 3 {
		return errno(abi.ErrnoInval)
	}
	if !putU64(m, uint32(a[1]), 1000) {
		return in.fault()
	}
	return ok()
}

func clockTimeGet(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	var t uint64
	switch uint32(a[0]) {
	case 0:
		t = uint64(in.M.Kernel.Platform.Now())
	case 1, 2, 3:
		t = uint64(time.Since(boot))
	default:
		return errno(abi.ErrnoInval)
	}
	if !putU64(m, uint32(a[2]), t) {
		return in.fault()
	}
	return ok()
}

func (in *Instance) chan_(fd uint64) (*core.Chan, error) { return in.Prog.Chan(int(i32(fd))) }

func fdCheck(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	if _, err := in.chan_(a[0]); err != nil {
		return in.fail(err)
	}
	return ok()
}

func fdCheck2(in *Instance, m Memory, a []uint64) ([]uint64, error) { return fdCheck(in, m, a) }

func fdAllocate(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	d, err := in.Prog.Fstat(int(i32(a[0])))
	if err != nil {
		return in.fail(err)
	}
	if end := a[1] + a[2]; end > d.Length {
		nd := core.NullDir()
		nd.Length = end
		if err := in.Prog.Fwstat(int(i32(a[0])), &nd); err != nil {
			return in.fail(err)
		}
	}
	return ok()
}

func fdClose(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	fd := int(i32(a[0]))
	if err := in.Prog.Close(fd); err != nil {
		return in.fail(err)
	}
	in.mu.Lock()
	delete(in.dirs, fd)
	in.mu.Unlock()
	return ok()
}

// fdSync is a null wstat, Inferno's way to ask for a sync.
func fdSync(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	d := core.NullDir()
	if err := in.Prog.Fwstat(int(i32(a[0])), &d); err != nil {
		if core.Errstr(err) == abi.Eperm {
			return ok() // devices that refuse wstat have nothing to sync
		}
		return in.fail(err)
	}
	return ok()
}

const (
	ftUnknown = 0
	ftChar    = 2
	ftDir     = 3
	ftFile    = 4
)

func filetype(c *core.Chan) uint8 {
	switch {
	case c.IsDir():
		return ftDir
	case c.Dev.DC() == 'U' || c.Dev.DC() == 'M':
		return ftFile
	}
	return ftChar
}

func dirFiletype(d *styx.Dir) uint8 {
	switch {
	case d.Qid.Type&styx.QTDIR != 0:
		return ftDir
	case d.Type == 'U' || d.Type == 'M':
		return ftFile
	}
	return ftChar
}

const rightsAll = ^uint64(0) >> 35

func fdFdstatGet(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	c, err := in.chan_(a[0])
	if err != nil {
		return in.fail(err)
	}
	var st [24]byte
	st[0] = filetype(c)
	le.PutUint64(st[8:], rightsAll)
	le.PutUint64(st[16:], rightsAll)
	if !m.Write(uint32(a[1]), st[:]) {
		return in.fault()
	}
	return ok()
}

func putFilestat(m Memory, buf uint32, d *styx.Dir) bool {
	var st [64]byte
	le.PutUint64(st[0:], uint64(d.Type)<<32|uint64(d.Dev))
	le.PutUint64(st[8:], d.Qid.Path)
	st[16] = dirFiletype(d)
	le.PutUint64(st[24:], 1)
	le.PutUint64(st[32:], d.Length)
	le.PutUint64(st[40:], uint64(d.Atime)*1e9)
	le.PutUint64(st[48:], uint64(d.Mtime)*1e9)
	le.PutUint64(st[56:], uint64(d.Mtime)*1e9)
	return m.Write(buf, st[:])
}

func fdFilestatGet(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	d, err := in.Prog.Fstat(int(i32(a[0])))
	if err != nil {
		return in.fail(err)
	}
	if !putFilestat(m, uint32(a[1]), &d) {
		return in.fault()
	}
	return ok()
}

func fdFilestatSetSize(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	d := core.NullDir()
	d.Length = a[1]
	if err := in.Prog.Fwstat(int(i32(a[0])), &d); err != nil {
		return in.fail(err)
	}
	return ok()
}

// timesDir makes a wstat Dir from WASI times: only mtime is kept, as
// Inferno's wstat takes; "now" flags use the clock.
func timesDir(in *Instance, mtim, fst uint64) styx.Dir {
	d := core.NullDir()
	switch {
	case fst&8 != 0:
		d.Mtime = uint32(in.M.Kernel.Platform.Now() / 1e9)
	case fst&4 != 0:
		d.Mtime = uint32(mtim / 1e9)
	}
	return d
}

func fdFilestatSetTimes(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	d := timesDir(in, a[2], a[3])
	if err := in.Prog.Fwstat(int(i32(a[0])), &d); err != nil {
		return in.fail(err)
	}
	return ok()
}

func preopenName(fd int32) (string, bool) {
	switch fd {
	case abi.PreopenRootFD:
		return "/", true
	case abi.PreopenDotFD:
		return ".", true
	}
	return "", false
}

func fdPrestatGet(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	name, isPre := preopenName(i32(a[0]))
	if !isPre {
		return errno(abi.ErrnoBadf)
	}
	if _, err := in.chan_(a[0]); err != nil {
		return errno(abi.ErrnoBadf)
	}
	var b [8]byte
	le.PutUint32(b[4:], uint32(len(name)))
	if !m.Write(uint32(a[1]), b[:]) {
		return in.fault()
	}
	return ok()
}

func fdPrestatDirName(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	name, isPre := preopenName(i32(a[0]))
	if !isPre {
		return errno(abi.ErrnoBadf)
	}
	if uint32(a[2]) < uint32(len(name)) {
		return errno(abi.ErrnoInval)
	}
	if !m.Write(uint32(a[1]), []byte(name)) {
		return in.fault()
	}
	return ok()
}

// iovecs reads a WASI iovec array.
func iovecs(m Memory, iovs, n uint32) ([][]byte, bool) {
	out := make([][]byte, 0, n)
	for i := uint32(0); i < n; i++ {
		ptr, ok1 := getU32(m, iovs+8*i)
		ln, ok2 := getU32(m, iovs+8*i+4)
		if !ok1 || !ok2 {
			return nil, false
		}
		b, ok := m.Read(ptr, ln)
		if !ok {
			return nil, false
		}
		out = append(out, b)
	}
	return out, true
}

// rw does one read or write per call: a short count ends it, as on a
// terminal or pipe.
func rw(in *Instance, m Memory, a []uint64, f func(fd int, b []byte, off int64) (int, error), off int64, outArg uint64) ([]uint64, error) {
	vs, okv := iovecs(m, uint32(a[1]), uint32(a[2]))
	if !okv {
		return in.fault()
	}
	fd := int(i32(a[0]))
	total := 0
	for _, b := range vs {
		if len(b) == 0 {
			continue
		}
		n, err := f(fd, b, off+int64(total))
		if err != nil {
			if total > 0 {
				break
			}
			return in.fail(err)
		}
		total += n
		if n < len(b) {
			break
		}
	}
	if !putU32(m, uint32(outArg), uint32(total)) {
		return in.fault()
	}
	return ok()
}

func fdRead(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	read := func(fd int, b []byte, _ int64) (int, error) { return in.Prog.Read(fd, b) }
	return rw(in, m, a, read, 0, a[3])
}

func fdWrite(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	write := func(fd int, b []byte, _ int64) (int, error) { return in.Prog.Write(fd, b) }
	return rw(in, m, a, write, 0, a[3])
}

func fdPread(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	return rw(in, m, a, in.Prog.Pread, int64(a[3]), a[4])
}

func fdPwrite(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	return rw(in, m, a, in.Prog.Pwrite, int64(a[3]), a[4])
}

func fdSeek(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	off, err := in.Prog.Seek(int(i32(a[0])), int64(a[1]), int(uint8(a[2])))
	if err != nil {
		return in.fail(err)
	}
	if !putU64(m, uint32(a[3]), uint64(off)) {
		return in.fault()
	}
	return ok()
}

func fdTell(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	c, err := in.chan_(a[0])
	if err != nil {
		return in.fail(err)
	}
	if !putU64(m, uint32(a[1]), uint64(c.Offset())) {
		return in.fault()
	}
	return ok()
}

func fdRenumber(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	from, to := int(i32(a[0])), int(i32(a[1]))
	if _, err := in.Prog.Dup(from, to); err != nil {
		return in.fail(err)
	}
	if from != to {
		_ = in.Prog.Close(from)
	}
	return ok()
}

// atPath names a path given relative to directory fd dirfd: the "."
// preopen always means the prog's current directory; any other directory
// fd means the name it was opened by.
func (in *Instance) atPath(m Memory, dirfd, ptr, n uint64) (string, bool) {
	path, okp := getStr(m, ptr, n)
	if !okp {
		return "", false
	}
	if len(path) > 0 && path[0] == '/' {
		return core.Cleanname(path), true
	}
	base := in.Prog.Dot()
	if fd := i32(dirfd); fd != abi.PreopenDotFD {
		if c, err := in.Prog.Chan(int(fd)); err == nil {
			base = c.Name
		}
	}
	if len(path) > 0 && path[0] == '#' {
		return core.Cleanname(path), true
	}
	return core.Absolute(path, base), true
}

func pathFilestatGet(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	p, okp := in.atPath(m, a[0], a[2], a[3])
	if !okp {
		return in.fault()
	}
	d, err := in.Prog.Stat(p)
	if err != nil {
		return in.fail(err)
	}
	if !putFilestat(m, uint32(a[4]), &d) {
		return in.fault()
	}
	return ok()
}

const (
	oflagCreat = 1
	oflagDir   = 2
	oflagExcl  = 4
	oflagTrunc = 8

	rightRead    = 1 << 1
	rightWrite   = 1 << 6
	rightReaddir = 1 << 14

	fdflagAppend = 1
)

func pathOpen(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	p, okp := in.atPath(m, a[0], a[2], a[3])
	if !okp {
		return in.fault()
	}
	oflags, rights, fdflags := uint16(a[4]), a[5], uint16(a[7])
	mode := abi.OREAD
	wr := rights&rightWrite != 0 || fdflags&fdflagAppend != 0
	rd := rights&(rightRead|rightReaddir) != 0
	switch {
	case wr && rd:
		mode = abi.ORDWR
	case wr:
		mode = abi.OWRITE
	}
	if oflags&oflagTrunc != 0 {
		mode |= abi.OTRUNC
	}
	var (
		c   *core.Chan
		err error
	)
	exists := false
	if d, serr := in.Prog.Stat(p); serr == nil {
		exists = true
		if d.Qid.Type&styx.QTDIR != 0 {
			if wr {
				return in.fail(core.Error(abi.Eisdir))
			}
			mode = abi.OREAD
		} else if oflags&oflagDir != 0 {
			return in.fail(core.Error(abi.Enotdir))
		}
	}
	switch {
	case exists && oflags&oflagCreat != 0 && oflags&oflagExcl != 0:
		return in.fail(core.Error(abi.Eexist))
	case !exists && oflags&oflagCreat != 0:
		c, err = in.Prog.CreateChan(p, mode, 0o666)
	default:
		c, err = in.Prog.OpenChan(p, mode)
	}
	if err != nil {
		return in.fail(err)
	}
	fd, err := in.Prog.Fgrp().Alloc(c)
	if err != nil {
		c.DecRef()
		return in.fail(err)
	}
	if fdflags&fdflagAppend != 0 {
		if d, err := in.Prog.Fstat(fd); err == nil {
			c.SetOffset(int64(d.Length))
		}
	}
	if !putU32(m, uint32(a[8]), uint32(fd)) {
		_ = in.Prog.Close(fd)
		return in.fault()
	}
	return ok()
}

func pathCreateDirectory(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	p, okp := in.atPath(m, a[0], a[1], a[2])
	if !okp {
		return in.fault()
	}
	if _, err := in.Prog.Stat(p); err == nil {
		return in.fail(core.Error(abi.Eexist))
	}
	fd, err := in.Prog.Create(p, abi.OREAD, styx.DMDIR|0o777)
	if err != nil {
		return in.fail(err)
	}
	_ = in.Prog.Close(fd)
	return ok()
}

func removeKind(in *Instance, m Memory, a []uint64, wantDir bool) ([]uint64, error) {
	p, okp := in.atPath(m, a[0], a[1], a[2])
	if !okp {
		return in.fault()
	}
	d, err := in.Prog.Stat(p)
	if err != nil {
		return in.fail(err)
	}
	isDir := d.Qid.Type&styx.QTDIR != 0
	if wantDir && !isDir {
		return in.fail(core.Error(abi.Enotdir))
	}
	if !wantDir && isDir {
		return in.fail(core.Error(abi.Eisdir))
	}
	if err := in.Prog.Remove(p); err != nil {
		return in.fail(err)
	}
	return ok()
}

// pathRename renames within one directory by wstat, as Inferno does;
// across directories it is EXDEV.
func pathRename(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	from, ok1 := in.atPath(m, a[0], a[1], a[2])
	to, ok2 := in.atPath(m, a[3], a[4], a[5])
	if !ok1 || !ok2 {
		return in.fault()
	}
	dir := func(p string) string {
		for i := len(p) - 1; i >= 0; i-- {
			if p[i] == '/' {
				return p[:i]
			}
		}
		return ""
	}
	if dir(from) != dir(to) {
		return in.fail(core.Error(abi.Exdev))
	}
	if from == to {
		return ok()
	}
	if _, err := in.Prog.Stat(to); err == nil {
		if err := in.Prog.Remove(to); err != nil {
			return in.fail(err)
		}
	}
	d := core.NullDir()
	d.Name = to[len(dir(to))+1:]
	if err := in.Prog.Wstat(from, &d); err != nil {
		return in.fail(err)
	}
	return ok()
}

func fdReaddir(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	fd := int(i32(a[0]))
	buf, blen, cookie := uint32(a[1]), uint32(a[2]), a[3]
	in.mu.Lock()
	ents, have := in.dirs[fd]
	in.mu.Unlock()
	if !have || cookie == 0 {
		ds, err := in.Prog.Dirents(fd)
		if err != nil {
			return in.fail(err)
		}
		c, _ := in.Prog.Chan(fd)
		ents = []dirent{{".", c.Qid.Path, true}, {"..", 0, true}}
		for _, d := range ds {
			ents = append(ents, dirent{d.Name, d.Qid.Path, d.Qid.Type&styx.QTDIR != 0})
		}
		in.mu.Lock()
		if in.dirs == nil {
			in.dirs = map[int][]dirent{}
		}
		in.dirs[fd] = ents
		in.mu.Unlock()
	}
	var out []byte
	for i := cookie; i < uint64(len(ents)) && uint32(len(out)) < blen; i++ {
		e := ents[i]
		var h [24]byte
		le.PutUint64(h[0:], i+1)
		le.PutUint64(h[8:], e.qid)
		le.PutUint32(h[16:], uint32(len(e.name)))
		h[20] = ftFile
		if e.dir {
			h[20] = ftDir
		}
		out = append(out, h[:]...)
		out = append(out, e.name...)
	}
	if uint32(len(out)) > blen {
		out = out[:blen]
	}
	if !m.Write(buf, out) || !putU32(m, uint32(a[4]), uint32(len(out))) {
		return in.fault()
	}
	return ok()
}

// pollOneoff waits for clock subscriptions and for fds to be ready, using
// the devices' Ready and Watch where they have them.
func pollOneoff(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	inp, out, nsub := uint32(a[0]), uint32(a[1]), uint32(a[2])
	if nsub == 0 {
		return errno(abi.ErrnoInval)
	}
	const subSize, evSize = 48, 32
	raw, okr := m.Read(inp, nsub*subSize)
	if !okr {
		return in.fault()
	}
	subs := append([]byte(nil), raw...)
	timeout := -1
	type fdsub struct {
		i     uint32
		c     *core.Chan
		write bool
	}
	var fds []fdsub
	now := in.M.Kernel.Platform.Now()
	for i := uint32(0); i < nsub; i++ {
		s := subs[i*subSize:]
		switch s[8] {
		case 0:
			d := int64(le.Uint64(s[24:]))
			if le.Uint16(s[40:])&1 != 0 {
				d -= now
			}
			ms := int(max(0, (d+999999)/1e6))
			if timeout < 0 || ms < timeout {
				timeout = ms
			}
		case 1, 2:
			c, err := in.Prog.Chan(int(le.Uint32(s[16:])))
			fds = append(fds, fdsub{i, c, s[8] == 2})
			_ = err
		}
	}
	ready := func(f fdsub) bool {
		if f.c == nil {
			return true
		}
		if w, ok := f.c.Dev.(core.Waiter); ok {
			return w.Ready(f.c, f.write)
		}
		return true
	}
	anyReady := func() bool {
		for _, f := range fds {
			if ready(f) {
				return true
			}
		}
		return false
	}
	var r core.Rendez
	for _, f := range fds {
		if f.c != nil {
			if w, ok := f.c.Dev.(core.Waiter); ok {
				w.Watch(f.c, f.write, &r)
			}
		}
	}
	_, err := core.ParkTimeout(in.Prog.Ctx, &r, anyReady, timeout)
	for _, f := range fds {
		if f.c != nil {
			if w, ok := f.c.Dev.(core.Waiter); ok {
				w.Unwatch(f.c, &r)
			}
		}
	}
	if err != nil {
		return in.fail(err)
	}
	var n uint32
	fired := false
	for _, f := range fds {
		if !ready(f) {
			continue
		}
		s := subs[f.i*subSize:]
		var ev [evSize]byte
		copy(ev[0:8], s[0:8])
		ev[10] = s[8]
		if f.c == nil {
			le.PutUint16(ev[8:], abi.ErrnoBadf)
		} else {
			le.PutUint64(ev[16:], 1)
		}
		if !m.Write(out+n*evSize, ev[:]) {
			return in.fault()
		}
		n++
		fired = true
	}
	if !fired {
		for i := uint32(0); i < nsub; i++ {
			s := subs[i*subSize:]
			if s[8] != 0 {
				continue
			}
			var ev [evSize]byte
			copy(ev[0:8], s[0:8])
			if !m.Write(out+n*evSize, ev[:]) {
				return in.fault()
			}
			n++
		}
	}
	if !putU32(m, uint32(a[3]), n) {
		return in.fault()
	}
	return ok()
}
