// Command mash is Inferno's mash(1): a programmable shell with rc-like
// syntax, list expressions and make-style dependencies and rules
// (mash-make(1), with load make).
//
// Builtins, functions and control flow run in mash itself, and so do
// groups and generators, as in Inferno's mash; commands are progs. A
// background command (&) that is not a plain command runs in a new mash
// given this one's variables, functions and modules, where Inferno's
// runs a thread that shares them.
package main

import (
	"bufio"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	profile = "/lib/mashinit"
	console = "/dev/cons"
)

var cmd string

// isconsole reports whether fd is the console.
func isconsole(fd int) bool {
	d1, err1 := sys.Fstat(fd)
	d2, err2 := sys.Stat(console)
	if err1 != nil || err2 != nil {
		return false
	}
	return d1.Type == d2.Type && d1.Qid.Path == d2.Qid.Path
}

func usage(e *Env) {
	fprint(e.stderr, "usage: mash [-denx] [-c command] [src [args]]\n")
	exits("usage")
}

func flags(e *Env, l []string) []string {
	for len(l) > 0 && l[0] != "" && l[0][0] == '-' {
		s := l[0]
		l = l[1:]
		if s == "--" {
			break
		}
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case 'c':
				if i+1 < len(s) {
					if len(l) != 0 {
						usage(e)
					}
					cmd = s[i+1:]
				} else {
					if len(l) != 1 {
						usage(e)
					}
					cmd = l[0]
				}
				return nil
			case 'd':
				e.flags |= EDumping
			case 'e':
				e.flags |= ERaise
			case 'n':
				e.flags |= ENoxeq
			case 'x':
				e.flags |= EEcho
			default:
				usage(e)
			}
		}
	}
	return l
}

func main() {
	if len(os.Args) > 1 && helper(os.Args[1]) {
		return
	}
	e := newEnv()
	status := ""
	switch x := catch(func() { mash(e, os.Args) }).(type) {
	case mashRaise:
		status = string(x)
	}
	stophelpers()
	if status != "" {
		sys.Exits(status)
	}
}

func mash(e *Env, argv []string) {
	args := flags(e, argv[1:])
	e.doload("builtins")
	prompt = "mash% "
	contin = "\t"
	if cmd == "" && len(args) == 0 {
		e.global.assign("mashinit", []string{"true"})
		if fd, err := sys.Open(profile, sys.OREAD); err == nil {
			e.fopen(fd, profile)
			parse(e)
			closefd(fd)
		}
	}
	e.global.assign("mashinit", nil)
	if cmd != "" {
		e.sopen(cmd)
		parse(e)
		return
	}
	fd := -1
	if len(args) > 0 {
		s := args[0]
		var err error
		if fd, err = sys.Open(s, sys.OREAD); err != nil {
			e.couldnot("open", s)
		}
		e.fopen(fd, s)
		e.global.assign("args", args[1:])
	}
	if fd < 0 {
		if isconsole(0) {
			e.interactive(0)
		}
		e.fopen(0, "<stdin>")
	}
	parse(e)
}

// getline reads a line, joining lines that end with a backslash.
func getline(in *bufio.Reader) (string, bool) {
	s, err := readline(in)
	if err != nil && s == "" {
		return "", false
	}
	for strings.HasSuffix(s, "\\\n") {
		s = s[:len(s)-2] + "  "
		prprompt(1)
		t, err := readline(in)
		if err != nil && t == "" {
			break
		}
		s += t
	}
	return s, true
}

// readline reads a line from the console, or a line written to
// /chan/mash once history has started.
func readline(in *bufio.Reader) (string, error) {
	if mashfd >= 0 && in.Buffered() == 0 {
		i, err := sys.Wait([][2]int{{sys.WaitRead, 0}, {sys.WaitRead, mashfd}}, -1)
		if err == nil && i == 1 {
			b := make([]byte, 8192)
			n, err := fdreader(mashfd).Read(b)
			if err == nil && n > 0 {
				s := string(b[:n])
				fprint(1, s)
				return s, nil
			}
			closefd(mashfd)
			mashfd = -1
		}
	}
	return in.ReadString('\n')
}

// interactive is the console loop: each line is a command.
func (e *Env) interactive(fd int) {
	in := bufio.NewReader(fdreader(fd))
	e.flags |= EInter
	for {
		prprompt(0)
		s, ok := getline(in)
		if !ok {
			exitmash()
		}
		e.sopen(s)
		parse(e)
		addhist(s)
	}
}
