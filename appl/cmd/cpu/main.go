// Command cpu runs a command on another machine with this machine's name
// space (Inferno cpu(1)). It dials the rstyx service, asks it to run
// auxi/cpuslave with the command (sh by default), and exports / on the
// connection until the command ends. The remote command sees the name
// space in /n/client, with its devices bound before the remote /dev.
//
// Keyring authentication is not implemented yet. Inferno's cpu always
// authenticates; here the extra option -A skips authentication (pair it
// with listen -A at the other end) and without it cpu fails cleanly.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const defcmd = "/bin/sh"

func usage() {
	os.Stderr.WriteString("Usage: cpu [-C cryptoalg] mach command args...\n")
	sys.Exits("fail:usage")
}

func main() {
	a := arg.New(os.Args)
	doauth := true
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'C':
			a.Arg()
		case 'A':
			doauth = false
		default:
			usage()
		}
	}
	argv := a.Argv()
	args := "auxi/cpuslave --"
	if len(argv) == 0 {
		usage()
	}
	mach := argv[0]
	if len(argv) == 1 {
		args += " " + defcmd
	} else {
		args += " " + quoted(argv[1:])
	}

	if doauth {
		user := getuser()
		kd := "/usr/" + user + "/keyring/"
		cert := kd + dial.Netmkaddr(mach, "tcp", "")
		if !exists(cert) {
			cert = kd + "default"
			if !exists(cert) {
				os.Stderr.WriteString("cpu: cannot find certificate in " + kd + "; use getauthinfo\n")
				sys.Exits("fail:no certificate")
			}
		}
	}
	c, err := dial.Dial(dial.Netmkaddr(mach, "net", "rstyx"), "")
	if err != nil {
		os.Stderr.WriteString("cpu: can't dial " + mach + ": " + err.Error() + "\n")
		sys.Exits("fail:dial")
	}
	if doauth {
		os.Stderr.WriteString("cpu: authentication failed: keyring authentication not implemented (use -A)\n")
		sys.Exits("fail:authentication failure")
	}
	t := fmt.Sprintf("%d\n%s\n", len(args)+1, args)
	if n, err := c.Dfd.Write([]byte(t)); err != nil || n != len(t) {
		os.Stderr.WriteString("cpu: export args write error: " + sys.Errstr() + "\n")
		sys.Exits("fail:write error")
	}
	if err := sys.Export(int(c.Dfd.Fd()), "/", sys.EXPWAIT); err != nil {
		os.Stderr.WriteString("cpu: export failed: " + sys.Errstr() + "\n")
		sys.Exits("fail:export error")
	}
}

// quoted joins args with blanks, quoting those that need it, as
// String->quoted does.
func quoted(args []string) string {
	var b strings.Builder
	for i, s := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if s == "" || strings.ContainsAny(s, " \t\n'") {
			b.WriteString("'" + strings.ReplaceAll(s, "'", "''") + "'")
		} else {
			b.WriteString(s)
		}
	}
	return b.String()
}

func exists(f string) bool {
	_, err := sys.Stat(f)
	return err == nil
}

func getuser() string {
	fd, err := sys.Open("/dev/user", sys.OREAD)
	if err != nil {
		os.Stderr.WriteString("cpu: cannot open /dev/user: " + sys.Errstr() + "\n")
		sys.Exits("fail:no user id")
	}
	f := os.NewFile(uintptr(fd), "/dev/user")
	defer f.Close()
	b := make([]byte, 50)
	n, err := f.Read(b)
	if err != nil && n == 0 {
		return ""
	}
	return string(b[:n])
}
