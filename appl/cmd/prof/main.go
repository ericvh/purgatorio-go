// Command prof profiles programs with #P (Inferno prof(1)).
//
//	prof [-bflnve] [-m modname]... [-s rate] [cmd arg ...]
//
// It samples which wasm function each profiled guest is in and prints
// the share of samples per Go function, named from the guest's own Go
// symbol table. There are no per-line counts, so -l is taken as -f.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wasmobj"
)

const (
	profdir   = "/prof"
	ctlfile   = profdir + "/ctl"
	fFunction = 1 << iota
	fLine
	fFullhdr
	fVerbose
)

var (
	lasterr string
	modl    string
)

func ignored(s string) { fmt.Fprintf(os.Stderr, "prof: warning: %s ignored\n", s) }

func exits(e string) {
	write(ctlfile, "end")
	sys.Exits("fail:" + e)
}

func pfatal(s string) {
	fmt.Fprintf(os.Stderr, "prof: %s: %s\n", s, lasterr)
	exits("error")
}

func usage(s string) {
	fmt.Fprintf(os.Stderr, "prof: %s\n", s)
	fmt.Fprintf(os.Stderr, "usage: prof [-bflnv] [-m modname]... [-s rate] [cmd arg ...]\n")
	exits("usage")
}

// write writes s to the file f, as profile.b's write does.
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

func initprof() bool {
	if _, err := sys.Stat(ctlfile); err != nil {
		if _, err := sys.Bind("#P", profdir, sys.MREPL|sys.MCREATE); err != nil {
			lasterr = "cannot bind prof device to /prof: " + sys.Errstr()
			return false
		}
	}
	return true
}

func start() bool { return write(ctlfile, "module "+modl) && write(ctlfile, "start") }

func main() {
	a := arg.New(os.Args)
	if !initprof() {
		pfatal("cannot initialize profile device")
	}
	v, begin, rate, ep := 0, false, 0, false
	var mods []string
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'b':
			begin = true
		case 'f':
			v |= fFunction
		case 'l':
			v |= fLine
		case 'n':
			v |= fFullhdr
		case 'v':
			v |= fVerbose
		case 's':
			s := a.Arg()
			if s == "" {
				usage("missing sample rate")
			}
			rate, _ = strconv.Atoi(s)
			if rate <= 0 {
				usage("bad sample rate: '" + s + "'")
			}
		case 'm':
			s := a.Arg()
			if s == "" {
				usage("missing module name")
			}
			mods = append([]string{s}, mods...)
		case 'e':
			ep = true
		case 'g':
		default:
			usage(fmt.Sprintf("unknown option -%c", c))
		}
	}
	exec := a.Argv()
	if len(exec) == 0 {
		exec = nil
	}
	if begin && v != 0 {
		ignored("output format")
	}
	if begin && exec != nil {
		begin = false
	}
	if !begin && exec == nil {
		if mods != nil {
			ignored("-m option")
		}
		if rate > 0 {
			ignored("-s option")
		}
		mods, rate = nil, 0
	}
	if rate > 0 && !write(ctlfile, "interval "+strconv.Itoa(rate)) {
		pfatal("cannot set sample rate")
	}
	for _, m := range mods {
		modl = m + " " + modl
	}
	if begin {
		if !start() {
			pfatal("cannot start profiling")
		}
		return
	}
	if exec != nil {
		if ep {
			modl = wasmname(exec[0]) + " " + modl
		}
		if !start() {
			pfatal("cannot start profiling")
		}
		run(exec)
	}
	if !write(ctlfile, "stop") {
		pfatal("cannot stop profiling")
	}
	mp, total := stats()
	if mp == nil {
		pfatal("no profile information")
	}
	if v&(fFunction|fLine) == 0 {
		v |= fLine
	}
	show(mp, total, v)
	write(ctlfile, "end")
}

// wasmname is the module a command name runs, as disname is for Dis.
func wasmname(cmd string) string {
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

// run runs the command in a new process group and name space, with fds
// 0, 1 and 2, and waits for it.
func run(argv []string) {
	me, _ := sys.Pctl(0, nil)
	wfd, err := os.Open("#p/" + strconv.Itoa(me) + "/wait")
	if err != nil {
		lasterr = ""
		pfatal("fd == nil in wait")
	}
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
		fmt.Fprintf(os.Stderr, "prof: %s: %s\n", argv[0], sys.Errstr())
		return
	}
	buf := make([]byte, 256)
	for {
		n, err := wfd.Read(buf)
		if err != nil || n <= 0 {
			lasterr = ""
			pfatal("bad read in wait")
		}
		s := string(buf[:n])
		if i := strings.IndexByte(s, ' '); i > 0 {
			if w, _ := strconv.Atoi(s[:i]); w == pid {
				return
			}
		}
	}
}

// funprof is one function's profile.
type funprof struct {
	name  string
	file  string
	line  int
	count uint64
}

// modprof is one module's profile.
type modprof struct {
	name, path string
	funs       []funprof
	total      uint64
}

func stats() ([]modprof, uint64) {
	fd, err := sys.Open(profdir, sys.OREAD)
	if err != nil {
		lasterr = "cannot open " + profdir + " for reading"
		return nil, 0
	}
	defer sys.Close(fd)
	var out []modprof
	var total uint64
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			break
		}
		for _, d := range ds {
			if d.Name == "ctl" {
				continue
			}
			dn := profdir + "/" + d.Name
			mp := modprof{name: read(dn + "/name"), path: read(dn + "/path")}
			if !tprofile(&mp, dn+"/histogram") {
				continue
			}
			if mp.total != 0 {
				out = append([]modprof{mp}, out...)
				total += mp.total
			}
		}
	}
	return out, total
}

// tprofile reads a module's histogram and adds each count to the Go
// function the wasm function index belongs to.
func tprofile(mp *modprof, hist string) bool {
	code, err := os.ReadFile(mp.path)
	if err != nil {
		lasterr = fmt.Sprintf("cannot read %s", mp.path)
		return false
	}
	m, err := wasmobj.Parse(code)
	if err != nil {
		lasterr = fmt.Sprintf("cannot locate symbol table for %s: %s", mp.path, err)
		return false
	}
	st := m.Syms()
	fd, err := os.Open(hist)
	if err != nil {
		return false
	}
	defer fd.Close()
	counts := map[uint64]uint64{} // function entry -> count
	buf := make([]byte, 32)
	for {
		n, err := fd.Read(buf)
		if err != nil || n <= 0 {
			break
		}
		f := strings.Fields(string(buf[:n]))
		if len(f) != 2 {
			lasterr = "bad histogram data"
			return false
		}
		pc, _ := strconv.ParseUint(f[0], 10, 32)
		c, _ := strconv.ParseUint(f[1], 10, 64)
		fn := st.Lookup(uint32(pc))
		if fn == nil {
			continue
		}
		counts[fn.Entry] += c
		mp.total += c
	}
	for _, fn := range st.Funcs() {
		mp.funs = append(mp.funs, funprof{name: fn.Name, file: fn.File, line: fn.Line, count: counts[fn.Entry]})
	}
	return true
}

// show prints the profile as profile.b's show does. Every count is a
// function's, so the line profile is the function profile.
func show(mods []modprof, tot uint64, v int) {
	if tot == 0 {
		return
	}
	for _, mp := range mods {
		if mp.total == 0 {
			continue
		}
		fmt.Printf("\nModule: %s(%s)\n\n", mp.name, mp.path)
		for _, f := range mp.funs {
			if v&fVerbose != 0 || f.count != 0 {
				if v&fFullhdr != 0 {
					fmt.Printf("%s:", f.file)
				}
				fmt.Printf("%d\t%.2f\t%s()\n", f.line, 100*float64(f.count)/float64(tot), f.name)
			}
		}
		fmt.Printf("\n**** module sampling points %d ****\n\n", mp.total)
	}
	if len(mods) > 1 {
		fmt.Printf("\n**** total sampling points %d ****\n\n", tot)
	}
}
