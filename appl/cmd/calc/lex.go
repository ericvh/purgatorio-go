package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
)

// Operators and token kinds. The order matters: ranges of them are
// tested below (atoms, assignment operators, printable expressions).
const (
	Oeof = iota
	Ostring
	Onum
	Oident
	Ocon
	Ovar
	Ofun
	Olfun
	Oadd
	Osub
	Omul
	Odiv
	Omod
	Oidiv
	Oexp
	Oand
	Oor
	Oxor
	Olsh
	Orsh
	Oadde
	Osube
	Omule
	Odive
	Omode
	Oidive
	Oexpe
	Oande
	Oore
	Oxore
	Olshe
	Orshe
	Oeq
	One
	Ogt
	Olt
	Oge
	Ole
	Oinc
	Opreinc
	Opostinc
	Odec
	Opredec
	Opostdec
	Oandand
	Ooror
	Oexc
	Onot
	Ofact
	Ocom
	Oas
	Odas
	Oplus
	Ominus
	Oinv
	Ocomma
	Oscomma
	Oquest
	Ocolon
	Onand
	Onor
	Oimp
	Oimpby
	Oiff
	Olbr
	Orbr
	Olcbr
	Orcbr
	Oscolon
	Onl
	Onothing
	Oprint
	Oread
	Oif
	Oelse
	Ofor
	Owhile
	Odo
	Obreak
	Ocont
	Oexit
	Oret
	Ofn
	Oinclude
	Osigma
	Opi
	Ocfrac
	Oderiv
	Ointeg
	Osolve
	Olog
	Olog10
	Olog2
	Ologb
	Oexpf
	Opow
	Osqrt
	Ocbrt
	Ofloor
	Oceil
	Omin
	Omax
	Oabs
	Ogamma
	Osign
	Oint
	Ofrac
	Oround
	Oerf
	Oatan2
	Osin
	Ocos
	Otan
	Oasin
	Oacos
	Oatan
	Osinh
	Ocosh
	Otanh
	Oasinh
	Oacosh
	Oatanh
	Orand
	Olast
)

// Operator properties: precedence in the low byte.
const (
	Binary    = 1 << 8
	Preunary  = 1 << 9
	Postunary = 1 << 10
	Assoc     = 1 << 11
	Rassoc    = 1 << 12
	Prec      = Binary - 1
)

var opss = [Olast]string{
	Oeof: "eof", Ostring: "string", Onum: "number", Oident: "identifier",
	Ocon: "constant", Ovar: "variable", Ofun: "function", Olfun: "library function",
	Oadd: "+", Osub: "-", Omul: "*", Odiv: "/", Omod: "%", Oidiv: "//",
	Oexp: "**", Oand: "&", Oor: "|", Oxor: "^", Olsh: "<<", Orsh: ">>",
	Oadde: "+=", Osube: "-=", Omule: "*=", Odive: "/=", Omode: "%=", Oidive: "//=",
	Oexpe: "**=", Oande: "&=", Oore: "|=", Oxore: "^=", Olshe: "<<=", Orshe: ">>=",
	Oeq: "==", One: "!=", Ogt: ">", Olt: "<", Oge: ">=", Ole: "<=",
	Oinc: "++", Opreinc: "++", Opostinc: "++", Odec: "--", Opredec: "--", Opostdec: "--",
	Oandand: "&&", Ooror: "||", Oexc: "!", Onot: "!", Ofact: "!", Ocom: "~",
	Oas: "=", Odas: ":=", Oplus: "+", Ominus: "-", Oinv: "1/",
	Ocomma: ",", Oscomma: ",", Oquest: "?", Ocolon: ":",
	Onand: "↑", Onor: "↓", Oimp: "->", Oimpby: "<-", Oiff: "<->",
	Olbr: "(", Orbr: ")", Olcbr: "{", Orcbr: "}", Oscolon: ";", Onl: "\n",
}

var ops = [Olast]int{
	Ostring: 17, Onum: 17, Oident: 17, Ocon: 17, Ovar: 17, Ofun: 17, Olfun: 17,
	Oadd: 12 | Binary | Assoc | Preunary, Osub: 12 | Binary | Preunary,
	Omul: 13 | Binary | Assoc, Odiv: 13 | Binary, Omod: 13 | Binary, Oidiv: 13 | Binary,
	Oexp: 14 | Binary | Rassoc,
	Oand: 8 | Binary | Assoc, Oor: 6 | Binary | Assoc, Oxor: 7 | Binary | Assoc,
	Olsh: 11 | Binary, Orsh: 11 | Binary,
	Oadde: 2 | Binary | Rassoc, Osube: 2 | Binary | Rassoc, Omule: 2 | Binary | Rassoc,
	Odive: 2 | Binary | Rassoc, Omode: 2 | Binary | Rassoc, Oidive: 2 | Binary | Rassoc,
	Oexpe: 2 | Binary | Rassoc, Oande: 2 | Binary | Rassoc, Oore: 2 | Binary | Rassoc,
	Oxore: 2 | Binary | Rassoc, Olshe: 2 | Binary | Rassoc, Orshe: 2 | Binary | Rassoc,
	Oeq: 9 | Binary, One: 9 | Binary,
	Ogt: 10 | Binary, Olt: 10 | Binary, Oge: 10 | Binary, Ole: 10 | Binary,
	Oinc: 15 | Rassoc | Preunary | Postunary, Opreinc: 15 | Rassoc | Preunary,
	Opostinc: 15 | Rassoc | Postunary, Odec: 15 | Rassoc | Preunary | Postunary,
	Opredec: 15 | Rassoc | Preunary, Opostdec: 15 | Rassoc | Postunary,
	Oandand: 5 | Binary | Assoc, Ooror: 4 | Binary | Assoc,
	Oexc: 15 | Rassoc | Preunary | Postunary, Onot: 15 | Rassoc | Preunary,
	Ofact: 15 | Rassoc | Postunary, Ocom: 15 | Rassoc | Preunary,
	Oas: 2 | Binary | Rassoc, Odas: 2 | Binary | Rassoc,
	Oplus: 15 | Rassoc | Preunary, Ominus: 15 | Rassoc | Preunary, Oinv: 15 | Rassoc | Postunary,
	Ocomma: 1 | Binary | Assoc, Oscomma: 1 | Binary | Assoc,
	Oquest: 3 | Binary | Rassoc, Ocolon: 3 | Binary | Rassoc,
	Onand: 8 | Binary, Onor: 6 | Binary, Oimp: 9 | Binary, Oimpby: 9 | Binary,
	Oiff: 10 | Binary | Assoc, Olbr: 16, Orbr: 16,
}

type nameop struct {
	name string
	op   int
}

var keyw = []nameop{
	{"include", Oinclude}, {"if", Oif}, {"else", Oelse}, {"for", Ofor},
	{"while", Owhile}, {"do", Odo}, {"break", Obreak}, {"continue", Ocont},
	{"exit", Oexit}, {"return", Oret}, {"print", Oprint}, {"read", Oread},
	{"fn", Ofn},
}

var conw = []struct {
	name string
	val  float64
}{
	{"π", math.Pi}, {"Pi", math.Pi}, {"e", math.E}, {"γ", gammaC}, {"Gamma", gammaC},
	{"φ", math.Phi}, {"Phi", math.Phi}, {"∞", math.Inf(1)}, {"Infinity", math.Inf(1)},
	{"NaN", math.NaN()}, {"Nan", math.NaN()}, {"nan", math.NaN()},
}

const (
	gammaC = 0.57721566490153286060
	degS   = "degrees"
	baseS  = "printbase"
	limitS = "solvelimit"
	stepS  = "solvestep"
)

var varw = []struct {
	name string
	val  float64
}{{degS, 0}, {baseS, 10}, {limitS, 100}, {stepS, 1}}

var funw = []struct {
	name   string
	op, na int
}{
	{"log", Olog, 1}, {"ln", Olog, 1}, {"log10", Olog10, 1}, {"log2", Olog2, 1},
	{"logb", Ologb, 2}, {"exp", Oexpf, 1}, {"pow", Opow, 2}, {"sqrt", Osqrt, 1},
	{"cbrt", Ocbrt, 1}, {"floor", Ofloor, 1}, {"ceiling", Oceil, 1},
	{"min", Omin, 2}, {"max", Omax, 2}, {"abs", Oabs, 1}, {"Γ", Ogamma, 1},
	{"gamma", Ogamma, 1}, {"sign", Osign, 1}, {"int", Oint, 1}, {"frac", Ofrac, 1},
	{"round", Oround, 1}, {"erf", Oerf, 1}, {"atan2", Oatan2, 2},
	{"sin", Osin, 1}, {"cos", Ocos, 1}, {"tan", Otan, 1}, {"asin", Oasin, 1},
	{"acos", Oacos, 1}, {"atan", Oatan, 1}, {"sinh", Osinh, 1}, {"cosh", Ocosh, 1},
	{"tanh", Otanh, 1}, {"asinh", Oasinh, 1}, {"acosh", Oacosh, 1},
	{"atanh", Oatanh, 1}, {"rand", Orand, 0},
	{"Σ", Osigma, 3}, {"sigma", Osigma, 3}, {"Π", Opi, 3}, {"pi", Opi, 3},
	{"cfrac", Ocfrac, 3}, {"Δ", Oderiv, 2}, {"differential", Oderiv, 2},
	{"∫", Ointeg, 3}, {"integral", Ointeg, 3}, {"solve", Osolve, 1},
}

func opstring(t int) string {
	if s := opss[t]; s != "" || t == Onothing {
		return s
	}
	for _, k := range keyw {
		if k.op == t {
			return k.name
		}
	}
	for _, f := range funw {
		if f.op == t {
			return f.name
		}
	}
	return ""
}

// calcError is an error raised while reading or running; the main loop
// prints it and goes on.
type calcError string

// errEOF is the end of input inside a construct.
type eofError struct{}

const (
	cNone = -2
	cEOF  = -1
)

// input is one source of characters: a file, standard input, or the
// command-line arguments as an expression.
type input struct {
	bufc   int
	buft   int
	lineno int
	file   string
	bin    *bufio.Reader
}

var (
	in       = input{bufc: cNone, buft: Olast, lineno: 1}
	iostack  []input
	geof     bool
	garg     []rune
	gargs    []string
	lexes    int
	lexval   float64
	lexstr   string
	lexsym   *Sym
	usestdin bool
)

func errorf(n *Node, format string, a ...any) {
	lno := in.lineno
	if n != nil {
		lno = n.src
	}
	s := fmt.Sprintf("line %d: %s", lno, fmt.Sprintf(format, a...))
	if in.file != "" {
		s = fmt.Sprintf("file %s: %s", in.file, s)
	}
	panic(calcError(s))
}

func stack(name string, f *bufio.Reader) {
	iostack = append(iostack, in)
	in = input{bufc: cNone, buft: Olast, lineno: 1, file: name, bin: f}
}

func unstack() {
	in = iostack[len(iostack)-1]
	iostack = iostack[:len(iostack)-1]
}

func doinclude(s string) {
	f, err := os.Open(s)
	if err != nil {
		errorf(nil, "cannot open %s", s)
	}
	stack(s, bufio.NewReader(f))
}

func getc() int {
	var c int
	switch {
	case in.bufc != cNone:
		c = in.bufc
		in.bufc = cNone
	case in.bin != nil:
		r, _, err := in.bin.ReadRune()
		if err != nil {
			c = cEOF
		} else {
			c = int(r)
		}
	case len(garg) == 0:
		switch {
		case len(gargs) > 0:
			garg = []rune(gargs[0])
			gargs = gargs[1:]
			c = ' '
		case !geof:
			geof = true
			c = '\n'
		default:
			c = cEOF
		}
	default:
		c = int(garg[0])
		garg = garg[1:]
	}
	if c == cEOF && len(iostack) > 0 {
		unstack()
		return getc()
	}
	return c
}

func ungetc(c int) { in.bufc = c }

func slash(c int) int {
	if c != '\\' {
		return c
	}
	nc := getc()
	switch nc {
	case 'b':
		return '\b'
	case 'f':
		return '\f'
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	}
	return nc
}

func lexstring() int {
	var sp []rune
	for {
		c := getc()
		if c == '"' {
			break
		}
		if c == cEOF {
			panic(eofError{})
		}
		sp = append(sp, rune(slash(c)))
	}
	lexstr = string(sp)
	return Ostring
}

func lexchar() int {
	for {
		c := getc()
		if c == '\'' {
			break
		}
		if c == cEOF {
			panic(eofError{})
		}
		lexval = float64(slash(c))
	}
	return Onum
}

func basev(c, base int) int {
	switch {
	case c >= 'a' && c <= 'z':
		c += 10 - 'a'
	case c >= 'A' && c <= 'Z':
		c += 10 - 'A'
	case c >= '0' && c <= '9':
		c -= '0'
	default:
		return -1
	}
	if c >= base {
		errorf(nil, "bad digit")
	}
	return c
}

func lexe(base int) int {
	neg := false
	var v int64
	c := getc()
	if c == '-' {
		neg = true
	} else {
		ungetc(c)
	}
	for {
		c = getc()
		cc := basev(c, base)
		if cc < 0 {
			ungetc(c)
			break
		}
		v = int64(base)*v + int64(cc)
	}
	if neg {
		v = -v
	}
	return int(int32(v))
}

func lexnum() int {
	base := 10
	exp := 0
	r, f, e := false, 0, false
	var v int64
	c := getc()
	if c == '0' {
		base = 8
		c = getc()
		switch {
		case c == '.':
			base = 10
			ungetc(c)
		case c == 'x' || c == 'X':
			base = 16
		default:
			ungetc(c)
		}
	} else {
		ungetc(c)
	}
	for {
		c = getc()
		if !r && (c == 'r' || c == 'R') {
			if f != 0 || e {
				errorf(nil, "bad base")
			}
			r = true
			base = int(v)
			if base < 2 || base > 36 {
				errorf(nil, "bad base")
			}
			v = 0
			continue
		}
		if c == '.' {
			if f != 0 || e {
				errorf(nil, "bad real")
			}
			f = 1
			continue
		}
		if base == 10 && (c == 'e' || c == 'E') {
			if e {
				errorf(nil, "bad E part")
			}
			e = true
			exp = lexe(base)
			continue
		}
		cc := basev(c, base)
		if cc < 0 {
			ungetc(c)
			break
		}
		v = int64(base)*v + int64(cc)
		if f != 0 {
			f++
		}
	}
	lexval = float64(v)
	if f != 0 {
		lexval /= ipow(float64(base), f-1)
	}
	if exp > 0 {
		lexval *= ipow(float64(base), exp)
	} else if exp < 0 {
		lexval *= math.Pow(float64(base), float64(exp))
	}
	return Onum
}

func idchar(c int) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c >= 'α' && c <= 'ω' || c >= 'Α' && c <= 'Ω' || c == '_'
}

func lexid() int {
	var sp []rune
	for {
		c := getc()
		if !idchar(c) {
			ungetc(c)
			break
		}
		sp = append(sp, rune(c))
	}
	lexsym = enter(string(sp), Oident)
	return lexsym.kind
}

func follow(c, c1, c2 int) int {
	nc := getc()
	if nc == c {
		return c1
	}
	ungetc(nc)
	return c2
}

// skip discards the rest of the line after an error.
func skip() {
	if t := in.buft; t != Olast {
		lex()
		if t == Onl {
			return
		}
	}
	for {
		c := getc()
		if c == cEOF {
			ungetc(c)
			return
		}
		if c == '\n' {
			in.lineno++
			return
		}
	}
}

func lex() int {
	lexes++
	if t := in.buft; t != Olast {
		in.buft = Olast
		if t == Onl {
			in.lineno++
		}
		return t
	}
	for {
		c := getc()
		switch {
		case c == cEOF:
			return Oeof
		case c == '#':
			for {
				c = getc()
				if c == '\n' {
					break
				}
				if c == cEOF {
					panic(eofError{})
				}
			}
			in.lineno++
		case c == '\n':
			in.lineno++
			return Onl
		case c == ' ' || c == '\t' || c == '\r' || c == '\v':
		case c == '"':
			return lexstring()
		case c == '\'':
			return lexchar()
		case c >= '0' && c <= '9' || c == '.':
			ungetc(c)
			return lexnum()
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= 'α' && c <= 'ω' || c >= 'Α' && c <= 'Ω' || c == '_':
			ungetc(c)
			return lexid()
		case c == '+':
			c = getc()
			if c == '=' {
				return Oadde
			}
			ungetc(c)
			return follow('+', Oinc, Oadd)
		case c == '-':
			c = getc()
			if c == '=' {
				return Osube
			}
			if c == '>' {
				return Oimp
			}
			ungetc(c)
			return follow('-', Odec, Osub)
		case c == '*':
			c = getc()
			if c == '=' {
				return Omule
			}
			if c == '*' {
				return follow('=', Oexpe, Oexp)
			}
			ungetc(c)
			return Omul
		case c == '/':
			c = getc()
			if c == '=' {
				return Odive
			}
			if c == '/' {
				return follow('=', Oidive, Oidiv)
			}
			ungetc(c)
			return Odiv
		case c == '%':
			return follow('=', Omode, Omod)
		case c == '&':
			c = getc()
			if c == '=' {
				return Oande
			}
			ungetc(c)
			return follow('&', Oandand, Oand)
		case c == '|':
			c = getc()
			if c == '=' {
				return Oore
			}
			ungetc(c)
			return follow('|', Ooror, Oor)
		case c == '^':
			return follow('=', Oxore, Oxor)
		case c == '=':
			return follow('=', Oeq, Oas)
		case c == '!':
			return follow('=', One, Oexc)
		case c == '>':
			c = getc()
			if c == '=' {
				return Oge
			}
			if c == '>' {
				return follow('=', Orshe, Orsh)
			}
			ungetc(c)
			return Ogt
		case c == '<':
			c = getc()
			if c == '=' {
				return Ole
			}
			if c == '<' {
				return follow('=', Olshe, Olsh)
			}
			if c == '-' {
				return follow('>', Oiff, Oimpby)
			}
			ungetc(c)
			return Olt
		case c == '(':
			return Olbr
		case c == ')':
			return Orbr
		case c == '{':
			return Olcbr
		case c == '}':
			return Orcbr
		case c == '~':
			return Ocom
		case c == ',':
			return Ocomma
		case c == '?':
			return Oquest
		case c == ':':
			return follow('=', Odas, Ocolon)
		case c == ';':
			return Oscolon
		case c == '↑':
			return Onand
		case c == '↓':
			return Onor
		case c == '∞':
			lexval = math.Inf(1)
			return Onum
		default:
			errorf(nil, "bad character %c", rune(c))
		}
	}
}

func unlex(t int) {
	lexes--
	in.buft = t
	if t == Onl {
		in.lineno--
	}
}

func mustbe(t int) {
	if nt := lex(); nt != t {
		errorf(nil, "expected %s not %s", opstring(t), opstring(nt))
	}
}

func consume(t int) {
	if nt := lex(); nt != t {
		unlex(nt)
	}
}
