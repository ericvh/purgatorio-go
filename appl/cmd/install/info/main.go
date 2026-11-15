// Command info describes an installed package, or a package archive
// (Inferno install/info): the time it is complete as of, and each
// package and update that makes it up, newest first. -r gives the root
// the package is installed under.
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wrap"
)

var typestr = []string{"???", "package", "update", "full update"}

func fatal(err string) {
	fmt.Fprintf(os.Stderr, "%s\n", err)
	wrap.End()
	sys.Exits("fail:error")
}

func main() {
	root := ""
	a := arg.New(os.Args)
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'r':
			root = a.Arg()
			if root == "" {
				fatal("missing root name")
			}
		default:
			fatal(fmt.Sprintf("bad argument -%c", c))
		}
	}
	args := a.Argv()
	if len(args) != 1 {
		fatal("usage: install/info [-r root] package")
	}
	w := wrap.OpenHdr(args[0], root, false)
	if w == nil {
		fatal("no such package found")
	}
	tm := daytime.Text(daytime.Local(w.Tfull))
	if len(tm) > 28 {
		tm = tm[:28]
	}
	fmt.Printf("%s (complete as of %s)\n", w.Name, tm)
	for i := len(w.U) - 1; i >= 0; i-- {
		u := w.U[i]
		if u.Typ < 0 || u.Typ >= len(typestr) {
			fmt.Print(typestr[0])
		} else {
			fmt.Print(typestr[u.Typ])
		}
		fmt.Printf(" %s", wrap.Now2string(u.Time, false))
		if u.Typ&wrap.UPD != 0 {
			fmt.Printf(" updating %s", wrap.Now2string(u.Utime, false))
		}
		if u.Desc != "" {
			fmt.Printf(": %s", u.Desc)
		}
		fmt.Println()
	}
	wrap.End()
}
