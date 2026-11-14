// Command calc interprets a calculator language with Limbo-like syntax
// (Inferno calc(1)). It reads a file, the expression given by its
// arguments, or standard input.
package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
)

var (
	bits, debug, strict bool
	deg, pbase, errdec  *Dec
	out                 = bufio.NewWriter(os.Stdout)
)

func err(s string) { fmt.Fprintf(out, "error: %s\n", s) }

func fatal(s string) {
	fmt.Fprintf(out, "fatal: %s\n", s)
	panic(exitError{})
}

func main() {
	defer out.Flush()
	defer func() {
		if e := recover(); e != nil {
			if _, ok := e.(exitError); !ok {
				panic(e)
			}
		}
	}()
	a := arg.New(os.Args)
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'b':
			bits = true
		case 'd':
			debug = true
		case 's':
			strict = true
		}
	}
	args := a.Argv()
	gargs = args
	switch len(args) {
	case 0:
		usestdin = true
		in.bin = stdinReader()
	case 1:
		// A file if it can be opened, else an expression.
		if f, e := os.Open(args[0]); e == nil {
			in.bin = bufio.NewReader(f)
		}
	}

	pushscope()
	for _, k := range keyw {
		enter(k.name, k.op)
	}
	for _, c := range conw {
		adddec(c.name, Ocon, c.val, 0)
	}
	for _, v := range varw {
		adddec(v.name, Ovar, v.val, 0)
	}
	for _, f := range funw {
		adddec(f.name, Olfun, float64(f.op), f.na)
	}
	deg = lookup(degS).dec
	pbase = lookup(baseS).dec
	errdec = &Dec{}

	pushscope()
	for {
		e, ok, eof := statement()
		if eof {
			break
		}
		if !ok || e == nil || e.op == Ofn {
			continue
		}
		k, v, ok := run(e)
		if ok && pexp(e) {
			printnum(v, "\n")
		}
		if k == Oexit {
			return
		}
		out.Flush()
	}
	popscope()
	popscope()
}

// statement parses the next statement, reporting errors and skipping
// the rest of their line.
func statement() (e *Node, ok, eof bool) {
	defer func() {
		if x := recover(); x != nil {
			switch x := x.(type) {
			case eofError:
				err("premature eof")
			case calcError:
				err(string(x))
			default:
				panic(x)
			}
			skip()
			e, ok = nil, false
		}
	}()
	t := lex()
	if t == Oeof {
		return nil, false, true
	}
	unlex(t)
	ls := lexes
	e = stat(true)
	ckstat(e, Onothing, false)
	if ls == lexes {
		t = lex()
		errorf(nil, "syntax error near %s", opstring(t))
	}
	consume(Onl)
	return e, true, false
}

func run(e *Node) (k int, v float64, ok bool) {
	defer func() {
		if x := recover(); x != nil {
			switch x := x.(type) {
			case calcError:
				err(string(x))
			case eofError:
				err("premature eof")
			default:
				panic(x)
			}
			k, v, ok = Onothing, 0, false
		}
	}()
	k, v = estat(e)
	return k, v, true
}
