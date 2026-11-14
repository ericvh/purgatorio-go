package main

import (
	"sort"
)

// Context options, as in sh.m.
const (
	optINTERACTIVE = 1 << iota
	optVERBOSE
	optEXECPRINT
	optERROREXIT
)

// Listnode is one element of a list value: a word, or a parsed block
// (whose word, once computed, caches its text).
type Listnode struct {
	cmd  *Node
	word string
}

func wordnode(s string) *Listnode { return &Listnode{word: s} }

// Var flags.
const (
	varCHANGED = 1 << iota
	varNOEXPORT
)

type Var struct {
	name  string
	val   []*Listnode
	flags int
}

const envhashsize = 7

// Localenv is one scope of variables, pushed on entry to a block.
type Localenv struct {
	vars   [envhashsize][]*Var
	pushed *Localenv
	flags  int
}

// Module is a set of builtins, as Inferno's Shellbuiltin.
type Module interface {
	initbuiltin(c *Context) string
	runbuiltin(c *Context, args []*Listnode, last bool) string
	runsbuiltin(c *Context, args []*Listnode) []*Listnode
	whatis(c *Context, name string, wtype int) string
}

// whatis types.
const (
	wBUILTIN = iota
	wSBUILTIN
	wOTHER
)

type bentry struct {
	name string
	mods []Module
}

// Builtins is a table of names, kept sorted, each with a stack of the
// modules that define it.
type Builtins struct {
	ba []bentry
}

type bmod struct {
	name string
	mod  Module
}

type Environment struct {
	sbuiltins *Builtins
	builtins  *Builtins
	bmods     []bmod // most recently loaded first
	localenv  *Localenv
}

// Context is a shell's state: its variables and builtins, and the fds
// commands see (fd number in the command to fd in sh).
type Context struct {
	env *Environment
	fds *fdtab
}

// hashfn is sh.y's hash, with Limbo's 32-bit int.
func hashfn(s string, n int) int {
	var h int32
	for _, c := range s {
		h = 65599*h + c
	}
	return int(h&0x7fffffff) % n
}

func hashfind(ht *[envhashsize][]*Var, idx int, n string) *Var {
	for _, v := range ht[idx] {
		if v.name == n {
			return v
		}
	}
	return nil
}

func hashadd(ht *[envhashsize][]*Var, idx int, v *Var) {
	ht[idx] = append([]*Var{v}, ht[idx]...)
}

func noexport(name string) bool {
	return name == "0" || name == "*" || name == "status"
}

func varfind(e *Localenv, name string) *Var {
	idx := hashfn(name, envhashsize)
	for ; e != nil; e = e.pushed {
		if v := hashfind(&e.vars, idx, name); v != nil {
			return v
		}
	}
	return nil
}

func newlocalenv(pushed *Localenv) *Localenv {
	e := &Localenv{pushed: pushed}
	if pushed == nil {
		for _, kv := range envgetall() {
			hashadd(&e.vars, hashfn(kv[0], envhashsize), &Var{name: kv[0], val: envstringtoval(kv[1])})
		}
	} else {
		e.flags = pushed.flags
	}
	return e
}

func copylocalenv(e *Localenv) *Localenv {
	n := &Localenv{flags: e.flags}
	for ; e != nil; e = e.pushed {
		for i := range e.vars {
			for _, v := range e.vars[i] {
				idx := hashfn(v.name, envhashsize)
				if hashfind(&n.vars, idx, v.name) == nil {
					nv := *v
					hashadd(&n.vars, idx, &nv)
				}
			}
		}
	}
	return n
}

func (c *Context) set(name string, val []*Listnode) {
	e := c.env.localenv
	idx := hashfn(name, envhashsize)
	for {
		v := hashfind(&e.vars, idx, name)
		if v == nil {
			if e.pushed == nil {
				flags := varCHANGED
				if noexport(name) {
					flags |= varNOEXPORT
				}
				hashadd(&e.vars, idx, &Var{name: name, val: val, flags: flags})
				return
			}
		} else {
			v.val = val
			v.flags |= varCHANGED
			return
		}
		e = e.pushed
	}
}

func (c *Context) setlocal(name string, val []*Listnode) {
	e := c.env.localenv
	idx := hashfn(name, envhashsize)
	if v := hashfind(&e.vars, idx, name); v != nil {
		v.val = val
		v.flags |= varCHANGED
		return
	}
	flags := varCHANGED
	if noexport(name) {
		flags |= varNOEXPORT
	}
	hashadd(&e.vars, idx, &Var{name: name, val: val, flags: flags})
}

func (c *Context) get(name string) []*Listnode {
	if name == "" {
		return nil
	}
	idx := -1
	if name[0] > '0' && name[0] <= '9' {
		i := 0
		for i < len(name) && name[i] >= '0' && name[i] <= '9' {
			i++
		}
		if i >= len(name) {
			idx = limboAtoi(name) - 1
			name = "*"
		}
	}
	v := varfind(c.env.localenv, name)
	if v == nil {
		return nil
	}
	if idx != -1 {
		return index(v.val, idx)
	}
	return v.val
}

func index(val []*Listnode, k int) []*Listnode {
	if k < 0 {
		k = 0
	}
	if k >= len(val) {
		return nil
	}
	return []*Listnode{val[k]}
}

type envent struct {
	name string
	val  []*Listnode
}

// envlist returns every variable, innermost definition first, in
// sh.y's order.
func (c *Context) envlist() []envent {
	var t [envhashsize][]*Var
	for e := c.env.localenv; e != nil; e = e.pushed {
		for i := range e.vars {
			for _, v := range e.vars[i] {
				idx := hashfn(v.name, envhashsize)
				if hashfind(&t, idx, v.name) == nil {
					hashadd(&t, idx, v)
				}
			}
		}
	}
	var l []envent
	for i := range t {
		for _, v := range t[i] {
			l = append([]envent{{v.name, v.val}}, l...)
		}
	}
	return l
}

func (c *Context) push() { c.env.localenv = newlocalenv(c.env.localenv) }

func (c *Context) pop() {
	if c.env.localenv.pushed == nil {
		shpanic("unbalanced contexts in shell environment")
	}
	old := c.env.localenv.vars
	c.env.localenv = c.env.localenv.pushed
	for i := range old {
		for _, v := range old[i] {
			if ov := varfind(c.env.localenv, v.name); ov != nil {
				ov.flags |= varCHANGED
			} else {
				c.set(v.name, nil)
			}
		}
	}
}

func (c *Context) options() int { return c.env.localenv.flags }

func (c *Context) setoptions(flags int, on bool) int {
	old := c.env.localenv.flags
	if on {
		c.env.localenv.flags |= flags
	} else {
		c.env.localenv.flags &^= flags
	}
	return old
}

// fail prints err when verbose and raises the exception ename.
func (c *Context) fail(ename, err string) {
	if c.options()&optVERBOSE != 0 {
		c.eprint(err + "\n")
	}
	raise("fail:" + ename)
}

func (c *Context) diagnostic(s string) {
	if c.options()&optVERBOSE != 0 {
		c.eprint("sh: " + s + "\n")
	}
}

// export writes changed variables to the environment, outermost scope
// first.
func export(e *Localenv) {
	if e.pushed != nil {
		export(e.pushed)
	}
	for i := range e.vars {
		for _, v := range e.vars[i] {
			if v.flags&varCHANGED != 0 && v.flags&varNOEXPORT == 0 {
				setenv(v.name, v.val)
				v.flags &^= varCHANGED
			}
		}
	}
}

// Builtin tables.

func (b *Builtins) find(name string) (int, []Module) {
	i := sort.Search(len(b.ba), func(i int) bool { return b.ba[i].name >= name })
	if i < len(b.ba) && b.ba[i].name == name {
		return i, b.ba[i].mods
	}
	return i, nil
}

func (b *Builtins) copy() *Builtins {
	return &Builtins{ba: append([]bentry(nil), b.ba...)}
}

// add defines name in mod. Internal builtins are only covered, never
// replaced, and no module but sh itself defines "builtin".
func (b *Builtins) add(name string, mod Module) {
	if mod == nil || (name == "builtin" && mod != myself) {
		return
	}
	n, mods := b.find(name)
	if mods != nil {
		if mods[0] == myself {
			b.ba[n].mods = append([]Module{mod}, mods...)
		} else {
			b.ba[n].mods = []Module{mod}
		}
		return
	}
	b.ba = append(b.ba, bentry{})
	copy(b.ba[n+1:], b.ba[n:])
	b.ba[n] = bentry{name, []Module{mod}}
}

func (b *Builtins) remove(name string, mod Module) {
	n, mods := b.find(name)
	if mods == nil || mods[0] != mod {
		return
	}
	if len(mods) > 1 {
		b.ba[n].mods = mods[1:]
	} else {
		b.ba = append(b.ba[:n], b.ba[n+1:]...)
	}
}

func (b *Builtins) removemod(mod Module) {
	var nb []bentry
	for _, e := range b.ba {
		mods := e.mods
		if mods[0] == mod {
			mods = mods[1:]
		}
		if len(mods) > 0 {
			nb = append(nb, bentry{e.name, mods})
		}
	}
	b.ba = nb
}

func (c *Context) addbuiltin(name string, mod Module)     { c.env.builtins.add(name, mod) }
func (c *Context) removebuiltin(name string, mod Module)  { c.env.builtins.remove(name, mod) }
func (c *Context) addsbuiltin(name string, mod Module)    { c.env.sbuiltins.add(name, mod) }
func (c *Context) removesbuiltin(name string, mod Module) { c.env.sbuiltins.remove(name, mod) }

func modname(c *Context, mod Module) string {
	for _, b := range c.env.bmods {
		if b.mod == mod {
			return b.name
		}
	}
	return "builtin"
}

// List helpers.

// words returns the text of each element of nl, converting blocks.
func words(nl []*Listnode) []string {
	r := make([]string, len(nl))
	for i, el := range nl {
		r[i] = word(el)
	}
	return r
}

func word(el *Listnode) string {
	if el.word != "" || el.cmd == nil {
		return el.word
	}
	el.word = cmd2string(el.cmd)
	return el.word
}

func stringlist(sl []string) []*Listnode {
	r := make([]*Listnode, len(sl))
	for i, s := range sl {
		r[i] = wordnode(s)
	}
	return r
}

func setstatus(c *Context, val string) string {
	c.setlocal("status", []*Listnode{wordnode(val)})
	return val
}

// quoted makes a list into one string that parses back to it; with
// quoteblocks, blocks are quoted too.
func quoted(val []*Listnode, quoteblocks bool) string {
	s := ""
	for i, el := range val {
		if el.cmd == nil || (quoteblocks && el.word != "") {
			s += quote(el.word, false)
		} else {
			cmd := cmd2string(el.cmd)
			if quoteblocks {
				cmd = quote(cmd, false)
			}
			s += cmd
		}
		if i < len(val)-1 {
			s += " "
		}
	}
	return s
}

func squash(l []string, sep string) string {
	s := ""
	for i, w := range l {
		if i > 0 {
			s += sep
		}
		s += w
	}
	return s
}

// limboAtoi converts s as Limbo's int of a string does: optional blanks
// and sign, then leading decimal digits.
func limboAtoi(s string) int { return int(int32(limboAtob(s))) }

func limboAtob(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	var n int64
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int64(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}
