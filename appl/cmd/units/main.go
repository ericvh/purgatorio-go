// Command units converts quantities between scales (Inferno units(1)).
// It reads a database of units, by default /lib/units, then asks for
// pairs of quantities on standard input and prints the factors between
// them.
package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	ndim = 15    // dimensions; dim[0] marks a defined name
	maxe = 695.0 // log of the largest number
)

type node struct {
	val float64
	dim [ndim]int
}

func mk(v float64) node { return node{val: v} }

type variable struct {
	name string
	node node
}

var (
	fund    [ndim]*variable
	vars    = map[string]*variable{}
	vflag   bool
	line    []rune
	linep   int
	lineno  int
	nerrors int
	sym     string // the last name read
	out     = bufio.NewWriter(os.Stdout)
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("units [-v] [file]")
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'v':
			vflag = true
		default:
			a.Usage()
		}
	}
	file := "/lib/units"
	if args := a.Argv(); len(args) > 0 {
		file = args[0]
	}
	f, err := os.Open(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "units: cannot open %s: %s\n", file, sys.Errstr())
		sys.Exits("fail:open")
	}
	defer out.Flush()

	// The database: each line defines a name.
	in := bufio.NewReader(f)
	for lineno = 1; ; lineno++ {
		if !readline(in) {
			break
		}
		if len(line) == 0 || line[0] == '/' {
			continue
		}
		parse(':')
	}
	f.Close()

	// The questions: alternate lines are what you have and what you want.
	in = bufio.NewReader(os.Stdin)
	var have node
	for lineno = 0; ; {
		if lineno&1 != 0 {
			out.WriteString("you want: ")
		} else {
			out.WriteString("you have: ")
		}
		out.Flush()
		if !readline(in) {
			break
		}
		nerrors = 0
		want := parse('?')
		if nerrors != 0 {
			continue
		}
		if lineno&1 != 0 {
			if r, ok := specialcase(have, want); ok {
				fmt.Fprintf(out, "\tis %s\n", r.text())
			} else {
				fmt.Fprintf(out, "\t* %s\n", div(have, want).text())
				fmt.Fprintf(out, "\t/ %s\n", div(want, have).text())
			}
		} else {
			have = want
		}
		lineno++
	}
	out.WriteString("\n")
}

// readline reads a line into line; a last line with no newline is
// ignored, as in Inferno.
func readline(in *bufio.Reader) bool {
	linep = 0
	s, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	line = []rune(s[:len(s)-1])
	return true
}

func yyerror(s string) {
	fmt.Fprintf(out, "%d: %s\n\t%s\n", lineno, string(line), s)
	nerrors++
	if nerrors > 5 {
		out.WriteString("too many errors\n")
		out.Flush()
		sys.Exits("fail:errors")
	}
}

// Tokens.
const (
	tEOF = 0
	tVAL = -1
	tVAR = -2
	tSUP = -3
)

// lexer state: the pushed-back rune and the current token's values.
var (
	peek    rune
	tok     int
	tokval  float64
	tokvar  *variable
	toknumb int
)

func getc() rune {
	if linep >= len(line) {
		return 0
	}
	c := line[linep]
	linep++
	return c
}

func next() {
	c := peek
	peek = ' '
	for c == ' ' || c == '\t' {
		if linep >= len(line) {
			tok = tEOF
			return
		}
		c = getc()
	}
	switch c {
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.':
		tokval, peek = readreal(c)
		tok = tVAL
		return
	case '×':
		tok = '*'
		return
	case '÷':
		tok = '/'
		return
	case '¹', 'ⁱ':
		tok, toknumb = tSUP, 1
		return
	case '²', '⁲':
		tok, toknumb = tSUP, 2
		return
	case '³', '⁳':
		tok, toknumb = tSUP, 3
		return
	}
	if ralpha(c) {
		var s []rune
		for {
			s = append(s, c)
			if linep >= len(line) {
				c = ' '
				break
			}
			c = getc()
			if !ralpha(c) {
				break
			}
		}
		peek = c
		sym = string(s)
		tok, tokvar = tVAR, lookup(true)
		return
	}
	tok = int(c)
}

// ralpha reports whether c may be part of a name.
func ralpha(c rune) bool {
	switch c {
	case 0, '+', '-', '*', '/', '[', ']', '(', ')', '^', ':', '?', ' ', '\t',
		'.', '|', '#', '¹', 'ⁱ', '²', '⁲', '³', '⁳', '×', '÷':
		return false
	}
	return true
}

// readreal reads a number starting with c and returns it with the rune
// after it.
func readreal(c rune) (float64, rune) {
	var s []rune
	digits := func() {
		for c >= '0' && c <= '9' {
			s = append(s, c)
			c = getc()
		}
	}
	digits()
	if c == '.' {
		s = append(s, c)
		c = getc()
		digits()
	}
	mant := len(s)
	if len(s) > 0 && (c == 'e' || c == 'E') {
		s = append(s, c)
		c = getc()
		if c == '-' || c == '+' {
			s = append(s, c)
			c = getc()
		}
		digits()
	}
	v, err := strconv.ParseFloat(string(s), 64)
	if err != nil {
		// An incomplete exponent or a lone point: use what parses.
		v, err = strconv.ParseFloat(string(s[:mant]), 64)
		if err != nil {
			v = 0
		}
	}
	return v, c
}

type syntaxError struct{}

func expect(t int) {
	if tok != t {
		panic(syntaxError{})
	}
	next()
}

// parse parses one line; start is ':' for a definition and '?' for a
// question, whose value it returns.
func parse(start rune) (r node) {
	defer func() {
		if e := recover(); e != nil {
			if _, ok := e.(syntaxError); !ok {
				panic(e)
			}
			yyerror("syntax error, last name: " + sym)
			r = mk(1)
		}
	}()
	peek = start
	next()
	switch tok {
	case ':':
		next()
		if tok != tVAR {
			panic(syntaxError{})
		}
		v := tokvar
		next()
		if tok == '#' {
			next()
			expect(tEOF)
			i := 1
			for i < ndim && fund[i] != nil {
				i++
			}
			if i >= ndim {
				yyerror("too many dimensions")
				i = ndim - 1
			}
			fund[i] = v
			f := v.node.dim[0]
			v.node = mk(1)
			v.node.dim[0] = 1
			v.node.dim[i] = 1
			if f != 0 {
				yyerror("redefinition of " + v.name)
			} else if vflag {
				fmt.Fprintf(out, "%s\t#\n", v.name)
			}
			return
		}
		e := expr()
		expect(tEOF)
		f := v.node.dim[0]
		v.node = e
		v.node.dim[0] = 1
		if f != 0 {
			yyerror("redefinition of " + v.name)
		} else if vflag {
			fmt.Fprintf(out, "%s\t%s\n", v.name, v.node.text())
		}
		return
	case '?':
		next()
		if tok == tEOF {
			return mk(1)
		}
		r = expr()
		expect(tEOF)
		return r
	}
	panic(syntaxError{})
}

// expr: terms joined by + and -.
func expr() node {
	n := expr4()
	for tok == '+' || tok == '-' {
		op := tok
		next()
		m := expr4()
		if op == '+' {
			n = add(n, m, "add")
		} else {
			m.val = -m.val
			n = add(n, m, "sub")
		}
	}
	return n
}

// expr4: products and quotients with * and /.
func expr4() node {
	n := expr3()
	for tok == '*' || tok == '/' {
		op := tok
		next()
		m := expr3()
		if op == '*' {
			n = mul(n, m)
		} else {
			n = div(n, m)
		}
	}
	return n
}

// expr3: juxtaposition multiplies.
func expr3() node {
	n := expr2()
	for tok == tVAR || tok == tVAL || tok == '(' {
		n = mul(n, expr2())
	}
	return n
}

// expr2: powers, by superscript or ^.
func expr2() node {
	n := expr1()
	for tok == tSUP || tok == '^' {
		if tok == tSUP {
			k := toknumb
			next()
			n = xpn(n, k)
			continue
		}
		next()
		e := expr1()
		units := false
		for i := 1; i < ndim; i++ {
			if e.dim[i] != 0 {
				units = true
			}
		}
		if units {
			yyerror("exponent has units")
			continue
		}
		k := int(e.val)
		if float64(k) != e.val {
			yyerror("exponent not integral")
		}
		n = xpn(n, k)
	}
	return n
}

// expr1: | divides, binding tighter than anything but a name.
func expr1() node {
	n := expr0()
	for tok == '|' {
		next()
		n = div(n, expr0())
	}
	return n
}

func expr0() node {
	switch tok {
	case tVAR:
		v := tokvar
		next()
		if v.node.dim[0] == 0 {
			yyerror("undefined " + v.name)
			return mk(1)
		}
		return v.node
	case tVAL:
		v := tokval
		next()
		return mk(v)
	case '(':
		next()
		n := expr()
		expect(')')
		return n
	}
	panic(syntaxError{})
}

func add(a, b node, what string) node {
	c := mk(a.val + b.val)
	for i := range c.dim {
		c.dim[i] = a.dim[i]
		if a.dim[i] != b.dim[i] {
			yyerror(what + " must be like units")
		}
	}
	return c
}

func mul(a, b node) node {
	c := mk(fmul(a.val, b.val))
	for i := range c.dim {
		c.dim[i] = a.dim[i] + b.dim[i]
	}
	return c
}

func div(a, b node) node {
	c := mk(fdiv(a.val, b.val))
	for i := range c.dim {
		c.dim[i] = a.dim[i] - b.dim[i]
	}
	return c
}

func xpn(a node, b int) node {
	c := mk(1)
	if b < 0 {
		for i := 0; i < -b; i++ {
			c = div(c, a)
		}
	} else {
		for i := 0; i < b; i++ {
			c = mul(c, a)
		}
	}
	return c
}

func logabs(x float64) float64 { return math.Log(math.Abs(x)) }

// fmul and fdiv check for overflow and underflow first.
func fmul(a, b float64) float64 {
	if a == 0 || b == 0 {
		return 0
	}
	l := logabs(a) + logabs(b)
	if l > maxe {
		yyerror("overflow in multiply")
		return 1
	}
	if l < -maxe {
		yyerror("underflow in multiply")
		return 0
	}
	return a * b
}

func fdiv(a, b float64) float64 {
	if a == 0 {
		return 0
	}
	if b == 0 {
		yyerror("division by zero")
		return 1
	}
	l := logabs(a) - logabs(b)
	if l > maxe {
		yyerror("overflow in divide")
		return 1
	}
	if l < -maxe {
		yyerror("underflow in divide")
		return 0
	}
	return a / b
}

// specialcase converts between Celsius and Fahrenheit, which differ by
// more than a factor.
func specialcase(a, b node) (node, bool) {
	d1, d2 := 0, 0
	for i := 1; i < ndim; i++ {
		if d := a.dim[i]; d != 0 {
			if d != 1 || d1 != 0 {
				return node{}, false
			}
			d1 = i
		}
		if d := b.dim[i]; d != 0 {
			if d != 1 || d2 != 0 {
				return node{}, false
			}
			d2 = i
		}
	}
	if d1 == 0 || d2 == 0 || b.val != 1 {
		return node{}, false
	}
	c := b
	switch {
	case fund[d1].name == "°C" && fund[d2].name == "°F":
		c.val = a.val*9/5 + 32
	case fund[d1].name == "°F" && fund[d2].name == "°C":
		c.val = (a.val - 32) * 5 / 9
	default:
		return node{}, false
	}
	return c, true
}

func printdim(d, n int) string {
	if n == 0 {
		return ""
	}
	s := fmt.Sprintf(" [%d]", d)
	if v := fund[d]; v != nil {
		s = " " + v.name
	}
	switch n {
	case 1:
	case 2:
		s += "²"
	case 3:
		s += "³"
	case 4:
		s += "⁴"
	default:
		s += fmt.Sprintf("^%d", n)
	}
	return s
}

func (n node) text() string {
	s := fmt.Sprintf("%.7g", n.val)
	neg := false
	for i := 1; i < ndim; i++ {
		if d := n.dim[i]; d > 0 {
			s += printdim(i, d)
		} else if d < 0 {
			neg = true
		}
	}
	if neg {
		s += " /"
		for i := 1; i < ndim; i++ {
			if d := n.dim[i]; d < 0 {
				s += printdim(i, -d)
			}
		}
	}
	return s
}

// lookup finds the name in sym, creating it if it is new: a new name
// takes the value of a known name it reaches by removing metric prefixes
// and a plural s. With create false, an unknown name is nil.
func lookup(create bool) *variable {
	if v := vars[sym]; v != nil {
		return v
	}
	if !create {
		return nil
	}
	v := &variable{name: sym}
	vars[sym] = v
	p := 1.0
	for {
		p = fmul(p, pname())
		if p == 0 {
			break
		}
		if w := lookup(false); w != nil {
			v.node = w.node
			v.node.val = fmul(v.node.val, p)
			break
		}
	}
	return v
}

var prefixes = []struct {
	val  float64
	name string
}{
	{1e-24, "yocto"}, {1e-21, "zepto"}, {1e-18, "atto"}, {1e-15, "femto"},
	{1e-12, "pico"}, {1e-9, "nano"}, {1e-6, "micro"}, {1e-6, "μ"},
	{1e-3, "milli"}, {1e-2, "centi"}, {1e-1, "deci"}, {1e1, "deka"},
	{1e2, "hecta"}, {1e2, "hecto"}, {1e3, "kilo"}, {1e6, "mega"},
	{1e6, "meg"}, {1e9, "giga"}, {1e12, "tera"}, {1e15, "peta"},
	{1e18, "exa"}, {1e21, "zetta"}, {1e24, "yotta"},
}

// pname strips a prefix or a plural s from sym and returns its factor,
// or 0 if there is none.
func pname() float64 {
	for _, p := range prefixes {
		if len(sym) >= len(p.name) && sym[:len(p.name)] == p.name {
			sym = sym[len(p.name):]
			return p.val
		}
	}
	// More than two runes, so that ms does not find m.
	r := []rune(sym)
	if j := len(r) - 1; j > 1 && r[j] == 's' {
		sym = string(r[:j])
		return 1
	}
	return 0
}
