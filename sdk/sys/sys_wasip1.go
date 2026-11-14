//go:build wasip1

package sys

import (
	"os"
	"unsafe"
)

//go:wasmimport purgatorio errstr
func errstr(buf unsafe.Pointer, n uint32) int32

//go:wasmimport purgatorio werrstr
func werrstr(p unsafe.Pointer, n uint32)

//go:wasmimport purgatorio exits
func exits(p unsafe.Pointer, n uint32)

//go:wasmimport purgatorio open
func open(p unsafe.Pointer, n uint32, mode int32) int32

//go:wasmimport purgatorio create
func create(p unsafe.Pointer, n uint32, mode int32, perm uint32) int32

//go:wasmimport purgatorio remove
func remove(p unsafe.Pointer, n uint32) int32

//go:wasmimport purgatorio chdir
func chdir(p unsafe.Pointer, n uint32) int32

//go:wasmimport purgatorio stat
func stat(p unsafe.Pointer, n uint32, buf unsafe.Pointer, blen uint32) int32

//go:wasmimport purgatorio fstat
func fstat(fd int32, buf unsafe.Pointer, blen uint32) int32

//go:wasmimport purgatorio wstat
func wstat(p unsafe.Pointer, n uint32, d unsafe.Pointer, dlen uint32) int32

//go:wasmimport purgatorio fwstat
func fwstat(fd int32, d unsafe.Pointer, dlen uint32) int32

//go:wasmimport purgatorio dirread
func dirread(fd int32, buf unsafe.Pointer, blen uint32) int32

//go:wasmimport purgatorio fd2path
func fd2path(fd int32, buf unsafe.Pointer, blen uint32) int32

//go:wasmimport purgatorio dup
func dup(old, new int32) int32

//go:wasmimport purgatorio pipe
func pipe(fds unsafe.Pointer) int32

//go:wasmimport purgatorio iounit
func iounit(fd int32) int32

//go:wasmimport purgatorio seek
func seek(fd int32, off int64, whence int32, res unsafe.Pointer) int32

//go:wasmimport purgatorio bind
func bind(n unsafe.Pointer, nl uint32, o unsafe.Pointer, ol uint32, flags int32) int32

//go:wasmimport purgatorio unmount
func unmount(n unsafe.Pointer, nl uint32, o unsafe.Pointer, ol uint32) int32

//go:wasmimport purgatorio pctl
func pctl(flags int32, fds unsafe.Pointer, n uint32) int32

//go:wasmimport purgatorio spawn_prog
func spawnProg(p unsafe.Pointer, pl uint32, av unsafe.Pointer, al uint32, flags int32, fdmap unsafe.Pointer, n uint32) int32

//go:wasmimport purgatorio exec
func exec(p unsafe.Pointer, pl uint32, av unsafe.Pointer, al uint32) int32

//go:wasmimport purgatorio stream
func stream(src, dst, bufsiz int32) int32

//go:wasmimport purgatorio sleep
func sleep(ms int32) int32

//go:wasmimport purgatorio millisec
func millisec() int32

func sp(s string) (unsafe.Pointer, uint32) {
	return unsafe.Pointer(unsafe.StringData(s)), uint32(len(s))
}

func bp(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

func ip(v []int32) unsafe.Pointer {
	if len(v) == 0 {
		return nil
	}
	return unsafe.Pointer(&v[0])
}

func check(r int32) error {
	if r < 0 {
		return lastErr()
	}
	return nil
}

// Errstr returns the prog's last error string, in Inferno's wording.
func Errstr() string {
	var b [ERRMAX]byte
	n := errstr(unsafe.Pointer(&b[0]), ERRMAX)
	if n < 0 {
		return ""
	}
	return string(b[:n])
}

// Werrstr sets the prog's error string.
func Werrstr(s string) { p, n := sp(s); werrstr(p, n) }

// Exits ends the prog with an Inferno exit status; empty means success.
func Exits(s string) {
	p, n := sp(s)
	exits(p, n)
	panic("exits returned")
}

func writeStderr(s string) { _, _ = os.Stderr.WriteString(s) }

// Open opens path with an Inferno mode and returns the fd.
func Open(path string, mode int) (int, error) {
	p, n := sp(path)
	fd := open(p, n, int32(mode))
	return int(fd), check(fd)
}

// Create creates path with Inferno permission bits (DMDIR for a
// directory) and returns the fd.
func Create(path string, mode int, perm uint32) (int, error) {
	p, n := sp(path)
	fd := create(p, n, int32(mode), perm)
	return int(fd), check(fd)
}

func Remove(path string) error { p, n := sp(path); return check(remove(p, n)) }

// Chdir changes the prog's current directory and the Go runtime's.
func Chdir(path string) error {
	p, n := sp(path)
	if err := check(chdir(p, n)); err != nil {
		return err
	}
	_ = os.Chdir(path)
	return nil
}

func getdir(f func(buf []byte) int32) (*Dir, error) {
	buf := make([]byte, 256)
	for {
		n := f(buf)
		if n < 0 {
			return nil, lastErr()
		}
		if int(n) > len(buf) {
			buf = make([]byte, n)
			continue
		}
		return UnpackDir(buf[:n])
	}
}

// Stat returns path's directory entry.
func Stat(path string) (*Dir, error) {
	p, n := sp(path)
	return getdir(func(b []byte) int32 { return stat(p, n, bp(b), uint32(len(b))) })
}

func Fstat(fd int) (*Dir, error) {
	return getdir(func(b []byte) int32 { return fstat(int32(fd), bp(b), uint32(len(b))) })
}

func Wstat(path string, d *Dir) error {
	p, n := sp(path)
	b := d.Pack()
	return check(wstat(p, n, bp(b), uint32(len(b))))
}

func Fwstat(fd int, d *Dir) error {
	b := d.Pack()
	return check(fwstat(int32(fd), bp(b), uint32(len(b))))
}

// Dirread returns the next entries of the directory open on fd; none at
// the end.
func Dirread(fd int) ([]Dir, error) {
	buf := make([]byte, 16*1024)
	n := dirread(int32(fd), bp(buf), uint32(len(buf)))
	if n < 0 {
		return nil, lastErr()
	}
	return UnpackDirs(buf[:n])
}

func Fd2path(fd int) (string, error) {
	buf := make([]byte, 256)
	for {
		n := fd2path(int32(fd), bp(buf), uint32(len(buf)))
		if n < 0 {
			return "", lastErr()
		}
		if int(n) > len(buf) {
			buf = make([]byte, n)
			continue
		}
		return string(buf[:n]), nil
	}
}

// Dup is Sys->dup; new = -1 takes the lowest free fd.
func Dup(old, new int) (int, error) {
	fd := dup(int32(old), int32(new))
	return int(fd), check(fd)
}

func Pipe() ([2]int, error) {
	var fds [2]int32
	if err := check(pipe(unsafe.Pointer(&fds[0]))); err != nil {
		return [2]int{}, err
	}
	return [2]int{int(fds[0]), int(fds[1])}, nil
}

func Iounit(fd int) int { return int(iounit(int32(fd))) }

func Seek(fd int, off int64, whence int) (int64, error) {
	var res int64
	if err := check(seek(int32(fd), off, int32(whence), unsafe.Pointer(&res))); err != nil {
		return -1, err
	}
	return res, nil
}

func Bind(name, old string, flags int) error {
	np, nl := sp(name)
	op, ol := sp(old)
	return check(bind(np, nl, op, ol, int32(flags)))
}

// Unmount removes name from the union at old, or everything when name is
// empty.
func Unmount(name, old string) error {
	np, nl := sp(name)
	op, ol := sp(old)
	return check(unmount(np, nl, op, ol))
}

// Pctl is Sys->pctl and returns the pid.
func Pctl(flags int, movefd []int) (int, error) {
	v := make([]int32, len(movefd))
	for i, fd := range movefd {
		v[i] = int32(fd)
	}
	pid := pctl(int32(flags), ip(v), uint32(len(v)))
	return int(pid), check(pid)
}

// Spawn starts the module at path as a new prog with args (args[0] is
// the command name) and returns its pid. With NEWFD in flags the child
// gets only fdmap's fds: each pair is (child fd, parent fd).
func Spawn(path string, args []string, flags int, fdmap [][2]int) (int, error) {
	p, pl := sp(path)
	av := argv(args)
	a, al := sp(av)
	var m []int32
	for _, x := range fdmap {
		m = append(m, int32(x[0]), int32(x[1]))
	}
	pid := spawnProg(p, pl, a, al, int32(flags), ip(m), uint32(len(fdmap)))
	return int(pid), check(pid)
}

// Exec replaces this prog's module with the one at path. It returns only
// on error.
func Exec(path string, args []string) error {
	p, pl := sp(path)
	av := argv(args)
	a, al := sp(av)
	return check(exec(p, pl, a, al))
}

// Stream copies src to dst until end of file and returns the bytes copied.
func Stream(src, dst, bufsiz int) int { return int(stream(int32(src), int32(dst), int32(bufsiz))) }

// Sleep sleeps for ms milliseconds.
func Sleep(ms int) error { return check(sleep(int32(ms))) }

// Millisec is a millisecond clock.
func Millisec() int { return int(millisec()) }
