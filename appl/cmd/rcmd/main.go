// Command rcmd runs a command on another machine (Inferno rcmd(1)). It
// dials the rstyx service, sends the command line and exports its name
// space (/ or the -x path) on the connection until the command ends;
// auxi/rstyxd at the other end mounts it and runs the command with this
// machine's /dev/cons.
//
// That is Inferno's protocol, spoken when the protocol named by -p,
// $authproto or /lib/keys/proto is keyring: unless -A, it authenticates
// with Inferno's keyring and the key in keyfile (default
// /usr/user/keyring/default), asking for ssl with alg (none by default). Otherwise rcmd speaks pgauth's rstyx protocol
// (pgauth.go, docs/rcmd.md).
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/auth"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/rstyx"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == rstyx.Pump {
		rstyx.RunPump()
		return
	}
	if pgmain("/bin/rcmd.wasm") {
		return
	}
	a := arg.New(os.Args)
	a.SetUsage("rcmd [-A] [-f keyfile] [-e alg] [-x exportpath] tcp!mach cmd")
	doauth := true
	exportpath := "/"
	keyfile, alg := "", ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'e', 'a':
			alg = a.EArg()
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
	fd := c.Dfd
	if doauth {
		// A key that cannot be read is nil, which auth.Client reports.
		ai, _ := keyring.ReadAuthinfo(keyfile)
		var err error
		if fd, _, err = auth.Client(alg, ai, c.Dfd); err != nil {
			os.Stderr.WriteString("rcmd: authentication failed: " + err.Error() + "\n")
			sys.Exits("fail:auth failed")
		}
	}
	t := fmt.Sprintf("%d\n%s\n", len(args)+1, args)
	if n, err := fd.Write([]byte(t)); err != nil || n != len(t) {
		os.Stderr.WriteString("rcmd: cannot write arguments: " + sys.Errstr() + "\n")
		sys.Exits("fail:bad arg write")
	}
	if err := sys.Export(int(fd.Fd()), exportpath, sys.EXPWAIT); err != nil {
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
