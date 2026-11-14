// Command auxi/cpuslave runs the command cpu(1) asked for, on the
// machine rstyxd(8) runs on. The -s option names a public screen for
// graphical commands; there is no draw device yet, so it fails.
package main

import (
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("usage: cpuslave [-s screenid] command args\n")
	sys.Exits("fail:usage")
}

func main() {
	a := arg.New(os.Args)
	screenid := -1
	for o := a.Opt(); o != 0; o = a.Opt() {
		s := ""
		if o == 's' {
			s = a.Arg()
		}
		if s == "" {
			usage()
		}
		screenid, _ = strconv.Atoi(s)
	}
	args := a.Argv()
	if len(args) == 0 {
		usage()
	}
	file := args[0] + ".wasm"
	if _, err := sys.Stat(file); err != nil {
		file = "/bin/" + file
		if _, err := sys.Stat(file); err != nil {
			os.Stderr.WriteString("cpuslave: can't load " + args[0] + ": " + sys.Errstr() + "\n")
			sys.Exits("fail:bad command")
		}
	}
	// Under pgauth's rstyxd the caller's devices come first here; Inferno's
	// rstyxd has bound them already, and the bind is then harmless.
	if _, err := sys.Stat("/n/client/dev"); err == nil {
		sys.Bind("/n/client/dev", "/dev", sys.MBEFORE)
	}
	if screenid >= 0 {
		os.Stderr.WriteString("cpuslave: can't initialize display: no draw device\n")
		sys.Exits("fail:no display")
	}
	sys.Exec(file, args)
	os.Stderr.WriteString("cpuslave: can't load " + args[0] + ": " + sys.Errstr() + "\n")
	sys.Exits("fail:bad command")
}
