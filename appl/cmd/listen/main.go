// Command listen listens for network calls and runs a command for each
// (Inferno listen(1)), with the connection as its standard input and
// output and $net naming the connection directory. Calls are served one
// at a time, as Inferno's listen runs each command before taking the
// next.
//
// When the protocol named by -p, $authproto or /lib/keys/proto is
// keyring, listen authenticates each caller with Inferno's keyring
// before running the command, unless -A: with the key in keyfile
// (default /usr/user/keyring/default), allowing the ssl algorithms given
// with -a (by default none and every algorithm #D has), and pushing ssl
// when the caller asks for one; $user is the caller's name. listen does
// not run pgauth's handshake, which is Styx's: under hybrid and classical
// each command runs with authproto set to the protocol, or to none with
// -A, so that an export it runs authenticates its client that way
// (docs/auth.md). The command is run directly when it is a module in /bin
// (or a path); otherwise, and for -i, it is given to /bin/sh.
package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/auth"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/ssl"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var verbose bool

func main() {
	a := arg.New(os.Args)
	a.SetUsage("listen [-i {initscript}] [-Ast] [-k keyfile] [-a alg]... [-p proto] addr command [arg...]")
	proto, noauth := "", false
	var algs []string
	keyfile, initscript := "", ""
	synchronous := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			algs = append(algs, a.EArg())
		case 'A':
			noauth = true
		case 'p':
			proto = a.EArg()
		case 'f', 'k':
			keyfile = a.EArg()
			if !strings.HasPrefix(keyfile, "/") && !strings.HasPrefix(keyfile, "./") {
				keyfile = "/usr/" + user() + "/keyring/" + keyfile
			}
		case 'i':
			initscript = a.EArg()
		case 'v':
			verbose = true
		case 's':
			synchronous = true
		case 't':
		default:
			a.Usage()
		}
	}
	protos, err := authproto.Resolve(proto)
	if err != nil {
		os.Stderr.WriteString("listen: " + err.Error() + "\n")
		sys.Exits("fail:usage")
	}
	var serverkey *keyring.Authinfo
	if protos[0] == authproto.Keyring && !noauth {
		if algs == nil {
			enc, hash := ssl.Algs()
			if enc != nil || hash != nil {
				algs = append(append([]string{"none"}, enc...), hash...)
			}
		}
		if algs != nil {
			if keyfile == "" {
				keyfile = "/usr/" + user() + "/keyring/default"
			}
			if serverkey, err = keyring.ReadAuthinfo(keyfile); err != nil {
				os.Stderr.WriteString("listen: cannot read " + keyfile + ": " + err.Error() + "\n")
				sys.Exits("fail:bad keyfile")
			}
		}
	}
	sys.Unmount("", "/mnt/keys")
	argv := a.Argv()
	if len(argv) < 2 {
		a.Usage()
	}
	addr, cmd := argv[0], argv[1:]

	sys.Pctl(sys.FORKFD|sys.FORKENV, nil)
	acon, err := dial.Announce(addr)
	if err != nil {
		os.Stderr.WriteString("listen: failed to announce on '" + addr + "': " + err.Error() + "\n")
		sys.Exits("fail:cannot announce")
	}
	setenv("user", "")
	// The commands run with the protocol named, or with none under -A;
	// keyring stays keyring, whose -A is only listen's own (Inferno's).
	if noauth && protos[0] != authproto.Keyring {
		setenv("authproto", authproto.None)
	} else {
		setenv("authproto", strings.Join(protos, ","))
	}
	if initscript != "" {
		setenv("net", acon.Dir)
		run([]string{initscript})
	}
	if !synchronous {
		sys.Detach()
	}
	for {
		nc, err := dial.Listen(acon)
		if err != nil {
			os.Stderr.WriteString("listen: listen error on '" + addr + "': " + err.Error() + "\n")
			sys.Exits("fail:listen")
		}
		if verbose {
			os.Stderr.WriteString("listen: got connection on " + addr + " from " + readfile(nc.Dir+"/remote"))
		}
		dfd, err := dial.Accept(nc)
		if err != nil {
			os.Stderr.WriteString("listen: cannot open " + nc.Dir + "/data: " + err.Error() + "\n")
			nc.Cfd.Close()
			continue
		}
		nc.Cfd.Write([]byte("keepalive"))
		fd := dfd
		if serverkey != nil {
			// One at a time, as calls are served; Inferno authenticates
			// in a process of its own and serves calls as they finish.
			afd, uname, err := auth.Server(algs, serverkey, dfd, false)
			if err != nil {
				os.Stderr.WriteString("listen: auth on " + addr + " failed: " + err.Error() + "\n")
				dfd.Close()
				nc.Cfd.Close()
				continue
			}
			if verbose {
				os.Stderr.WriteString("listen: authenticated on " + addr + " as " + uname + "\n")
			}
			setenv("user", uname)
			fd = afd
		}
		setenv("net", nc.Dir)
		runon(cmd, int(fd.Fd()))
		if fd != dfd {
			fd.Close()
		}
		dfd.Close()
		nc.Cfd.Close()
	}
}

func setenv(name, val string) {
	if val == "" {
		sys.Remove("/env/" + name)
		return
	}
	fd, err := sys.Create("/env/"+name, sys.OWRITE, 0o666)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(fd), name)
	f.Write([]byte(val))
	f.Close()
}

func run(argv []string) { runon(argv, 0) }

// runon runs argv with fd as its standard input and output, and waits
// for it.
func runon(argv []string, fd int) {
	path := cmdpath(argv[0])
	if path == "" || strings.HasPrefix(argv[0], "{") {
		path, argv = "/bin/sh.wasm", append([]string{"sh", "-c", "$*"}, argv...)
	}
	pid, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		os.Stderr.WriteString("listen: can't open wait file: " + sys.Errstr() + "\n")
		return
	}
	wf := os.NewFile(uintptr(wfd), "wait")
	defer wf.Close()
	child, err := sys.Spawn(path, argv, sys.NEWFD, [][2]int{{0, fd}, {1, fd}, {2, 2}})
	if err != nil {
		os.Stderr.WriteString("listen: " + argv[0] + ": " + sys.Errstr() + "\n")
		return
	}
	buf := make([]byte, 256)
	for {
		n, err := wf.Read(buf)
		if err != nil || n == 0 {
			return
		}
		if p, _, _ := strings.Cut(string(buf[:n]), " "); p == strconv.Itoa(child) {
			return
		}
	}
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
	return "nobody"
}

func readfile(f string) string {
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	return string(b)
}
