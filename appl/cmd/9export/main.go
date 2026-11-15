// Command 9export serves the name space to a Plan 9 import on standard
// input, speaking exportfs's opening protocol (Inferno 9export(4)):
// unless -A, it authenticates the client with p9any through factotum
// (role=server, with -k's keyspec); then the client names the tree it
// wants, 9export answers OK and reads the client's "impo filter crypt"
// line, and 9P follows. With -e alg and authentication, the connection
// is then encrypted with ssl under the secret p9any established, keyed
// as Plan 9's exportfs keys it (Inferno's 9export writes the secret past
// the end of its key buffer); otherwise it stays in the clear.
//
// As Plan 9's exportfs, and unlike Inferno's 9export, it also takes a
// client that sends no impo line and starts 9P at once, as the Rust
// sibling's import -9 does. Then 9export relays the connection to the
// export through a pipe, with a helper prog (the internal option -R)
// copying from the client. -r serves the tree read-only (the Rust
// sibling's addition).
package main

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const factotumfile = "/mnt/factotum/rpc"

func fail(status, msg string) {
	os.Stderr.WriteString("9export: " + msg + "\n")
	sys.Exits("fail:" + status)
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "-R" {
		relay(os.Args[2])
		return
	}
	a := arg.New(os.Args)
	a.SetUsage("9export [-aA9r] [-k keyspec] [-e enc digest]")
	noauth := false
	cryptalg, keyspec := "", ""
	xflag, rdonly := sys.EXPWAIT, 0
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			xflag = sys.EXPASYNC
		case 'A':
			noauth = true
		case 'e':
			if cryptalg = a.EArg(); cryptalg == "clear" {
				cryptalg = ""
			}
		case 'k':
			keyspec = a.EArg()
		case 'r':
			rdonly = sys.EXPRDONLY
		case '9':
		default:
			a.Usage()
		}
	}
	sys.Pctl(sys.FORKFD|sys.FORKNS, nil)
	conn := os.NewFile(0, "conn")

	var secret []byte
	if !noauth {
		fac, err := authproto.OpenFactotum()
		if err != nil {
			fail("factotum", "can't open "+factotumfile+": "+err.Error())
		}
		ai, err := authproto.Proxy(conn, fac, "proto=p9any role=server "+keyspec)
		if err != nil {
			fail("auth", "can't authenticate 9export: "+err.Error())
		}
		fac.Close()
		secret = ai.Secret
	}

	// Read the tree; it's a Plan 9 bug that there's no reliable delimiter.
	btree := make([]byte, 2048)
	n, _ := conn.Read(btree)
	if n <= 0 {
		fail("tree", "can't read tree: "+sys.Errstr())
	}
	tree := strings.TrimRight(string(btree[:n]), "\x00")
	if err := sys.Chdir(tree); err != nil {
		e := sys.Errstr()
		conn.Write([]byte("chdir(" + strconv.Itoa(n) + ":\"" + tree + "\"): " + e))
		fail("tree", "bad tree: "+tree)
	}
	if n, _ := conn.Write([]byte("OK")); n != 2 {
		fail("tree", "can't OK tree: "+sys.Errstr())
	}
	impo := make([]byte, 2048)
	if _, err := io.ReadFull(conn, impo[:4]); err != nil {
		fail("impo", "can't read impo: "+sys.Errstr())
	}
	if string(impo[:4]) != "impo" {
		export9p(impo[:4], xflag|rdonly)
		return
	}
	for n = 4; n < len(impo); n++ {
		if k, _ := conn.Read(impo[n : n+1]); k != 1 {
			fail("impo", "can't read impo: "+sys.Errstr())
		}
		if impo[n] == 0 || impo[n] == '\n' {
			break
		}
	}
	if !noauth && cryptalg != "" {
		if secret == nil {
			fail("import", "didn't establish shared secret")
		}
		in, out, err := authproto.ImpoKey(conn, secret, false)
		if err != nil {
			fail("import", "can't exchange key with remote: "+sys.Errstr())
		}
		if conn, err = authproto.PushSSL(conn, in, out, cryptalg); err != nil {
			fail("import", "can't push security layer: "+err.Error())
		}
	}
	if err := sys.Export(int(conn.Fd()), ".", xflag|rdonly); err != nil {
		fail("export", "can't export "+tree+": "+sys.Errstr())
	}
}

// export9p serves a client that started 9P without an impo line: the
// export reads a pipe holding the bytes already read, then everything
// the client sends.
func export9p(pre []byte, flag int) {
	p, err := sys.Pipe()
	if err != nil {
		fail("export", "can't make pipe: "+sys.Errstr())
	}
	if err := sys.Export(p[0], ".", sys.EXPASYNC|flag&sys.EXPRDONLY); err != nil {
		fail("export", "can't export: "+sys.Errstr())
	}
	sys.Close(p[0])
	srv := os.NewFile(uintptr(p[1]), "export")
	if _, err := srv.Write(pre); err != nil {
		fail("export", "can't write to export: "+sys.Errstr())
	}
	if flag&sys.EXPASYNC != 0 {
		sys.Detach()
	}
	me, _ := sys.Pctl(0, nil)
	if _, err := sys.Spawn("/bin/9export.wasm", []string{"9export", "-R", strconv.Itoa(me)},
		sys.NEWFD, [][2]int{{0, 0}, {1, p[1]}, {2, 2}}); err != nil {
		fail("export", "can't start relay: "+sys.Errstr())
	}
	io.Copy(os.NewFile(0, "conn"), srv)
}

// relay copies the client's messages to the export; when the client
// hangs up it kills the prog copying the other way, whose end of the
// pipe then closes and ends the export.
func relay(pid string) {
	io.Copy(os.Stdout, os.Stdin)
	if fd, err := sys.Open("/prog/"+pid+"/ctl", sys.OWRITE); err == nil {
		f := os.NewFile(uintptr(fd), "ctl")
		f.Write([]byte("kill"))
		f.Close()
	}
}
