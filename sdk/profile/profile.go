// Package profile drives #P for cprof and mprof, as Inferno's Profile
// module (appl/lib/profile.b, module/profile.m) does for its commands.
package profile

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	dir = "/prof"
	ctl = dir + "/ctl"
)

var (
	lasterr string
	modl    string
)

// LastError is the error of the last call that failed.
func LastError() string { return lasterr }

func write(f, s string) bool {
	fd, err := os.OpenFile(f, os.O_WRONLY, 0)
	if err != nil {
		lasterr = fmt.Sprintf("cannot open %s for writing: %s", f, sys.Errstr())
		return false
	}
	defer fd.Close()
	if n, err := fd.WriteString(s); err != nil || n != len(s) {
		lasterr = fmt.Sprintf("cannot write %s to file %s: %s", s, f, sys.Errstr())
		return false
	}
	return true
}

func read(f string) string {
	fd, err := os.Open(f)
	if err != nil {
		lasterr = fmt.Sprintf("cannot open %s for reading: %s", f, sys.Errstr())
		return ""
	}
	defer fd.Close()
	buf := make([]byte, 128)
	n, _ := fd.Read(buf)
	return string(buf[:n])
}

// Init binds #P on /prof if it is not there.
func Init() bool {
	if _, err := sys.Stat(ctl); err != nil {
		if _, err := sys.Bind("#P", dir, sys.MREPL|sys.MCREATE); err != nil {
			lasterr = "cannot bind prof device to /prof: " + sys.Errstr()
			return false
		}
	}
	return true
}

// Profile adds a module, by name or path, to those to profile.
func Profile(m string) { modl = m + " " + modl }

// CPStart starts coverage profiling of the modules prog pid and its
// descendants start from now on.
func CPStart(pid int) bool {
	return write(ctl, "module "+modl) && write(ctl, "startcp "+strconv.Itoa(pid))
}

// MemStart starts memory profiling.
func MemStart() bool {
	if modl != "" && !write(ctl, "module "+modl) {
		return false
	}
	return write(ctl, "startmp")
}

// Stop stops profiling, keeping the counts.
func Stop() bool { return write(ctl, "stop") }

// End stops profiling and drops the counts.
func End() { write(ctl, "end") }

// Mod is one profiled module: its name, path and histogram.
type Mod struct {
	Name, Path string
	Hist       map[uint32]uint64
}

// Stats reads every profiled module's histogram.
func Stats() []Mod {
	fd, err := sys.Open(dir, sys.OREAD)
	if err != nil {
		lasterr = "cannot open " + dir + " for reading"
		return nil
	}
	defer sys.Close(fd)
	var out []Mod
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			break
		}
		for _, d := range ds {
			if d.Name == "ctl" {
				continue
			}
			dn := dir + "/" + d.Name
			m := Mod{Name: read(dn + "/name"), Path: read(dn + "/path"), Hist: map[uint32]uint64{}}
			if !histogram(&m, dn+"/histogram") {
				continue
			}
			out = append([]Mod{m}, out...)
		}
	}
	return out
}

func histogram(m *Mod, f string) bool {
	fd, err := os.Open(f)
	if err != nil {
		return false
	}
	defer fd.Close()
	buf := make([]byte, 32)
	for {
		n, err := fd.Read(buf)
		if err != nil || n <= 0 {
			return true
		}
		w := strings.Fields(string(buf[:n]))
		if len(w) != 2 {
			lasterr = "bad histogram data"
			return false
		}
		pc, _ := strconv.ParseUint(w[0], 10, 32)
		c, _ := strconv.ParseUint(w[1], 10, 64)
		m.Hist[uint32(pc)] += c
	}
}

// Wasmname is the module a command name runs, as disname is for Dis.
func Wasmname(cmd string) string {
	file := cmd
	if !strings.HasSuffix(file, ".wasm") {
		file += ".wasm"
	}
	if _, err := sys.Stat(file); err == nil {
		return file
	}
	if !strings.HasPrefix(file, "/") && !strings.HasPrefix(file, "./") {
		file = "/bin/" + file
	}
	return file
}

// Run runs a command in a new process group and name space, with fds 0,
// 1 and 2, and waits for it. When the wait file fails it returns the
// message for the caller's pfatal, with LastError cleared; a command that
// cannot start is reported on standard error as prog's.
func Run(prog string, argv []string) string {
	me, _ := sys.Pctl(0, nil)
	wfd, err := os.Open("#p/" + strconv.Itoa(me) + "/wait")
	if err != nil {
		lasterr = ""
		return "fd == nil in wait"
	}
	defer wfd.Close()
	file := argv[0]
	if !strings.HasSuffix(file, ".wasm") {
		file += ".wasm"
	}
	fds := [][2]int{{0, 0}, {1, 1}, {2, 2}}
	flags := sys.FORKNS | sys.NEWFD | sys.NEWPGRP
	pid, err := sys.Spawn(file, argv, flags, fds)
	if err != nil && !strings.HasPrefix(file, "/") && !strings.HasPrefix(file, "./") {
		pid, err = sys.Spawn("/bin/"+file, argv, flags, fds)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s: %s\n", prog, argv[0], sys.Errstr())
		return ""
	}
	buf := make([]byte, 256)
	for {
		n, err := wfd.Read(buf)
		if err != nil || n <= 0 {
			lasterr = ""
			return "bad read in wait"
		}
		s := string(buf[:n])
		if i := strings.IndexByte(s, ' '); i > 0 {
			if w, _ := strconv.Atoi(s[:i]); w == pid {
				return ""
			}
		}
	}
}
