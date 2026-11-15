// Command kfscmd sends each cmd to the console of the kfs(4) started
// with -n name (main by default), /chan/kfs.name.cmd, and copies what it
// answers to standard output (Inferno disk/kfscmd, kfscmd(8)). The
// commands are allow, allowoff (or disallow), check [cdfpPqrtw], sync and
// users; a command of several words is one argument.
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func fail(s string) {
	fmt.Fprintf(os.Stderr, "kfscmd: %s\n", s)
	sys.Exits("fail:error")
}

func main() {
	cfs := "main"
	a := arg.New(os.Args)
	a.SetUsage("disk/kfscmd [-n fsname] cmd ...")
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'n':
			cfs = a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	ctlf := "/chan/kfs." + cfs + ".cmd"
	fd, err := sys.Open(ctlf, sys.ORDWR)
	if err != nil {
		fail("can't open " + ctlf + ": " + sys.Errstr())
	}
	ctl := os.NewFile(uintptr(fd), ctlf)
	buf := make([]byte, 1024)
	for _, c := range args {
		if n, err := ctl.Write([]byte(c)); n <= 0 || err != nil {
			fail(quote.Quote(c) + ": " + sys.Errstr())
		}
		for {
			n, err := ctl.Read(buf)
			if n <= 0 || err != nil {
				break
			}
			os.Stdout.Write(buf[:n])
		}
	}
}
