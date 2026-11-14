package main

import (
	"bufio"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
)

const (
	maxint = 0x7FFFFFFF
	big    = 1 << 30
	eps    = 1e-10
	bigeps = 1e-2
	ln2    = 0.6931471805599453
	ln10   = 2.302585092994046
)

var (
	nan = math.NaN()
	inf = math.Inf(1)
)

// exitError ends the program, as exit in calc or a fatal error does.
type exitError struct{}

// toint converts as Limbo does: round half away from zero.
func toint(v float64) int {
	if v < 0 {
		v -= .5
	} else {
		v += .5
	}
	if math.IsNaN(v) || v >= 1<<31 || v <= -(1<<31)-1 {
		return math.MinInt32
	}
	return int(int32(v))
}

// tobig is toint for Limbo's 64-bit big.
func tobig(v float64) int64 {
	if v < 0 {
		v -= .5
	} else {
		v += .5
	}
	if math.IsNaN(v) || v >= 1<<63 || v <= -(1<<63) {
		return math.MinInt64
	}
	return int64(v)
}

func truth(v float64) bool { return toint(v) != 0 }

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// ipow raises x to an integer power by repeated squaring, as Limbo's **.
func ipow(x float64, n int) float64 {
	inv := n < 0
	if inv {
		n = -n
	}
	r := 1.0
	for {
		if n&1 != 0 {
			r *= x
		}
		n >>= 1
		if n == 0 {
			break
		}
		x *= x
	}
	if inv {
		r = 1 / r
	}
	return r
}

func estat(n *Node) (int, float64) {
	if n == nil {
		return Onothing, 0
	}
	l, r := n.left, n.right
	switch n.op {
	case Ocomma:
		k, v := estat(l)
		if k == Oexit || k == Oret || k == Obreak || k == Ocont {
			return k, v
		}
		return estat(r)
	case Oprint:
		return Onothing, print(l)
	case Oread:
		return Onothing, read(l)
	case Obreak, Ocont, Oexit:
		return n.op, 0
	case Oret:
		return Oret, eval(l)
	case Oif:
		v := eval(l)
		if truth(v) {
			return estat(r.left)
		} else if r.right != nil {
			return estat(r.right)
		}
		return Onothing, v
	case Ofor:
		var v float64
		for {
			v = eval(l)
			if !truth(v) {
				break
			}
			var k int
			k, v = estat(r.left)
			if k == Oexit || k == Oret {
				return k, v
			}
			if k == Obreak {
				break
			}
			if r.right != nil {
				v = eval(r.right)
			}
		}
		return Onothing, v
	case Odo:
		var v float64
		for {
			var k int
			k, v = estat(l)
			if k == Oexit || k == Oret {
				return k, v
			}
			if k == Obreak {
				break
			}
			v = eval(r)
			if !truth(v) {
				break
			}
		}
		return Onothing, v
	}
	return Onothing, eval(n)
}

func zdiv(b int64) {
	if b == 0 {
		panic(calcError("zero divide"))
	}
}

func eval(e *Node) float64 {
	var lv, rv float64
	if e == nil {
		return 1
	}
	o, l, r := e.op, e.left, e.right
	if o != Ofun && o != Olfun {
		lv = eval(l)
	}
	if o != Oandand && o != Ooror && o != Oquest {
		rv = eval(r)
	}
	switch o {
	case Ostring:
		return 0
	case Onum:
		return e.val
	case Ocon, Ovar:
		return e.dec.val
	case Ofun:
		return call(e.dec, l)
	case Olfun:
		return libfun(int(e.dec.val), l)
	case Oadd:
		return lv + rv
	case Osub:
		return lv - rv
	case Omul:
		return lv * rv
	case Odiv:
		return lv / rv
	case Omod:
		b := tobig(rv)
		zdiv(b)
		return float64(tobig(lv) % b)
	case Oidiv:
		b := tobig(rv)
		zdiv(b)
		return float64(tobig(lv) / b)
	case Oand:
		return float64(tobig(lv) & tobig(rv))
	case Oor:
		return float64(tobig(lv) | tobig(rv))
	case Oxor:
		return float64(tobig(lv) ^ tobig(rv))
	case Olsh:
		return float64(tobig(lv) << uint(toint(rv)&63))
	case Orsh:
		return float64(tobig(lv) >> uint(toint(rv)&63))
	case Oeq:
		return b2f(lv == rv)
	case One:
		return b2f(lv != rv)
	case Ogt:
		return b2f(lv > rv)
	case Olt:
		return b2f(lv < rv)
	case Oge:
		return b2f(lv >= rv)
	case Ole:
		return b2f(lv <= rv)
	case Opreinc:
		l.dec.val++
		return l.dec.val
	case Opostinc:
		l.dec.val++
		return l.dec.val - 1
	case Opredec:
		l.dec.val--
		return l.dec.val
	case Opostdec:
		l.dec.val--
		return l.dec.val + 1
	case Oexp:
		if isinteger(rv) && rv >= 0 {
			return ipow(lv, toint(rv))
		}
		return math.Pow(lv, rv)
	case Oandand:
		if !truth(lv) {
			return lv
		}
		return eval(r)
	case Ooror:
		if truth(lv) {
			return lv
		}
		return eval(r)
	case Onot:
		return b2f(!truth(lv))
	case Ofact:
		if isinteger(lv) && lv >= 0 {
			n := toint(lv)
			lv = 1
			for i := 2; i <= n; i++ {
				lv *= float64(i)
			}
			return lv
		}
		return gamma(lv + 1)
	case Ocom:
		return float64(^tobig(lv))
	case Oas, Odas:
		l.dec.val = rv
		return rv
	case Oplus:
		return lv
	case Ominus:
		return -lv
	case Oinv:
		return 1 / lv
	case Ocomma:
		return rv
	case Oquest:
		if truth(lv) {
			return eval(r.left)
		}
		return eval(r.right)
	case Onand:
		return b2f(toint(lv)&toint(rv) == 0)
	case Onor:
		return b2f(toint(lv)|toint(rv) == 0)
	case Oimp:
		return float64(b2i(!truth(lv)) | toint(rv))
	case Oimpby:
		return float64(toint(lv) | b2i(!truth(rv)))
	case Oiff:
		return b2f(toint(lv)^toint(rv) == 0)
	}
	fatal(fmt.Sprintf("case %s in eval", opstring(o)))
	return 0
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func libfun(o int, a *Node) float64 {
	switch o {
	case Osolve:
		return solve(a)
	case Osigma, Opi, Ocfrac:
		return series(o, a)
	case Oderiv:
		return differential(a)
	case Ointeg:
		return integral(a)
	}
	var a1, a2 float64
	if a != nil && a.op == Ocomma {
		a1 = eval(a.left)
		a2 = eval(a.right)
	} else {
		a1 = eval(a)
	}
	switch o {
	case Olog:
		return math.Log(a1)
	case Olog10:
		return math.Log10(a1)
	case Olog2:
		return math.Log(a1) / math.Log(2)
	case Ologb:
		return math.Log(a1) / math.Log(a2)
	case Oexpf:
		return math.Exp(a1)
	case Opow:
		return math.Pow(a1, a2)
	case Osqrt:
		return math.Sqrt(a1)
	case Ocbrt:
		return math.Cbrt(a1)
	case Ofloor:
		return math.Floor(a1)
	case Oceil:
		return math.Ceil(a1)
	case Omin:
		return math.Min(a1, a2)
	case Omax:
		return math.Max(a1, a2)
	case Oabs:
		return math.Abs(a1)
	case Ogamma:
		return gamma(a1)
	case Osign:
		switch {
		case a1 > 0:
			return 1
		case a1 < 0:
			return -1
		}
		return 0
	case Oint:
		i, _ := math.Modf(a1)
		return float64(int32(i))
	case Ofrac:
		_, f := math.Modf(a1)
		return f
	case Oround:
		return math.RoundToEven(a1)
	case Oerf:
		return math.Erf(a1)
	case Osin:
		return math.Sin(d2r(a1))
	case Ocos:
		return math.Cos(d2r(a1))
	case Otan:
		return math.Tan(d2r(a1))
	case Oasin:
		return r2d(math.Asin(a1))
	case Oacos:
		return r2d(math.Acos(a1))
	case Oatan:
		return r2d(math.Atan(a1))
	case Oatan2:
		return r2d(math.Atan2(a1, a2))
	case Osinh:
		return math.Sinh(a1)
	case Ocosh:
		return math.Cosh(a1)
	case Otanh:
		return math.Tanh(a1)
	case Oasinh:
		return math.Asinh(a1)
	case Oacosh:
		return math.Acosh(a1)
	case Oatanh:
		return math.Atanh(a1)
	case Orand:
		return float64(rand.Int31n(big)) / big
	}
	fatal(fmt.Sprintf("case %s in libfun", opstring(o)))
	return 0
}

// series evaluates sigma, pi and cfrac: the first argument assigns the
// index's start, the second is its limit, the third the term.
func series(o int, a *Node) float64 {
	var p0, p1, q0, q1 float64
	l, r := a.left, a.right
	var v float64
	switch o {
	case Osigma:
		v = 0
	case Opi:
		v = 1
	default:
		p0, q1 = 0, 0
		p1, q0 = 1, 1
		v = inf
	}
	i := l.left.left.dec
	ov := i.val
	i.val = eval(l.left.right)
	eq := 0
	for {
		rv := eval(l.right)
		if i.val > rv {
			break
		}
		lv := v
		ev := eval(r)
		switch o {
		case Osigma:
			v += ev
		case Opi:
			v *= ev
		default:
			t := ev*p1 + p0
			p0, p1 = p1, t
			t = ev*q1 + q0
			q0, q1 = q1, t
			v = p1 / q1
		}
		if v == lv && rv == inf {
			eq++
			if eq > 100 {
				break
			}
		} else {
			eq = 0
		}
		i.val++
	}
	i.val = ov
	return v
}

// params lists a function's parameter or argument nodes in order.
func params(n *Node, l []*Node) []*Node {
	if n == nil {
		return l
	}
	if n.op == Ocomma {
		return params(n.right, params(n.left, l))
	}
	return append(l, n)
}

// call evaluates the arguments, saves the parameters' values, binds
// them, runs the body and restores them.
func call(d *Dec, a *Node) float64 {
	var vals []float64
	for _, n := range params(a, nil) {
		vals = append(vals, eval(n))
	}
	ps := params(d.code.left, nil)
	saved := make([]float64, len(ps))
	for i, p := range ps {
		saved[i] = p.dec.val
		p.dec.val = vals[i]
	}
	k, v := estat(d.code.right)
	for i, p := range ps {
		p.dec.val = saved[i]
	}
	if k == Oexit {
		panic(exitError{})
	}
	return v
}

func print(n *Node) float64 {
	if n == nil {
		return 0
	}
	if n.op == Ocomma {
		print(n.left)
		return print(n.right)
	}
	if n.op == Ostring {
		out.WriteString(n.str)
		return 0
	}
	v := eval(n)
	printnum(v, "")
	return v
}

func read(n *Node) float64 {
	if n == nil {
		return 0
	}
	if n.op == Ocomma {
		read(n.left)
		return read(n.right)
	}
	fmt.Fprintf(out, "%s ? ", n.dec.sym.name)
	out.Flush()
	var bio *bufio.Reader
	if !usestdin {
		bio = stdinReader()
		stack("", bio)
	}
	lexnum()
	consume(Onl)
	n.dec.val = lexval
	if !usestdin && in.bin == bio {
		unstack()
	}
	return n.dec.val
}

var stdinBuf *bufio.Reader

func stdinReader() *bufio.Reader {
	if stdinBuf == nil {
		stdinBuf = bufio.NewReader(os.Stdin)
	}
	return stdinBuf
}

func isint(v float64) bool { return v >= -maxint && v <= maxint }

func isinteger(v float64) bool { return isint(v) && v == float64(toint(v)) }

// split returns the integer part (rounded down) and the fraction of a
// non-negative number.
func split(v float64) (int, float64) {
	n := toint(v)
	if float64(n) > v {
		n--
	}
	return n, v - float64(n)
}

func n2c(n int) byte {
	if n < 10 {
		return byte(n + '0')
	}
	return byte(n - 10 + 'a')
}

func gamma(v float64) float64 {
	lg, s := math.Lgamma(v)
	return float64(s) * math.Exp(lg)
}

func d2r(a float64) float64 {
	if deg.val != 0 {
		a *= math.Pi / 180
	}
	return a
}

func r2d(a float64) float64 {
	if deg.val != 0 {
		a /= math.Pi / 180
	}
	return a
}

func sametree(n1, n2 *Node) bool {
	if n1 == n2 {
		return true
	}
	if n1 == nil || n2 == nil || n1.op != n2.op {
		return false
	}
	switch n1.op {
	case Ostring:
		return n1.str == n2.str
	case Onum:
		return n1.val == n2.val
	case Ocon, Ovar:
		return n1.dec == n2.dec
	case Ofun, Olfun:
		return n1.dec == n2.dec && sametree(n1.left, n2.left)
	}
	return sametree(n1.left, n2.left) && sametree(n1.right, n2.right)
}

func iscon(n *Node) bool  { return n.op == Onum || n.op == Ocon }
func iszero(n *Node) bool { return iscon(n) && eval(n) == 0 }
func isone(n *Node) bool  { return iscon(n) && eval(n) == 1 }
func isnan(n *Node) bool  { return iscon(n) && math.IsNaN(eval(n)) }

func simplify(n *Node) *Node {
	if n == nil {
		return nil
	}
	op := n.op
	n.left = simplify(n.left)
	n.right = simplify(n.right)
	l, r := n.left, n.right
	if l != nil && iscon(l) && (r == nil || iscon(r)) {
		if isnan(l) {
			return l
		}
		if r != nil && isnan(r) {
			return r
		}
		return vtree(eval(n))
	}
	switch op {
	case Onum, Ocon, Ovar, Olfun, Ocomma:
		return n
	case Oplus:
		return l
	case Ominus:
		if l.op == Ominus {
			return l.left
		}
	case Oinv:
		if l.op == Oinv {
			return l.left
		}
	case Oadd:
		switch {
		case iszero(l):
			return r
		case iszero(r):
			return l
		case sametree(l, r):
			return tree(Omul, itree(2), l)
		}
	case Osub:
		switch {
		case iszero(l):
			return simplify(tree(Ominus, r, nil))
		case iszero(r):
			return l
		case sametree(l, r):
			return itree(0)
		}
	case Omul:
		switch {
		case iszero(l):
			return l
		case iszero(r):
			return r
		case isone(l):
			return r
		case isone(r):
			return l
		case sametree(l, r):
			return tree(Oexp, l, itree(2))
		}
	case Odiv:
		switch {
		case iszero(l):
			return l
		case iszero(r):
			return vtree(inf)
		case isone(l):
			return ptree(r, -1)
		case isone(r):
			return l
		case sametree(l, r):
			return itree(1)
		}
	case Oexp:
		switch {
		case iszero(l):
			return l
		case iszero(r):
			return itree(1)
		case isone(l):
			return l
		case isone(r):
			return l
		}
	default:
		fatal(fmt.Sprintf("case %s in simplify", opstring(op)))
	}
	return n
}

func deriv(n *Node, d *Dec) *Node {
	if n == nil {
		return nil
	}
	op, l, r := n.op, n.left, n.right
	switch op {
	case Onum, Ocon:
		n = itree(0)
	case Ovar:
		if d == n.dec {
			n = itree(1)
		} else {
			n = itree(0)
		}
	case Olfun:
		switch int(n.dec.val) {
		case Olog:
			n = ptree(l, -1)
		case Olog10:
			n = ptree(tree(Omul, l, vtree(ln10)), -1)
		case Olog2:
			n = ptree(tree(Omul, l, vtree(ln2)), -1)
		case Oexpf:
		case Opow:
			return deriv(tree(Oexp, l.left, l.right), d)
		case Osqrt:
			return deriv(tree(Oexp, l, vtree(0.5)), d)
		case Ocbrt:
			return deriv(tree(Oexp, l, vtree(1.0/3.0)), d)
		case Osin:
			n = ltree("cos", l)
		case Ocos:
			n = tree(Ominus, ltree("sin", l), nil)
		case Otan:
			n = ptree(ltree("cos", l), -2)
		case Oasin:
			n = ptree(tree(Osub, itree(1), ptree(l, 2)), -0.5)
		case Oacos:
			n = tree(Ominus, ptree(tree(Osub, itree(1), ptree(l, 2)), -0.5), nil)
		case Oatan:
			n = ptree(tree(Oadd, itree(1), ptree(l, 2)), -1)
		case Osinh:
			n = ltree("cosh", l)
		case Ocosh:
			n = ltree("sinh", l)
		case Otanh:
			n = ptree(ltree("cosh", l), -2)
		case Oasinh:
			n = ptree(tree(Oadd, itree(1), ptree(l, 2)), -0.5)
		case Oacosh:
			n = ptree(tree(Osub, ptree(l, 2), itree(1)), -0.5)
		case Oatanh:
			n = ptree(tree(Osub, itree(1), ptree(l, 2)), -1)
		default:
			return vtree(nan)
		}
		return tree(Omul, n, deriv(l, d))
	case Oplus, Ominus:
		n = tree(op, deriv(l, d), nil)
	case Oinv:
		n = tree(Omul, tree(Ominus, ptree(l, -2), nil), deriv(l, d))
	case Oadd, Osub, Ocomma:
		n = tree(op, deriv(l, d), deriv(r, d))
	case Omul:
		n = tree(Oadd, tree(Omul, deriv(l, d), r), tree(Omul, l, deriv(r, d)))
	case Odiv:
		n = tree(Osub, tree(Omul, deriv(l, d), r), tree(Omul, l, deriv(r, d)))
		n = tree(Odiv, n, ptree(r, 2))
	case Oexp:
		nn := tree(Oadd, tree(Omul, deriv(l, d), tree(Odiv, r, l)), tree(Omul, ltree("log", l), deriv(r, d)))
		n = tree(Omul, n, nn)
	default:
		n = vtree(nan)
	}
	return n
}

func derivative(n *Node, d *Dec) *Node {
	n = simplify(deriv(n, d))
	if isnan(n) {
		errorf(n, "no derivative")
	}
	if debug {
		prnode(n)
	}
	return n
}

func newton(f, e *Node, d *Dec, v1, v2 float64) (bool, float64) {
	v := (v1 + v2) / 2
	lv := 0.0
	for its := 0; ; its++ {
		lv = v
		d.val = v
		v = eval(e)
		if math.IsNaN(v) {
			return false, 0
		}
		if its > 100 || math.Abs(v-lv) < eps {
			break
		}
	}
	if math.Abs(v-lv) > bigeps || math.Abs(eval(f)) > bigeps {
		return false, 0
	}
	return true, v
}

func solve(n *Node) float64 {
	var d *Dec
	if n == nil {
		return nan
	}
	if n.op == Ocomma {
		isvar(n.right)
		d = n.right.dec
		n = n.left
		if !varmem(n, d) {
			errorf(n, "variable not in equation")
		}
	} else {
		d = findvar(n, nil)
		if d == nil {
			errorf(n, "variable missing")
		}
		if d == errdec {
			errorf(n, "one variable only required")
		}
	}
	if n.op == Oeq {
		n.op = Osub
	}
	dn := derivative(n, d)
	x := tree(Ovar, nil, nil)
	x.dec = d
	nr := tree(Osub, x, tree(Odiv, n, dn))
	ov := d.val
	lim := lookup(limitS).dec.val
	step := lookup(stepS).dec.val
	rval := inf
	d.val = -lim - step
	v1 := 0.0
	v2 := eval(n)
	for v := -lim; v <= lim; v += step {
		d.val = v
		v1 = v2
		v2 = eval(n)
		if math.IsNaN(v2) {
			continue
		}
		if math.Abs(v2) < eps {
			if v >= -lim && v <= lim && v != rval {
				printnum(v, " ")
				rval = v
			}
		} else if v1*v2 <= 0 {
			f, rv := newton(n, nr, x.dec, v-step, v)
			if f && rv >= -lim && rv <= lim && rv != rval {
				printnum(rv, " ")
				rval = rv
			}
		}
	}
	d.val = ov
	if rval == inf {
		errorf(n, "no roots found")
	}
	out.WriteString("\n")
	return rval
}

func differential(n *Node) float64 {
	x := n.left.left.dec
	ov := x.val
	v := evalx(derivative(n.right, x), x, eval(n.left.right))
	x.val = ov
	return v
}

// integral uses Simpson's rule, halving the step until the area settles.
func integral(n *Node) float64 {
	l, r := n.left, n.right
	x := l.left.left.dec
	ov := x.val
	a := eval(l.left.right)
	b := eval(l.right)
	h := b - a
	end := evalx(r, x, a) + evalx(r, x, b)
	odd, even := 0.0, 0.0
	oldarea := 0.0
	area := h * end / 2
	for i := 1; i < 1<<16; i <<= 1 {
		even += odd
		odd = 0
		xv := a + h/2
		for j := 0; j < i; j++ {
			odd += evalx(r, x, xv)
			xv += h
		}
		h /= 2
		oldarea = area
		area = h * (end + 4*odd + 2*even) / 3
		if math.IsNaN(area) {
			errorf(n, "integral not found")
		}
		if math.Abs(area-oldarea) < eps {
			break
		}
	}
	if math.Abs(area-oldarea) > bigeps {
		errorf(n, "integral not found")
	}
	x.val = ov
	return area
}

func evalx(n *Node, d *Dec, v float64) float64 {
	d.val = v
	return eval(n)
}

func findvar(n *Node, d *Dec) *Dec {
	if n == nil {
		return d
	}
	d = findvar(n.left, d)
	d = findvar(n.right, d)
	if n.op == Ovar {
		if d == nil {
			d = n.dec
		}
		if n.dec != d {
			d = errdec
		}
	}
	return d
}

func varmem(n *Node, d *Dec) bool {
	if n == nil {
		return false
	}
	if n.op == Ovar {
		return d == n.dec
	}
	return varmem(n.left, d) || varmem(n.right, d)
}

// cvt formats v in base: %g style for base 10, otherwise digits with a
// base prefix (16r1f) and an E exponent for non-integers out of range.
func cvt(v float64, base int) string {
	if base == 10 {
		return gfmt(v)
	}
	neg := false
	if v < 0 {
		neg = true
		v = -v
	}
	if !isint(v) {
		var n int
		lg := math.Log(v) / math.Log(float64(base))
		if lg < 0 {
			n, _ = split(-lg)
			v *= ipow(float64(base), n)
			n = -n
		} else {
			n, _ = split(lg)
			v /= ipow(float64(base), n)
		}
		s := cvt(v, base) + "E" + strconv.Itoa(n)
		if neg {
			s = "-" + s
		}
		return s
	}
	n, f := split(v)
	var s []byte
	for {
		s = append(s, n2c(n%base))
		n /= base
		if n == 0 {
			break
		}
	}
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	if f != 0 {
		s = append(s, '.')
		for i := 0; i < 16 && f != 0; i++ {
			f *= float64(base)
			n, f = split(f)
			s = append(s, n2c(n))
		}
	}
	r := strconv.Itoa(base) + "r" + string(s)
	if neg {
		r = "-" + r
	}
	return r
}

// gfmt formats a number as Inferno's %g does: the shortest digits that
// read back exactly, no leading zero before the point, and an exponent
// of at least two digits.
func gfmt(v float64) string {
	sign := ""
	if math.Signbit(v) && !math.IsNaN(v) {
		sign = "-"
		v = -v
	}
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 0) {
		return sign + "Infinity"
	}
	if v == 0 {
		return sign + "0"
	}
	e := strconv.FormatFloat(v, 'e', -1, 64)
	mant, exps, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exps)
	decpt := x + 1
	var b strings.Builder
	b.WriteString(sign)
	switch {
	case decpt <= -4 || decpt > len(digits)+5:
		b.WriteByte(digits[0])
		if len(digits) > 1 {
			b.WriteString("." + digits[1:])
		}
		decpt--
		if decpt < 0 {
			b.WriteString("e-")
			decpt = -decpt
		} else {
			b.WriteString("e+")
		}
		fmt.Fprintf(&b, "%02d", decpt)
	case decpt <= 0:
		b.WriteString("." + strings.Repeat("0", -decpt) + digits)
	default:
		if decpt >= len(digits) {
			b.WriteString(digits + strings.Repeat("0", decpt-len(digits)))
		} else {
			b.WriteString(digits[:decpt] + "." + digits[decpt:])
		}
	}
	return b.String()
}

func printnum(v float64, s string) {
	base := toint(pbase.val)
	if !isinteger(pbase.val) || base < 2 || base > 36 {
		base = 10
	}
	out.WriteString(cvt(v, base) + s)
	if bits {
		u := math.Float64bits(v)
		for i := 7; i >= 0; i-- {
			fmt.Fprintf(out, "%2.2x ", byte(u>>(8*i)))
		}
		out.WriteString("\n")
	}
}

// Expression printing for -d.
const (
	pLeft = 1 << iota
	pRight
	pPre
	pPost
)

var (
	lspace = []bool{false, false, true, true, true, true, false, false, false, true, true, false, false, false, false, false, false, false}
	rspace = []bool{false, true, true, true, true, true, false, false, false, true, true, false, false, false, false, false, false, false}
)

func preced(op1, op2, s int) bool {
	p1, p2 := prec(op1), prec(op2)
	if p1 > p2 {
		return true
	}
	if p1 < p2 {
		return false
	}
	var br bool
	if op1 == op2 {
		if rassoc(op1) {
			br = s == pLeft
		} else {
			br = s == pRight && !assoc(op1)
		}
		return br
	}
	if rassoc(op1) {
		br = s == pLeft
	} else {
		br = s == pRight && op1 != Oadd
	}
	if postunary(op1) && preunary(op2) {
		br = true
	}
	return br
}

func prnode(n *Node) {
	pnode(n, Onothing, pPre)
	out.WriteString("\n")
}

func pnode(n *Node, opp, s int) {
	if n == nil {
		return
	}
	op := n.op
	br := preced(opp, op, s)
	if br {
		out.WriteString("(")
	}
	switch {
	case op == Oas && n.right.op >= Oadd && n.right.op <= Orsh && n.left == n.right.left:
		pnode(n.left, op, pLeft)
		fmt.Fprintf(out, " %s ", opstring(n.right.op+Oadde-Oadd))
		pnode(n.right.right, op, pRight)
	case binary(op):
		p := prec(op)
		pnode(n.left, op, pLeft)
		if lspace[p] {
			out.WriteString(" ")
		}
		out.WriteString(opstring(op))
		if rspace[p] {
			out.WriteString(" ")
		}
		pnode(n.right, op, pRight)
	case op == Oinv:
		out.WriteString(opstring(op))
		pnode(n.left, Odiv, pRight)
	case preunary(op):
		out.WriteString(opstring(op))
		pnode(n.left, op, pPre)
	case postunary(op):
		pnode(n.left, op, pPost)
		out.WriteString(opstring(op))
	default:
		switch op {
		case Ostring:
			out.WriteString(n.str)
		case Onum:
			out.WriteString(gfmt(n.val))
		case Ocon, Ovar:
			out.WriteString(n.dec.sym.name)
		case Ofun, Olfun:
			out.WriteString(n.dec.sym.name + "(")
			pnode(n.left, Onothing, pPre)
			out.WriteString(")")
		default:
			fatal(fmt.Sprintf("bad op %s in pnode()", opstring(op)))
		}
	}
	if br {
		out.WriteString(")")
	}
}
