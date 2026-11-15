package main

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// Tokens.
const (
	tokEOF = -1
	Land   = iota + 1
	Lat
	Lbackq
	Lcaret
	Lcase
	Lcolon
	Lcolonmatch
	Lcons
	Ldefeq
	Lelse
	Leof
	Leq
	Leqeq
	Lerror
	Lfn
	Lfor
	Lgreat
	Lgreatgreat
	Lhd
	Lif
	Lin
	Llen
	Lless
	Llessgreat
	Lmatch
	Lmatched
	Lnot
	Lnoteq
	Loffcurly
	Loffparen
	Loncurly
	Lonparen
	Lpipe
	Lquote
	Lrescue
	Lsemi
	Ltl
	Lwhile
	Lword
)

var keywords = map[string]int{
	"case":   Lcase,
	"else":   Lelse,
	"fn":     Lfn,
	"for":    Lfor,
	"hd":     Lhd,
	"if":     Lif,
	"in":     Lin,
	"len":    Llen,
	"rescue": Lrescue,
	"tl":     Ltl,
	"while":  Lwhile,
}

// Character classes.
const (
	cALPHA = 1 << iota
	cNUMERIC
	cONE
	cWS
	cMETA
)

const nctype = 128

var ctype [nctype]int

func init() {
	for _, c := range "\x00 \t\n\r\v" {
		ctype[c] = cWS
	}
	for _, c := range ":#;&|^$=@~`{}()<>" {
		ctype[c] = cONE
	}
	for c := 'a'; c <= 'z'; c++ {
		ctype[c] = cALPHA
		ctype[c-'a'+'A'] = cALPHA
	}
	ctype['_'] = cALPHA
	for c := '0'; c <= '9'; c++ {
		ctype[c] = cNUMERIC
	}
	for _, c := range "*[]?" {
		ctype[c] = cMETA
	}
}

func isalpha(c rune) bool { return c >= nctype || (c >= 0 && ctype[c]&cALPHA != 0) }
func isalnum(c rune) bool { return c >= nctype || (c >= 0 && ctype[c]&(cALPHA|cNUMERIC) != 0) }
func isdigit(c rune) bool { return c >= 0 && c < nctype && ctype[c]&cNUMERIC != 0 }
func isquote(c rune) bool { return c < nctype && (c < 0 || ctype[c]&(cONE|cWS|cMETA) != 0) }
func isspace(c rune) bool { return c >= 0 && c < nctype && ctype[c]&cWS != 0 }
func isterm(c rune) bool  { return c < nctype && (c < 0 || ctype[c]&(cONE|cWS) != 0) }

// ident reports whether s is an identifier.
func ident(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if i == 0 && !isalpha(c) || !isalnum(c) {
			return false
		}
	}
	return true
}

// enquote quotes s for mash.
func enquote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, c := range s {
		if c == '\'' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	b.WriteByte('\'')
	return b.String()
}

// quote quotes s if it needs it.
func quote(s string) string {
	for _, c := range s {
		if isquote(c) {
			return enquote(s)
		}
	}
	return s
}

// File is a source of commands.
type File struct {
	in   *bufio.Reader
	name string
	line int
	err  error
}

func (f *File) getc() rune {
	c, _, err := f.in.ReadRune()
	if err != nil {
		if err != io.EOF {
			f.err = err
		}
		return -1
	}
	return c
}

func (f *File) ungetc() { _ = f.in.UnreadRune() }

// fopen attaches a source file to e.
func (e *Env) fopen(fd int, s string) {
	e.file = &File{in: bufio.NewReader(fdreader(fd)), name: s, line: 1}
}

// sopen attaches a source string to e.
func (e *Env) sopen(s string) {
	e.file = &File{in: bufio.NewReader(strings.NewReader(s)), name: "<string>", line: 1}
}

func fclose(e *Env) {
	if f := e.file; f != nil && f.err != nil {
		fprint(e.stderr, "error reading "+f.name+": "+f.err.Error()+"\n")
	}
	e.file = nil
}

// sword checks that i is a single identifier.
func (i *Item) sword(e *Env) *Item {
	if i.op == Iword && ident(i.word.text) {
		return i
	}
	e.report("malformed identifier: " + i.text())
	return nil
}

func where(e *Env) string {
	if e.flags&EInter != 0 || e.file == nil {
		return ""
	}
	return e.file.name + ":" + strconv.Itoa(e.file.line) + ": "
}

// suck discards the rest of the input line, after an error.
func (e *Env) suck() {
	if e.file == nil {
		return
	}
	for {
		c := e.file.getc()
		if c < 0 || c == '\n' {
			return
		}
	}
}

func keyval(i *Item) int {
	if i.op != Iword || i.word.flags&Wquoted != 0 {
		return -1
	}
	if v, ok := keywords[i.word.text]; ok {
		return v
	}
	return -1
}

// lex returns the next token, and its item for Lword.
func (e *Env) lex() (int, *Item) {
	for {
		if e.file == nil {
			return tokEOF, nil
		}
		f := e.file
		c := f.getc()
		for isspace(c) {
			if c == '\n' {
				f.line++
			}
			c = f.getc()
		}
		if c < 0 {
			fclose(e)
			return Leof, nil
		}
		switch c {
		case ':':
			d := f.getc()
			switch d {
			case ':':
				return Lcons, nil
			case '=':
				return Ldefeq, nil
			case '~':
				return Lcolonmatch, nil
			}
			if d >= 0 {
				f.ungetc()
			}
			return Lcolon, nil
		case '#':
			for {
				c = f.getc()
				if c < 0 {
					fclose(e)
					return Leof, nil
				}
				if c == '\n' {
					f.line++
					break
				}
			}
			continue
		case ';':
			return Lsemi, nil
		case '&':
			return Land, nil
		case '|':
			return Lpipe, nil
		case '^':
			return Lcaret, nil
		case '@':
			return Lat, nil
		case '!':
			d := f.getc()
			if d == '=' {
				return Lnoteq, nil
			}
			if d >= 0 {
				f.ungetc()
			}
			return Lnot, nil
		case '~':
			return Lmatch, nil
		case '=':
			d := f.getc()
			switch d {
			case '>':
				return Lmatched, nil
			case '=':
				return Leqeq, nil
			}
			if d >= 0 {
				f.ungetc()
			}
			return Leq, nil
		case '`':
			return Lbackq, nil
		case '"':
			return Lquote, nil
		case '{':
			return Loncurly, nil
		case '}':
			return Loffcurly, nil
		case '(':
			return Lonparen, nil
		case ')':
			return Loffparen, nil
		case '<':
			d := f.getc()
			if d == '>' {
				return Llessgreat, nil
			}
			if d >= 0 {
				f.ungetc()
			}
			return Lless, nil
		case '>':
			d := f.getc()
			if d == '>' {
				return Lgreatgreat, nil
			}
			if d >= 0 {
				f.ungetc()
			}
			return Lgreat, nil
		case '\\':
			d := f.getc()
			if d == '\n' {
				f.line++
				continue
			}
			if d >= 0 {
				f.ungetc()
			}
		}
		// Carets for free.
		var i, r *Item
		for {
			if c == '$' {
				i, c = getdollar(f)
			} else {
				i, c = getword(e, f, c)
			}
			if i == nil {
				return Lerror, nil
			}
			if isterm(c) && c != '$' {
				break
			}
			if r != nil {
				r = item2(Iicaret, r, i)
			} else {
				r = i
			}
		}
		if c >= 0 {
			f.ungetc()
		}
		if r != nil {
			return Lword, item2(Iicaret, r, i)
		}
		if k := keyval(i); k >= 0 {
			return k, nil
		}
		return Lword, i
	}
}

// getdollar reads $n, $name or $"name.
func getdollar(f *File) (*Item, rune) {
	var s []rune
	l := f.line
	o := Idollar
	c := f.getc()
	if isdigit(c) {
		for isdigit(c) {
			s = append(s, c)
			c = f.getc()
		}
		o = Imatch
	} else {
		if c == '"' {
			o = Idollarq
			c = f.getc()
		}
		if isalpha(c) {
			for isalnum(c) {
				s = append(s, c)
				c = f.getc()
			}
		} else {
			if o == Idollar {
				s = []rune("$")
			} else {
				s = []rune("$\"")
			}
			o = Iword
		}
	}
	return &Item{op: o, word: &Word{text: string(s), line: l, file: f.name}}, c
}

// getword reads a word, quoted or not, starting with c.
func getword(e *Env, f *File, c rune) (*Item, rune) {
	var s []rune
	l := f.line
	wf := 0
	if c == '\'' {
		wf = Wquoted
	collect:
		for {
			c = f.getc()
			if c < 0 {
				break
			}
			switch c {
			case '\'':
				c = f.getc()
				break collect
			case '\\':
				c = f.getc()
				if c != '\'' && c != '\\' {
					if c == '\n' {
						continue collect
					}
					if c >= 0 {
						f.ungetc()
					}
					c = '\\'
				}
			case '\n':
				f.line++
				e.report("newline in quoted word")
				return nil, 0
			}
			s = append(s, c)
		}
	} else {
		for {
			switch c {
			case '*', '[', '?':
				wf |= Wexpand
			}
			s = append(s, c)
			c = f.getc()
			if isterm(c) || c == '\'' {
				break
			}
		}
	}
	return &Item{op: Iword, word: &Word{text: string(s), flags: wf, line: l, file: f.name}}, c
}
