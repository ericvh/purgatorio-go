// Command rcmd runs a command on another machine (Inferno rcmd(1)). It
// dials the rstyx service, sends the command line and exports its name
// space (/ or the -x path) on the connection until the command ends;
// auxi/rstyxd at the other end mounts it and runs the command with this
// machine's /dev/cons.
//
// Keyring authentication is not implemented yet, so rcmd fails after
// dialling unless -A says not to authenticate.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("rcmd [-A] [-f keyfile] [-e alg] [-x exportpath] tcp!mach cmd")
	doauth := true
	exportpath := "/"
	keyfile := ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'e', 'a':
			a.EArg()
		case 'A':
			doauth = false
		case 'x':
			exportpath = a.EArg()
			if _, err := sys.Stat(exportpath); err != nil || exportpath == "" {
				a.Usage()
			}
		case 'f':
			keyfile = a.EArg()
			if !strings.HasPrefix(keyfile, "/") && !(len(keyfile) > 2 && strings.HasPrefix(keyfile, "./")) {
				keyfile = "/usr/" + user() + "/keyring/" + keyfile
			}
		default:
			a.Usage()
		}
	}
	argv := a.Argv()
	if len(argv) == 0 {
		a.Usage()
	}
	addr := argv[0]
	args := ""
	for _, s := range argv[1:] {
		args += " " + s
	}
	if args == "" {
		args = "sh"
	}
	if doauth && keyfile == "" {
		keyfile = "/usr/" + user() + "/keyring/default"
	}

	c, err := dial.Dial(dial.Netmkaddr(addr, "tcp", "rstyx"), "")
	if err != nil {
		fail("dial " + addr + " failed: " + err.Error())
	}
	if doauth {
		os.Stderr.WriteString("rcmd: authentication failed: cannot read " + keyfile + ": keyring authentication not implemented (use -A)\n")
		sys.Exits("fail:auth failed")
	}
	t := fmt.Sprintf("%d\n%s\n", len(args)+1, args)
	if n, err := c.Dfd.Write([]byte(t)); err != nil || n != len(t) {
		os.Stderr.WriteString("rcmd: cannot write arguments: " + sys.Errstr() + "\n")
		sys.Exits("fail:bad arg write")
	}
	if err := sys.Export(int(c.Dfd.Fd()), exportpath, sys.EXPWAIT); err != nil {
		os.Stderr.WriteString("rcmd: export: " + sys.Errstr() + "\n")
		sys.Exits("fail:export failed")
	}
}

func user() string {
	b, err := os.ReadFile("/dev/user")
	if err != nil {
		return ""
	}
	return string(b)
}

func fail(e string) {
	os.Stderr.WriteString("rcmd: " + e + "\n")
	sys.Exits("fail:errors")
}
