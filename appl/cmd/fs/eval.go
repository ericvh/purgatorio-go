package main

// The eval verb: parse an expression and run its verbs, converting
// values as needed (appl/cmd/fs/eval.b).

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/quote"
)

const (
	tWORD = iota + 256
	tSHCMD
	tVAR
	tEOF = -1
)

type parseerr struct{}

type evalstate struct {
	s       []rune
	spos    int
	report  *Report
	args    []value
	verbose bool
}

func evaltypes() string { return "as-v" }

func evalrun(r *Report, opts []option, args []value) value {
	e := &evalstate{s: []rune(vs(args[0])), report: r, verbose: opts != nil}
	return e.expr()
}

func tok2s(t int, s string) string {
	switch t {
	case tWORD:
		return s
	case tSHCMD:
		return "@"
	case tVAR:
		return "$" + s
	}
	return string(rune(t))
}

func errorf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
}

// gettok1 is gettok with parse errors as a flag.
func (p *evalstate) gettok1() (t int, s string, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, is := r.(parseerr); !is {
				panic(r)
			}
			ok = false
		}
	}()
	t, s = p.gettok()
	return t, s, true
}

// expr: WORD exprs; exprs: | exprs '{' expr '}' | exprs WORD | exprs
// SHCMD | exprs VAR.
func (p *evalstate) expr() value {
	t, s, ok := p.gettok1()
	if !ok {
		return nil
	}
	if t != tWORD {
		errorf("fs: eval: syntax error (char %d), expected word, found %s\n", p.spos, quote.Quote(tok2s(t, s)))
		return nil
	}
	cmd := s
	var args []value
loop:
	for {
		t, s, ok = p.gettok1()
		if !ok {
			go discardall(nil, args)
			return nil
		}
		switch t {
		case '{':
			v := p.expr()
			if v == nil {
				go discardall(nil, args)
				return nil
			}
			args = append(args, v)
		case '}':
			break loop
		case tWORD:
			args = append(args, vS{s})
		case tVAR:
			n, _ := strconv.Atoi(s)
			if n < 0 || n >= len(p.args) {
				errorf("fs: eval: invalid arg reference $%s\n", s)
				go discardall(nil, args)
				return nil
			}
			if p.args[n] == nil {
				errorf("fs: eval: cannot use $%d twice\n", n)
				go discardall(nil, args)
				return nil
			}
			args = append(args, p.args[n])
			p.args[n] = nil
		case tSHCMD:
			args = append(args, vC{s})
		case tEOF:
			break loop
		default:
			go discardall(nil, args)
			errorf("fs: eval: syntax error; unexpected token %d before char %d\n", t, p.spos)
			return nil
		}
	}
	return p.runcmd(cmd, args)
}

func (p *evalstate) runcmd(cmd string, args []value) value {
	m := loadmodule(cmd)
	if m == nil {
		go discardall(nil, args)
		return nil
	}
	otype := m.types
	ok, opts, args := p.cvtargs(args, cmd, otype)
	if !ok {
		errorf("fs: eval: usage: %s\n", cmdusage(cmd, otype))
		go discardall(opts, args)
		return nil
	}
	r := m.run(p.report, opts, args)
	if r == nil {
		go discardall(opts, args)
	}
	return r
}

func (p *evalstate) cvtargs(args []value, cmd, otype string) (bool, []option, []value) {
	_, at, t := splittype(otype)
	ok, opts, args := p.cvtopts(t, cmd, args)
	if !ok {
		return false, opts, args
	}
	if len(at) < 1 || at[0] == '*' {
		errorf("fs: eval: invalid type descriptor %s for %s\n", quote.Quote(at), quote.Quote(cmd))
		return false, opts, args
	}
	n := len(args)
	ab := []byte(at)
	if ab[len(ab)-1] == '*' {
		tc := ab[len(ab)-2]
		ab = ab[:len(ab)-2]
		for i := len(ab); i < n; i++ {
			ab = append(ab, tc)
		}
	}
	if n != len(ab) {
		errorf("fs: eval: wrong number of arguments to %s\n", quote.Quote(cmd))
		return false, opts, args
	}
	ok, cargs, rest := p.cvtvalues(string(ab), cmd, args)
	if !ok {
		return false, opts, append(cargs, rest...)
	}
	return true, opts, cargs
}

func (p *evalstate) cvtvalues(t, cmd string, args []value) (bool, []value, []value) {
	var cargs []value
	for i := 0; i < len(t); i++ {
		tc := t[i]
		if len(args) == 0 {
			errorf("fs: eval: %s missing argument of type %s\n", quote.Quote(cmd), type2s(tc))
			return false, cargs, args
		}
		v := p.cvt(args[0], tc)
		if v == nil {
			errorf("fs: eval: %s: %s cannot be converted to %s\n", quote.Quote(cmd), type2s(args[0].typec()), type2s(tc))
			return false, cargs, args
		}
		cargs = append(cargs, v)
		args = args[1:]
	}
	return true, cargs, args
}

func (p *evalstate) cvtopts(opttype, cmd string, args []value) (bool, []option, []value) {
	if opttype == "" {
		return true, nil, args
	}
	var opts []option
getopts:
	for len(args) > 0 {
		s := ""
		if v, ok := args[0].(vS); ok {
			s = v.i
			if s == "" || s[0] != '-' || len(s) == 1 {
				s = ""
			} else if s == "--" {
				args = args[1:]
				s = ""
			}
		}
		if s == "" {
			return true, opts, args
		}
		s = s[1:]
		for len(s) > 0 {
			opt := s[0]
			t, ok := opttypes(opt, opttype)
			if !ok {
				errorf("fs: eval: %s: unknown option -%c\n", cmd, opt)
				return false, opts, args
			}
			if t == "" {
				s = s[1:]
				opts = append([]option{{opt, nil}}, opts...)
				continue
			}
			if len(s) > 1 {
				args = append([]value{vS{s[1:]}}, args[1:]...)
			} else {
				args = args[1:]
			}
			ok, vl, rest := p.cvtvalues(t, cmd, args)
			if !ok {
				return false, opts, append(vl, rest...)
			}
			args = rest
			opts = append([]option{{opt, vl}}, opts...)
			continue getopts
		}
		args = args[1:]
	}
	return true, opts, args
}

// fsmod is a verb: its type and what runs it.
type fsmod struct {
	types string
	run   func(r *Report, opts []option, args []value) value
}

var mods map[string]*fsmod

func loadmodule(cmd string) *fsmod {
	if m := mods[cmd]; m != nil {
		return m
	}
	p := cmd + ".dis"
	if p[0] != '/' && (len(p) < 2 || p[:2] != "./") {
		p = "/dis/fs/" + p
	}
	errorf("fs: eval: cannot load %s: file does not exist\n", p)
	errorf("fs: eval: unknown verb %s\n", quote.Quote(cmd))
	return nil
}

func (p *evalstate) runexternal(cmd, t string, opts []option, args []value) value {
	m := loadmodule(cmd)
	if m == nil {
		return nil
	}
	if !typecompat(t, m.types) {
		errorf("fs: eval: %s has incompatible type\n", cmd)
		errorf("fs: eval: expected usage: %s\n", cmdusage(cmd, t))
		errorf("fs: eval: actually usage: %s\n", cmdusage(cmd, m.types))
		return nil
	}
	return m.run(p.report, opts, args)
}

type cvterr struct{}

func (p *evalstate) cvt(v value, t byte) (r value) {
	defer func() {
		if x := recover(); x != nil {
			if _, ok := x.(cvterr); !ok {
				panic(x)
			}
			r = nil
		}
	}()
	return p.cvt1(v, t)
}

func (p *evalstate) cvt1(v value, t byte) value {
	if v.typec() == t {
		return v
	}
	var r value
	switch t {
	case 't':
		r = p.runexternal("entries", "tx", nil, []value{p.cvt1(v, 'x')})
	case 'x':
		r = p.runexternal("walk", "xs", nil, []value{p.cvt1(v, 's')})
	case 'p':
		r = p.runexternal("match", "ps", nil, []value{p.cvt1(v, 's')})
	case 's':
		r = p.runexternal("run", "sc", nil, []value{p.cvt1(v, 'c')})
	case 'v':
		r = p.runexternal("print", "vt", nil, []value{p.cvt1(v, 't')})
	}
	if r == nil {
		panic(cvterr{})
	}
	return r
}

func (p *evalstate) getc() rune {
	c := rune(-1)
	if p.spos < len(p.s) {
		c = p.s[p.spos]
	}
	p.spos++
	return c
}

func (p *evalstate) ungetc() { p.spos-- }

func in(c rune, s string) bool {
	for _, r := range s {
		if r == c {
			return true
		}
	}
	return false
}

func (p *evalstate) gettok() (int, string) {
	c := p.getc()
	for c == ' ' || c == '\t' {
		c = p.getc()
	}
	var s []rune
	switch c {
	case -1:
		return tEOF, ""
	case '\n', '{', '}':
		return int(c), ""
	case '@':
		nc := p.getc()
		for nc == ' ' || nc == '\t' {
			nc = p.getc()
		}
		if nc != '{' {
			errorf("fs: eval: expected '{' after '@'\n")
			panic(parseerr{})
		}
		s = []rune("{")
		d := 1
	getcmd:
		for {
			nc = p.getc()
			if nc == -1 {
				break
			}
			s = append(s, nc)
			switch nc {
			case '{':
				d++
			case '}':
				d--
				if d == 0 {
					break getcmd
				}
			case '\'':
				s = append(s, []rune(p.getqword(true))...)
			}
		}
		if nc == -1 {
			errorf("fs: eval: unbalanced '{' in shell command\n")
			panic(parseerr{})
		}
		return tSHCMD, string(s)
	case '$':
		return tVAR, p.getvar()
	case '\'':
		return tWORD, p.getqword(false)
	}
	for {
		s = append(s, c)
		c = p.getc()
		if in(c, " \t{}\n") {
			p.ungetc()
			break
		}
		if c < 0 {
			break
		}
	}
	return tWORD, string(s)
}

func (p *evalstate) getvar() string {
	c := p.getc()
	if c == -1 {
		errorf("fs: eval: unexpected eof after '$'\n")
		panic(parseerr{})
	}
	var v []rune
	for !in(c, " \t\n@{}'") && c != -1 {
		v = append(v, c)
		c = p.getc()
	}
	p.ungetc()
	i := 0
	for ; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			break
		}
	}
	if i < len(v) || len(v) == 0 {
		errorf("fs: eval: invalid $ reference $%s\n", quote.Quote(string(v)))
		panic(parseerr{})
	}
	return string(v)
}

// getqword gets a quoted word whose opening quote has been read; keepq
// keeps its quotes.
func (p *evalstate) getqword(keepq bool) string {
	var s []rune
	for {
		nc := p.getc()
		for nc != '\'' && nc >= 0 {
			s = append(s, nc)
			nc = p.getc()
		}
		if nc == -1 {
			errorf("fs: eval: unterminated quote\n")
			panic(parseerr{})
		}
		if p.getc() != '\'' {
			p.ungetc()
			if keepq {
				s = append(s, '\'')
			}
			return string(s)
		}
		s = append(s, '\'')
		if keepq {
			s = append(s, '\'')
		}
	}
}
