// Command basename strips the directory, and optionally a suffix, from a
// name; with -d it prints the directory instead (Inferno basename(1)).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/names"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("basename [-d] string [suffix]")
	dirname := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd':
			dirname = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 || len(args) > 1 && (dirname || len(args) > 2) {
		a.Usage()
	}
	if dirname {
		s := names.Dirname(args[0])
		if s == "" {
			s = "."
		}
		os.Stdout.WriteString(s + "\n")
		return
	}
	suffix := ""
	if len(args) > 1 {
		suffix = args[1]
	}
	os.Stdout.WriteString(names.Basename(args[0], suffix) + "\n")
}
