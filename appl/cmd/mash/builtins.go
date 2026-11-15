package main

import (
	"io"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Module is a module of builtins, as Inferno's Mashbuiltin: init
// defines its builtins and cmd runs one.
type Module struct {
	name string
	init func(e *Env, m *Module)
	cmd  func(e *Env, l []string)
}

// modules are the builtin modules load knows.
var modules = map[string]*Module{}

// loaded lists the modules loaded, in order.
var loaded []*Module

const lib = "/bin/lib/mash/"

// doload loads a module of builtins.
func (e *Env) doload(s string) {
	name := strings.TrimSuffix(strings.TrimSuffix(s, ".wasm"), ".dis")
	name = strings.TrimPrefix(name, lib)
	m := modules[name]
	if m == nil {
		file := wasm(s)
		_, err := sys.Stat(file)
		if err != nil {
			es := err.Error()
			if nonexistent(es) && !absolute(file) {
				if _, err = sys.Stat(lib + file); err != nil {
					es = err.Error()
				}
			}
			if err != nil {
				e.report(s + ": " + es)
				return
			}
		}
		e.report(s + ": not a mash builtin module")
		return
	}
	found := false
	for _, l := range loaded {
		found = found || l == m
	}
	if !found {
		loaded = append(loaded, m)
	}
	m.init(e, m)
}

var prompt, contin string

func prprompt(n int) {
	switch n {
	case 0:
		fprint(1, prompt)
	case 1:
		fprint(1, contin)
	}
}

func init() {
	modules["builtins"] = &Module{name: "builtins", init: initBuiltins, cmd: builtins}
}

func initBuiltins(e *Env, m *Module) {
	for _, s := range []string{"env", "eval", "exit", "load", "prompt", "quote", "run", "status", "time", "whatis"} {
		e.defbuiltin(s, m)
	}
}

func builtins(e *Env, l []string) {
	args := l[1:]
	switch l[0] {
	case "env":
		if len(args) != 0 {
			e.usage("env")
			return
		}
		e.output(prsymbs(e.global, "=") + prsymbs(e.local, ":="))
	case "eval":
		eval(e, args)
	case "exit":
		panic(mashExit{})
	case "load":
		if len(args) == 1 {
			e.doload(args[0])
		} else {
			e.usage("load file")
		}
	case "prompt":
		switch len(args) {
		case 0:
			e.output(prompt)
		case 1:
			prompt = args[0]
		case 2:
			prompt = args[0]
			contin = args[1]
		default:
			e.usage("prompt [string]")
		}
	case "quote":
		if len(args) != 0 {
			q := make([]string, len(args))
			for i, s := range args {
				q[i] = quote(s)
			}
			e.output(strings.Join(q, " ") + "\n")
		}
	case "run":
		if !run(e, args) {
			e.usage("run [-] [-denx] file [arg ...]")
		}
	case "status":
		if len(args) != 0 {
			status(e, args)
		} else {
			e.usage("status cmd [arg ...]")
		}
	case "time":
		if len(args) != 0 {
			timecmd(e, args)
		} else {
			e.usage("time cmd [arg ...]")
		}
	case "whatis":
		var b strings.Builder
		for _, s := range args {
			whatis(e, &b, s)
		}
		e.output(b.String())
	}
}

// prone is a variable and its value as mash input.
func prone(name, eq string, s *Symb) string {
	r := name + " " + eq
	if !s.empty {
		for _, v := range s.value {
			r += " " + quote(v)
		}
	}
	return r + ";\n"
}

func prsymbs(t *Stab, eq string) string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	for _, s := range t.all() {
		if s.isvar {
			b.WriteString(prone(s.name, eq, s))
		}
	}
	return b.String()
}

func whatis(e *Env, b *strings.Builder, s string) {
	f := false
	if v := e.global.find(s); v != nil {
		if v.isvar {
			b.WriteString(prone(s, "=", v))
		}
		if v.fn != nil {
			b.WriteString("fn " + s + " { " + v.fn.text() + " };\n")
		}
		if v.builtin != nil {
			b.WriteString("load " + v.builtin.name + "; " + s + ";\n")
		}
		f = true
	}
	if e.local != nil {
		if v := e.local.find(s); v != nil {
			if v.isvar {
				b.WriteString(prone(s, ":=", v))
			}
			f = true
		}
	}
	if !f {
		b.WriteString(s + ": not found\n")
	}
}

// eval runs its arguments as mash input.
func eval(e *Env, l []string) {
	var s string
	for _, a := range l {
		s += " " + a
	}
	e = e.copy()
	e.flags &^= EInter
	e.sopen(s)
	parse(e)
}

// run runs a file as mash input.
func run(e *Env, l []string) bool {
	quiet := false
	if len(l) == 0 {
		return false
	}
	e = e.copy()
	s := l[0]
	for strings.HasPrefix(s, "-") {
		if s == "-" {
			quiet = true
		} else {
			for _, c := range s[1:] {
				switch c {
				case 'd':
					e.flags |= EDumping
				case 'e':
					e.flags |= ERaise
				case 'n':
					e.flags |= ENoxeq
				case 'x':
					e.flags |= EEcho
				default:
					return false
				}
			}
		}
		l = l[1:]
		if len(l) == 0 {
			return false
		}
		s = l[0]
	}
	fd, err := sys.Open(s, sys.OREAD)
	if err != nil {
		es := err.Error()
		if nonexistent(es) && !absolute(s) {
			if fd, err = sys.Open(lib+s, sys.OREAD); err != nil {
				es = err.Error()
			} else {
				s = lib + s
			}
		}
		if err != nil {
			if !quiet {
				e.report(s + ": " + es)
			}
			return true
		}
	}
	defer closefd(fd)
	e.local = newStab()
	e.local.assign("args", l[1:])
	e.flags &^= EInter
	e.fopen(fd, s)
	parse(e)
	return true
}

// status runs a command and outputs true if it wrote nothing on its
// standard error.
func status(e *Env, l []string) {
	n := e.copy()
	fds := n.pipe()
	if fds == nil {
		return
	}
	n.stderr = fds[0]
	func() {
		defer closefd(fds[0])
		n.runit(l, -1, -1, false)
	}()
	b, _ := io.ReadAll(fdreader(fds[1]))
	closefd(fds[1])
	if len(b) == 0 {
		e.output(vtrue)
	}
}

// timecmd times a command.
func timecmd(e *Env, l []string) {
	t1 := sys.Millisec()
	e.runit(l, -1, -1, true)
	t2 := sys.Millisec()
	fprint(e.stderr, strconv.FormatFloat(float64(t2-t1)/1000, 'g', 4, 64)+"\n")
}
