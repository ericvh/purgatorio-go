// Command kill ends progs by pid or by module name (Inferno kill(1));
// -g kills each prog's whole group.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("usage: kill [-g] pid|module [...]\n")
	sys.Exits("fail: usage")
}

func main() {
	msg := "kill"
	a := arg.New(os.Args)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'g':
			msg = "killgrp"
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		usage()
	}
	n := 0
	for _, s := range args {
		if s == "" {
			usage()
		}
		if s[0] >= '0' && s[0] <= '9' {
			n += killpid(s, msg, true)
		} else {
			n += killmod(s, msg)
		}
	}
	if n == 0 {
		sys.Exits("fail:nothing killed")
	}
}

// killpid writes msg to the prog's ctl file. Unless sbok (should be ok)
// is set, a prog that has gone away meanwhile is not reported.
func killpid(pid, msg string, sbok bool) int {
	f, err := os.OpenFile("/prog/"+pid+"/ctl", os.O_WRONLY, 0)
	if err != nil {
		e := sys.Errstr()
		if sbok || e != "thread exited" && len(e) >= 14 && !strings.HasSuffix(e, "does not exist") {
			fmt.Fprintf(os.Stderr, "kill: cannot open /prog/%s/ctl: %s\n", pid, e)
		}
		return 0
	}
	defer f.Close()
	if _, err := f.WriteString(msg); err != nil {
		e := sys.Errstr()
		if sbok || e != "thread exited" {
			fmt.Fprintf(os.Stderr, "kill: cannot kill %s: %s\n", pid, e)
		}
		return 0
	}
	return 1
}

func killmod(mod, msg string) int {
	fd, err := sys.Open("/prog", sys.OREAD)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kill: open /prog: %s\n", sys.Errstr())
		return 0
	}
	var pids []string
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kill: read /prog: %s\n", sys.Errstr())
			break
		}
		if len(ds) == 0 {
			break
		}
		for _, d := range ds {
			if match(d.Name, mod) {
				pids = append([]string{d.Name}, pids...)
			}
		}
	}
	if len(pids) == 0 {
		fmt.Fprintf(os.Stderr, "kill: cannot find %s\n", mod)
		return 0
	}
	n := 0
	for _, p := range pids {
		if killpid(p, msg, false) == 1 {
			fmt.Printf("%s ", p)
			n++
		}
	}
	if n > 0 {
		fmt.Printf("\n")
	}
	return n
}

// match reports whether the prog in /prog/dir runs module mod: the last
// field of its status, without a builtin suffix such as [$Sys].
func match(dir, mod string) bool {
	f, err := os.Open("/prog/" + dir + "/status")
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		if e := sys.Errstr(); e != "thread exited" {
			fmt.Fprintf(os.Stderr, "kill: cannot read /prog/%s/status: %s\n", dir, e)
		}
		return false
	}
	fields := strings.FieldsFunc(string(buf[:n]), func(r rune) bool { return r == ' ' })
	s := ""
	if len(fields) > 0 {
		s = fields[len(fields)-1]
	}
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[:i]
	}
	return s == mod
}
