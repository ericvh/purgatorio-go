package main

import (
	"strings"
)

// Redirections.
const (
	Rin = iota
	Rout
	Rappend
	Rinout
	Rcount
)

// Items.
const (
	Icaret = iota
	Iicaret
	Idollar
	Idollarq
	Imatch
	Iword
	Iexpr
	Ibackq
	Iquote
	Iinpipe
	Ioutpipe
	Iredir
)

// Commands.
const (
	Csimple = iota
	Cseq
	Cfor
	Cif
	Celse
	Cwhile
	Ccase
	Ccases
	Cmatched
	Cdefeq
	Ceq
	Cfn
	Crescue
	Casync
	Cgroup
	Clistgroup
	Csubgroup
	Cnop
	Cword
	Clist
	Ccaret
	Chd
	Clen
	Cnot
	Ctl
	Ccons
	Ceqeq
	Cnoteq
	Cmatch
	Cpipe
	Cdepend
	Crule
	Cprivate
)

// Word flags.
const (
	Wquoted = 1 << iota
	Wexpand
)

// Word is a word as the lexer read it.
type Word struct {
	text  string
	flags int
	line  int
	file  string
}

// Item is a word, or something that evaluates to words.
type Item struct {
	op          int
	word        *Word
	left, right *Item
	cmd         *Cmd
	redir       *Redir
}

// Redir is a redirection.
type Redir struct {
	op   int
	word *Item
}

// Cmd is a node of a parsed command or expression. Its fields' meanings
// depend on op, as in Inferno's mash.
type Cmd struct {
	op          int
	words       []*Item
	left, right *Cmd
	item        *Item
	redirs      []*Redir // most recently added first
	value       []string
	err         bool
}

func cmd1(op int, l *Cmd) *Cmd           { return &Cmd{op: op, left: l} }
func cmd2(op int, l, r *Cmd) *Cmd        { return &Cmd{op: op, left: l, right: r} }
func cmd1i(op int, l *Cmd, i *Item) *Cmd { return &Cmd{op: op, left: l, item: i} }
func cmd1w(op int, l *Cmd, w []*Item) *Cmd {
	return &Cmd{op: op, words: w, left: l}
}
func cmdiw(op int, i *Item, w []*Item) *Cmd { return &Cmd{op: op, words: w, item: i} }

func (c *Cmd) cmde(op int, l, r *Cmd) *Cmd {
	c.op = op
	c.left = l
	c.right = r
	return c
}

const (
	pin = 1 << iota
	pout
)

var rdmap = [...]int{Rin: pin, Rout: pout, Rappend: pout, Rinout: pin | pout}

var rdsymbs = [...]string{Rin: "<", Rout: ">", Rappend: ">>", Rinout: "<>"}

var ionames = map[int]string{pin: "input", pout: "ouput", pin | pout: "input/output"}

// checkpipe checks a pipeline for redirections that conflict with it.
func (c *Cmd) checkpipe(e *Env, f int) bool {
	if c.err {
		return false
	}
	if c.op == Cpipe {
		if !c.left.checkpipe(e, f|pout) {
			return false
		}
		if !c.right.checkpipe(e, f|pin) {
			return false
		}
	}
	if f != 0 {
		t := 0
		for _, r := range c.redirs {
			t |= rdmap[r.op]
		}
		f &= t
		if f != 0 {
			e.report(ionames[f] + " redirection conflicts with pipe")
			return false
		}
	}
	return true
}

// cmdio adds a redirection to a command.
func (c *Cmd) cmdio(e *Env, i *Item) {
	f := 0
	for _, r := range c.redirs {
		f |= rdmap[r.op]
	}
	r := i.redir
	f &= rdmap[r.op]
	if f != 0 {
		e.report("repeat " + ionames[f] + " redirection")
		c.err = true
	}
	c.redirs = append([]*Redir{r}, c.redirs...)
}

// mkcmd makes a command of a pipeline, maybe run in the background.
func (c *Cmd) mkcmd(e *Env, async bool) *Cmd {
	if !c.checkpipe(e, 0) {
		return nil
	}
	if async {
		return &Cmd{op: Casync, left: c}
	}
	return c
}

func item2(op int, l, r *Item) *Item                   { return &Item{op: op, left: l, right: r} }
func itemc(op int, c *Cmd) *Item                       { return &Item{op: op, cmd: c} }
func itemr(op int, i *Item) *Item                      { return &Item{op: Iredir, redir: &Redir{op, i}} }
func itemw(s string) *Item                             { return &Item{op: Iword, word: &Word{text: s, flags: Wquoted}} }
func itemExpr(c *Cmd) *Item                            { return itemc(Iexpr, c) }
func newWord(s string, f, line int, file string) *Word { return &Word{s, f, line, file} }

// iteml makes an item of a list of strings.
func iteml(l []string) *Item {
	if len(l) == 1 {
		return itemw(l[0])
	}
	return itemExpr(&Cmd{op: Clist, value: append([]string(nil), l...)})
}

func concat(l []string) string { return strings.Join(l, " ") }

// mklist makes an item list in which no redirections are allowed.
func (e *Env) mklist(l []*Item) []*Item {
	var r []*Item
	for _, i := range l {
		if i.op == Iredir {
			e.report("redirection in list")
		} else {
			r = append(r, i)
		}
	}
	return r
}

// mksimple makes a simple command.
func (e *Env) mksimple(l []*Item) *Cmd {
	c := &Cmd{op: Csimple}
	for _, i := range l {
		if i.op == Iredir {
			c.cmdio(e, i)
		} else {
			c.words = append(c.words, i)
		}
	}
	// cmdio prepends; the parser's list is in reverse, so a simple
	// command keeps its redirections in order.
	for i, j := 0, len(c.redirs)-1; i < j; i, j = i+1, j-1 {
		c.redirs[i], c.redirs[j] = c.redirs[j], c.redirs[i]
	}
	return c
}
