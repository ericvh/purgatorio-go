// Command cleanname prints names with redundant elements removed
// (Inferno cleanname(1)).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/names"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("cleanname [-d pwd] name ...")
	dir := ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd':
			dir = a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	for _, n := range args {
		if dir != "" && n != "" && n[0] != '/' && n[0] != '#' {
			n = dir + "/" + n
		}
		os.Stdout.WriteString(names.Cleanname(n) + "\n")
	}
}
