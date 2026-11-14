// Command fc is a postfix (RPN) calculator on its arguments (Inferno
// fc(1)). The stack is printed bottom first at the end, in the output
// base an option selects.
package main

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	unary = iota
	binary
	special
)

type op struct {
	name string
	kind int
}

var ops = []op{
	{"swap", special}, {"dup", special}, {"rep", special}, {"sum", special}, {"p", special},
	{"x", binary}, {"×", binary}, {"pow", binary}, {"xx", binary},
	{"+", binary}, {"-", binary}, {"/", binary}, {"div", binary}, {"%", binary},
	{"shl", binary}, {"shr", binary}, {"and", binary}, {"or", binary},
	{"⋀", binary}, {"⋁", binary}, {"xor", binary},
	{"not", unary}, {"_", unary}, {"factorial", unary}, {"!", unary},
	{"pow", binary}, {"hypot", binary}, {"atan2", binary}, {"jn", binary}, {"yn", binary},
	{"scalbn", binary}, {"copysign", binary}, {"fdim", binary}, {"fmin", binary},
	{"fmax", binary}, {"nextafter", binary}, {"remainder", binary}, {"fmod", binary},
	{"pow10", unary}, {"sqrt", unary}, {"exp", unary}, {"expm1", unary}, {"log", unary},
	{"log10", unary}, {"log1p", unary}, {"cos", unary}, {"cosh", unary}, {"sin", unary},
	{"sinh", unary}, {"tan", unary}, {"tanh", unary}, {"acos", unary}, {"asin", unary},
	{"acosh", unary}, {"asinh", unary}, {"atan", unary}, {"atanh", unary}, {"erf", unary},
	{"erfc", unary}, {"j0", unary}, {"j1", unary}, {"y0", unary}, {"y1", unary},
	{"ilogb", unary}, {"fabs", unary}, {"ceil", unary}, {"floor", unary}, {"finite", unary},
	{"isnan", unary}, {"rint", unary}, {"rad", unary}, {"deg", unary},
	{"lgamma", special}, {"modf", special},
}

// Number syntaxes, tried in order.
const (
	nHex = iota
	nBinary
	nOctal
	nRadix1
	nRadix2
	nReal
	nChar
)

var pats = []*regexp.Regexp{
	nHex:    regexp.MustCompile(`^-?0[xX][0-9a-fA-F]+$`),
	nBinary: regexp.MustCompile(`^-?0[bB][01]+$`),
	nOctal:  regexp.MustCompile(`^-?0[0-7]+$`),
	nRadix1: regexp.MustCompile(`^-?[0-9][rR][0-8]+$`),
	nRadix2: regexp.MustCompile(`^-?[0-3][0-9][rR][0-9a-zA-Z]+$`),
	nReal:   regexp.MustCompile(`^-?(([0-9]+(\.[0-9]+)?)|([0-9]*(\.[0-9]+)))([eE]-?[0-9]+)?$`),
	nChar:   regexp.MustCompile(`^@.$`),
}

const (
	radixFlag = 1 << (iota + 10)
	annotate
	char
)

var (
	outbase = 10
	stack   []float64 // top is the last element
	lastOp  op
)

func usage() {
	var b strings.Builder
	b.WriteString("usage: fc [-xdbB] [-r radix] <postfix expression>\n" +
		"option specifies output format:\n" +
		"\t-d decimal (default)\n" +
		"\t-x hex\n" +
		"\t-o octal\n" +
		"\t-b binary\n" +
		"\t-B annotated binary\n" +
		"\t-c character\n" +
		"\t-r <radix> specified base in Limbo 99r9999 format\n" +
		"operands are decimal(default), hex(0x), octal(0), binary(0b), radix(99r)\n")
	b.WriteString("operators are:\n")
	for _, o := range ops {
		b.WriteString(o.name + " ")
	}
	b.WriteString("\n")
	os.Stderr.WriteString(b.String())
	sys.Exits("fail:usage")
}

func main() {
	argv := os.Args[1:]
	if len(argv) == 0 {
		return
	}
	if a := argv[0]; len(a) > 1 && a[0] == '-' {
		if ok, _ := number(a); !ok {
			switch a[1] {
			case 'd':
				outbase = 10
			case 'x':
				outbase = 16
			case 'o':
				outbase = 8
			case 'b':
				outbase = 2
			case 'c':
				outbase = char
			case 'r':
				r := 0
				if len(a) > 2 {
					r = limboInt(a[2:])
				} else if len(argv) < 2 {
					usage()
				} else {
					argv = argv[1:]
					r = limboInt(argv[0])
				}
				if r < 2 || r > 36 {
					usage()
				}
				outbase = r | radixFlag
			case 'B':
				outbase = 2 | annotate
			default:
				fmt.Fprintf(os.Stderr, "fc: unknown option -%c\n", []rune(a[1:])[0])
				usage()
			}
			argv = argv[1:]
		}
	}
	for _, a := range argv {
		if ok, x := number(a); ok {
			stack = append(stack, x)
		} else {
			o := find(a)
			exec(o)
			lastOp = o
		}
	}
	for _, v := range stack {
		printnum(v)
	}
}

// toBig converts a real to an integer as Limbo does, rounding half
// away from zero.
func toBig(v float64) int64 { return int64(math.Round(v)) }

func toInt(v float64) int { return int(int32(toBig(v))) }

func printnum(n float64) {
	switch outbase {
	case char:
		fmt.Printf("@%c\n", rune(toInt(n)))
	case 2:
		fmt.Printf("%s\n", binstr(toBig(n)))
	case 2 | annotate:
		fmt.Printf("%s\n", annotatebinary(toBig(n)))
	case 8:
		fmt.Printf("%s\n", sharp(toBig(n), 8))
	case 10:
		fmt.Printf("%s\n", fmtg(n))
	case 16:
		fmt.Printf("%s\n", sharp(toBig(n), 16))
	default:
		fmt.Printf("%s\n", big2string(toBig(n), outbase&^radixFlag))
	}
}

// fmtg is C's %g.
func fmtg(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}

// sharp is %#bo or %#bx: a 0 or 0x prefix on the magnitude, with the
// sign before it.
func sharp(n int64, base int) string {
	neg := n < 0
	u := uint64(n)
	if neg {
		u = -u
	}
	s := strconv.FormatUint(u, base)
	switch {
	case base == 16:
		s = "0x" + s
	case s[0] != '0':
		s = "0" + s
	}
	if neg {
		s = "-" + s
	}
	return s
}

// binstr writes n in binary in whole bytes, at least one.
func binstr(n int64) string {
	j := 7
	for ; j > 0; j-- {
		if n&(0xff<<(j*8)) != 0 {
			break
		}
	}
	s := []byte("0b")
	for i := 63; i >= 0; i-- {
		if i/8 <= j {
			s = append(s, byte((n>>i)&1)+'0')
		}
	}
	return string(s)
}

func annotatebinary(n int64) string {
	s := binstr(n)
	a := []byte(s + "\n  ")
	ndig := len(s) - 2
	for i := ndig - 1; i >= 0; i-- {
		a = append(a, byte(i%10)+'0')
	}
	if ndig < 10 {
		return string(a)
	}
	a = append(a, "\n  "...)
	for i := ndig - 1; i >= 10; i-- {
		if i%10 == 0 {
			a = append(a, byte(i/10)+'0')
		} else {
			a = append(a, ' ')
		}
	}
	return string(a)
}

func find(name string) op {
	for _, o := range ops {
		if o.name == name {
			return o
		}
	}
	fail("invalid operator '" + name + "'")
	return op{}
}

func exec(o op) {
	switch o.kind {
	case unary:
		unaryop(o.name)
	case binary:
		binaryop(o.name)
	case special:
		specialop(o.name)
	}
}

func top() float64 { return stack[len(stack)-1] }

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func unaryop(name string) {
	assure(1, name)
	v := top()
	switch name {
	case "not":
		v = b2f(toInt(v) == 0)
	case "_":
		v = -v
	case "factorial", "!":
		n := toInt(v)
		v = 1
		for ; n > 0; n-- {
			v *= float64(n)
		}
	case "pow10":
		v = math.Pow10(toInt(v))
	case "sqrt":
		v = math.Sqrt(v)
	case "exp":
		v = math.Exp(v)
	case "expm1":
		v = math.Expm1(v)
	case "log":
		v = math.Log(v)
	case "log10":
		v = math.Log10(v)
	case "log1p":
		v = math.Log1p(v)
	case "cos":
		v = math.Cos(v)
	case "cosh":
		v = math.Cosh(v)
	case "sin":
		v = math.Sin(v)
	case "sinh":
		v = math.Sinh(v)
	case "tan":
		v = math.Tan(v)
	case "tanh":
		v = math.Tanh(v)
	case "acos":
		v = math.Acos(v)
	case "asin":
		v = math.Asin(v)
	case "acosh":
		v = math.Acosh(v)
	case "asinh":
		v = math.Asinh(v)
	case "atan":
		v = math.Atan(v)
	case "atanh":
		v = math.Atanh(v)
	case "erf":
		v = math.Erf(v)
	case "erfc":
		v = math.Erfc(v)
	case "j0":
		v = math.J0(v)
	case "j1":
		v = math.J1(v)
	case "y0":
		v = math.Y0(v)
	case "y1":
		v = math.Y1(v)
	case "ilogb":
		v = float64(math.Ilogb(v))
	case "fabs":
		v = math.Abs(v)
	case "ceil":
		v = math.Ceil(v)
	case "floor":
		v = math.Floor(v)
	case "finite":
		v = b2f(!math.IsInf(v, 0) && !math.IsNaN(v))
	case "isnan":
		v = b2f(math.IsNaN(v))
	case "rint":
		v = math.RoundToEven(v)
	case "rad":
		v = (v / 360.0) * 2.0 * math.Pi
	case "deg":
		v = v / (2.0 * math.Pi) * 360.0
	default:
		fail("unknown unary operator '" + name + "'")
	}
	stack[len(stack)-1] = v
}

func binaryop(name string) {
	assure(2, name)
	v1 := stack[len(stack)-1]
	v0 := stack[len(stack)-2]
	i0, i1 := toBig(v0), toBig(v1)
	switch name {
	case "x", "×":
		v0 *= v1
	case "+":
		v0 += v1
	case "-":
		v0 -= v1
	case "/":
		v0 /= v1
	case "div":
		if i1 == 0 {
			fail("divide by zero")
		}
		v0 = float64(i0 / i1)
	case "%":
		if i1 == 0 {
			fail("divide by zero")
		}
		v0 = float64(i0 % i1)
	case "shl":
		v0 = float64(i0 << uint(toInt(v1)&63))
	case "shr":
		v0 = float64(i0 >> uint(toInt(v1)&63))
	case "and", "⋀":
		v0 = float64(i0 & i1)
	case "or", "⋁":
		v0 = float64(i0 | i1)
	case "xor":
		v0 = float64(i0 ^ i1)
	case "pow", "xx":
		v0 = math.Pow(v0, v1)
	case "hypot":
		v0 = math.Hypot(v0, v1)
	case "atan2":
		v0 = math.Atan2(v0, v1)
	case "jn":
		v0 = math.Jn(toInt(v0), v1)
	case "yn":
		v0 = math.Yn(toInt(v0), v1)
	case "scalbn":
		v0 = math.Ldexp(v0, toInt(v1))
	case "copysign":
		v0 = math.Copysign(v0, v1)
	case "fdim":
		v0 = math.Dim(v0, v1)
	case "fmin":
		v0 = math.Min(v0, v1)
	case "fmax":
		v0 = math.Max(v0, v1)
	case "nextafter":
		v0 = math.Nextafter(v0, v1)
	case "remainder":
		v0 = math.Remainder(v0, v1)
	case "fmod":
		v0 = math.Mod(v0, v1)
	default:
		fail("unknown binary operator '" + name + "'")
	}
	stack = append(stack[:len(stack)-2], v0)
}

func specialop(name string) {
	n := len(stack)
	switch name {
	case "swap":
		assure(2, name)
		stack[n-1], stack[n-2] = stack[n-2], stack[n-1]
	case "dup":
		assure(1, name)
		stack = append(stack, top())
	case "rep":
		if lastOp.kind != binary {
			fail("invalid operator '" + lastOp.name + "' for rep")
		}
		for len(stack) >= 2 {
			exec(lastOp)
		}
	case "sum":
		sum := 0.0
		for i := len(stack) - 1; i >= 0; i-- {
			sum += stack[i]
		}
		stack = []float64{sum}
	case "p":
		assure(1, name)
		printnum(top())
		stack = stack[:n-1]
	case "lgamma":
		assure(1, name)
		lg, s := math.Lgamma(top())
		stack = append(stack[:n-1], float64(s), lg)
	case "modf":
		assure(1, name)
		i, f := math.Modf(top())
		stack = append(stack[:n-1], i, f)
	default:
		fail("unknown operator '" + name + "'")
	}
}

func number(s string) (bool, float64) {
	switch s {
	case "pi", "π":
		return true, math.Pi
	case "e":
		return true, 2.71828182845904509
	case "nan", "NaN":
		return true, math.NaN()
	case "-nan", "-NaN":
		return true, -math.NaN()
	case "infinity", "Infinity", "∞":
		return true, math.Inf(1)
	case "-infinity", "-Infinity", "-∞":
		return true, math.Inf(-1)
	case "eps", "macheps":
		return true, 2.220446049250313e-16
	}
	i := 0
	for i < len(pats) && !pats[i].MatchString(s) {
		i++
	}
	switch i {
	case nHex:
		return base(s, 2, 16)
	case nBinary:
		return base(s, 2, 2)
	case nOctal:
		return base(s, 1, 8)
	case nRadix1:
		return base(s, 2, limboInt(s))
	case nRadix2:
		return base(s, 3, limboInt(s))
	case nReal:
		v, _ := strconv.ParseFloat(s, 64)
		return true, v
	case nChar:
		return true, float64([]rune(s)[1])
	}
	return false, math.NaN()
}

func base(s string, i, radix int) (bool, float64) {
	neg := s[0] == '-'
	if neg {
		i++
		if radix < 0 {
			radix = -radix
		}
	}
	if radix == 0 || radix > 36 {
		return false, math.NaN()
	}
	var n int64
	for _, c := range s[i:] {
		var d int
		switch {
		case '0' <= c && c <= '9':
			d = int(c - '0')
		case 'a' <= c && int(c) < 'a'+radix-10:
			d = int(c-'a') + 10
		case 'A' <= c && int(c) < 'A'+radix-10:
			d = int(c-'A') + 10
		default:
			return false, math.NaN()
		}
		n = n*int64(radix) + int64(d)
	}
	if neg {
		n = -n
	}
	return true, float64(n)
}

func big2string(n int64, radix int) string {
	neg := n < 0
	u := uint64(n)
	if neg {
		u = -u
	}
	t := strconv.FormatUint(u, radix)
	if radix != 10 {
		t = strconv.Itoa(radix) + "r" + t
	}
	if neg {
		return "-" + t
	}
	return t
}

func fail(e string) {
	os.Stderr.WriteString("fc: " + e + "\n")
	sys.Exits("fail:error")
}

func assure(n int, name string) {
	if len(stack) < n {
		fail("stack too small for op '" + name + "'")
	}
}

func limboInt(s string) int {
	i := 0
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	n := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}
