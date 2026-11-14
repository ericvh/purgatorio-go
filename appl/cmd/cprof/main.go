// Command cprof is Inferno's coverage profiler (cprof(1)) over #P.
//
//	cprof [-fne] [-m modname]... cmd [arg ...]
//
// It runs the command with #P's startcp, which counts the calls of each
// function of the modules the command starts. Inferno marks each line
// of Limbo source executed or not; a guest carries no table from wasm
// code to lines, so cprof marks each Go function (or each wasm function
// of a module that is not Go) called (+) or not (-), at the line it
// starts, and -f gives the number of calls. The coverage is the share of
// functions called. A Go function is entered again when its goroutine
// resumes after a switch, so its count can exceed its calls. Inferno's -r (keep results in .prf files) and the
// form with no command, which reads them, are not provided.
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/profile"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wasmobj"
)

func exits(e string) {
	profile.End()
	sys.Exits("fail:" + e)
}

func pfatal(s string) {
	fmt.Fprintf(os.Stderr, "cprof: %s: %s\n", s, profile.LastError())
	exits("error")
}

func usage(s string) {
	fmt.Fprintf(os.Stderr, "cprof: %s\n", s)
	fmt.Fprintf(os.Stderr, "usage: cprof [-fne] [-m modname]... cmd [arg ... ]\n")
	exits("usage")
}

func main() {
	a := arg.New(os.Args)
	if !profile.Init() {
		pfatal("cannot initialize profile device")
	}
	fullhdr, freq, ep := false, false, false
	var mods []string
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'n':
			fullhdr = true
		case 'f':
			freq = true
		case 'm':
			s := a.Arg()
			if s == "" {
				usage("missing module/file")
			}
			mods = append([]string{s}, mods...)
		case 'e':
			ep = true
		default:
			usage(fmt.Sprintf("unknown option -%c", c))
		}
	}
	exec := a.Argv()
	if len(exec) == 0 {
		pfatal("no profile information")
	}
	for _, m := range mods {
		profile.Profile(m)
	}
	if ep {
		profile.Profile(profile.Wasmname(exec[0]))
	}
	// Inferno names the command's prog; here the command starts as soon
	// as it is spawned, so cprof names itself, and #P counts in the
	// modules its descendants start.
	me, _ := sys.Pctl(0, nil)
	if !profile.CPStart(me) {
		pfatal("cannot start profiling")
	}
	if e := profile.Run("cprof", exec); e != "" {
		pfatal(e)
	}
	if !profile.Stop() {
		pfatal("cannot stop profiling")
	}
	mods2 := profile.Stats()
	if len(mods2) == 0 {
		pfatal("no profile information")
	}
	show(mods2, fullhdr, freq)
	profile.End()
}

// show prints the profile as Inferno's cpshow does, by function.
func show(mods []profile.Mod, fullhdr, freq bool) {
	var total uint64
	shown := 0
	for _, mp := range mods {
		b, err := os.ReadFile(mp.Path)
		if err != nil {
			continue
		}
		m, err := wasmobj.Parse(b)
		if err != nil {
			continue
		}
		syms := m.Syms()
		counts := map[uint64]uint64{}
		var mtotal uint64
		for fn, c := range mp.Hist {
			if f := syms.Lookup(fn); f != nil {
				counts[f.Entry] += c
				mtotal += c
			}
		}
		funcs := syms.Funcs()
		cov := 0
		if len(funcs) > 0 {
			cov = 100 * len(counts) / len(funcs)
		}
		fmt.Printf("\nModule: %s(%s)", mp.Name, mp.Path)
		fmt.Printf("\t%d%% coverage\n\n", cov)
		shown++
		total += mtotal
		if cov == 100 && !freq {
			continue
		}
		for _, f := range funcs {
			if fullhdr {
				fmt.Printf("%s:", f.File)
			}
			c, ok := counts[f.Entry]
			mark := "-"
			switch {
			case freq:
				mark = fmt.Sprint(c)
			case ok:
				mark = "+"
			}
			fmt.Printf("%d\t%s\t%s()\n", f.Line, mark, f.Name)
		}
		fmt.Printf("\n**** module function calls %d ****\n\n", mtotal)
	}
	if shown > 1 {
		fmt.Printf("\n**** total number function calls %d ****\n\n", total)
	}
}
