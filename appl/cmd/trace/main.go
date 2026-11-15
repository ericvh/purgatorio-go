// Command trace runs a command and prints, on standard error, each host
// call it makes that may block, as the engine reports them to #p
// (docs/plan.md 5.1). It is the Rust sibling's trace in role, but the
// Rust sibling keeps stacks only for traced progs, while here every prog
// has its stack at each host call; what trace adds is holding the
// command at each one through /prog/n/dbgctl.
//
//	trace [-v] command [arg ...]
//
// Each call is a line "pid: frame", the frame being the innermost guest
// function, as stack prints it: for a Go guest, the stub the Go compiler
// makes for the wasm import (syscall.fd_write, ...). With -v the whole
// stack follows, indented. When the command ends with a status that is
// not a fail: one (a trap, or a Go panic), trace says so as
// `trace: pid "name": broken: status`. The exit status is the command's.
// Only the command's own prog is followed, not the progs it starts.
// A prog that ends while debugged is left broken (docs/plan.md 5.1), so
// trace kills it once its wait message has come.
//
// To miss none of the command's calls, trace starts a copy of itself
// that waits for trace to attach and then execs the command in the same
// prog; the calls before the exec are not printed.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wasmobj"
)

const helper = "trace-exec"

func usage() {
	fmt.Fprintf(os.Stderr, "usage: trace [-v] command [arg ...]\n")
	sys.Exits("fail:usage")
}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "trace: %s\n", s)
	sys.Exits("fail:error")
}

func main() {
	argv := os.Args
	if len(argv) > 0 && argv[0] == helper {
		execcmd(argv[1:])
	}
	argv = argv[1:]
	verbose := false
	if len(argv) > 0 && argv[0] == "-v" {
		verbose, argv = true, argv[1:]
	}
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		usage()
	}
	me, _ := sys.Pctl(0, nil)
	self := "/prog/" + strconv.Itoa(me) + "/"
	text, err := os.ReadFile(self + "text")
	if err != nil {
		fatal("cannot read " + self + "text: " + sys.Errstr())
	}
	wfd, err := os.Open(self + "wait")
	if err != nil {
		fatal("cannot open " + self + "wait: " + sys.Errstr())
	}
	p, err := sys.Pipe()
	if err != nil {
		fatal("pipe: " + sys.Errstr())
	}
	pid, err := sys.Spawn(string(text), append([]string{helper}, argv...), sys.NEWFD,
		[][2]int{{0, 0}, {1, 1}, {2, 2}, {5, p[1]}})
	if err != nil {
		fatal(fmt.Sprintf("%s: %s", argv[0], sys.Errstr()))
	}
	sys.Close(p[1])
	dir := "/prog/" + strconv.Itoa(pid) + "/"
	dbg, err := os.OpenFile(dir+"dbgctl", os.O_RDWR, 0)
	if err != nil {
		fatal("cannot open " + dir + "dbgctl: " + sys.Errstr())
	}
	// Hold the helper at its next host call, then let it go on to its
	// exec: the command's calls all come after it.
	ctl(dbg, "stop")
	ctl(dbg, "cont")
	pf := os.NewFile(uintptr(p[0]), "pipe")
	if _, err := pf.Write([]byte{0}); err != nil {
		fatal("pipe write: " + sys.Errstr())
	}
	pf.Close()
	ctl(dbg, "unstop")
	var nm wasmobj.Namer
	// Each step runs the prog to its next host call; the step fails
	// once the prog has gone.
	for ctl(dbg, "step") == "" {
		show(&nm, pid, dir, string(text), verbose)
	}
	name, status := waitfor(wfd, pid)
	// A prog that ends while it is debugged is left broken, as in
	// Inferno; the debugger kills it.
	if k, err := os.OpenFile(dir+"ctl", os.O_WRONLY, 0); err == nil {
		k.Write([]byte("kill"))
		k.Close()
	}
	// As in Inferno, a prog that ends with a fail: status has exited;
	// any other status is a trap's, and the prog broke.
	if status != "" && !strings.HasPrefix(status, "fail:") {
		first, _, _ := strings.Cut(status, "\n")
		fmt.Fprintf(os.Stderr, "trace: %d %s: broken: %s\n", pid, name, first)
	}
	if status != "" {
		sys.Exits(status)
	}
}

// ctl writes a message to dbgctl and returns its error, or "".
func ctl(fd *os.File, msg string) string {
	if _, err := fd.Write([]byte(msg)); err != nil {
		return sys.Errstr()
	}
	return ""
}

// show prints the call the prog is held at, unless it is the helper's.
func show(nm *wasmobj.Namer, pid int, dir, self string, verbose bool) {
	s, err := os.ReadFile(dir + "stack")
	if err != nil {
		return
	}
	fs := wasmobj.ParseStack(string(s))
	for i := range fs {
		if fs[i].Host() {
			continue
		}
		if fs[i].Path == self {
			return // still the helper
		}
		fmt.Fprintf(os.Stderr, "%d: %s\n", pid, nm.Line(&fs[i]))
		break
	}
	if verbose {
		for i := range fs {
			fmt.Fprintf(os.Stderr, "\t%s\n", nm.Line(&fs[i]))
		}
	}
}

// waitfor reads exit messages, pid "name":status, until pid's, and
// returns its quoted name and its status.
func waitfor(wfd *os.File, pid int) (string, string) {
	buf := make([]byte, 256)
	for {
		n, err := wfd.Read(buf)
		if err != nil || n <= 0 {
			fatal("bad read in wait")
		}
		s := string(buf[:n])
		w, rest, _ := strings.Cut(s, " ")
		if who, _ := strconv.Atoi(w); who != pid {
			continue
		}
		i := strings.Index(rest, "\":")
		if i < 0 {
			return rest, ""
		}
		return rest[:i+1], rest[i+2:]
	}
}

// execcmd is the helper: it waits for trace to write a byte on fd 5,
// then execs the command in its place.
func execcmd(argv []string) {
	b := make([]byte, 1)
	f := os.NewFile(5, "trace")
	f.Read(b)
	f.Close()
	file := argv[0]
	if !strings.HasSuffix(file, ".wasm") {
		file += ".wasm"
	}
	err := sys.Exec(file, argv)
	if err != nil && !strings.HasPrefix(file, "/") && !strings.HasPrefix(file, "./") {
		err = sys.Exec("/bin/"+file, argv)
	}
	fmt.Fprintf(os.Stderr, "trace: %s: %s\n", argv[0], sys.Errstr())
	sys.Exits("fail:exec")
}
