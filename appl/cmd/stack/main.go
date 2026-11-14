// Command stack prints the call stack of a prog from /prog/n/stack
// (Inferno stack(1)).
//
//	stack [-v] pid
//
// Each frame is a line, innermost first: a Go guest's function, named
// from the guest's own symbol table, with the file and line it starts
// at; otherwise "unknown fn()" with the module and the wasm function
// index. A host function's frame has the module $wasi_snapshot_preview1
// or $purgatorio, as Inferno's built-in modules show $Sys. A guest has
// no table of its arguments and locals, so -v gives each frame's raw
// location instead: its wasm function index, code offset and module.
// The stack is the one at the prog's current host call; a prog running
// guest code has none and the read fails with "thread must be stopped".
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wasmobj"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("stack [-v] pid")
	verbose := false
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'v':
			verbose = true
		default:
			a.Usage()
		}
	}
	argv := a.Argv()
	if len(argv) == 0 {
		a.Usage()
	}
	pid, _ := strconv.Atoi(argv[0])
	dir := "/prog/" + strconv.Itoa(pid) + "/"
	// As Inferno's debug module does, open the prog's files first.
	for _, f := range []struct {
		name, what string
		mode       int
	}{
		{"heap", "heap file", sys.ORDWR},
		{"ctl", "ctl file", sys.OWRITE},
		{"dbgctl", "debug ctl file", sys.ORDWR},
	} {
		fd, err := sys.Open(dir+f.name, f.mode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stack: can't open %s: %s\n", f.what, sys.Errstr())
			return
		}
		defer sys.Close(fd)
	}
	sf, err := os.Open(dir + "stack")
	if err != nil {
		fmt.Fprintf(os.Stderr, "stack: can't open stack file: %s\n", sys.Errstr())
		return
	}
	buf := make([]byte, 64*1024)
	n, err := sf.Read(buf)
	if err != nil && n == 0 {
		fmt.Fprintf(os.Stderr, "stack: can't read stack file: %s\n", sys.Errstr())
		return
	}
	var nm wasmobj.Namer
	for _, f := range wasmobj.ParseStack(string(buf[:n])) {
		fmt.Println(nm.Line(&f))
		if verbose {
			fmt.Printf("\tpc=%d offset=%d module=%s\n", f.Func, f.Offset, f.Path)
		}
	}
}
