// Command auxi/rstyxd is the server side of rcmd(1) and cpu(1) (Inferno
// rstyxd(8)). Its standard input is the connection: it reads a byte
// count and that many bytes of command line, mounts the connection on
// /n/client, binds /n/client/dev before /dev, opens the caller's
// /dev/cons as standard input, output and error, and runs the command.
//
// That is Inferno's protocol, spoken when the protocol named by
// $authproto (which listen sets) or /lib/keys/proto is keyring; its
// authentication is listen(1)'s business. Otherwise rstyxd speaks pgauth's
// rstyx protocol (pgauth.go, docs/rcmd.md), hybrid or classical, and
// none (listen -A) serves it without authentication. The command runs
// with the identity of the listener.
package main

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/newns"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if pgmain() {
		return
	}
	args := readargs(os.Stdin)
	if args == nil {
		fail("error reading arguments: " + sys.Errstr())
	}
	os.Stderr.WriteString("rstyxd: cmd: " + strings.Join(args, " ") + " \n")
	cmd := args[0]
	file := cmd + ".wasm"
	if cmd == "sh" {
		file = "/bin/sh.wasm"
	}
	if _, err := sys.Stat(file); err != nil {
		file = "/bin/" + file
		if _, err := sys.Stat(file); err != nil {
			badmod(file)
		}
	}
	sys.Pctl(sys.FORKNS|sys.FORKENV, nil)
	if _, err := sys.Mount(0, -1, "/n/client", sys.MREPL, ""); err != nil {
		fail("cannot mount connection on /n/client: " + sys.Errstr())
	}
	if _, err := sys.Bind("/n/client/dev", "/dev", sys.MBEFORE); err != nil {
		fail("cannot bind /n/client/dev to /dev: " + sys.Errstr())
	}
	in, err := sys.Open("/dev/cons", sys.OREAD)
	if err != nil {
		in = 0
	}
	out, err := sys.Open("/dev/cons", sys.OWRITE)
	if err != nil {
		out = 2
	}
	// Run the command as a child and hang up when it is done: the mount
	// of the connection holds it open while any prog sharing this name
	// space runs, and the caller waits for the connection to close.
	pid, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		fail("can't open wait file: " + sys.Errstr())
	}
	child, err := sys.Spawn(file, args, sys.NEWFD, [][2]int{{0, in}, {1, out}, {2, out}})
	if err != nil {
		badmod(file)
	}
	wf := os.NewFile(uintptr(wfd), "wait")
	buf := make([]byte, 256)
	for {
		n, err := wf.Read(buf)
		if err != nil || n == 0 {
			break
		}
		if p, _, _ := strings.Cut(string(buf[:n]), " "); p == strconv.Itoa(child) {
			break
		}
	}
	if net, err := os.ReadFile("/env/net"); err == nil && len(net) > 0 {
		if fd, err := sys.Open(string(net)+"/ctl", sys.OWRITE); err == nil {
			os.NewFile(uintptr(fd), "ctl").Write([]byte("hangup"))
		}
	}
}

// readargs reads a decimal count on a line of its own, then that many
// bytes, and splits them into words with sh's quoting rules.
func readargs(r io.Reader) []string {
	var line []byte
	c := make([]byte, 1)
	for {
		if len(line) >= 1024 {
			return nil
		}
		if n, _ := r.Read(c); n != 1 {
			return nil
		}
		if c[0] == '\n' {
			break
		}
		line = append(line, c[0])
	}
	nb, _ := strconv.Atoi(string(line))
	if nb <= 0 {
		return nil
	}
	buf := make([]byte, nb)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil
	}
	return newns.Unquoted(string(buf))
}

func fail(s string) {
	os.Stderr.WriteString("rstyxd: " + s + "\n")
	sys.Exits("fail:error")
}

func badmod(s string) {
	os.Stderr.WriteString("rstyxd: can't load " + s + ": " + sys.Errstr() + "\n")
	sys.Exits("fail:load")
}
