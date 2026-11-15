package main

// Dependencies and rules (depends.b) and the make module (make.b).

import (
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const dhash = 127

// Depend is a dependency: targets, what they depend on, and maybe a
// rule to make them.
type Depend struct {
	targets []string
	depends []string
	op      int
	cmd     *Cmd
	mark    bool
}

// Target lists the dependencies of a target, newest first.
type Target struct {
	target  string
	depends []*Depend
}

// Lhs is a rule's pattern, split into path elements.
type Lhs struct {
	text  string
	elems []string
}

// Rule is an implicit rule.
type Rule struct {
	lhs *Lhs
	rhs *Item
	op  int
	cmd *Cmd
}

var (
	rules   []*Rule // newest first
	dephash [][]*Target
)

func initdep() {
	dephash = make([][]*Target, dhash)
	rules = nil
}

func tokenize(s, delim string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(delim, r) })
}

func target(s string, insert bool) *Target {
	h := fun1(s, dhash)
	for _, t := range dephash[h] {
		if t.target == s {
			return t
		}
	}
	if !insert {
		return nil
	}
	t := &Target{target: s}
	dephash[h] = append([]*Target{t}, dephash[h]...)
	return t
}

func findtarget(s string) *Target {
	if dephash == nil {
		return nil
	}
	return target(s, false)
}

// depend runs a dependency (:) command.
func (c *Cmd) depend(e *Env) {
	if e.flags&ETop == 0 {
		e.report("dependency not at top level")
		return
	}
	if dephash == nil {
		initdep()
	}
	w := pass1(e, c.words)
	if w == nil {
		return
	}
	l := pass2(e, w)
	if l == nil {
		return
	}
	var r []string
	if c.left.words != nil {
		w = pass1(e, c.left.words)
		if w == nil {
			return
		}
		r = pass2(e, w)
		if r == nil {
			return
		}
	}
	d := &Depend{targets: l, depends: r, op: c.left.op, cmd: c.left.left}
	for _, s := range l {
		t := target(s, true)
		t.depends = append([]*Depend{d}, t.depends...)
	}
}

func rulelhs(e *Env, i *Item) *Lhs {
	i = i.ieval1(e)
	if i == nil {
		return nil
	}
	s, l, _ := i.ieval2(e)
	if l != nil {
		e.report("rule pattern evaluates to a list")
		return nil
	}
	if s == "" {
		e.report("rule pattern evaluates to nil")
		return nil
	}
	return &Lhs{s, tokenize(s, "/")}
}

// rule runs a rule (:~) command; its right side gets its first pass
// now.
func (c *Cmd) rule(e *Env) {
	if e.flags&ETop == 0 {
		e.report("rule not at top level")
		return
	}
	l := rulelhs(e, c.item)
	if l == nil {
		return
	}
	r := c.left.item.ieval1(e)
	if r == nil {
		return
	}
	rules = append([]*Rule{{l, r, c.left.op, c.left.left}}, rules...)
}

// matchelem matches a path element against a pattern with *.
func matchelem(p, s string) bool {
	m, n := len(p), len(s)
	if m == n && p == s {
		return true
	}
	for i := 0; i < m; i++ {
		if p[i] == '*' {
			j := i + 1
			if j == m {
				return true
			}
			q := p[j:]
			for {
				if matchelem(q, s[min(i, n):]) {
					return true
				}
				i++
				if i >= n {
					return false
				}
			}
		} else if i >= n || p[i] != s[i] {
			return false
		}
	}
	return false
}

// matches matches a path element and returns what each * matched.
func matches(p, s string) (bool, []string) {
	m, n := len(p), len(s)
	for i := 0; i < m; i++ {
		if p[i] == '*' {
			j := i + 1
			if j == m {
				return true, []string{s[min(i, n):]}
			}
			q := p[j:]
			for {
				if r, l := matches(q, s[min(i, n):]); r {
					return true, append([]string{s[j-1 : min(i, n)]}, l...)
				}
				i++
				if i >= n {
					return false, nil
				}
			}
		} else if i >= n || p[i] != s[i] {
			return false, nil
		}
	}
	return m == n, nil
}

func (r *Rule) match(a bool, t []string) bool {
	l := r.lhs
	if len(l.elems) != len(t) || (l.text[0] == '/') != a {
		return false
	}
	for i, e := range l.elems {
		if !matchelem(e, t[i]) {
			return false
		}
	}
	return true
}

// matches returns $0, $1 ... for a target the rule matches, with $0
// left for the caller, or nil.
func (r *Rule) matches(t []string) []string {
	a := []string{""}
	for i, e := range r.lhs.elems {
		x, l := matches(e, t[i])
		if !x {
			return nil
		}
		a = append(a, l...)
	}
	return a
}

// rulematch lists the rules that match s.
func rulematch(s string) []*Rule {
	var m []*Rule
	a := strings.HasPrefix(s, "/")
	t := tokenize(s, "/")
	for _, r := range rules {
		if r.match(a, t) {
			m = append([]*Rule{r}, m...)
		}
	}
	return m
}

// The make module.

func init() {
	modules["make"] = &Module{name: "make", init: initMake, cmd: makecmd}
}

func initMake(e *Env, m *Module) {
	for _, s := range []string{"depends", "make", "match", "rules"} {
		e.defbuiltin(s, m)
	}
}

func makecmd(e *Env, l []string) {
	s, l := l[0], l[1:]
	var b strings.Builder
	switch s {
	case "depends":
		if len(l) == 0 {
			alldeps(&b)
		} else {
			depends(&b, l)
		}
		e.output(b.String())
	case "make":
		domake(e, l)
	case "match":
		domatch(e, l)
	case "rules":
		if len(l) == 0 {
			for _, r := range rules {
				prrule(&b, r)
			}
		} else {
			for _, s := range l {
				b.WriteString(s + ":\n")
				for _, r := range rulematch(s) {
					prrule(&b, r)
				}
			}
		}
		e.output(b.String())
	}
}

// Node states.
const (
	SUnknown = iota
	SNoexist
	SExist
	SStale
	SMade
	SDir
	SDirload
)

const fmark = 1

// Node is a file make knows about.
type Node struct {
	name  string
	state int
	flags int
	mtime int
}

// step is a step in a chain of implicit rules.
type step struct {
	r *Rule
	a []string
	n *Node
}

// mmatch is an implicit chain: the node it starts from and its steps,
// most recent first.
type mmatch struct {
	node *Node
	path []step
}

var ntab map[string]*Node

func initnodes() { ntab = map[string]*Node{} }

func getnode(s string) *Node {
	if n := ntab[s]; n != nil {
		return n
	}
	n := &Node{name: s}
	ntab[s] = n
	return n
}

func mkpath(d, s string) string {
	switch d {
	case ".":
		return s
	case "/":
		return "/" + s
	}
	return d + "/" + s
}

func loaddir(s string) {
	fd, err := sys.Open(s, sys.OREAD)
	if err != nil {
		return
	}
	defer closefd(fd)
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			break
		}
		for _, d := range ds {
			n := getnode(mkpath(s, d.Name))
			if d.Mode&sys.DMDIR != 0 {
				n.state = SDir
			} else {
				n.state = SExist
			}
			n.mtime = int(d.Mtime)
		}
	}
}

func loadfile(s string) *Node {
	n := getnode(s)
	switch n.state {
	case SUnknown:
		d, err := sys.Stat(s)
		if err == nil {
			n.mtime = int(d.Mtime)
			if d.Mode&sys.DMDIR != 0 {
				loaddir(s)
				n.state = SDirload
			} else {
				n.state = SExist
			}
		} else {
			n.state = SNoexist
		}
	case SDir:
		loaddir(s)
		n.state = SDirload
	}
	return n
}

// getfile gets the node for a file, loading the directories on its
// path.
func getfile(s string) *Node {
	for strings.HasPrefix(s, "./") {
		s = s[2:]
	}
	d := "."
	if strings.HasPrefix(s, "/") {
		d = "/"
		s = s[1:]
	}
	l := tokenize(s, "/")
	for {
		w := loadfile(d)
		if len(l) == 0 {
			return w
		}
		d = mkpath(d, l[0])
		l = l[1:]
	}
}

// propagate marks all the targets of a rule made.
func propagate(l []string) {
	if len(l) < 2 {
		return
	}
	for _, s := range l {
		getfile(s).state = SMade
	}
}

// explicit makes a node by its dependencies, or marks it stale.
// It returns -1 on a (reported) error, 0 on failure, 1 on success.
func explicit(e *Env, t *Target, n *Node) int {
	var d *Depend
	for _, x := range t.depends {
		if x.op != Cnop {
			if d != nil {
				e.report("make: too many rules for " + t.target)
				return -1
			}
			d = x
		}
	}
	for _, x := range t.depends {
		for _, s := range x.depends {
			m := getfile(s)
			r := mk(e, m, s)
			if r < 0 {
				e.output("don't know how to make " + s + "\n")
				return r
			}
			if m.state == SMade || m.mtime > n.mtime {
				n.state = SStale
			}
		}
	}
	if d == nil {
		return 0
	}
	if n.state == SNoexist || n.state == SStale {
		e = e.copy()
		e.flags |= EEcho | ERaise
		e.flags &^= EInter
		d.cmd.xeq(e)
		propagate(d.targets)
		n.state = SMade
	}
	return 1
}

func multimatch(e *Env, n *Node, l []mmatch) {
	e.report(strconv.Itoa(len(l)) + " rules match for " + n.name)
	var b strings.Builder
	for _, m := range l {
		b.WriteString(m.node.name)
		for _, p := range m.path {
			b.WriteString(" -> " + p.n.name)
		}
		b.WriteString("\n")
	}
	fprint(e.stderr, b.String())
}

func cycle(e *Env, n *Node) {
	e.report("make: cycle in dependencies for target " + n.name)
}

func markchain(e *Env, l []step) bool {
	for _, s := range l[:len(l)-1] {
		if s.n.flags&fmark != 0 {
			cycle(e, s.n)
			return false
		}
		s.n.flags |= fmark
	}
	return true
}

func unmarkchain(l []step) {
	for _, s := range l[:len(l)-1] {
		s.n.flags &^= fmark
	}
}

// xeqmatch runs a chain of implicit rules.
func xeqmatch(e *Env, b, n *Node, l []step) int {
	if !markchain(e, l) {
		return -1
	}
	e.args = nil
	if x := mk(e, n, n.name); x < 0 {
		e.output("don't know how to make " + n.name + "\n")
		return x
	}
	if n.state == SMade || n.mtime > b.mtime || b.state == SStale {
		e = e.copy()
		e.flags |= EEcho | ERaise
		e.flags &^= EInter
		for i, s := range l {
			e.args = s.a
			s.r.cmd.xeq(e)
			s.n.state = SMade
			if i == len(l)-1 {
				break
			}
			s.n.flags &^= fmark
		}
	} else {
		unmarkchain(l)
	}
	return 1
}

// implicit finds the shortest chain of implicit rules that makes base.
func implicit(e *Env, base *Node) int {
	var win, lose []mmatch
	cand := []mmatch{{base, nil}}
	for len(cand) > 0 {
		lose = nil
		for _, c := range cand {
			b, x := c.node, c.path
			s := b.name
			var l []*Rule
		exclude:
			for _, r := range rulematch(s) {
				for _, y := range x {
					if y.r == r {
						continue exclude
					}
				}
				l = append([]*Rule{r}, l...)
			}
			if l == nil {
				continue
			}
			t := tokenize(s, "/")
			for _, r := range l {
				a := r.matches(t)
				if a == nil {
					e.report("rule match cock up")
					return -1
				}
				a[0] = s
				e.args = a
				v, _, _ := r.rhs.ieval2(e)
				if v == "" {
					continue
				}
				y := append([]step{{r, a, b}}, x...)
				z := getfile(v)
				if z.state != SNoexist || findtarget(v) != nil {
					win = append([]mmatch{{z, y}}, win...)
				} else {
					lose = append([]mmatch{{z, y}}, lose...)
				}
			}
		}
		if win != nil {
			if len(win) > 1 {
				multimatch(e, base, win)
				return -1
			}
			return xeqmatch(e, base, win[0].node, win[0].path)
		}
		cand = lose
	}
	return 0
}

// mk makes a node. It returns -1 on a (reported) error, 0 on failure,
// 1 on success.
func mk(e *Env, n *Node, s string) int {
	if n == nil {
		n = getfile(s)
	}
	if n.state == SMade {
		return 1
	}
	if n.flags&fmark != 0 {
		cycle(e, n)
		return -1
	}
	n.flags |= fmark
	if t := findtarget(s); t != nil {
		if x := explicit(e, t, n); x != 0 {
			n.flags &^= fmark
			return x
		}
	}
	x := implicit(e, n)
	n.flags &^= fmark
	if x != 0 {
		return x
	}
	if n.state == SExist {
		return 0
	}
	return -1
}

var makelevel int

func domake(e *Env, l []string) {
	if e.flags&ETop == 0 {
		e.report("make not at top level")
		return
	}
	inited := makelevel > 0
	makelevel++
	defer func() { makelevel-- }()
	if len(l) == 0 {
		l = []string{"default"}
	}
	for _, s := range l {
		if strings.HasPrefix(s, "-") {
			if s == "-clear" {
				initdep()
			} else {
				e.report("make: unknown option: " + s)
			}
			continue
		}
		if !inited {
			initnodes()
			inited = true
		}
		if mk(e, nil, s) < 0 {
			e.output("don't know how to make " + s + "\n")
			panic(mashRaise("fail: make error"))
		}
	}
}

func prcmd(b *strings.Builder, op int, c *Cmd) {
	if op == Clistgroup {
		b.WriteString(":")
	}
	if c != nil {
		b.WriteString("{ " + c.text() + " }")
	} else {
		b.WriteString("{}")
	}
}

func domatch(e *Env, l []string) {
	var b strings.Builder
	e = e.copy()
	for _, s := range l {
		b.WriteString(s + ":\n")
		t := tokenize(s, "/")
		for _, r := range rulematch(s) {
			b.WriteString("\tlhs " + r.lhs.text + "\n")
			a := r.matches(t)
			if a == nil {
				b.WriteString("\tcock up\n")
				continue
			}
			a[0] = s
			for i, x := range a {
				b.WriteString("\t$" + strconv.Itoa(i) + " '" + x + "'\n")
			}
			e.args = a
			v, w, _ := r.rhs.ieval2(e)
			if v != "" {
				b.WriteString("\trhs '" + v + "'\n")
			} else {
				b.WriteString("\trhs list " + strconv.Itoa(len(w)) + "\n")
			}
			if r.cmd != nil {
				b.WriteString("\t")
				prcmd(&b, r.op, r.cmd)
				b.WriteString(";\n")
			}
		}
	}
	e.output(b.String())
}

func prwords(b *strings.Builder, l []string, pre bool) {
	for _, s := range l {
		if pre {
			b.WriteString(" ")
		}
		b.WriteString(quote(s))
		if !pre {
			b.WriteString(" ")
		}
	}
}

func prdep(b *strings.Builder, d *Depend) {
	prwords(b, d.targets, false)
	b.WriteString(":")
	prwords(b, d.depends, true)
	if d.op != Cnop {
		b.WriteString(" ")
		prcmd(b, d.op, d.cmd)
	}
	b.WriteString(";\n")
}

func alldeps(b *strings.Builder) {
	for p := 0; p < 2; p++ {
		for _, l := range dephash {
			for _, t := range l {
				for _, d := range t.depends {
					switch p {
					case 0:
						d.mark = false
					case 1:
						if !d.mark {
							prdep(b, d)
							d.mark = true
						}
					}
				}
			}
		}
	}
}

func depends(b *strings.Builder, l []string) {
	for _, s := range l {
		b.WriteString(s + ":\n")
		if t := findtarget(s); t != nil {
			for _, d := range t.depends {
				prdep(b, d)
			}
		}
	}
}

func prrule(b *strings.Builder, r *Rule) {
	b.WriteString(r.lhs.text + " :~ " + r.rhs.text() + " ")
	prcmd(b, r.op, r.cmd)
	b.WriteString(";\n")
}
