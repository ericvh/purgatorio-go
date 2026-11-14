// Command emu is the hosted Purgatorio emulator, after Inferno's emu(1).
//
//	emu [-r root] [-vm compiler|interpreter] [-g WxH] [-w addr] [cmd [args ...]]
//
// It boots the name space as Inferno's emu does and runs /bin/emuinit.wasm,
// which runs the command (sh -l by default). The exit status is 0 when the
// command's Inferno status is empty.
//
// With -g the screen behind #i is WxH and shown in a web browser: the emu
// serves it on addr (loopback, any port, by default) and prints the URL
// to open. The browser's pointer feeds #m and its keys #c/keyboard.
// Without -g the screen is 640×480 and headless.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

func main() {
	root := flag.String("r", "root", "root directory served as #U")
	mode := flag.String("vm", string(wazero.Compiler), "wazero mode: compiler or interpreter")
	geom := flag.String("g", "", "show a WxH screen in a web browser")
	addr := flag.String("w", host.DefaultWebAddr, "address to serve the -g screen on")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: emu [-r root] [-vm compiler|interpreter] [-g WxH] [-w addr] [cmd [args ...]]")
		os.Exit(1)
	}
	flag.Parse()
	h := &host.Host{}
	if *geom != "" {
		if _, err := fmt.Sscanf(*geom, "%dx%d", &h.Width, &h.Height); err != nil || h.Width <= 0 || h.Height <= 0 {
			flag.Usage()
		}
		ws, err := host.NewWebScreen(*addr, h.Width, h.Height)
		if err != nil {
			fmt.Fprintln(os.Stderr, "emu: screen:", err)
			os.Exit(1)
		}
		h.Screen = ws
		fmt.Fprintln(os.Stderr, "emu: screen at", ws.URL())
	}
	st := run(h, *root, wazero.Mode(*mode), flag.Args())
	if ws, ok := h.Screen.(*host.WebScreen); ok {
		ws.Close()
	}
	os.Exit(st)
}

func run(h *host.Host, root string, mode wazero.Mode, args []string) int {
	eng, err := wazero.NewCached(mode, cacheDir())
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

// cacheDir is where compiled guests are kept between runs: the user's
// cache directory, or none if there is not one.
func cacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "purgatorio-go", "wazero")
}
