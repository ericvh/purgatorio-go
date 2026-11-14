package vm

import (
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/styx"
)

// The purgatorio import module: Inferno calls WASI p1 cannot express.
// Calls return -1 on error and set the errstr. See docs/abi.md.

func purgatorioFuncs() []Func {
	f := func(name, params, results string, call hostFn) Func {
		return Func{Module: abi.Module, Name: name, Params: sig(params), Results: sig(results), Call: call}
	}
	return []Func{
		f("errstr", "ii", "i", pErrstr),
		f("werrstr", "ii", "", pWerrstr),
		f("exits", "ii", "", pExits),
		f("open", "iii", "i", pOpen),
		f("create", "iiii", "i", pCreate),
		f("remove", "ii", "i", pathCall(func(in *Instance, p string) error { return in.Prog.Remove(p) })),
		f("chdir", "ii", "i", pathCall(func(in *Instance, p string) error { return in.Prog.Chdir(p) })),
		f("stat", "iiii", "i", pStat),
		f("fstat", "iii", "i", pFstat),
		f("wstat", "iiii", "i", pWstat),
		f("fwstat", "iii", "i", pFwstat),
		f("dirread", "iii", "i", pDirread),
		f("fd2path", "iii", "i", pFd2path),
		f("dup", "ii", "i", pDup),
		f("pipe", "i", "i", pPipe),
		f("iounit", "i", "i", pIounit),
		f("seek", "iIii", "i", pSeek),
		f("bind", "iiiii", "i", pBind),
		f("unmount", "iiii", "i", pUnmount),
		f("pctl", "iii", "i", pPctl),
		f("spawn_prog", "iiiiiii", "i", pSpawn),
		f("stream", "iii", "i", pStream),
		f("sleep", "i", "i", pSleep),
		f("millisec", "", "i", pMillisec),
		f("exec", "iiii", "i", pExec),
		f("detach", "", "i", func(in *Instance, m Memory, a []uint64) ([]uint64, error) {
			return ret(int64(in.Prog.Detach()))
		}),
	}
}

func pErrstr(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	s := in.Prog.Errstr()
	if n := int(uint32(a[1])); len(s) > n {
		s = s[:n]
	}
	if !m.Write(uint32(a[0]), []byte(s)) {
		return ret(-1)
	}
	return ret(int64(len(s)))
}

func pWerrstr(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	n := min(uint32(a[1]), core.ERRMAX)
	b, okb := m.Read(uint32(a[0]), n)
	if !okb {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return nil, nil
	}
	in.Prog.SetErrstr(string(b))
	return nil, nil
}

func pExits(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	b, okb := m.Read(uint32(a[0]), uint32(a[1]))
	if !okb {
		b = []byte("bad exit status")
	}
	in.Status = string(b)
	return nil, &Exit{Status: in.Status}
}

func (in *Instance) str(m Memory, ptr, n uint64) (string, bool) {
	s, okp := getStr(m, ptr, n)
	if !okp {
		in.Prog.SetErrstr(abi.Ebadaddr)
	}
	return s, okp
}

func pathCall(f func(*Instance, string) error) hostFn {
	return func(in *Instance, m Memory, a []uint64) ([]uint64, error) {
		p, okp := in.str(m, a[0], a[1])
		if !okp {
			return ret(-1)
		}
		if err := f(in, p); err != nil {
			return in.pfail(err)
		}
		return ret(0)
	}
}

func pOpen(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	p, okp := in.str(m, a[0], a[1])
	if !okp {
		return ret(-1)
	}
	fd, err := in.Prog.Open(p, int(i32(a[2])))
	if err != nil {
		return in.pfail(err)
	}
	return ret(int64(fd))
}

func pCreate(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	p, okp := in.str(m, a[0], a[1])
	if !okp {
		return ret(-1)
	}
	fd, err := in.Prog.Create(p, int(i32(a[2])), uint32(a[3]))
	if err != nil {
		return in.pfail(err)
	}
	return ret(int64(fd))
}

// putDir writes a packed Dir; when it does not fit it writes nothing and
// returns the size needed, so the caller can retry.
func putDir(in *Instance, m Memory, buf, blen uint64, d *styx.Dir) ([]uint64, error) {
	b := styx.MarshalDir(nil, d)
	if len(b) > int(uint32(blen)) {
		return ret(int64(len(b)))
	}
	if !m.Write(uint32(buf), b) {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	return ret(int64(len(b)))
}

func pStat(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	p, okp := in.str(m, a[0], a[1])
	if !okp {
		return ret(-1)
	}
	d, err := in.Prog.Stat(p)
	if err != nil {
		return in.pfail(err)
	}
	return putDir(in, m, a[2], a[3], &d)
}

func pFstat(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	d, err := in.Prog.Fstat(int(i32(a[0])))
	if err != nil {
		return in.pfail(err)
	}
	return putDir(in, m, a[1], a[2], &d)
}

func getDir(in *Instance, m Memory, ptr, n uint64) (*styx.Dir, error) {
	b, okb := m.Read(uint32(ptr), uint32(n))
	if !okb {
		return nil, core.Error(abi.Ebadaddr)
	}
	d, err := styx.UnmarshalDir(b)
	if err != nil {
		return nil, core.Error(abi.Ebadstat)
	}
	return d, nil
}

func pWstat(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	p, okp := in.str(m, a[0], a[1])
	if !okp {
		return ret(-1)
	}
	d, err := getDir(in, m, a[2], a[3])
	if err == nil {
		err = in.Prog.Wstat(p, d)
	}
	if err != nil {
		return in.pfail(err)
	}
	return ret(0)
}

func pFwstat(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	d, err := getDir(in, m, a[1], a[2])
	if err == nil {
		err = in.Prog.Fwstat(int(i32(a[0])), d)
	}
	if err != nil {
		return in.pfail(err)
	}
	return ret(0)
}

func pDirread(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	buf := make([]byte, uint32(a[2]))
	n, err := in.Prog.Dirread(int(i32(a[0])), buf)
	if err != nil {
		return in.pfail(err)
	}
	if !m.Write(uint32(a[1]), buf[:n]) {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	return ret(int64(n))
}

// putStr writes s, or returns its length without writing when too long.
func putStr(in *Instance, m Memory, buf, blen uint64, s string) ([]uint64, error) {
	if len(s) > int(uint32(blen)) {
		return ret(int64(len(s)))
	}
	if !m.Write(uint32(buf), []byte(s)) {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	return ret(int64(len(s)))
}

func pFd2path(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	s, err := in.Prog.Fd2path(int(i32(a[0])))
	if err != nil {
		return in.pfail(err)
	}
	return putStr(in, m, a[1], a[2], s)
}

func pDup(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	fd, err := in.Prog.Dup(int(i32(a[0])), int(i32(a[1])))
	if err != nil {
		return in.pfail(err)
	}
	return ret(int64(fd))
}

func pPipe(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	fds, err := in.Prog.Pipe()
	if err != nil {
		return in.pfail(err)
	}
	if !putU32(m, uint32(a[0]), uint32(fds[0])) || !putU32(m, uint32(a[0])+4, uint32(fds[1])) {
		_ = in.Prog.Close(fds[0])
		_ = in.Prog.Close(fds[1])
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	return ret(0)
}

func pIounit(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	n, err := in.Prog.Iounit(int(i32(a[0])))
	if err != nil {
		return in.pfail(err)
	}
	return ret(int64(n))
}

// seek(fd, off i64, whence, result_ptr) -> 0: the new offset is written
// at result_ptr, as an i64.
func pSeek(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	off, err := in.Prog.Seek(int(i32(a[0])), int64(a[1]), int(i32(a[2])))
	if err != nil {
		return in.pfail(err)
	}
	if !putU64(m, uint32(a[3]), uint64(off)) {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	return ret(0)
}

func pBind(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	n, ok1 := in.str(m, a[0], a[1])
	o, ok2 := in.str(m, a[2], a[3])
	if !ok1 || !ok2 {
		return ret(-1)
	}
	if err := in.Prog.Bind(n, o, uint32(a[4])); err != nil {
		return in.pfail(err)
	}
	return ret(0)
}

// unmount(name, nlen, old, olen): nlen 0 unmounts everything on old.
func pUnmount(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	n, ok1 := in.str(m, a[0], a[1])
	o, ok2 := in.str(m, a[2], a[3])
	if !ok1 || !ok2 {
		return ret(-1)
	}
	if err := in.Prog.Unmount(n, o); err != nil {
		return in.pfail(err)
	}
	return ret(0)
}

func getI32s(m Memory, ptr, n uint64) ([]int, bool) {
	if n > 1<<20 {
		return nil, false
	}
	b, okb := m.Read(uint32(ptr), uint32(n)*4)
	if !okb {
		return nil, false
	}
	out := make([]int, n)
	for i := range out {
		out[i] = int(int32(le.Uint32(b[4*i:])))
	}
	return out, true
}

func pPctl(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	fds, okf := getI32s(m, a[1], a[2])
	if !okf {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	// The WASI preopens stay with the prog through NEWFD.
	if uint32(a[0])&abi.NEWFD != 0 {
		fds = append(fds, abi.PreopenRootFD, abi.PreopenDotFD)
	}
	pid, err := in.Prog.Pctl(uint32(a[0]), fds)
	if err != nil {
		return in.pfail(err)
	}
	return ret(int64(pid))
}

// spawn_prog(path, plen, argv, alen, flags, fdmap, nfd) -> pid. argv is
// the arguments, each ending in a NUL; fdmap is nfd pairs of i32
// (child fd, parent fd), used when flags has NEWFD.
func pSpawn(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	path, ok1 := in.str(m, a[0], a[1])
	av, ok2 := in.str(m, a[2], a[3])
	pairs, ok3 := getI32s(m, a[5], 2*a[6])
	if !ok1 || !ok2 || !ok3 {
		in.Prog.SetErrstr(abi.Ebadaddr)
		return ret(-1)
	}
	argv := strings.Split(strings.TrimSuffix(av, "\x00"), "\x00")
	if av == "" {
		argv = nil
	}
	var fdmap [][2]int
	for i := 0; i+1 < len(pairs); i += 2 {
		fdmap = append(fdmap, [2]int{pairs[i], pairs[i+1]})
	}
	pid, err := in.M.Spawn(in.Prog, path, argv, uint32(a[4]), fdmap)
	if err != nil {
		return in.pfail(err)
	}
	return ret(int64(pid))
}

func pStream(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	n, why, err := in.Prog.Stream(int(i32(a[0])), int(i32(a[1])), int(i32(a[2])))
	if err != nil {
		return in.pfail(err)
	}
	if why != nil {
		in.Prog.SetError(why)
	}
	return ret(n)
}

// sleep(ms): Sys->sleep, interrupted by a kill.
func pSleep(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	ms := int(i32(a[0]))
	if ms <= 0 {
		return ret(0)
	}
	var r core.Rendez
	if _, err := core.ParkTimeout(in.Prog.Ctx, &r, func() bool { return false }, ms); err != nil {
		return in.pfail(err)
	}
	return ret(0)
}

func pMillisec(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	return ret(time.Since(boot).Milliseconds())
}

// Exec replaces the running module with another in the same prog, as
// Plan 9's exec does; emuinit and sh's exec use it. It is returned by a
// host function and handled by Machine.RunCode.
type Exec struct {
	Path string
	Code []byte
	Argv []string
}

func (e *Exec) Error() string { return "exec " + e.Path }

// exec(path, plen, argv, alen): argv as for spawn_prog. It returns only
// on error.
func pExec(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	path, ok1 := in.str(m, a[0], a[1])
	av, ok2 := in.str(m, a[2], a[3])
	if !ok1 || !ok2 {
		return ret(-1)
	}
	var argv []string
	if av != "" {
		argv = strings.Split(strings.TrimSuffix(av, "\x00"), "\x00")
	}
	code, rec, err := in.M.LoadChecked(in.Prog, path)
	if err != nil {
		return in.pfail(err)
	}
	in.Prog.AddModule(rec, true)
	return nil, &Exec{Path: path, Code: code, Argv: argv}
}
