// Command listen listens for network calls and runs a command for each
// (Inferno listen(1)), with the connection as its standard input and
// output and $net naming the connection directory. Calls are served one
// at a time, as Inferno's listen runs each command before taking the
// next.
//
// Keyring authentication is not implemented yet: asking for it with -a
// fails, and as in an Inferno without #D, no algorithms are offered
// otherwise. The command is run directly when it is a module in /bin
// (or a path); otherwise, and for -i, it is given to /bin/sh.
package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var verbose bool

func main() {
	a := arg.New(os.Args)
	a.SetUsage("listen [-i {initscript}] [-Ast] [-k keyfile] [-a alg]... addr command [arg...]")
	var algs []string
	keyfile, initscript := "", ""
	synchronous := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			algs = append(algs, a.EArg())
		case 'A':
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
	if len(algs) > 0 {
		if keyfile == "" {
			keyfile = "/usr/" + user() + "/keyring/default"
		}
		os.Stderr.WriteString("listen: cannot read " + keyfile + ": keyring authentication not implemented\n")
		sys.Exits("fail:bad keyfile")
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
		setenv("net", nc.Dir)
		runon(cmd, int(dfd.Fd()))
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
