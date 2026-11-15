package main

import "strconv"

// Symbol table: a hash table whose buckets keep Inferno's order, which
// env and whatis show.

const shash = 31

// Symbol tags.
const (
	Svalue = iota
	Sfunc
	Sbuiltin
)

// Symb is a variable, function and builtin of one name. A variable
// assigned the null list is set but empty.
type Symb struct {
	name    string
	value   []string
	isvar   bool
	empty   bool
	fn      *Cmd
	builtin *Module
}

// Stab is a symbol table. Each bucket lists its newest symbol first.
type Stab struct {
	tab [shash][]*Symb
}

// fun1 is Hash->fun1.
func fun1(s string, n int) int {
	var h int32
	for _, c := range s {
		h = 65599*h + int32(c)
	}
	return int(h&0x7fffffff) % n
}

func newStab() *Stab { return &Stab{} }

// clone copies t, so that changes to either are not seen in the other.
func (t *Stab) clone() *Stab {
	n := &Stab{}
	for i, l := range t.tab {
		for _, s := range l {
			c := *s
			n.tab[i] = append(n.tab[i], &c)
		}
	}
	return n
}

func (t *Stab) update(s string, tag int, v []string, empty bool, f *Cmd, b *Module) *Symb {
	x := fun1(s, shash)
	for _, h := range t.tab[x] {
		if h.name == s {
			switch tag {
			case Svalue:
				h.value, h.empty, h.isvar = v, empty, v != nil || empty
			case Sfunc:
				h.fn = f
			case Sbuiltin:
				h.builtin = b
			}
			return h
		}
	}
	n := &Symb{name: s}
	switch tag {
	case Svalue:
		n.value, n.empty, n.isvar = v, empty, v != nil || empty
	case Sfunc:
		n.fn = f
	case Sbuiltin:
		n.builtin = b
	}
	t.tab[x] = append([]*Symb{n}, t.tab[x]...)
	return n
}

// all lists t's symbols in the order Inferno's Stab.all gives.
func (t *Stab) all() []*Symb {
	var r []*Symb
	for i := shash - 1; i >= 0; i-- {
		l := t.tab[i]
		for j := len(l) - 1; j >= 0; j-- {
			r = append(r, l[j])
		}
	}
	return r
}

// assign sets a variable; a null list is kept as set but empty.
func (t *Stab) assign(s string, v []string) {
	if len(v) == 0 {
		t.update(s, Svalue, nil, true, nil, nil)
		return
	}
	t.update(s, Svalue, v, false, nil, nil)
}

func (t *Stab) defbuiltin(s string, b *Module) { t.update(s, Sbuiltin, nil, false, nil, b) }
func (t *Stab) define(s string, f *Cmd)        { t.update(s, Sfunc, nil, false, f, nil) }

func (t *Stab) find(s string) *Symb {
	for _, h := range t.tab[fun1(s, shash)] {
		if h.name == s {
			return h
		}
	}
	return nil
}

// Env flags.
const (
	ETop = 1 << iota
	EInter
	EEcho
	ERaise
	EDumping
	ENoxeq
)

// Env is the state a command runs in. Copies share symbol tables; a
// clone has copies of them. An fd of -1 means the prog's own.
type Env struct {
	global, local *Stab
	flags         int
	in, out       int
	stderr        int
	file          *File
	args          []string
	level         int
}

func newEnv() *Env {
	return &Env{global: newStab(), flags: ETop, in: -1, out: -1, stderr: 2}
}

// clone makes an environment that is no longer top level or
// interactive, with its own symbol tables.
func (e *Env) clone() *Env {
	n := e.copy()
	n.flags &^= ETop | EInter
	n.global = n.global.clone()
	if n.local != nil {
		n.local = n.local.clone()
	}
	return n
}

func (e *Env) copy() *Env {
	n := *e
	return &n
}

// arg is $n while a make rule runs.
func (e *Env) arg(s string) string {
	n, _ := strconv.Atoi(s)
	if e.args == nil || n >= len(e.args) {
		return "$" + s
	}
	return e.args[n]
}

func (e *Env) builtin(s string) *Module {
	if v := e.global.find(s); v != nil {
		return v.builtin
	}
	return nil
}

func (e *Env) defbuiltin(s string, b *Module) { e.global.defbuiltin(s, b) }
func (e *Env) define(s string, f *Cmd)        { e.global.define(s, f) }

// dollar is the variable s: a local, else a global.
func (e *Env) dollar(s string) *Symb {
	if e.local != nil {
		if l := e.local.find(s); l != nil && l.isvar {
			return l
		}
	}
	if g := e.global.find(s); g != nil && g.isvar {
		return g
	}
	return nil
}

func (e *Env) fn(s string) *Cmd {
	if v := e.global.find(s); v != nil {
		return v.fn
	}
	return nil
}

// let assigns a local.
func (e *Env) let(s string, v []string) {
	if e.local == nil {
		e.local = newStab()
	}
	e.local.assign(s, v)
}

// set updates a local, or defines a global.
func (e *Env) set(s string, v []string) {
	if e.local != nil && e.local.find(s) != nil {
		e.local.assign(s, v)
	} else {
		e.global.assign(s, v)
	}
}

func (e *Env) undefined(s string) { e.report(s + ": undefined") }
