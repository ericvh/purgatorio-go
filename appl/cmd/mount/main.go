// Command mount attaches a Styx server to the name space (Inferno
// bind(1)): the server is a network address, dialled through /net; a
// file, opened; or {command}, run with a pipe as its standard input.
//
// Unless -A is given, mount authenticates with the server using the key
// in keyfile (default /usr/user/keyring/net!addr, then default) and
// pushes ssl with cryptoalg, none by default (a {command} is never
// authenticated, as in Inferno). -9 (factotum), -o (old Styx), -P
// (persistent) and -S (trace) are not implemented.
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/auth"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	doauth  = true
	do9     bool
	oldstyx bool
	persist bool
	show    bool
	quiet   bool
	verbose bool
	keyfile string
	alg     = "none"
	conn    *dial.Connection // keeps the connection's files from being finalized
	authfd  *os.File         // likewise the authenticated connection
)

func fail(status, msg string) {
	os.Stderr.WriteString("mount: " + msg + "\n")
	sys.Exits("fail:" + status)
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("mount [-a|-b] [-coA9] [-C cryptoalg] [-k keyfile] [-q] net!addr|file|{command} mountpoint [spec]")
	flags := 0
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			flags |= sys.MAFTER
		case 'b':
			flags |= sys.MBEFORE
		case 'c':
			flags |= sys.MCREATE
		case 'C':
			alg = a.EArg()
		case 'k', 'f':
			keyfile = a.EArg()
		case 'A':
			doauth = false
		case '9':
			doauth = false
			do9 = true
		case 'o':
			oldstyx = true
		case 'v':
			verbose = true
		case 'P':
			persist = true
		case 'S':
			show = true
		case 'q':
			quiet = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	spec := ""
	if len(args) != 2 {
		if len(args) != 3 {
			a.Usage()
		}
		spec = args[2]
	}
	addr, mountpoint := args[0], args[1]
	if oldstyx && do9 {
		fail("usage", "cannot combine -o and -9 options")
	}
	switch {
	case do9:
		fail("load", "can't load /dis/lib/factotum.dis: factotum not implemented")
	case oldstyx:
		fail("error", "cannot load /dis/lib/styxconv/new2old.dis: old Styx not implemented")
	case persist:
		fail("load", "cannot load /dis/lib/styxpersist.dis: styxpersist not implemented")
	case show:
		fail("error", "-S not implemented")
	}
	fd := connect(addr)
	if doauth {
		var id string
		var err error
		if authfd, id, err = authenticate(keyfile, alg, os.NewFile(uintptr(fd), addr), addr); err != nil {
			fail("error", err.Error())
		}
		if verbose {
			os.Stdout.WriteString("remote username is " + id + "\n")
		}
		fd = int(authfd.Fd())
	}
	if err := sys.Mount(fd, -1, mountpoint, flags, spec); err != nil && !quiet {
		fail("mount failed", "mount failed: "+sys.Errstr())
	}
}

func connect(dest string) int {
	if len(dest) > 1 && dest[0] == '{' && dest[len(dest)-1] == '}' {
		doauth = false
		return popen(dest[1 : len(dest)-1])
	}
	if len(strings.FieldsFunc(dest, func(r rune) bool { return r == '!' })) == 1 {
		fd, err := sys.Open(dest, sys.ORDWR)
		if err == nil {
			return fd
		}
		if dest != "" && dest[0] == '/' {
			fail("open failed", "can't open "+dest+": "+sys.Errstr())
		}
	}
	dest = dial.Netmkaddr(dest, "net", "styx")
	var err error
	if conn, err = dial.Dial(dest, ""); err != nil {
		fail("dial failed", "can't dial "+dest+": "+err.Error())
	}
	return int(conn.Dfd.Fd())
}

// popen runs cmd with one end of a pipe as its standard input and returns
// the other end. Without a shell to parse it, a command made only of
// words is run directly; anything else needs /bin/sh.
func popen(cmd string) int {
	p, err := sys.Pipe()
	if err != nil {
		fail("error", "can't create pipe: "+sys.Errstr())
	}
	argv := strings.Fields(cmd)
	path := ""
	if len(argv) > 0 && !strings.ContainsAny(cmd, "{}|;&<>$'\"`=^") {
		path = cmdpath(argv[0])
	}
	if path == "" {
		path, argv = "/bin/sh.wasm", []string{"sh", "-c", cmd}
	}
	if _, err := sys.Spawn(path, argv, sys.NEWFD, [][2]int{{0, p[0]}, {1, 1}, {2, 2}}); err != nil {
		fail("error", "can't run "+cmd+": "+sys.Errstr())
	}
	os.NewFile(uintptr(p[0]), "pipe").Close()
	return p[1]
}

// cmdpath finds the module for a command name as the shell does: a name
// with no slash is looked for in /bin.
func cmdpath(name string) string {
	path := name
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "./") {
		path = "/bin/" + path
	}
	if !strings.HasSuffix(path, ".wasm") {
		path += ".wasm"
	}
	if _, err := sys.Stat(path); err != nil {
		return ""
	}
	return path
}

// authenticate runs Auth's client on the connection with the key from
// keyfile, as mount.b does.
func authenticate(keyfile, alg string, dfd *os.File, addr string) (*os.File, string, error) {
	kd := "/usr/" + user() + "/keyring/"
	cert := keyfile
	switch {
	case cert == "":
		cert = kd + dial.Netmkaddr(addr, "tcp", "")
		if _, err := sys.Stat(cert); err != nil {
			cert = kd + "default"
		}
	case cert[0] != '/':
		cert = kd + cert
	}
	ai, err := keyring.ReadAuthinfo(cert)
	if err != nil {
		return nil, "", sys.Error("cannot read " + cert + ": " + err.Error())
	}
	fd, id, err := auth.Client(alg, ai, dfd)
	if err != nil {
		return nil, "", sys.Error("authentication failed: " + err.Error())
	}
	return fd, id, nil
}

func user() string {
	b, err := os.ReadFile("/dev/user")
	if err != nil {
		return ""
	}
	return string(b)
}
