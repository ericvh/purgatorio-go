package main

import (
	"strconv"
	"strings"
)

// echo prints a command for -x.
func echo(e *Env, s []string) {
	e.output("+ " + strings.Join(s, " ") + "\n")
}

func (w *Word) word(d string) string {
	if w == nil {
		return ""
	}
	if d != "" {
		return d + w.text
	}
	if w.flags&Wquoted != 0 {
		return enquote(w.text)
	}
	return w.text
}

// The text of items and commands, as -d, whatis and the make builtins
// print it. With full set, a group's redirections are included, which
// Inferno's mash leaves out; a sub-mash is given full text.
var full bool

func fulltext(c *Cmd) string {
	full = true
	defer func() { full = false }()
	return c.text()
}

func (i *Item) text() string {
	if i == nil {
		return ""
	}
	switch i.op {
	case Icaret:
		return i.left.text() + " ^ " + i.right.text()
	case Iicaret:
		return i.left.text() + i.right.text()
	case Idollarq:
		return i.word.word("$\"")
	case Idollar, Imatch:
		return i.word.word("$")
	case Iword:
		return i.word.word("")
	case Iexpr:
		return "(" + i.cmd.text() + ")"
	case Ibackq:
		return "`" + group(i.cmd)
	case Iquote:
		return "\"" + group(i.cmd)
	case Iinpipe:
		return "<" + group(i.cmd)
	case Ioutpipe:
		return ">" + group(i.cmd)
	}
	return "?" + strconv.Itoa(i.op)
}

func words(l []*Item) string {
	var s []string
	for _, i := range l {
		s = append(s, i.text())
	}
	return strings.Join(s, " ")
}

func redir(s string, c *Cmd) string {
	if c == nil {
		return s
	}
	for _, r := range c.redirs {
		s += " " + rdsymbs[r.op] + " " + r.word.text()
	}
	return s
}

func cmd2in(c *Cmd, s string) string {
	return c.left.text() + " " + s + " " + c.right.text()
}

func group(c *Cmd) string {
	if c == nil {
		return "{ }"
	}
	return redir("{ "+c.text()+" }", c)
}

func sequence(c *Cmd) string {
	var s string
	for ; c != nil; c = c.left {
		r := c.right
		t := ";"
		if r != nil && r.op == Casync {
			r = r.left
			t = "&"
		}
		if s == "" {
			s = r.text() + t
		} else {
			s = r.text() + t + " " + s
		}
	}
	return s
}

func (c *Cmd) text() string {
	if c == nil {
		return ""
	}
	switch c.op {
	case Csimple:
		return redir(words(c.words), c)
	case Cseq:
		return sequence(c)
	case Cfor:
		return "for (" + c.item.text() + " in " + words(c.words) + ") " + c.left.text()
	case Cif:
		return "if (" + c.left.text() + ") " + c.right.text()
	case Celse:
		return c.left.text() + " else " + c.right.text()
	case Cwhile:
		return "while (" + c.left.text() + ") " + c.right.text()
	case Ccase:
		return redir("case "+c.left.text()+" { "+c.right.text()+"}", c)
	case Ccases:
		s := c.left.text()
		if s == "" || s[len(s)-1] != '&' {
			return s + "; " + c.right.text()
		}
		return s + " " + c.right.text()
	case Cmatched:
		return cmd2in(c, "=>")
	case Cdefeq:
		return c.item.text() + " := " + words(c.words)
	case Ceq:
		return c.item.text() + " = " + words(c.words)
	case Cfn:
		return "fn " + c.item.text() + " " + group(c.left)
	case Crescue:
		return "rescue " + c.item.text() + " " + group(c.left)
	case Casync:
		return c.left.text() + "&"
	case Cgroup:
		if full {
			return redir(group(c.left), c)
		}
		return group(c.left)
	case Clistgroup:
		return ":" + group(c.left)
	case Csubgroup:
		if full {
			return redir("@"+group(c.left), c)
		}
		return "@" + group(c.left)
	case Cnop:
		return ""
	case Cword:
		return c.item.text()
	case Ccaret:
		return cmd2in(c, "^")
	case Chd:
		return "hd " + c.left.text()
	case Clen:
		return "len " + c.left.text()
	case Cnot:
		return "!" + c.left.text()
	case Ctl:
		return "tl " + c.left.text()
	case Ccons:
		return cmd2in(c, "::")
	case Ceqeq:
		return cmd2in(c, "==")
	case Cnoteq:
		return cmd2in(c, "!=")
	case Cmatch:
		return cmd2in(c, "~")
	case Cpipe:
		return cmd2in(c, "|")
	case Cdepend:
		return words(c.words) + " : " + words(c.left.words) + " " + c.left.text()
	case Crule:
		return c.item.text() + " :~ " + c.left.item.text() + " " + c.left.text()
	}
	if c.op >= Cprivate {
		return "Priv+" + strconv.Itoa(c.op-Cprivate)
	}
	return "?" + strconv.Itoa(c.op)
}
