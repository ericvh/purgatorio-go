// Command emu is the hosted Purgatorio emulator.
//
//	emu [-r root] [-vm compiler|interpreter] [cmd [args ...]]
//
// For now it runs root/bin/<cmd>.wasm (emuinit by default) as one prog with
// the host's standard streams. M1 replaces the direct host lookup with the
// prog's name space (#U at /, #c at /dev).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/vm"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

func main() {
	root := flag.String("r", "root", "root directory served as #U")
	engine := flag.String("vm", string(wazero.Compiler), "wazero engine: compiler or interpreter")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: emu [-r root] [-vm compiler|interpreter] [cmd [args ...]]")
		os.Exit(1)
	}
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		args = []string{"emuinit"}
	}

	code, err := os.ReadFile(filepath.Join(*root, "bin", args[0]+".wasm"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "emu: %s: file does not exist\n", args[0])
		os.Exit(1)
	}
	h := host.Host{}
	p := &vm.Proc{
		Args:    args,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Now:     h.Now,
		Entropy: h.Entropy,
	}
	status, err := wazero.Run(context.Background(), wazero.Engine(*engine), code, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "emu: %s: %v\n", args[0], err)
		os.Exit(1)
	}
	if status != "" {
		os.Exit(1)
	}
}
