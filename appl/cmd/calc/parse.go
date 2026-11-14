package main

// Node is a parse tree node.
type Node struct {
	op          int
	left, right *Node
	val         float64
	str         string
	dec         *Dec
	src         int
}

// Dec is a declaration of a name in a scope: a constant, a variable, a
// function or a library function.
type Dec struct {
	kind  int
	scope int
	sym   *Sym
	val   float64
	na    int
	code  *Node
	old   *Dec
	next  *Dec
}

// Sym is a name and its current declaration.
type Sym struct {
	name string
	kind int
	dec  *Dec
}

var (
	syms     = map[string]*Sym{}
	inloop   int
	infn     int
	scope    int
	curscope *Dec
	scopes   []*Dec
)

func elex() int {
	t := lex()
	if binary(t) {
		return t
	}
	if hexp(t) {
		unlex(t)
		return Oscomma
	}
	return t
}

func hexp(o int) bool      { return preunary(o) || o == Olbr || atom(o) }
func atom(o int) bool      { return o >= Ostring && o <= Olfun }
func preunary(o int) bool  { return ops[o]&Preunary != 0 }
func postunary(o int) bool { return ops[o]&Postunary != 0 }
func binary(o int) bool    { return ops[o]&Binary != 0 }
func prec(o int) int       { return ops[o] & Prec }
func assoc(o int) bool     { return ops[o]&Assoc != 0 }
func rassoc(o int) bool    { return ops[o]&Rassoc != 0 }

func asop(o int) bool {
	return o == Oas || o == Odas || o >= Oadde && o <= Orshe || o >= Oinc && o <= Opostdec
}

func preop(o int) int {
	switch o {
	case Oadd:
		return Oplus
	case Osub:
		return Ominus
	case Oinc:
		return Opreinc
	case Odec:
		return Opredec
	case Oexc:
		return Onot
	}
	return o
}

func postop(o int) int {
	switch o {
	case Oinc:
		return Opostinc
	case Odec:
		return Opostdec
	case Oexc:
		return Ofact
	}
	return o
}

// tree makes a node; x op= y becomes x = x op y.
func tree(o int, l, r *Node) *Node {
	p := &Node{op: o, left: l, right: r, src: in.lineno}
	if o >= Oadde && o <= Orshe {
		p = tree(Oas, l, p)
		p.right.op += Oadd - Oadde
	}
	return p
}

func itree(n int) *Node { return vtree(float64(n)) }

func vtree(v float64) *Node {
	n := tree(Onum, nil, nil)
	n.val = v
	return n
}

func ltree(s string, a *Node) *Node {
	n := tree(Olfun, a, nil)
	n.dec = lookup(s).dec
	return n
}

func ptree(n *Node, p float64) *Node {
	if isinteger(p) {
		i := toint(p)
		switch {
		case i == 0:
			return itree(1)
		case i == 1:
			return n
		case i == -1:
			return tree(Oinv, n, nil)
		case i < 0:
			return tree(Oinv, tree(Oexp, n, itree(-i)), nil)
		}
	}
	return tree(Oexp, n, vtree(p))
}

func stat(scoped bool) *Node {
	var e1, e2, e3, e4 *Node
	consume(Onl)
	t := lex()
	switch t {
	case Olcbr:
		if scoped {
			pushscope()
		}
		for {
			e2 = stat(true)
			if e1 == nil {
				e1 = e2
			} else {
				e1 = tree(Ocomma, e1, e2)
			}
			consume(Onl)
			t = lex()
			if t == Oeof {
				panic(eofError{})
			}
			if t == Orcbr {
				break
			}
			unlex(t)
		}
		if scoped {
			popscope()
		}
		return e1
	case Oprint, Oread, Oret:
		if t == Oret && infn == 0 {
			errorf(nil, "return not in fn")
		}
		e1 = tree(t, expr(0, true), nil)
		consume(Oscolon)
		if t == Oread {
			allvar(e1.left)
		}
		return e1
	case Oif:
		e1 = expr(0, true)
		e2 = stat(true)
		consume(Onl)
		t = lex()
		if t == Oelse {
			e3 = stat(true)
		} else {
			unlex(t)
		}
		return tree(Oif, e1, tree(Ocomma, e2, e3))
	case Ofor:
		inloop++
		mustbe(Olbr)
		e1 = expr(0, true)
		mustbe(Oscolon)
		e2 = expr(0, true)
		mustbe(Oscolon)
		e3 = expr(0, true)
		mustbe(Orbr)
		e4 = stat(true)
		inloop--
		return tree(Ocomma, e1, tree(Ofor, e2, tree(Ocomma, e4, e3)))
	case Owhile:
		inloop++
		e1 = expr(0, true)
		e2 = stat(true)
		inloop--
		return tree(Ofor, e1, tree(Ocomma, e2, nil))
	case Odo:
		inloop++
		e1 = stat(true)
		consume(Onl)
		mustbe(Owhile)
		e2 = expr(0, true)
		consume(Oscolon)
		inloop--
		return tree(Odo, e1, e2)
	case Obreak, Ocont, Oexit:
		if (t == Obreak || t == Ocont) && inloop == 0 {
			errorf(nil, "break/continue not in loop")
		}
		consume(Oscolon)
		return tree(t, nil, nil)
	case Ofn:
		if infn != 0 {
			errorf(nil, "nested functions not allowed")
		}
		infn++
		mustbe(Oident)
		s := lexsym
		d := mkdec(s, Ofun, true)
		d.code = tree(Ofn, nil, nil)
		pushscope()
		d.na, d.code.left = args(0)
		allvar(d.code.left)
		pushparams(d.code.left)
		d.code.right = stat(false)
		popscope()
		infn--
		return d.code
	case Oinclude:
		e1 = expr(0, false)
		if e1.op != Ostring {
			errorf(nil, "bad include file")
		}
		consume(Oscolon)
		doinclude(e1.str)
		return nil
	}
	unlex(t)
	e1 = expr(0, true)
	consume(Oscolon)
	if debug {
		prnode(e1)
	}
	return e1
}

func ckstat(n *Node, parop int, pr bool) {
	if n == nil {
		return
	}
	pr = pr || n.op == Oprint
	ckstat(n.left, n.op, pr)
	ckstat(n.right, n.op, pr)
	if n.op == Ostring && (!pr || parop != Oprint && parop != Ocomma) {
		errorf(n, "illegal string operation")
	}
}

// pexp reports whether a statement's value should be printed.
func pexp(e *Node) bool {
	if e == nil {
		return false
	}
	if e.op == Ocomma {
		return pexp(e.right)
	}
	return e.op >= Ostring && e.op <= Oiff && !asop(e.op)
}

func expr(p int, zok bool) *Node {
	n := exp(p, zok)
	ckexp(n, Onothing)
	return n
}

func exp(p int, zok bool) *Node {
	l := prim(zok)
	if l == nil {
		return nil
	}
	var t int
	for {
		t = elex()
		if !binary(t) || prec(t) < p {
			break
		}
		o := prec(t)
		var r *Node
		if rassoc(t) {
			r = exp(o, false)
		} else {
			r = exp(o+1, false)
		}
		if t == Oscomma {
			t = Ocomma
		}
		l = tree(t, l, r)
	}
	if t != Oscomma {
		unlex(t)
	}
	return l
}

func prim(zok bool) *Node {
	var p *Node
	t := lex()
	if preunary(t) {
		t = preop(t)
		return tree(t, exp(prec(t), false), nil)
	}
	switch t {
	case Olbr:
		p = exp(0, zok)
		mustbe(Orbr)
	case Ostring:
		p = tree(t, nil, nil)
		p.str = lexstr
	case Onum:
		p = tree(t, nil, nil)
		p.val = lexval
	case Oident:
		s := lexsym
		d := s.dec
		if d == nil {
			d = mkdec(s, Ovar, false)
		}
		t = d.kind
		p = tree(t, nil, nil)
		p.dec = d
		if t == Ofun || t == Olfun {
			var na int
			na, p.left = args(prec(t))
			if !(t == Olfun && d.val == Osolve && na == 2) && na != d.na {
				errorf(p, "wrong number of arguments")
			}
			if t == Olfun {
				switch int(d.val) {
				case Osigma, Opi, Ocfrac, Ointeg:
					if op := p.left.left.left.op; op != Oas && op != Odas {
						errorf(p.left, "expression not an assignment")
					}
				case Oderiv:
					if op := p.left.left.op; op != Oas && op != Odas {
						errorf(p.left, "expression not an assignment")
					}
				}
			}
		}
	default:
		unlex(t)
		if !zok {
			errorf(nil, "missing expression")
		}
		return nil
	}
	for {
		t = lex()
		if !postunary(t) {
			break
		}
		p = tree(postop(t), p, nil)
	}
	unlex(t)
	return p
}

func ckexp(n *Node, parop int) {
	if n == nil {
		return
	}
	o, l, r := n.op, n.left, n.right
	if asop(o) {
		isvar(l)
	}
	switch o {
	case Ovar:
		s := n.dec.sym
		d := s.dec
		if d == nil {
			if strict {
				errorf(n, "%s undefined", s.name)
			}
			d = mkdec(s, Ovar, true)
		}
		n.dec = d
	case Odas:
		ckexp(r, o)
		l.dec = mkdec(l.dec.sym, Ovar, true)
	default:
		ckexp(l, o)
		ckexp(r, o)
		if o == Oquest && r.op != Ocolon {
			errorf(n, "bad '?' operator")
		}
		if o == Ocolon && parop != Oquest {
			errorf(n, "bad ':' operator")
		}
	}
}

func commas(n *Node) int {
	if n == nil || n.op == Ofun || n.op == Olfun {
		return 0
	}
	c := commas(n.left) + commas(n.right)
	if n.op == Ocomma {
		c++
	}
	return c
}

func allvar(n *Node) {
	if n == nil {
		return
	}
	if n.op == Ocomma {
		allvar(n.left)
		allvar(n.right)
		return
	}
	isvar(n)
}

func args(p int) (int, *Node) {
	if p == 0 {
		mustbe(Olbr)
	}
	a := exp(p, true)
	if p == 0 {
		mustbe(Orbr)
	}
	na := 0
	if a != nil {
		na = commas(a) + 1
	}
	return na, a
}

func enter(name string, k int) *Sym {
	if s := syms[name]; s != nil {
		return s
	}
	s := &Sym{name: name, kind: k}
	syms[name] = s
	return s
}

func lookup(name string) *Sym { return enter(name, Oident) }

func mkdec(s *Sym, k int, declare bool) *Dec {
	d := &Dec{kind: k, sym: s}
	if declare {
		pushdec(d)
	}
	return d
}

func adddec(name string, k int, v float64, n int) *Dec {
	d := mkdec(enter(name, Oident), k, true)
	d.val = v
	d.na = n
	return d
}

func pushscope() {
	scope++
	scopes = append(scopes, curscope)
	curscope = nil
}

func popscope() {
	popdecs()
	curscope = scopes[len(scopes)-1]
	scopes = scopes[:len(scopes)-1]
	scope--
}

func pushparams(n *Node) {
	if n == nil {
		return
	}
	if n.op == Ocomma {
		pushparams(n.left)
		pushparams(n.right)
		return
	}
	n.dec = mkdec(n.dec.sym, Ovar, true)
}

func pushdec(d *Dec) {
	d.scope = scope
	s := d.sym
	if s.dec != nil && s.dec.scope == scope {
		errorf(nil, "redeclaration of %s", s.name)
	}
	d.old = s.dec
	s.dec = d
	d.next = curscope
	curscope = d
}

func popdecs() {
	var nd *Dec
	for d := curscope; d != nil; d = nd {
		d.sym.dec = d.old
		d.old = nil
		nd = d.next
		d.next = nil
	}
	curscope = nil
}

func isvar(e *Node) {
	if e == nil || e.op != Ovar || e.dec.kind != Ovar {
		errorf(e, "expected a variable")
	}
}
