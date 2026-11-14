package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/regex"
)

// Rule attributes.
const (
	meta = 1 << iota
	seq
	upd
	quiet
	vir
	regexpattr
	norec
	delattr
	novirt
	nominuse
)

// Rule is one target of a rule in the mkfile.
type Rule struct {
	target     string
	tail       []string
	recipe     *string // shared by the targets of one rule
	attr       int
	line       int
	file       string
	alltargets []string
	rule       int
	pat        *regex.Re
	prog       string
	hasprog    bool
}

// Node flags.
const (
	virtual = 1 << iota
	cycle
	ready
	canpretend
	pretending
	notmade
	beingmade
	made
	probable
	vacuousf
	norecipe
	deletef
	nominusef
)

// Arc flags.
const togoflag = 1

type resub struct {
	beg, end int // byte offsets
	ok       bool
}

type Arc struct {
	flag    int
	n       *Node
	r       *Rule
	stem    string
	prog    string
	hasprog bool
	match   [nregexp]*string
}

type Node struct {
	name    string
	time    int
	flags   int
	prereqs []*Arc
	next    *Node // list of the targets of one job
}

func (n *Node) setmade(f int) { n.flags = n.flags&^(notmade|beingmade|made) | f }

var (
	rules     []*Rule
	metarules []*Rule
	nrules    int
)

func emptyrecipe(r *Rule) bool { return r.recipe == nil || *r.recipe == "" }
func emptytail(r *Rule) bool   { return len(r.tail) == 0 || r.tail[0] == "" }

func rcmp(r *Rule, target string, tail []string) bool {
	if r.target != target || len(r.tail) != len(tail) {
		return false
	}
	for i := range tail {
		if r.tail[i] != tail[i] {
			return false
		}
	}
	return true
}

func addrule(head string, tail []string, body *string, ahead []string, attr, hline int, prog string, hasprog bool) {
	var r *Rule
	reuse := false
	for _, x := range targets[head] {
		if rcmp(x, head, tail) {
			r, reuse = x, true
			break
		}
	}
	if r == nil {
		r = &Rule{}
	}
	r.target = head
	r.tail = tail
	r.recipe = body
	r.line = hline
	r.file = infile
	r.attr = attr
	r.alltargets = ahead
	r.prog = prog
	r.hasprog = hasprog
	r.rule = nrules
	nrules++
	if !reuse {
		// A new rule goes second in the target's chain, as mk links it.
		c := targets[head]
		if len(c) == 0 {
			targets[head] = []*Rule{r}
		} else {
			targets[head] = append([]*Rule{c[0], r}, c[1:]...)
		}
	}
	if attr&regexpattr != 0 || charin(head, "%&") >= 0 {
		r.attr |= meta
		if reuse {
			return
		}
		if attr&regexpattr != 0 {
			patrule = r
			re, e := regex.Compile(head, 1)
			if e != "" {
				regerror(e)
			}
			r.pat = re
		}
		metarules = append(metarules, r)
	} else {
		if reuse {
			return
		}
		rules = append(rules, r)
	}
}

func dumpr(s string, rs []*Rule) {
	start := 0
	if len(rs) > 0 {
		start = addr(rs[0])
	}
	fmt.Fprintf(bout, "%s: start=%x\n", s, start)
	for i, r := range rs {
		next := 0
		if i+1 < len(rs) {
			next = addr(rs[i+1])
		}
		chain := 0
		c := targets[r.target]
		for j, x := range c {
			if x == r && j+1 < len(c) {
				chain = addr(c[j+1])
			}
		}
		fmt.Fprintf(bout, "\tRule %x: %s[%d] attr=%x next=%x chain=%x alltarget='%s'", addr(r), r.file, r.line, r.attr, next, chain, strings.Join(r.alltargets, " "))
		if r.hasprog {
			fmt.Fprintf(bout, " prog='%s'", r.prog)
		}
		fmt.Fprintf(bout, "\n\ttarget=%s: %s\n", r.target, strings.Join(r.tail, " "))
		rec := ""
		if r.recipe != nil {
			rec = *r.recipe
		}
		fmt.Fprintf(bout, "\trecipe@%x='%s'\n", addr(r.recipe), rec)
	}
}

// The graph.

func graph(target string) *Node {
	cnt := make([]int, nrules)
	node := applyrules(target, cnt)
	cyclechk(node)
	node.flags |= probable
	vacuous(node)
	ambiguous(node)
	attribute(node)
	return node
}

func applyrules(target string, cnt []int) *Node {
	if n, ok := nodes[target]; ok {
		return n
	}
	node := newnode(target)
	var arcs []*Arc
	var nomatch [nregexp]resub
	for _, r := range targets[target] {
		if r.attr&meta != 0 || r.target != target {
			continue
		}
		if emptyrecipe(r) && emptytail(r) {
			continue
		}
		if cnt[r.rule] >= nreps {
			continue
		}
		cnt[r.rule]++
		node.flags |= probable
		if emptytail(r) {
			arcs = append(arcs, newarc(nil, r, "", "", nomatch))
		} else {
			for _, w := range r.tail {
				arcs = append(arcs, newarc(applyrules(w, cnt), r, "", "", nomatch))
			}
		}
		cnt[r.rule]--
	}
	for _, r := range metarules {
		if emptyrecipe(r) && emptytail(r) {
			continue
		}
		if r.attr&novirt != 0 && len(arcs) > 0 && arcs[len(arcs)-1].r.attr&vir != 0 {
			continue
		}
		stem := ""
		var rmatch [nregexp]resub
		if r.attr&regexpattr != 0 {
			patrule = r
			m := r.pat.Execute([]rune(node.name))
			if m == nil {
				continue
			}
			rmatch = regadd(node.name, m)
		} else {
			var ok bool
			if stem, ok = match(node.name, r.target); !ok {
				continue
			}
		}
		if cnt[r.rule] >= nreps {
			continue
		}
		cnt[r.rule]++
		if emptytail(r) {
			arcs = append(arcs, newarc(nil, r, stem, node.name, rmatch))
		} else {
			for _, w := range r.tail {
				var buf string
				if r.attr&regexpattr != 0 {
					buf = regsub(w, node.name, rmatch)
				} else {
					buf = subst(stem, w)
				}
				arcs = append(arcs, newarc(applyrules(buf, cnt), r, stem, node.name, rmatch))
			}
		}
		cnt[r.rule]--
	}
	node.prereqs = append(arcs, node.prereqs...)
	return node
}

// regadd turns rune ranges into byte offsets in s.
func regadd(s string, m []regex.Range) [nregexp]resub {
	var rm [nregexp]resub
	off := make([]int, 0, len(s)+1)
	for i := range s {
		off = append(off, i)
	}
	off = append(off, len(s))
	for i := 0; i < len(m) && i < nregexp; i++ {
		a, b := m[i].Beg, m[i].End
		if a >= 0 && b >= 0 && a < len(off) && b < len(off) {
			rm[i] = resub{off[a], off[b], true}
		}
	}
	return rm
}

// regsub replaces \n and & in s with the subexpression matches of name.
func regsub(s, name string, m [nregexp]resub) string {
	var d strings.Builder
	cp := func(k int) {
		if k < nregexp && m[k].ok {
			d.WriteString(name[m[k].beg:m[k].end])
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
				i++
				cp(int(s[i] - '0'))
			} else {
				d.WriteByte('\\')
			}
		case '&':
			cp(0)
		default:
			d.WriteByte(s[i])
		}
	}
	return d.String()
}

func togo(node *Node) {
	var keep []*Arc
	for _, a := range node.prereqs {
		if a.flag&togoflag == 0 {
			keep = append(keep, a)
		}
	}
	node.prereqs = keep
}

func vacuous(node *Node) bool {
	vac := node.flags&probable == 0
	if node.flags&ready != 0 {
		return node.flags&vacuousf != 0
	}
	node.flags |= ready
	for _, a := range node.prereqs {
		if a.n != nil && vacuous(a.n) && a.r.attr&meta != 0 {
			a.flag |= togoflag
		} else {
			vac = false
		}
	}
	// If a rule made arcs that stay, none of that rule's arcs go.
	for _, a := range node.prereqs {
		if a.flag&togoflag == 0 {
			for _, la := range node.prereqs {
				if la.flag&togoflag != 0 && la.r == a.r {
					la.flag &^= togoflag
				}
			}
		}
	}
	togo(node)
	if vac {
		node.flags |= vacuousf
	}
	return vac
}

func newnode(name string) *Node {
	node := &Node{name: name}
	nodes[name] = node
	node.time = timeof(name, false)
	if node.time != 0 {
		node.flags = probable
	}
	return node
}

func dumpn(s string, n *Node) {
	pre := "   "
	if strings.HasPrefix(s, " ") {
		pre = s + "   "
	}
	fmt.Fprintf(bout, "%s%s@%x: time=%d flags=0x%x next=%x\n", s, n.name, addr(n), n.time, n.flags, addr(n.next))
	for _, a := range n.prereqs {
		dumpa(pre, a)
	}
}

func dumpa(s string, a *Arc) {
	fmt.Fprintf(bout, "%sArc@%x: n=%x r=%x flag=0x%x stem='%s'", s, addr(a), addr(a.n), addr(a.r), a.flag, a.stem)
	if a.hasprog {
		fmt.Fprintf(bout, " prog='%s'", a.prog)
	}
	bout.WriteString("\n")
	if a.n != nil {
		pre := "    "
		if strings.HasPrefix(s, " ") {
			pre = s + "    "
		}
		dumpn(pre, a.n)
	}
}

func trace(s string, a *Arc) {
	fmt.Fprintf(os.Stderr, "\t%s", s)
	for a != nil {
		name := ""
		if a.n != nil {
			name = a.n.name
		}
		fmt.Fprintf(os.Stderr, " <-(%s:%d)- %s", a.r.file, a.r.line, name)
		var next *Arc
		if a.n != nil {
			for _, x := range a.n.prereqs {
				if !emptyrecipe(x.r) {
					next = x
					break
				}
			}
		}
		a = next
	}
	fmt.Fprintf(os.Stderr, "\n")
}

func cyclechk(n *Node) {
	if n.flags&cycle != 0 && len(n.prereqs) > 0 {
		fmt.Fprintf(os.Stderr, "mk: cycle in graph detected at target %s\n", n.name)
		exit()
	}
	n.flags |= cycle
	for _, a := range n.prereqs {
		if a.n != nil {
			cyclechk(a.n)
		}
	}
	n.flags &^= cycle
}

func ambiguous(n *Node) {
	var r *Rule
	var la *Arc
	bad := false
	for _, a := range n.prereqs {
		if a.n != nil {
			ambiguous(a.n)
		}
		if emptyrecipe(a.r) {
			continue
		}
		if r == nil {
			r, la = a.r, a
			continue
		}
		if r.recipe != a.r.recipe {
			if r.attr&meta != 0 && a.r.attr&meta == 0 {
				la.flag |= togoflag
				r, la = a.r, a
			} else if r.attr&meta == 0 && a.r.attr&meta != 0 {
				a.flag |= togoflag
				continue
			}
		}
		if r.recipe != a.r.recipe {
			if !bad {
				fmt.Fprintf(os.Stderr, "mk: ambiguous recipes for %s:\n", n.name)
				bad = true
				trace(n.name, la)
			}
			trace(n.name, a)
		}
	}
	if bad {
		exit()
	}
	togo(n)
}

func attribute(n *Node) {
	for _, a := range n.prereqs {
		if a.r.attr&vir != 0 {
			n.flags |= virtual
		}
		if a.r.attr&norec != 0 {
			n.flags |= norecipe
		}
		if a.r.attr&delattr != 0 {
			n.flags |= deletef
		}
		if a.n != nil {
			attribute(a.n)
		}
	}
	if n.flags&virtual != 0 {
		n.time = 0
	}
}

// newarc makes an arc; name is the target a regular expression matched,
// for the stem0...stem9 variables.
func newarc(n *Node, r *Rule, stem, name string, m [nregexp]resub) *Arc {
	a := &Arc{n: n, r: r, stem: stem, prog: r.prog, hasprog: r.hasprog}
	// stem0 is the target from the start of the match.
	if m[0].ok {
		s := name[m[0].beg:]
		a.match[0] = &s
	}
	for i := 1; i < nregexp; i++ {
		if m[i].ok {
			s := name[m[i].beg:m[i].end]
			a.match[i] = &s
		}
	}
	return a
}

func nrep() {
	if w, ok := vars["NREP"]; ok && len(w) > 0 && w[0] != "" {
		nreps = atoi(w[0])
	}
	if nreps < 1 {
		nreps = 1
	}
	if debug&dGraph != 0 {
		fmt.Fprintf(bout, "nreps = %d\n", nreps)
	}
}

// match reports whether name matches the meta-rule target template,
// and returns the stem.
func match(name, template string) (string, bool) {
	for name != "" && template != "" {
		r, _ := utf8.DecodeRuneInString(template)
		if r == '%' || r == '&' {
			break
		}
		if name[0] != template[0] {
			return "", false
		}
		name = name[1:]
		template = template[1:]
	}
	if !strings.HasPrefix(template, "%") && !strings.HasPrefix(template, "&") {
		return "", false
	}
	suf := template[1:]
	n := len(name) - len(suf)
	if n < 0 || name[n:] != suf {
		return "", false
	}
	stem := name[:n]
	if template[0] == '&' {
		return stem, !strings.ContainsAny(stem, "./")
	}
	return stem, true
}

// subst replaces each % or & in template with stem.
func subst(stem, template string) string {
	var d strings.Builder
	for _, r := range template {
		if r == '%' || r == '&' {
			d.WriteString(stem)
		} else {
			d.WriteRune(r)
		}
	}
	return d.String()
}
