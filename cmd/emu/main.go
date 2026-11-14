// Command emu is the hosted Purgatorio emulator, after Inferno's emu(1).
//
//	emu [-r root] [-vm compiler|interpreter] [-g WxH] [-web] [-w addr] [-eia devs] [cmd [args ...]]
//
// It boots the name space as Inferno's emu does and runs /bin/emuinit.wasm,
// which runs the command (sh -l by default). The exit status is 0 when the
// command's Inferno status is empty.
//
// With -g the screen behind #i is WxH and shown in a window on the
// host's display: X11 on Linux (through XWayland on a Wayland desktop)
// and Cocoa on macOS. The window's pointer feeds #m (buttons 1 to 3,
// and the wheel as buttons 4 and 5) and its keys #c/keyboard. The
// window may be resized, which resizes the screen; closing it ends the
// emu with status 0. Without a display -g fails with "cannot open
// display".
//
// With -web the screen is shown in a web browser instead: the emu
// serves it on addr (-w; loopback, any port, by default) and prints the
// URL to open. Giving -w implies -web. The screen is WxH from -g, or
// 800×600.
//
// Without either the screen is 640×480 and headless.
//
// The serial ports of #t are the host devices in devs, separated by
// commas (eia0 first), or those in $EMU_EIA, or by default Inferno's:
// /dev/ttyS0 to /dev/ttyS7 on Linux and the /dev/cu.* devices on macOS.
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
	geom := flag.String("g", "", "show a WxH screen in a window")
	web := flag.Bool("web", false, "show the screen in a web browser instead of a window")
	addr := flag.String("w", host.DefaultWebAddr, "address to serve the -web screen on")
	eia := flag.String("eia", "", "host serial devices for #t, separated by commas")
	flag.StringVar(&modules, "modules", "", "the first prog's module policy: warn or require, optionally ,classical")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: emu [-r root] [-vm compiler|interpreter] [-g WxH] [-web] [-w addr] [-eia devs] [-modules warn|require[,classical]] [cmd [args ...]]")
		os.Exit(1)
	}
	flag.Parse()
	if _, _, ok := modulePolicy(modules); !ok {
		flag.Usage()
	}
	h := &host.Host{}
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "eia":
			h.Eia = strings.FieldsFunc(*eia, func(r rune) bool { return r == ',' })
			if h.Eia == nil {
				h.Eia = []string{}
			}
		case "w":
			*web = true
		}
	})
	if *web && *geom == "" {
		*geom = host.DefaultScreenSize
	}
	if *geom != "" {
		if _, err := fmt.Sscanf(*geom, "%dx%d", &h.Width, &h.Height); err != nil || h.Width <= 0 || h.Height <= 0 {
			flag.Usage()
		}
	}
	args := flag.Args()
	switch {
	case *web:
		ws, err := host.NewWebScreen(*addr, h.Width, h.Height)
		if err != nil {
			fmt.Fprintln(os.Stderr, "emu: screen:", err)
			os.Exit(1)
		}
		h.Screen = ws
		fmt.Fprintln(os.Stderr, "emu: screen at", ws.URL())
		st := run(h, *root, wazero.Mode(*mode), args)
		ws.Close()
		os.Exit(st)
	case *geom != "":
		win, err := host.NewWindow(h.Width, h.Height)
		if err != nil {
			fmt.Fprintf(os.Stderr, "emu: %v (-web shows the screen in a browser)\n", err)
			os.Exit(1)
		}
		h.Screen = win
		win.Run(func() int { return run(h, *root, wazero.Mode(*mode), args) })
	}
	os.Exit(run(h, *root, wazero.Mode(*mode), args))
}

// modules is -modules: the first prog's module policy.
var modules string

func modulePolicy(s string) (core.ModLevel, bool, bool) {
	level, rest, _ := strings.Cut(s, ",")
	classical := rest == "classical"
	if rest != "" && !classical {
		return 0, false, false
	}
	switch level {
	case "":
		return core.ModNone, false, !classical
	case "warn":
		return core.ModWarn, classical, true
	case "require":
		return core.ModRequire, classical, true
	}
	return 0, false, false
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
	// The policy is set before the first module loads, pinned to the
	// boot namespace, so emuinit is checked too (docs/signing.md).
	if l, classical, _ := modulePolicy(modules); l != core.ModNone {
		if err := sys.Init.SetModulePolicy(l, classical); err != nil {
			fmt.Fprintln(os.Stderr, "emu:", core.Errstr(err))
			return 1
		}
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
