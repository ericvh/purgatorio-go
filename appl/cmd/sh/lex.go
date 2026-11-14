package main

import (
	"bufio"
	"io"
)

// Tokens beyond single characters.
const (
	tDUP = 1000 + iota
	tREDIR
	tWORD
	tOP
	tEND
	tERROR
	tANDAND
	tOROR

	notoken = -1
	eof     = -1
)

// lexer is sh.y's YYLEX: it reads from a string or a file and turns the
// text into tokens, inserting free carets.
type lexer struct {
	err     string
	errline int
	path    string

	wasdollar bool
	atendword bool
	eof       bool

	s   []rune
	pos int

	f      *bufio.Reader
	hist   []rune
	nback  int
	prompt string
	lastnl bool
	// promptf prints the secondary prompt.
	promptf func(string)

	linenum int

	word   string
	optype int
	redir  *Redir
}

func newStringLexer(s string) *lexer {
	return &lexer{s: []rune(s), path: "internal"}
}

func newFileLexer(r io.Reader, path string, promptf func(string)) *lexer {
	return &lexer{f: bufio.NewReader(r), path: path, linenum: 1, promptf: promptf}
}

func (l *lexer) error(s string) {
	if l.err == "" {
		l.err = s
		l.errline = l.linenum
	}
}

func (l *lexer) getc() rune {
	if l.eof {
		return eof
	}
	if l.f == nil {
		if l.pos >= len(l.s) {
			l.eof = true
			return eof
		}
		c := l.s[l.pos]
		l.pos++
		return c
	}
	if l.nback > 0 {
		c := l.hist[len(l.hist)-l.nback]
		l.nback--
		return c
	}
	if l.lastnl && l.prompt != "" && l.promptf != nil {
		l.promptf(l.prompt)
	}
	c, _, err := l.f.ReadRune()
	if err != nil {
		l.eof = true
		c = eof
	} else if c == '\n' {
		l.linenum++
	}
	l.lastnl = c == '\n'
	l.hist = append(l.hist, c)
	if len(l.hist) > 8 {
		l.hist = append(l.hist[:0], l.hist[len(l.hist)-4:]...)
	}
	return c
}

func (l *lexer) ungetc() {
	if l.f == nil {
		l.pos--
		return
	}
	if l.nback < len(l.hist) {
		l.nback++
	}
}

// in reports whether c is in the class cl, as String->in does: a leading
// ^ negates and a-z is a range.
func in(c rune, cl string) bool {
	s := []rune(cl)
	n := len(s)
	if n == 0 {
		return false
	}
	ans, negate := false, false
	if s[0] == '^' {
		negate = true
		s = s[1:]
		n--
	}
	for i := 0; i < n; i++ {
		if s[i] == '-' && i > 0 && i < n-1 {
			if c >= s[i-1] && c <= s[i+1] {
				ans = true
				break
			}
			i++
		} else if c == s[i] {
			ans = true
			break
		}
	}
	if negate {
		ans = !ans
	}
	return ans
}

func (l *lexer) lex() int {
	endword := false
	wasdollar := false
	tok := notoken
	for tok == notoken {
		c := l.getc()
		switch c {
		case eof:
			tok = tEND
		case '\n':
			tok = '\n'
		case '\r', '\t', ' ':
		case '#':
			for c = l.getc(); c != '\n' && c != eof; c = l.getc() {
			}
			l.ungetc()
		case ';':
			tok = ';'
		case '&':
			if l.getc() == '&' {
				tok = tANDAND
			} else {
				l.ungetc()
				tok = '&'
			}
		case '^':
			tok = '^'
		case '{':
			tok = '{'
		case '}':
			tok = '}'
		case ')':
			tok = ')'
		case '(':
			tok = '('
		case '=':
			tok, l.optype = '=', nASSIGN
		case '$':
			if l.atendword {
				l.ungetc()
				tok = '^'
				break
			}
			switch l.getc() {
			case '#':
				l.optype = nCOUNT
			case '"':
				l.optype = nSQUASH
			default:
				l.ungetc()
				l.optype = nVAR
			}
			tok = tOP
			wasdollar = true
		case '"', '`':
			if l.atendword {
				tok = '^'
				l.ungetc()
				break
			}
			tok = tOP
			if c == '"' {
				l.optype = nBQ2
			} else {
				l.optype = nBQ
			}
		case '>', '<':
			var rtype int
			nc := l.getc()
			if nc == '>' {
				if c == '>' {
					rtype = oWRITE | oappend
				} else {
					rtype = oRDWR
				}
				nc = l.getc()
			} else if c == '>' {
				rtype = oWRITE
			} else {
				rtype = oREAD
			}
			tok = tREDIR
			if nc == '[' {
				tok, l.redir = l.readfdassign()
				if tok == tERROR {
					l.err, l.errline = "syntax error in redirection", l.linenum
				}
			} else {
				l.ungetc()
				l.redir = &Redir{-1, -1, -1}
			}
			if l.redir != nil {
				l.redir.rtype = rtype
			}
		case '|':
			tok = '|'
			l.redir = nil
			if c = l.getc(); c == '[' {
				tok, l.redir = l.readfdassign()
				if tok == tERROR {
					l.err, l.errline = "syntax error in pipe redirection", l.linenum
					return tok
				}
				tok = '|'
			} else if c == '|' {
				tok = tOROR
			} else {
				l.ungetc()
			}
		case '\'':
			if l.atendword {
				l.ungetc()
				tok = '^'
				break
			}
			startline := l.linenum
			var s []rune
			for {
				nc := l.getc()
				for ; nc != '\'' && nc != eof; nc = l.getc() {
					s = append(s, nc)
				}
				if nc == eof {
					l.err, l.errline = "unterminated string literal", startline
					return tERROR
				}
				if l.getc() != '\'' {
					l.ungetc()
					break
				}
				s = append(s, '\'')
			}
			l.word = string(s)
			tok = tWORD
			endword = true
		default:
			if c == ':' {
				if l.getc() == '=' {
					tok, l.optype = '=', nLOCAL
					break
				}
				l.ungetc()
			}
			if l.atendword {
				l.ungetc()
				tok = '^'
				break
			}
			allowed := "^\n \t\r|$'#<>;^(){}`&=\""
			if l.wasdollar {
				allowed = "a-zA-Z0-9*_"
			}
			var word []rune
		loop:
			for {
				switch c {
				case '*', '?', '[', globc:
					word = append(word, globc)
				case ':':
					nc := l.getc()
					l.ungetc()
					if nc == '=' {
						break loop
					}
				}
				word = append(word, c)
				if c = l.getc(); c == eof || !in(c, allowed) {
					break
				}
			}
			l.ungetc()
			l.word = string(word)
			tok = tWORD
			endword = true
		}
		l.atendword = endword
		l.wasdollar = wasdollar
	}
	return tok
}

// readnum reads a decimal number, or returns -1 if there is none.
func (l *lexer) readnum() int {
	sum, nc := 0, 0
	for {
		c := l.getc()
		if c < '0' || c > '9' {
			break
		}
		sum = sum*10 + int(c-'0')
		nc++
	}
	l.ungetc()
	if nc == 0 {
		return -1
	}
	return sum
}

// readfdassign reads the rest of [n] or [n=m]; the [ has been read.
func (l *lexer) readfdassign() (int, *Redir) {
	n1 := l.readnum()
	c := l.getc()
	if c != '=' {
		if c == ']' {
			return tREDIR, &Redir{-1, n1, -1}
		}
		return tERROR, nil
	}
	n2 := l.readnum()
	if l.getc() != ']' {
		return tERROR, nil
	}
	return tDUP, &Redir{-1, n1, n2}
}
