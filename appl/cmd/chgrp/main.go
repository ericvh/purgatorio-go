// Command chgrp changes the group, or with -u or -o the owner, of files
// (Inferno chgrp(1)).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("usage: chgrp [-uo] group file ...\n")
	sys.Exits("fail:usage")
}

func main() {
	setuser := false
	a := arg.New(os.Args)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'o', 'u':
			setuser = true
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		usage()
	}
	id := args[0]
	bad := false
	for _, f := range args[1:] {
		d := sys.NullDir()
		if setuser {
			d.UID = id
		} else {
			d.GID = id
		}
		if err := sys.Wstat(f, &d); err != nil {
			os.Stderr.WriteString("chgrp: can't change " + f + ": " + sys.Errstr() + "\n")
			bad = true
		}
	}
	if bad {
		sys.Exits("fail:error")
	}
}
