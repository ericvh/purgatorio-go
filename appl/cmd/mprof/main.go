// Command mprof is Inferno's memory profiler (mprof(1)) over #P.
//
//	mprof [-bcMflnve] [-m modname]... [cmd arg ...]
//
// Inferno counts the allocations in the Dis heap. A guest's allocator is
// inside the guest, so #P's startmp counts what the host sees: the bytes
// each function's linear memory grew while it ran, in it or in what it
// called. Linear memory never shrinks, so the two columns, Inferno's
// memory in use and its high-water mark, are equal. There are no line
// counts, so -l is taken as -f; -1, -2 and -3 (Inferno's memory pools)
// are accepted and make no difference.
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/profile"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wasmobj"
)

const (
	vModule = 1 << iota
	vFunction
	vLine
	vFullhdr
	vVerbose
)

func ignored(s string) { fmt.Fprintf(os.Stderr, "mprof: warning: %s ignored\n", s) }

func exits(e string) {
	profile.End()
	sys.Exits("fail:" + e)
}

func pfatal(s string) {
	fmt.Fprintf(os.Stderr, "mprof: %s: %s\n", s, profile.LastError())
	exits("error")
}

func usage(s string) {
	fmt.Fprintf(os.Stderr, "mprof: %s\n", s)
	fmt.Fprintf(os.Stderr, "usage: mprof [-bcMflnve] [-m modname]... [cmd arg ...]\n")
	exits("usage")
}

func main() {
	a := arg.New(os.Args)
	if !profile.Init() {
		pfatal("cannot initialize profile device")
	}
	v := 0
	begin, end, ep := false, false, false
	var mods []string
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'b':
			begin = true
		case 'c':
			end = true
		case 'M':
			v |= vModule
		case 'f':
			v |= vFunction
		case 'l':
			v |= vLine
		case 'n':
			v |= vFullhdr
		case 'v':
			v |= vVerbose
		case 'm':
			s := a.Arg()
			if s == "" {
				usage("missing module name")
			}
			mods = append([]string{s}, mods...)
		case 'e':
			ep = true
		case '1', '2', '3':
		default:
			usage(fmt.Sprintf("unknown option -%c", c))
		}
	}
	exec := a.Argv()
	if begin && end {
		ignored("-e option")
	}
	if (begin || end) && v != 0 {
		ignored("output format")
	}
	if begin && len(exec) > 0 {
		begin = false
	}
	if !begin && len(exec) == 0 {
		if len(mods) > 0 {
			ignored("-m option")
		}
		mods = nil
	}
	if end {
		if len(mods) > 0 {
			ignored("-m option")
		}
		if ep || len(exec) > 0 {
			ignored("command")
		}
		profile.End()
		return
	}
	for _, m := range mods {
		profile.Profile(m)
	}
	if begin {
		if !profile.MemStart() {
			pfatal("cannot start profiling")
		}
		return
	}
	if len(exec) > 0 {
		if ep {
			profile.Profile(profile.Wasmname(exec[0]))
		}
		if !profile.MemStart() {
			pfatal("cannot start profiling")
		}
		if e := profile.Run("mprof", exec); e != "" {
			pfatal(e)
		}
	}
	if !profile.Stop() {
		pfatal("cannot stop profiling")
	}
	ms := stats(profile.Stats())
	if len(ms) == 0 {
		pfatal("no profile information")
	}
	if v&(vModule|vFunction|vLine) == 0 {
		v |= vModule | vLine
	}
	if v&vLine != 0 {
		v = v&^vLine | vFunction
	}
	show(ms, v)
	profile.End()
}

type funprof struct {
	f             wasmobj.Func
	count, counte uint64
}

type modprof struct {
	name, path    string
	funs          []funprof
	total, totale uint64
}

// stats reads each module's histogram, laid out as Inferno's memory
// profile: 0 and 1 the module's totals, 2(pc+1) and 2(pc+1)+1 a
// function's.
func stats(mods []profile.Mod) []modprof {
	var out []modprof
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
		r := modprof{name: mp.Name, path: mp.Path, total: mp.Hist[0], totale: mp.Hist[1]}
		idx := map[uint64]int{}
		for _, f := range syms.Funcs() {
			idx[f.Entry] = len(r.funs)
			r.funs = append(r.funs, funprof{f: f})
		}
		for i, c := range mp.Hist {
			if i < 2 {
				continue
			}
			f := syms.Lookup((i - 2) / 2)
			if f == nil {
				continue
			}
			fp := &r.funs[idx[f.Entry]]
			if i&1 == 0 {
				fp.count += c
			} else {
				fp.counte += c
			}
		}
		if r.total != 0 || r.totale != 0 {
			out = append(out, r)
		}
	}
	return out
}

// show prints the profile as Inferno's memshow does.
func show(ms []modprof, v int) {
	var total, totale uint64
	for _, mp := range ms {
		total += mp.total
		totale += mp.totale
		fmt.Printf("\nModule: %s(%s)\n\n", mp.name, mp.path)
		if v&vFunction != 0 {
			for _, fp := range mp.funs {
				if v&vVerbose != 0 || fp.count != 0 || fp.counte != 0 {
					if v&vFullhdr != 0 {
						fmt.Printf("%s:", fp.f.File)
					}
					fmt.Printf("%d\t%d\t%d\t%s()\n", fp.f.Line, fp.count, fp.counte, fp.f.Name)
				}
			}
			if v&vModule != 0 {
				fmt.Printf("\n")
			}
		}
		if v&vModule != 0 {
			fmt.Printf("Module totals\t%d\t%d\n\n", mp.total, mp.totale)
		}
	}
	if len(ms) > 1 {
		fmt.Printf("Grand totals\t%d\t%d\n\n", total, totale)
	}
}
