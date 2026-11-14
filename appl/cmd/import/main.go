// Command import mounts a remote file tree served by exportfs (Inferno
// import(4)): it dials host's exportfs service, names the tree, and
// mounts the connection.
//
// That is Inferno's import, used when the protocol named by -p,
// $authproto or /lib/keys/proto is keyring; otherwise import is the Rust
// sibling's (pgauth.go). Plan 9's p9any through factotum is not
// implemented, so Inferno's import fails as Inferno's does when
// /mnt/factotum/rpc cannot be opened. -A skips authentication, as Plan
// 9's import -A does, and then only the clear (unencrypted) protocol is
// available.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const factotumfile = "/mnt/factotum/rpc"

func fail(status, msg string) {
	os.Stderr.WriteString("import: " + msg + "\n")
	sys.Exits("fail:" + status)
}

func main() {
	if pgmain() {
		return
	}
	a := arg.New(os.Args)
	a.SetUsage("import [-a|-b] [-c] [-A] [-e enc digest] host file [localfile]")
	flags := 0
	cryptalg := ""
	noauth := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			flags |= sys.MAFTER
		case 'b':
			flags |= sys.MBEFORE
		case 'c':
			flags |= sys.MCREATE
		case 'e':
			if cryptalg = a.EArg(); cryptalg == "clear" {
				cryptalg = ""
			}
		case 'k':
			a.EArg()
		case 'A':
			noauth = true
		case '9':
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) != 2 && len(args) != 3 {
		a.Usage()
	}
	addr, file := args[0], args[1]
	mountpt := file
	if len(args) > 2 {
		mountpt = args[2]
	}
	sys.Pctl(sys.FORKFD, nil)
	if !noauth {
		if _, err := sys.Open(factotumfile, sys.ORDWR); err != nil {
			fail("factotum", "can't open "+factotumfile+": "+sys.Errstr())
		}
		fail("auth", "can't authenticate import: factotum not implemented")
	}
	dest := dial.Netmkaddr(addr, "net", "exportfs")
	c, err := dial.Dial(dest, "")
	if err != nil {
		fail("dial failed", "can't dial "+dest+": "+err.Error())
	}
	if _, err := c.Dfd.Write([]byte(file)); err != nil {
		fail("import", "can't write to remote: "+sys.Errstr())
	}
	buf := make([]byte, 256)
	n, _ := c.Dfd.Read(buf)
	if n != 2 || buf[0] != 'O' || buf[1] != 'K' {
		e := sys.Errstr()
		if n >= 4 {
			e = "bad remote tree: " + string(buf[:n])
		}
		fail("import", "import "+addr+" "+file+": "+e)
	}
	if cryptalg != "" {
		fail("import", "factotum didn't establish shared secret")
	}
	if _, err := c.Dfd.Write([]byte("impo nofilter clear\n")); err != nil {
		fail("import", "can't write to remote: "+sys.Errstr())
	}
	if err := sys.Mount(int(c.Dfd.Fd()), -1, mountpt, flags, ""); err != nil {
		fail("mount failed", "import "+addr+" "+file+": mount failed: "+sys.Errstr())
	}
}
