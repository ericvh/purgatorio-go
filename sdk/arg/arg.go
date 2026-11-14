// Package arg parses command options as Inferno's Arg module does
// (appl/lib/arg.b): single-letter options that may be combined (-lt),
// option arguments joined (-n5) or separate (-n 5), and "--" to end the
// options.
package arg

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Arg is the state of one parse.
type Arg struct {
	name  string
	args  []string
	cur   string
	usage string
}

// New starts parsing argv; argv[0] is the program name.
func New(argv []string) *Arg {
	a := &Arg{}
	if len(argv) > 0 {
		a.name, a.args = argv[0], argv[1:]
	}
	return a
}

// SetUsage sets the text Usage prints after "usage: "; by Inferno
// convention it starts with the command name.
func (a *Arg) SetUsage(u string) { a.usage = u }

// Progname is argv[0].
func (a *Arg) Progname() string { return a.name }

// Opt returns the next option letter, or 0 at the end of the options.
func (a *Arg) Opt() rune {
	if a.cur != "" {
		r := []rune(a.cur)
		a.cur = string(r[1:])
		return r[0]
	}
	if len(a.args) == 0 {
		return 0
	}
	next := a.args[0]
	if len(next) < 2 || next[0] != '-' {
		return 0
	}
	if next == "--" {
		a.args = a.args[1:]
		return 0
	}
	r := []rune(next[1:])
	a.cur = string(r[1:])
	a.args = a.args[1:]
	return r[0]
}

// Arg returns the current option's argument, or "" if there is none.
func (a *Arg) Arg() string {
	if a.cur != "" {
		s := a.cur
		a.cur = ""
		return s
	}
	if len(a.args) == 0 {
		return ""
	}
	s := a.args[0]
	a.args = a.args[1:]
	return s
}

// EArg is Arg, but a missing argument is a usage error.
func (a *Arg) EArg() string {
	if a.cur == "" && len(a.args) == 0 {
		a.Usage()
	}
	return a.Arg()
}

// Argv returns the remaining arguments; no options follow.
func (a *Arg) Argv() []string {
	r := a.args
	a.args = nil
	return r
}

// Usage prints the usage message, or "name: argument expected" when none
// was set, and exits with "fail:usage", as Inferno's arg->usage does.
func (a *Arg) Usage() {
	u := a.name + ": argument expected"
	if a.usage != "" {
		u = "usage: " + a.usage
	}
	os.Stderr.WriteString(u + "\n")
	sys.Exits("fail:usage")
}
