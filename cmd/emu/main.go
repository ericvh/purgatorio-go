// Command emu is the hosted Purgatorio emulator, after Inferno's emu(1).
//
//	emu [-r root] [-vm compiler|interpreter] [-g WxH] [cmd [args ...]]
//
// It boots the name space as Inferno's emu does and runs /bin/emuinit.wasm,
// which runs the command (sh -l by default). The exit status is 0 when the
// command's Inferno status is empty.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

func main() {
	root := flag.String("r", "root", "root directory served as #U")
	mode := flag.String("vm", string(wazero.Compiler), "wazero mode: compiler or interpreter")
	geom := flag.String("g", "640x480", "size of the screen behind #i")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: emu [-r root] [-vm compiler|interpreter] [-g WxH] [cmd [args ...]]")
		os.Exit(1)
	}
	flag.Parse()
	h := &host.Host{}
	if _, err := fmt.Sscanf(*geom, "%dx%d", &h.Width, &h.Height); err != nil || h.Width <= 0 || h.Height <= 0 {
		flag.Usage()
	}
	os.Exit(run(h, *root, wazero.Mode(*mode), flag.Args()))
}

func run(h *host.Host, root string, mode wazero.Mode, args []string) int {
	eng, err := wazero.New(mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "emu:", err)
		return 1
	}
	defer eng.Close()
	sys, err := host.Boot(h, root, eng)
	if err != nil {
		fmt.Fprintln(os.Stderr, "emu:", err)
		return 1
	}
	sys.Init.Egrp().Set("emuargs", []byte(strings.Join(append([]string{"emu"}, args...), " ")))
	status, err := sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", append([]string{"emuinit"}, args...))
	if err != nil {
		fmt.Fprintln(os.Stderr, "emu:", core.Errstr(err))
		return 1
	}
	sys.Init.Exit(status)
	if status != "" {
		return 1
	}
	return 0
}
