// Command dial connects to a network address and runs a command with the
// connection as its standard input and output (Inferno dial(1)), with
// $net naming the connection directory.
//
// Keyring authentication is not implemented yet, so dial fails unless -A
// is given. The command is run directly when it is a module in /bin (or a
// path); otherwise it is given to /bin/sh.
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("dial [-A] [-k keyfile] [-a alg] addr command [arg...]")
	alg, keyfile := "", ""
	doauth := true
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'A':
			doauth = false
		case 'a':
			alg = a.EArg()
		case 'f', 'k':
			keyfile = a.EArg()
			if !strings.HasPrefix(keyfile, "/") && !strings.HasPrefix(keyfile, "./") {
				keyfile = "/usr/" + user() + "/keyring/" + keyfile
			}
		case 'v':
		default:
			a.Usage()
		}
	}
	argv := a.Argv()
	if len(argv) < 2 {
		a.Usage()
	}
	addr, cmd := argv[0], argv[1:]
	if doauth && alg == "" {
		alg = "none"
	}
	if alg != "" {
		if keyfile == "" {
			kd := "/usr/" + user() + "/keyring/"
			keyfile = kd + "default"
			if _, err := sys.Stat(kd + addr); err == nil {
				keyfile = kd + addr
			}
		}
		os.Stderr.WriteString("dial: cannot read " + keyfile + ": keyring authentication not implemented (use -A)\n")
		sys.Exits("fail:bad keyfile")
	}
	c, err := dial.Dial(addr, "")
	if err != nil {
		os.Stderr.WriteString("dial: cannot dial " + addr + ": " + err.Error() + "\n")
		sys.Exits("fail:errors")
	}
	sys.Pctl(sys.FORKENV, nil)
	sys.Remove("/env/user")
	if fd, err := sys.Create("/env/net", sys.OWRITE, 0o666); err == nil {
		f := os.NewFile(uintptr(fd), "net")
		f.Write([]byte(c.Dir))
		f.Close()
	}
	fd := int(c.Dfd.Fd())
	path := cmdpath(cmd[0])
	if path == "" {
		path, cmd = "/bin/sh.wasm", append([]string{"sh", "-c", "$*"}, cmd...)
	}
	// Run the command in this prog, as sh's run does, with the
	// connection on 0 and 1.
	sys.Dup(fd, 0)
	sys.Dup(fd, 1)
	c.Dfd.Close()
	c.Cfd.Close()
	err = sys.Exec(path, cmd)
	os.Stderr.WriteString("dial: " + cmd[0] + ": " + err.Error() + "\n")
	sys.Exits("fail:exec")
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
	b, err := os.ReadFile("/dev/user")
	if err != nil || len(b) == 0 {
		return "nobody"
	}
	return string(b)
}
