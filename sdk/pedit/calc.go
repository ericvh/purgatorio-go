package pedit

import (
	"strconv"
)

// Parseexpr evaluates a partition editor expression: numbers, "." for
// dot, "$" for dollar, + - * / with the usual precedence, parentheses,
// unary minus, and a postfix % for that percentage of size (Inferno's
// calc.y).
func Parseexpr(s string, dot, dollar, size int64) (v int64, err string) {
	p := &calc{s: s, dot: dot, dollar: dollar, size: size}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(calcError); ok {
				v, err = 0, string(e)
				return
			}
			panic(r)
		}
	}()
	p.next()
	v = p.expr()
	if p.tok != eof {
		panic(calcError("syntax error"))
	}
	return v, ""
}

type calcError string

const (
	eof    = -1
	number = -2
)

type calc struct {
	s                 string
	n                 int
	tok               int
	val               int64
	dot, dollar, size int64
}

func isspace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func (p *calc) next() {
	for p.n < len(p.s) && isspace(p.s[p.n]) {
		p.n++
	}
	if p.n == len(p.s) {
		p.tok = eof
		return
	}
	if c := p.s[p.n]; c >= '0' && c <= '9' {
		o := p.n
		for o < len(p.s) && p.s[o] >= '0' && p.s[o] <= '9' {
			o++
		}
		p.val, _ = strconv.ParseInt(p.s[p.n:o], 10, 64)
		p.n = o
		p.tok = number
		return
	}
	p.tok = int(p.s[p.n])
	p.n++
}

func (p *calc) expr() int64 {
	v := p.term()
	for p.tok == '+' || p.tok == '-' {
		op := p.tok
		p.next()
		w := p.term()
		if op == '+' {
			v += w
		} else {
			v -= w
		}
	}
	return v
}

func (p *calc) term() int64 {
	v := p.postfix()
	for p.tok == '*' || p.tok == '/' {
		op := p.tok
		p.next()
		w := p.postfix()
		if op == '*' {
			v *= w
		} else {
			if w == 0 {
				panic(calcError("division by zero"))
			}
			v /= w
		}
	}
	return v
}

func (p *calc) postfix() int64 {
	v := p.prefix()
	for p.tok == '%' {
		p.next()
		v = p.size * v / 100
	}
	return v
}

func (p *calc) prefix() int64 {
	if p.tok == '-' {
		p.next()
		return -p.prefix()
	}
	return p.primary()
}

func (p *calc) primary() int64 {
	switch p.tok {
	case number:
		v := p.val
		p.next()
		return v
	case '.':
		p.next()
		return p.dot
	case '$':
		p.next()
		return p.dollar
	case '(':
		p.next()
		v := p.expr()
		if p.tok != ')' {
			panic(calcError("syntax error"))
		}
		p.next()
		return v
	}
	panic(calcError("syntax error"))
}
