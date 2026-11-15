package main

import (
	"io"
	"strconv"
	"strings"
)

// xeqTop runs a command the parser has read.
func (e *Env) xeqTop(c *Cmd) {
	mark := len(genfds)
	defer closegen(mark)
	c.xeq(e)
}

// xeq runs a command, dumping it first with -d.
func (c *Cmd) xeq(e *Env) {
	if e.flags&EDumping != 0 {
		s := c.text()
		if s != "" && s[len(s)-1] != '&' {
			s += ";"
		}
		e.output(s + "\n")
	}
	if e.flags&ENoxeq == 0 {
		c.xeqit(e, true)
	}
}

// xeqit runs a command; wait says whether to wait for a simple command.
func (c *Cmd) xeqit(e *Env, wait bool) {
	for c != nil {
		switch c.op {
		case Csimple:
			c.simple(e, wait)
		case Casync:
			e.async(c.left)
		case Cgroup:
			if len(c.redirs) == 0 {
				c = c.left
				continue
			}
			in, out, owned, ok := mkredirs(e, c.redirs)
			if !ok {
				return
			}
			n := e.copy()
			n.in, n.out = in, out
			func() {
				defer closefds(owned)
				c.left.xeqit(n, true)
			}()
		case Csubgroup:
			n := e.clone()
			var owned []int
			if len(c.redirs) != 0 {
				in, out, o, ok := mkredirs(n, c.redirs)
				if !ok {
					return
				}
				n.in, n.out, owned = in, out, o
			}
			func() {
				defer closefds(owned)
				c.left.xeqit(n, wait)
			}()
		case Cseq:
			c.left.xeqit(e, true)
			c = c.right
			continue
		case Cpipe:
			fds := e.pipe()
			if fds == nil {
				return
			}
			n := e.clone()
			n.out = fds[0]
			func() {
				defer closefd(fds[0])
				c.left.xeqit(n, false)
			}()
			n = e.clone()
			n.in = fds[1]
			func() {
				defer closefd(fds[1])
				c.right.xeqit(n, wait)
			}()
		case Cif:
			t := c.left.truth(e)
			if c.right.op == Celse {
				if t {
					c.right.left.xeqit(e, wait)
				} else {
					c.right.right.xeqit(e, wait)
				}
			} else if t {
				c.right.xeqit(e, wait)
			}
		case Celse:
			panic("mash panic: unexpected else")
		case Cwhile:
			for c.left.truth(e) {
				c.right.xeqit(e, wait)
			}
		case Cfor:
			l, ok := evalw(c.words, e)
			if !ok || c.item == nil {
				return
			}
			s := c.item.word.text
			for _, v := range l {
				e.let(s, []string{v})
				c.left.xeqit(e, true)
			}
		case Ccase:
			s1, l1 := c.left.eeval(e)
			var arm *Cmd
			for r := c.right; r != nil; r = r.right {
				l := r.left
				s2, l2 := l.left.eeval(e)
				if match2(s1, l1, s2, l2) {
					arm = l
					break
				}
			}
			if arm == nil {
				return
			}
			c = arm.right
			continue
		case Ceq:
			c.assign(e, false)
		case Cdefeq:
			c.assign(e, true)
		case Cfn:
			s, _, _ := c.item.ieval(e)
			if !ident(s) {
				e.report("bad function name")
				return
			}
			e.define(s, c.left)
		case Crescue:
			e.report("rescue not implemented")
		case Cdepend:
			c.depend(e)
		case Crule:
			c.rule(e)
		default:
			fprint(1, "number "+strconv.Itoa(c.op)+"\n")
		}
		return
	}
}

// quote runs a `{} (back) or "{} generator and returns its item.
func (c *Cmd) quote(e *Env, back bool) *Item {
	e = e.copy()
	fds := e.pipe()
	if fds == nil {
		return nil
	}
	e.out = fds[0]
	func() {
		defer closefd(fds[0])
		c.xeqit(e, false)
	}()
	b, _ := io.ReadAll(fdreader(fds[1]))
	closefd(fds[1])
	s := string(b)
	if back {
		return iteml(strings.FieldsFunc(s, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\r' || r == '\n'
		}))
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return itemw(s)
}

// serve runs a <{} or >{} generator: the command gets one end of a
// pipe and the word is a name for the other, /fd/n, which a command
// run for this simple command is given at the same fd.
func (c *Cmd) serve(e *Env, write bool) *Item {
	e = e.clone()
	fds := e.pipe()
	if fds == nil {
		return nil
	}
	if write {
		e.in = fds[0]
	} else {
		e.out = fds[0]
	}
	func() {
		defer closefd(fds[0])
		c.xeqit(e, false)
	}()
	// Added after the generator starts, which must not hold its own
	// pipe's other end.
	genfds = append(genfds, fds[1])
	return itemw("/fd/" + strconv.Itoa(fds[1]))
}

// eeval1 is the first pass of evaluating an expression: words are
// expanded and generators run. nil is an error.
func (c *Cmd) eeval1(e *Env) *Cmd {
	switch c.op {
	case Cword:
		l := c.item.ieval1(e)
		if l == nil {
			return nil
		}
		return cmd1i(Cword, nil, l)
	case Chd, Ctl, Clen, Cnot:
		l := c.left.eeval1(e)
		if l == nil {
			return nil
		}
		return cmd1(c.op, l)
	case Ccaret, Ccons, Ceqeq, Cnoteq, Cmatch:
		l := c.left.eeval1(e)
		r := c.right.eeval1(e)
		if l == nil || r == nil {
			return nil
		}
		return cmd2(c.op, l, r)
	}
	panic("mash panic: expr1: bad op")
}

const vtrue = "true"

// eeval2 is the second pass.
func (c *Cmd) eeval2(e *Env) (string, []string, int) {
	switch c.op {
	case Cword:
		return c.item.ieval2(e)
	case Clist:
		return "", c.value, 0
	case Ccaret:
		s1, l1, x1 := c.left.eeval2(e)
		s2, l2, x2 := c.right.eeval2(e)
		return caret(s1, l1, x1, s2, l2, x2)
	case Chd:
		s, l, x := c.left.eeval2(e)
		if s != "" {
			return s, nil, x
		}
		if l != nil {
			return l[0], nil, 0
		}
	case Ctl:
		s, l, _ := c.left.eeval2(e)
		if s != "" {
			break
		}
		if len(l) > 1 {
			return "", l[1:], 0
		}
	case Clen:
		s, l, _ := c.left.eeval2(e)
		if s != "" {
			return "1", nil, 0
		}
		return strconv.Itoa(len(l)), nil, 0
	case Cnot:
		s, l, _ := c.left.eeval2(e)
		if s == "" && l == nil {
			return vtrue, nil, 0
		}
	case Ccons:
		s1, l1, _ := c.left.eeval2(e)
		s2, l2, _ := c.right.eeval2(e)
		switch {
		case s1 != "":
			if s2 != "" {
				return "", []string{s1, s2}, 0
			}
			if l2 != nil {
				return "", append([]string{s1}, l2...), 0
			}
			return s1, nil, 0
		case l1 != nil:
			if s2 != "" {
				return "", append(append([]string(nil), l1...), s2), 0
			}
			if l2 != nil {
				return "", append(append([]string(nil), l1...), l2...), 0
			}
			return "", l1, 0
		default:
			return s2, l2, 0
		}
	case Ceqeq:
		if c.evaleq(e) {
			return vtrue, nil, 0
		}
	case Cnoteq:
		if !c.evaleq(e) {
			return vtrue, nil, 0
		}
	case Cmatch:
		if c.evalmatch(e) {
			return vtrue, nil, 0
		}
	default:
		panic("mash panic: expr2: bad op")
	}
	return "", nil, 0
}

// eeval evaluates an expression, globbing a pattern.
func (c *Cmd) eeval(e *Env) (string, []string) {
	c = c.eeval1(e)
	if c == nil {
		return "", nil
	}
	s, l, x := c.eeval2(e)
	if x != 0 && s != "" {
		s, l = glob(s)
	}
	return s, l
}

func (c *Cmd) assign(e *Env, def bool) {
	if c.item == nil {
		return
	}
	v, ok := evalw(c.words, e)
	if !ok {
		return
	}
	s := c.item.word.text
	if def {
		e.let(s, v)
	} else {
		e.set(s, v)
	}
}

// truth evaluates an expression: true unless null.
func (c *Cmd) truth(e *Env) bool {
	s, l := c.eeval(e)
	return s != "" || l != nil
}

func evalw(l []*Item, e *Env) ([]string, bool) {
	if len(l) == 0 {
		return nil, true
	}
	w := pass1(e, l)
	if w == nil {
		return nil, false
	}
	return pass2(e, w), true
}

// pass1 runs the first pass over a list of items; nil is an error.
func pass1(e *Env, l []*Item) []*Item {
	var r []*Item
	for _, i := range l {
		x := i.ieval1(e)
		if x == nil {
			return nil
		}
		r = append(r, x)
	}
	return r
}

// pass2 runs the second pass, with globbing.
func pass2(e *Env, l []*Item) []string {
	var r []string
	for _, i := range l {
		s, t, x := i.ieval2(e)
		if x != 0 && s != "" {
			s, t = glob(s)
		}
		if s != "" {
			r = append(r, s)
		} else {
			r = append(r, t...)
		}
	}
	return r
}

// simple runs a simple command, which may be a function.
func (c *Cmd) simple(e *Env, wait bool) {
	mark := len(genfds)
	defer closegen(mark)
	w := pass1(e, c.words)
	if w == nil {
		return
	}
	s := pass2(e, w)
	if s == nil {
		return
	}
	if e.flags&EEcho != 0 {
		echo(e, s)
	}
	in, out, owned, ok := mkredirs(e, c.redirs)
	if !ok {
		return
	}
	defer closefds(owned)
	e.runit(s, in, out, wait)
}

const maxelev = 256

// runit runs a function, builtin or command.
func (e *Env) runit(s []string, in, out int, wait bool) {
	d := e.fn(s[0])
	if d == nil {
		exec(s, e, in, out, wait)
		return
	}
	if e.level >= maxelev {
		e.report(s[0] + ": function nesting too deep")
		return
	}
	e = e.copy()
	e.level++
	// -1 is the fds of the thread running it, which are e's.
	e.in = orfd(in, e.in)
	e.out = orfd(out, e.out)
	e.local = newStab()
	e.local.assign("args", s[1:])
	d.xeqit(e, wait)
}

// ieval1 is the first pass over an item: variables are expanded,
// expressions get their first pass and generators run.
func (i *Item) ieval1(e *Env) *Item {
	if i == nil {
		return nil
	}
	switch i.op {
	case Icaret, Iicaret:
		l := i.left.ieval1(e)
		r := i.right.ieval1(e)
		if l == nil || r == nil {
			return nil
		}
		return item2(i.op, l, r)
	case Idollar, Idollarq:
		s := e.dollar(i.word.text)
		if s == nil {
			e.undefined(i.word.text)
			return nil
		}
		if s.empty {
			return itemw("")
		}
		if i.op == Idollar {
			return iteml(s.value)
		}
		return itemw(concat(s.value))
	case Iword, Imatch:
		return i
	case Iexpr:
		l := i.cmd.eeval1(e)
		if l == nil {
			return nil
		}
		return itemc(Iexpr, l)
	case Ibackq:
		return i.cmd.quote(e, true)
	case Iquote:
		return i.cmd.quote(e, false)
	case Iinpipe:
		return i.cmd.serve(e, false)
	case Ioutpipe:
		return i.cmd.serve(e, true)
	}
	panic("mash panic: ieval1: bad op")
}

// ieval2 is the second pass: carets and $n.
func (i *Item) ieval2(e *Env) (string, []string, int) {
	switch i.op {
	case Icaret, Iicaret:
		return i.caret(e)
	case Imatch:
		return e.arg(i.word.text), nil, 0
	case Iword:
		return i.word.text, nil, i.word.flags & Wexpand
	case Iexpr:
		return i.cmd.eeval2(e)
	}
	panic("mash panic: ieval2: bad op")
}

func (i *Item) ieval(e *Env) (string, []string, int) {
	i = i.ieval1(e)
	if i == nil {
		return "", nil, 0
	}
	return i.ieval2(e)
}

// reval evaluates a redirection's word, which must be one string.
func (i *Item) reval(e *Env) (string, bool) {
	s, l, _ := i.ieval(e)
	if s == "" {
		if l == nil {
			e.report("null redirect")
		} else {
			e.report("list for redirect")
		}
		return "", false
	}
	return s, true
}

// mkredirs opens a command's redirections. It returns the fds to use
// and those it opened, for the caller to close.
func mkredirs(e *Env, l []*Redir) (in, out int, owned []int, ok bool) {
	var f [Rcount]string
	for _, r := range l {
		s, ok := r.word.reval(e)
		if !ok {
			return -1, -1, nil, false
		}
		f[r.op] = s
	}
	return redirect(e, f, e.in, e.out)
}
