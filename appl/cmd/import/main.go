// Command import mounts a remote file tree served by exportfs (Inferno
// import(4)): it dials host's exportfs service, authenticates with p9any
// through factotum (role=client, with -k's keyspec), names the tree, and
// mounts the connection.
//
// That is Inferno's import, used when the protocol named by -p,
// $authproto or /lib/keys/proto is keyring; otherwise import is the Rust
// sibling's (pgauth.go). With -e alg the connection is encrypted with
// ssl after p9any, under the secret it established; otherwise it stays
// in the clear. -A skips authentication, as Plan 9's import -A does, and
// then only the clear protocol is available.
package main

import (
	"os"
	"runtime"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
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
	a.SetUsage("import [-a|-b] [-c] [-A] [-e enc digest] [-k keyspec] host file [localfile]")
	flags := 0
	cryptalg, keyspec := "", ""
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
			keyspec = a.EArg()
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
	var fac *os.File
	if !noauth {
		var err error
		if fac, err = authproto.OpenFactotum(); err != nil {
			fail("factotum", "can't open "+factotumfile+": "+err.Error())
		}
	}
	dest := dial.Netmkaddr(addr, "net", "exportfs")
	c, err := dial.Dial(dest, "")
	if err != nil {
		fail("dial failed", "can't dial "+dest+": "+err.Error())
	}
	var ai *authproto.Authinfo
	if !noauth {
		if ai, err = authproto.Proxy(c.Dfd, fac, "proto=p9any role=client "+keyspec); err != nil {
			fail("auth", "can't authenticate import: "+err.Error())
		}
	}
	conn := c.Dfd
	if _, err := conn.Write([]byte(file)); err != nil {
		fail("import", "can't write to remote: "+sys.Errstr())
	}
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	if n != 2 || buf[0] != 'O' || buf[1] != 'K' {
		e := sys.Errstr()
		if n >= 4 {
			e = "bad remote tree: " + string(buf[:n])
		}
		fail("import", "import "+addr+" "+file+": "+e)
	}
	if cryptalg != "" {
		if ai == nil || ai.Secret == nil {
			fail("import", "factotum didn't establish shared secret")
		}
		if _, err := conn.Write([]byte("impo nofilter ssl\n")); err != nil {
			fail("import", "can't write to remote: "+sys.Errstr())
		}
		in, out, err := authproto.ImpoKey(conn, ai.Secret, true)
		if err != nil {
			fail("import", "can't exchange key with remote: "+sys.Errstr())
		}
		if conn, err = authproto.PushSSL(conn, in, out, cryptalg); err != nil {
			fail("import", "can't push security layer: "+err.Error())
		}
	} else if _, err := conn.Write([]byte("impo nofilter clear\n")); err != nil {
		fail("import", "can't write to remote: "+sys.Errstr())
	}
	afd := -1
	var af *os.File
	if !noauth {
		// As Inferno's: authenticate the attach too, if the server asks.
		if fd, err := sys.Fauth(int(conn.Fd()), ""); err == nil {
			afd, af = fd, os.NewFile(uintptr(fd), "auth")
			authproto.Proxy(af, fac, "proto=p9any role=client")
		}
	}
	if err := sys.Mount(int(conn.Fd()), afd, mountpt, flags, ""); err != nil {
		fail("mount failed", "import "+addr+" "+file+": mount failed: "+sys.Errstr())
	}
	runtime.KeepAlive(af)
}
