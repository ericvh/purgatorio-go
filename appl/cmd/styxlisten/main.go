// Command styxlisten listens for network connections and serves each
// with the Styx server cmd (Inferno styxlisten(1)): cmd runs once, with a
// pipe as its standard input, and every client gets its own attach to it.
//
// Keyring authentication is not implemented yet, so styxlisten fails
// unless -A is given. cmd is run directly when it is a module in /bin (or
// a path); otherwise it is given to /bin/sh. Inferno kills the listener
// when cmd exits; this one carries on, and new clients get errors.
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	verbose       bool
	passhostnames bool
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("styxlisten [-a alg]... [-Atsv] [-k keyfile] address cmd [arg...]")
	doauth := true
	synchronous := false
	trusted := false
	keyfile := ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'v':
			verbose = true
		case 'a':
			a.EArg()
		case 'f', 'k':
			keyfile = a.EArg()
			if !strings.HasPrefix(keyfile, "/") && !strings.HasPrefix(keyfile, "./") {
				keyfile = "/usr/" + user() + "/keyring/" + keyfile
			}
		case 'h':
			passhostnames = true
		case 't':
			trusted = true
		case 's':
			synchronous = true
		case 'A':
			doauth = false
		default:
			a.Usage()
		}
	}
	argv := a.Argv()
	if len(argv) < 2 {
		a.Usage()
	}
	addr := dial.Netmkaddr(argv[0], "tcp", "styx")
	cmd := argv[1:]
	if doauth {
		if keyfile == "" {
			keyfile = "/usr/" + user() + "/keyring/default"
		}
		fatal("cannot read " + keyfile + ": keyring authentication not implemented (use -A)")
	}
	c, err := dial.Announce(addr)
	if err != nil {
		fatal("cannot announce on " + addr + ": " + err.Error())
	}
	if !trusted {
		sys.Unmount("", "/mnt/keys")
	}
	mfd := popen(cmd)
	if !synchronous {
		sys.Detach()
	}
	listener(c, mfd)
}

func listener(c *dial.Connection, mfd int) {
	// Each client's tree is mounted here, in a name space of our own,
	// and exported from there.
	sys.Pctl(sys.FORKNS, nil)
	for {
		nc, err := dial.Listen(c)
		if err != nil {
			fatal("listen failed: " + err.Error())
		}
		if verbose {
			os.Stderr.WriteString("styxlisten: got connection from " + readfile(nc.Dir+"/remote"))
		}
		dfd, err := dial.Accept(nc)
		if err != nil {
			nc.Cfd.Close()
			continue
		}
		nc.Cfd.Write([]byte("keepalive"))
		hostname := ""
		if passhostnames {
			hostname = strings.TrimSuffix(readfile(nc.Dir+"/remote"), "\n")
		}
		if err := sys.Mount(mfd, -1, "/n", sys.MREPL|sys.MCREATE, hostname); err != nil {
			os.Stderr.WriteString("styxlisten: cannot mount for user '': " + sys.Errstr() + "\n")
		} else if err := sys.Export(int(dfd.Fd()), "/n", sys.EXPASYNC); err != nil {
			os.Stderr.WriteString("styxlisten: cannot export: " + sys.Errstr() + "\n")
		}
		dfd.Close()
		nc.Cfd.Close()
	}
}

func fatal(e string) {
	os.Stderr.WriteString("styxlisten: " + e + "\n")
	sys.Exits("fail:error")
}

// popen runs argv with a pipe as its standard input and returns the
// other end of the pipe.
func popen(argv []string) int {
	p, err := sys.Pipe()
	if err != nil {
		fatal("cannot make pipe: " + sys.Errstr())
	}
	path := cmdpath(argv[0])
	if path == "" {
		path, argv = "/bin/sh.wasm", append([]string{"sh", "-c", "$*"}, argv...)
	}
	if _, err := sys.Spawn(path, argv, sys.NEWFD, [][2]int{{0, p[0]}, {1, 1}, {2, 2}}); err != nil {
		fatal("cannot run " + argv[0] + ": " + sys.Errstr())
	}
	os.NewFile(uintptr(p[0]), "pipe").Close()
	return p[1]
}

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

func user() string {
	if s := readfile("/dev/user"); s != "" {
		return s
	}
	return "none"
}

func readfile(f string) string {
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	return string(b)
}
