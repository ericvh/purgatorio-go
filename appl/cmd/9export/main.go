// Command 9export serves the name space to a Plan 9 import on standard
// input, speaking exportfs's opening protocol (Inferno 9export(4)): the
// client names the tree it wants, 9export answers OK and reads the
// client's "impo filter crypt" line, then 9P follows.
//
// Plan 9's p9any authentication through factotum is not implemented, so
// 9export runs only with -A, and then, as in Inferno, the connection is
// never encrypted. As Plan 9's exportfs, and unlike Inferno's 9export,
// it also takes a client that sends no impo line and starts 9P at once,
// as the Rust sibling's import -9 does. Then 9export relays the
// connection to the export through a pipe, with a helper prog (the
// internal option -R) copying from the client. -r serves the tree
// read-only (the Rust sibling's addition).
package main

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
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
	xflag, rdonly := sys.EXPWAIT, 0
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			xflag = sys.EXPASYNC
		case 'A':
			noauth = true
		case 'e', 'k':
			a.EArg()
		case 'r':
			rdonly = sys.EXPRDONLY
		case '9':
		default:
			a.Usage()
		}
	}
	sys.Pctl(sys.FORKFD|sys.FORKNS, nil)
	conn := os.NewFile(0, "conn")
	if !noauth {
		if _, err := sys.Open(factotumfile, sys.ORDWR); err != nil {
			fail("factotum", "can't open "+factotumfile+": "+sys.Errstr())
		}
		fail("auth", "can't authenticate 9export: p9any is not implemented; use -A")
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
	if err := sys.Export(0, ".", xflag|rdonly); err != nil {
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
