// Command styxlisten listens for network connections and serves each
// with the Styx server cmd (Inferno styxlisten(1)): cmd runs once, with a
// pipe as its standard input, and every client gets its own attach to it.
//
// Unless -A is given, each client is authenticated with the protocol
// named by -p, $authproto or /lib/keys/proto (docs/auth.md). With hybrid
// (the default) or classical, or both, the kernel's export of each
// client authenticates it with pgauth's handshake, trusting the keys
// under /lib/keys in the listener's namespace, and the server proves
// itself with keyfile when one is given (an authinfo or a seed). With
// keyring, Auth's server authenticates each client before the export,
// using keyfile (default /usr/user/keyring/default) and allowing the ssl
// algorithms given with -a (by default none and every algorithm #D has),
// one at a time, since the listener is a single prog. Clients are
// attached as the listener's user. cmd serves the listener over a pipe
// and runs with authproto=none. cmd is run
// directly when it is a module in /bin (or a path); otherwise it
// is given to /bin/sh. Inferno kills the listener when cmd exits; this
// one carries on, and new clients get errors.
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/auth"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/ssl"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	verbose       bool
	passhostnames bool
	authinfo      *keyring.Authinfo
	algs          []string
	pgflags       int    // export flags for hybrid or classical
	pgkey         []byte // the server's key for them
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("styxlisten [-a alg]... [-Atsv] [-k keyfile] [-p proto] address cmd [arg...]")
	proto := ""
	doauth := true
	synchronous := false
	trusted := false
	keyfile := ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'v':
			verbose = true
		case 'a':
			algs = append([]string{a.EArg()}, algs...)
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
		case 'p':
			proto = a.EArg()
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
	protos, err := authproto.Resolve(proto)
	if err != nil {
		fatal(err.Error())
	}
	if protos[0] == authproto.None {
		doauth = false
	}
	if doauth && protos[0] != authproto.Keyring {
		pgflags = authproto.ExportFlags(protos)
		if keyfile != "" {
			if pgkey, err = os.ReadFile(keyfile); err != nil {
				fatal("cannot read " + keyfile + ": " + sys.Errstr())
			}
		}
		doauth = false
	}
	if doauth {
		if algs == nil {
			enc, hash := ssl.Algs()
			algs = append(append([]string{"none"}, enc...), hash...)
		}
		if keyfile == "" {
			keyfile = "/usr/" + user() + "/keyring/default"
		}
		if authinfo, err = keyring.ReadAuthinfo(keyfile); err != nil {
			fatal("cannot read " + keyfile + ": " + err.Error())
		}
	} else {
		algs = nil
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
		fd, uname := dfd, ""
		if authinfo != nil {
			fd, uname, err = auth.Server(algs, authinfo, dfd, false)
			if err != nil {
				if verbose {
					os.Stderr.WriteString("styxlisten: authentication failed: " + err.Error() + "\n")
				}
				dfd.Close()
				nc.Cfd.Close()
				continue
			}
			if verbose {
				os.Stderr.WriteString("styxlisten: client authenticated as " + uname + "\n")
			}
		}
		exportproc(mfd, uname, hostname, fd)
		if fd != dfd {
			fd.Close()
		}
		dfd.Close()
		nc.Cfd.Close()
	}
}

// exportproc attaches to the server and exports the tree on the
// connection. Inferno attaches as the client, having set its name in
// the authenticating thread; a single prog cannot change its user and
// change it back, so the attach here is as the listener's user.
func exportproc(mfd int, uname, hostname string, dfd *os.File) {
	if err := sys.Mount(mfd, -1, "/n", sys.MREPL|sys.MCREATE, hostname); err != nil {
		os.Stderr.WriteString("styxlisten: cannot mount for user '" + uname + "': " + sys.Errstr() + "\n")
	} else if err := sys.ExportKey(int(dfd.Fd()), "/n", sys.EXPASYNC|pgflags, pgkey); err != nil {
		os.Stderr.WriteString("styxlisten: cannot export: " + sys.Errstr() + "\n")
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
	// cmd serves the listener over a pipe, which is not authenticated.
	if _, err := sys.Pctl(sys.FORKENV, nil); err == nil {
		setenv("authproto", authproto.None)
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

func setenv(name, val string) {
	sys.Remove("/env/" + name)
	if fd, err := sys.Create("/env/"+name, sys.OWRITE, 0o666); err == nil {
		f := os.NewFile(uintptr(fd), name)
		f.WriteString(val)
		f.Close()
	}
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
