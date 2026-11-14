package main

import "strconv"

// Node types, in the order of Inferno's sh.m.
const (
	nBLOCK = iota
	nVAR
	nBQ
	nBQ2
	nREDIR
	nDUP
	nLIST
	nSEQ
	nCONCAT
	nPIPE
	nADJ
	nWORD
	nNOWAIT
	nSQUASH
	nCOUNT
	nASSIGN
	nLOCAL
)

// glob marks a wildcard character the lexer saw unquoted.
const globc = '\x01'

// Open modes used by redirections; oappend is sh's own flag, as in sh.y.
const (
	oREAD   = 0
	oWRITE  = 1
	oRDWR   = 2
	oappend = 0x80000
	omask   = 7
)

// Node is a parsed command.
type Node struct {
	typ         int
	left, right *Node
	word        string
	redir       *Redir
}

// Redir is a redirection: its open mode and the fds it names (-1 for
// none).
type Redir struct {
	rtype    int
	fd1, fd2 int
}

func mk(typ int, left, right *Node) *Node { return &Node{typ: typ, left: left, right: right} }

func mkseq(left, right *Node) *Node {
	if left != nil && right != nil {
		return mk(nSEQ, left, right)
	}
	if left == nil {
		return right
	}
	return left
}

func fdassignstr(isassign bool, r *Redir) string {
	l := ""
	if r.fd1 >= 0 {
		l = strconv.Itoa(r.fd1)
	}
	if isassign {
		rs := ""
		if r.fd2 >= 0 {
			rs = strconv.Itoa(r.fd2)
		}
		return "[" + l + "=" + rs + "]"
	}
	return "[" + l + "]"
}

func redirstr(rtype int) string {
	switch rtype {
	case oWRITE:
		return ">"
	case oWRITE | oappend:
		return ">>"
	case oRDWR:
		return "<>"
	}
	return "<"
}

// cmd2string converts n to text the shell parses back to the same tree.
func cmd2string(n *Node) string {
	if n == nil {
		return ""
	}
	switch n.typ {
	case nBLOCK:
		return "{" + cmd2string(n.left) + "}"
	case nVAR:
		s := "$" + cmd2string(n.left)
		if n.right != nil {
			s += "(" + cmd2string(n.right) + ")"
		}
		return s
	case nSQUASH:
		return "$\"" + cmd2string(n.left)
	case nCOUNT:
		return "$#" + cmd2string(n.left)
	case nBQ:
		return "`" + cmd2string(n.left)
	case nBQ2:
		return "\"" + cmd2string(n.left)
	case nREDIR:
		s := redirstr(n.redir.rtype)
		if n.redir.fd1 != -1 {
			s += fdassignstr(false, n.redir)
		}
		return s + cmd2string(n.left)
	case nDUP:
		return redirstr(n.redir.rtype) + fdassignstr(true, n.redir)
	case nLIST:
		return "(" + cmd2string(n.left) + ")"
	case nSEQ:
		return cmd2string(n.left) + ";" + cmd2string(n.right)
	case nNOWAIT:
		return cmd2string(n.left) + "&"
	case nCONCAT:
		return cmd2string(n.left) + "^" + cmd2string(n.right)
	case nPIPE:
		s := cmd2string(n.left) + "|"
		if n.redir != nil && (n.redir.fd1 != -1 || n.redir.fd2 != -1) {
			s += fdassignstr(n.redir.fd2 != -1, n.redir)
		}
		return s + cmd2string(n.right)
	case nASSIGN:
		return cmd2string(n.left) + "=" + cmd2string(n.right)
	case nLOCAL:
		return cmd2string(n.left) + ":=" + cmd2string(n.right)
	case nADJ:
		return cmd2string(n.left) + " " + cmd2string(n.right)
	case nWORD:
		return quote(n.word, true)
	}
	return "unknown" + strconv.Itoa(n.typ)
}

// quote makes s suitable for reparsing; with isglob, glob marks are
// significant and the character they mark is left unquoted. A " is an
// operator to the lexer, so it is quoted too: sh.y leaves it out, and a
// block holding a quoted " then fails to reparse.
func quote(s string, isglob bool) string {
	needquote := false
	t := make([]byte, 0, len(s)+2)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{', '}', '(', ')', '`', '&', ';', '=', '>', '<', '#',
			'|', '*', '[', '?', '$', '^', ' ', '\t', '\n', '\r', '"':
			needquote = true
		case '\'':
			t = append(t, '\'')
			needquote = true
		case globc:
			if isglob && i < len(s)-1 {
				i++
			}
		}
		t = append(t, s[i])
	}
	if needquote || len(t) == 0 {
		return "'" + string(t) + "'"
	}
	return string(t)
}
