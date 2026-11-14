package main

import "strconv"

// The parser is a recursive-descent form of sh.y's yacc grammar:
//
//	shell:	line end | error end
//	line:	or2 | cmdsa line
//	body:	or2 | cmdsan body
//	cmdsa:	or2 ';' | or2 '&'
//	cmdsan:	cmdsa | or2 '\n'
//	or2:	and2 | or2 OROR and2
//	and2:	pipe | and2 ANDAND pipe
//	pipe:	(empty) | cmd | pipe '|' optnl cmd
//	cmd:	simple | redir cmd | redir | assign
//	assign:	word '=' assign | word '=' simple | word '='
//	redir:	DUP | REDIR word
//	simple:	word | simple word | simple redir
//	list:	optnl | nlsimple optnl
//	nlsimple: optnl word | nlsimple optnl word | nlsimple optnl redir
//	word:	comword | word '^' optnl comword
//	comword: WORD | OP comword | '(' list ')' | '{' body '}'

type parser struct {
	l      *lexer
	have   bool
	tok    int
	word   string
	optype int
	redir  *Redir
}

type syntaxError struct{}

func (p *parser) peek() int {
	if !p.have {
		p.tok = p.l.lex()
		p.word, p.optype, p.redir = p.l.word, p.l.optype, p.l.redir
		p.have = true
	}
	return p.tok
}

func (p *parser) next() int {
	t := p.peek()
	p.have = false
	return t
}

func (p *parser) fail() {
	panic(syntaxError{})
}

func (p *parser) expect(t int) {
	if p.next() != t {
		p.fail()
	}
}

func (p *parser) optnl() {
	for p.peek() == '\n' {
		p.next()
	}
}

// shell parses one command line, up to and including its end.
func (p *parser) shell() (n *Node) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(syntaxError); !ok {
				panic(r)
			}
			p.l.error("syntax error")
			// Error recovery: discard tokens up to the end of the line.
			for {
				t := p.peek()
				p.have = false
				if t == tEND || t == '\n' {
					break
				}
			}
			n = nil
		}
	}()
	n = p.line(false)
	if t := p.peek(); t != tEND && t != '\n' {
		p.fail()
	}
	p.next()
	return n
}

// line parses a sequence of commands; in a body, newlines separate
// commands too.
func (p *parser) line(body bool) *Node {
	var cmds []*Node
	for {
		n := p.or2()
		switch t := p.peek(); {
		case t == ';':
			p.next()
			cmds = append(cmds, n)
			continue
		case t == '&':
			p.next()
			cmds = append(cmds, &Node{typ: nNOWAIT, left: n})
			continue
		case t == '\n' && body:
			p.next()
			cmds = append(cmds, n)
			continue
		}
		cmds = append(cmds, n)
		break
	}
	r := cmds[len(cmds)-1]
	for i := len(cmds) - 2; i >= 0; i-- {
		r = mkseq(cmds[i], r)
	}
	return r
}

func (p *parser) or2() *Node {
	left := p.and2()
	for p.peek() == tOROR {
		p.next()
		right := p.and2()
		left = mk(nADJ, mk(nADJ, &Node{typ: nWORD, word: "or"}, mk(nBLOCK, left, nil)), mk(nBLOCK, right, nil))
	}
	return left
}

func (p *parser) and2() *Node {
	left := p.pipe()
	for p.peek() == tANDAND {
		p.next()
		right := p.pipe()
		left = mk(nADJ, mk(nADJ, &Node{typ: nWORD, word: "and"}, mk(nBLOCK, left, nil)), mk(nBLOCK, right, nil))
	}
	return left
}

func (p *parser) pipe() *Node {
	left := p.cmd()
	for p.peek() == '|' {
		r := p.redir
		p.next()
		p.optnl()
		right := p.cmd()
		if right == nil {
			p.fail()
		}
		left = &Node{typ: nPIPE, left: left, right: right, redir: r}
	}
	return left
}

func (p *parser) wordstart() bool {
	switch p.peek() {
	case tWORD, tOP, '(', '{':
		return true
	}
	return false
}

func (p *parser) redirstart() bool {
	t := p.peek()
	return t == tDUP || t == tREDIR
}

// cmd returns nil when no command starts here.
func (p *parser) cmd() *Node {
	if p.redirstart() {
		r := p.parseRedir()
		if p.redirstart() || p.wordstart() {
			return mk(nADJ, r, p.cmd())
		}
		return r
	}
	if !p.wordstart() {
		return nil
	}
	w := p.parseWord()
	if p.peek() == '=' {
		return p.assign(w)
	}
	return p.simple(w)
}

// assign parses the rest of an assignment whose name is w.
func (p *parser) assign(w *Node) *Node {
	op := p.optype
	p.next()
	if !p.wordstart() {
		return mk(op, w, nil)
	}
	w2 := p.parseWord()
	if p.peek() == '=' {
		return mk(op, w, p.assign(w2))
	}
	return mk(op, w, p.simple(w2))
}

func (p *parser) simple(left *Node) *Node {
	for {
		switch {
		case p.wordstart():
			left = mk(nADJ, left, p.parseWord())
		case p.redirstart():
			left = mk(nADJ, left, p.parseRedir())
		default:
			return left
		}
	}
}

func (p *parser) parseRedir() *Node {
	r := p.redir
	if p.next() == tDUP {
		return &Node{typ: nDUP, redir: r}
	}
	if !p.wordstart() {
		p.fail()
	}
	return &Node{typ: nREDIR, left: p.parseWord(), redir: r}
}

func (p *parser) parseWord() *Node {
	w := p.comword()
	for p.peek() == '^' {
		p.next()
		p.optnl()
		w = mk(nCONCAT, w, p.comword())
	}
	return w
}

func (p *parser) comword() *Node {
	switch p.peek() {
	case tWORD:
		w := p.word
		p.next()
		return &Node{typ: nWORD, word: w}
	case tOP:
		op := p.optype
		p.next()
		return mk(op, p.comword(), nil)
	case '(':
		p.next()
		l := p.list()
		p.expect(')')
		return mk(nLIST, l, nil)
	case '{':
		p.next()
		b := p.line(true)
		p.expect('}')
		return mk(nBLOCK, b, nil)
	}
	p.fail()
	return nil
}

func (p *parser) list() *Node {
	p.optnl()
	if !p.wordstart() {
		return nil
	}
	left := p.parseWord()
	for {
		p.optnl()
		switch {
		case p.wordstart():
			left = mk(nADJ, left, p.parseWord())
		case p.redirstart():
			left = mk(nADJ, left, p.parseRedir())
		default:
			return left
		}
	}
}

// doparse parses one command line from l. The error names the file, and
// the line when showline is set.
func doparse(l *lexer, prompt string, showline bool) (*Node, string) {
	l.prompt = prompt
	l.err = ""
	p := &parser{l: l}
	n := p.shell()
	l.lastnl = false
	if l.err != "" {
		if l.errline > 0 && showline {
			return nil, l.path + ":" + strconv.Itoa(l.errline) + ": " + l.err
		}
		return nil, l.path + ": parse error: " + l.err
	}
	return n, ""
}

// parse parses s, as Sh->parse does.
func parse(s string) (*Node, string) {
	return doparse(newStringLexer(s), "", false)
}
