package main

// The parser: recursive descent for the grammar of Inferno's mash.y,
// with its precedences, and its error recovery: a syntax error is
// reported unless another came within the last three tokens, the rest
// of the line is discarded, and so is the token that caused it. Top
// level commands run as soon as their terminator is read.

type synErr struct{}

type parser struct {
	e       *Env
	have    bool
	tok     int
	item    *Item
	errflag int
	run1    func(*Cmd) // runs a top level command; nil for xeqTop
}

// parse reads and runs commands from e's source until its end.
func parse(e *Env) {
	p := &parser{e: e}
	p.run()
}

func (p *parser) peek() int {
	if !p.have {
		p.tok, p.item = p.e.lex()
		p.have = true
	}
	return p.tok
}

// next consumes the current token, which is a shift for error recovery.
func (p *parser) next() (int, *Item) {
	p.peek()
	p.have = false
	if p.errflag > 0 {
		p.errflag--
	}
	return p.tok, p.item
}

func (p *parser) drop() { p.peek(); p.have = false }

func (p *parser) fail() { panic(synErr{}) }

func (p *parser) expect(t int) *Item {
	if p.peek() != t {
		p.fail()
	}
	_, i := p.next()
	return i
}

// startscmd reports whether t can start a command.
func startscmd(t int) bool {
	switch t {
	case Lword, Lonparen, Loncurly, Lat, Lfor, Lif, Lwhile, Lcase, Lfn, Lrescue:
		return true
	}
	return false
}

func isterm1(t int) bool { return t == Lsemi || t == Leof || t == Land }

func (p *parser) run() {
	for {
		t := p.peek()
		if t == tokEOF {
			return
		}
		if t == Leof {
			p.next()
			continue
		}
		if p.errflag > 0 && !startscmd(t) {
			p.drop()
			p.errflag = 3
			continue
		}
		c, ok := p.cmda()
		if !ok {
			p.recover()
			continue
		}
		if p.run1 != nil {
			p.run1(c)
		} else {
			p.e.xeqTop(c)
		}
	}
}

// recover handles a syntax error at the current token.
func (p *parser) recover() {
	if p.errflag == 0 {
		p.e.report("syntax error")
		p.e.suck()
	}
	p.errflag = 3
	if p.peek() == tokEOF {
		return
	}
	p.drop()
}

// cmda parses cmd term, catching a syntax error.
func (p *parser) cmda() (c *Cmd, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, is := r.(synErr); !is {
				panic(r)
			}
			c, ok = nil, false
		}
	}()
	x := p.cmd()
	t := p.peek()
	if !isterm1(t) {
		p.fail()
	}
	p.next()
	return x.mkcmd(p.e, t == Land), true
}

// cmd: simple | complex | cmd '|' cmd.
func (p *parser) cmd() *Cmd {
	c := p.unit()
	for p.peek() == Lpipe {
		p.next()
		c = cmd2(Cpipe, c, p.unit())
	}
	return c
}

// cmds: commands, each ended by a terminator but perhaps the last.
func (p *parser) cmds() *Cmd {
	var c *Cmd
	for startscmd(p.peek()) {
		x := p.cmd()
		t := p.peek()
		if !isterm1(t) {
			return cmd2(Cseq, c, x.mkcmd(p.e, false))
		}
		p.next()
		c = cmd2(Cseq, c, x.mkcmd(p.e, t == Land))
	}
	return c
}

func (p *parser) braced() *Cmd {
	p.expect(Loncurly)
	c := p.cmds()
	p.expect(Loffcurly)
	return c
}

func (p *parser) unit() *Cmd {
	switch p.peek() {
	case Loncurly:
		body := p.braced()
		return p.epilog().cmde(Cgroup, body, nil)
	case Lat:
		p.next()
		body := p.braced()
		return p.epilog().cmde(Csubgroup, body, nil)
	case Lfor:
		p.next()
		p.expect(Lonparen)
		w := p.expect(Lword).sword(p.e)
		p.expect(Lin)
		l := p.list()
		p.expect(Loffparen)
		c := cmd1i(Cfor, p.cmd(), w)
		c.words = l
		return c
	case Lif:
		p.next()
		p.expect(Lonparen)
		x := p.expr(0)
		p.expect(Loffparen)
		body := p.cmd()
		if p.peek() == Lelse {
			p.next()
			return cmd2(Cif, x, cmd2(Celse, body, p.cmd()))
		}
		return cmd2(Cif, x, body)
	case Lwhile:
		p.next()
		p.expect(Lonparen)
		x := p.expr(0)
		p.expect(Loffparen)
		return cmd2(Cwhile, x, p.cmd())
	case Lcase:
		p.next()
		x := p.expr(0)
		p.expect(Loncurly)
		var cases *Cmd
		for p.peek() != Loffcurly {
			m := p.expr(0)
			p.expect(Lmatched)
			c := p.cmd()
			t := p.peek()
			if !isterm1(t) {
				p.fail()
			}
			p.next()
			cases = cmd2(Ccases, cases, cmd2(Cmatched, m, c.mkcmd(p.e, t == Land)))
		}
		p.next()
		return cmd2(Ccase, x, cases.rotcases())
	case Lfn, Lrescue:
		t, _ := p.next()
		w := p.word()
		var body *Cmd
		if p.peek() == Loncurly {
			body = p.braced()
		}
		op := Cfn
		if t == Lrescue {
			op = Crescue
		}
		return cmd1i(op, body, w)
	case Lword:
		_, i := p.next()
		if t := p.peek(); t == Leq || t == Ldefeq {
			p.next()
			w := i.sword(p.e)
			op := Ceq
			if t == Ldefeq {
				op = Cdefeq
			}
			return cmdiw(op, w, p.list())
		}
		return p.simple(p.carets(i))
	case Lonparen:
		return p.simple(p.word())
	}
	p.fail()
	return nil
}

// simple parses the rest of a simple command, a dependency or a rule
// that starts with the word w.
func (p *parser) simple(w *Item) *Cmd {
	if p.peek() == Lcolonmatch {
		p.next()
		rhs := p.word()
		cb := p.cbrace()
		cb.item = rhs
		return cmd1i(Crule, cb, w)
	}
	items := []*Item{w}
	for {
		i := p.item1(true)
		if i == nil {
			break
		}
		items = append(items, i)
	}
	if p.peek() == Lcolon {
		p.next()
		l := p.list()
		cb := &Cmd{op: Cnop}
		if t := p.peek(); t == Lcolon || t == Loncurly {
			cb = p.cbrace()
		}
		cb.words = p.e.mklist(l)
		return cmd1w(Cdepend, cb, p.e.mklist(items))
	}
	return p.e.mksimple(items)
}

func (p *parser) cbrace() *Cmd {
	switch p.peek() {
	case Lcolon:
		p.next()
		return cmd1(Clistgroup, p.braced())
	case Loncurly:
		return cmd1(Cgroup, p.braced())
	}
	p.fail()
	return nil
}

// item1 parses an item of a simple command (redir allows a
// redirection) or a list, or returns nil if none starts here.
func (p *parser) item1(redir bool) *Item {
	switch p.peek() {
	case Lword, Lonparen:
		return p.word()
	case Lbackq, Lquote:
		t, _ := p.next()
		op := Ibackq
		if t == Lquote {
			op = Iquote
		}
		return itemc(op, p.braced())
	case Lless, Lgreat:
		t, _ := p.next()
		if p.peek() == Loncurly || !redir {
			op := Iinpipe
			if t == Lgreat {
				op = Ioutpipe
			}
			return itemc(op, p.braced())
		}
		op := Rin
		if t == Lgreat {
			op = Rout
		}
		return itemr(op, p.word())
	case Lgreatgreat, Llessgreat:
		if !redir {
			return nil
		}
		t, _ := p.next()
		op := Rappend
		if t == Llessgreat {
			op = Rinout
		}
		return itemr(op, p.word())
	}
	return nil
}

// list: items with no redirections.
func (p *parser) list() []*Item {
	var l []*Item
	for {
		i := p.item1(false)
		if i == nil {
			return l
		}
		l = append(l, i)
	}
}

// epilog: redirections after a group.
func (p *parser) epilog() *Cmd {
	c := &Cmd{}
	for {
		var op int
		switch p.peek() {
		case Lless:
			op = Rin
		case Lgreat:
			op = Rout
		case Lgreatgreat:
			op = Rappend
		case Llessgreat:
			op = Rinout
		default:
			return c
		}
		p.next()
		c.cmdio(p.e, itemr(op, p.word()))
	}
}

// word: Lword | word '^' word | '(' expr ')'.
func (p *parser) word() *Item {
	var w *Item
	switch p.peek() {
	case Lword:
		_, w = p.next()
	case Lonparen:
		p.next()
		x := p.expr(0)
		p.expect(Loffparen)
		w = itemc(Iexpr, x)
	default:
		p.fail()
	}
	return p.carets(w)
}

func (p *parser) carets(w *Item) *Item {
	for p.peek() == Lcaret {
		p.next()
		var r *Item
		switch p.peek() {
		case Lword:
			_, r = p.next()
		case Lonparen:
			p.next()
			x := p.expr(0)
			p.expect(Loffparen)
			r = itemc(Iexpr, x)
		default:
			p.fail()
		}
		w = item2(Icaret, w, r)
	}
	return w
}

// Expression precedences: == != ~ lowest, then :: (right
// associative), then ^; the prefix operators bind tightest.
func binprec(t int) (int, bool) {
	switch t {
	case Leqeq, Lnoteq, Lmatch:
		return 1, false
	case Lcons:
		return 2, true
	case Lcaret:
		return 3, false
	}
	return -1, false
}

var binop = map[int]int{Leqeq: Ceqeq, Lnoteq: Cnoteq, Lmatch: Cmatch, Lcons: Ccons, Lcaret: Ccaret}

func (p *parser) expr(min int) *Cmd {
	l := p.unary()
	for {
		t := p.peek()
		prec, right := binprec(t)
		if prec < 0 || prec < min {
			return l
		}
		p.next()
		n := prec + 1
		if right {
			n = prec
		}
		l = cmd2(binop[t], l, p.expr(n))
	}
}

func (p *parser) unary() *Cmd {
	switch p.peek() {
	case Lhd, Ltl, Llen, Lnot:
		t, _ := p.next()
		op := map[int]int{Lhd: Chd, Ltl: Ctl, Llen: Clen, Lnot: Cnot}[t]
		return cmd1(op, p.unary())
	case Lword:
		_, i := p.next()
		return cmd1i(Cword, nil, i)
	case Lonparen:
		p.next()
		x := p.expr(0)
		p.expect(Loffparen)
		return x
	case Lbackq, Lquote, Lless, Lgreat:
		t, _ := p.next()
		op := map[int]int{Lbackq: Ibackq, Lquote: Iquote, Lless: Iinpipe, Lgreat: Ioutpipe}[t]
		return cmd1i(Cword, nil, itemc(op, p.braced()))
	}
	p.fail()
	return nil
}

// rotcases turns the parser's list of cases into the order they are
// tried in.
func (c *Cmd) rotcases() *Cmd {
	l := c
	c = nil
	for l != nil {
		t := l.right
		l.right = c
		c = l
		l = l.left
		c.left = t
	}
	return c
}
