package main

import "strconv"

// exprModule is Inferno's expr shell module (sh-expr(1)): integer
// arithmetic in reverse Polish notation on 64-bit integers.
type exprModule struct{}

var exprmod Module = &exprModule{}

func (m *exprModule) initbuiltin(c *Context) string {
	c.addsbuiltin("expr", m)
	c.addbuiltin("ntest", m)
	return ""
}

func (m *exprModule) whatis(*Context, string, int) string { return "" }

func (m *exprModule) runbuiltin(c *Context, cmd []*Listnode, last bool) string {
	if cmd[0].word == "ntest" {
		if len(cmd) != 2 {
			c.fail("usage", "usage: ntest n")
		}
		if limboAtob(cmd[1].word) == 0 {
			return "false"
		}
	}
	return ""
}

const (
	opEQ = iota
	opGT
	opLT
	opGE
	opLE
	opPLUS
	opMINUS
	opDIVIDE
	opAND
	opTIMES
	opMOD
	opOR
	opXOR
	opUMINUS
	opSHL
	opSHR
	opNOT
	opBNOT
	opNEQ
	opREP
	opSEQ
)

func (m *exprModule) runsbuiltin(c *Context, cmd []*Listnode) []*Listnode {
	var stk []int64 // top is the end
	lastop, lastn := -1, -1
	lastname := ""
	cmd, radix := exprOpts(c, cmd[1:])
	for _, a := range cmd {
		w := a.word
		op, nops := -1, 2
		switch w {
		case "+":
			op = opPLUS
		case "-":
			op = opMINUS
		case "x", "*", "×":
			op = opTIMES
		case "/":
			op = opDIVIDE
		case "%":
			op = opMOD
		case "and":
			op = opAND
		case "or":
			op = opOR
		case "xor":
			op = opXOR
		case "_":
			op, nops = opUMINUS, 1
		case "<<", "shl":
			op = opSHL
		case ">>", "shr":
			op = opSHR
		case "=", "==", "eq":
			op = opEQ
		case "!=", "neq":
			op = opNEQ
		case ">", "gt":
			op = opGT
		case "<", "lt":
			op = opLT
		case ">=", "ge":
			op = opGE
		case "<=", "le":
			op = opLE
		case "!", "not":
			op, nops = opNOT, 1
		case "~":
			op, nops = opBNOT, 1
		case "rep":
			op, nops = opREP, 0
		case "seq":
			op, nops = opSEQ, 2
		}
		if op == -1 {
			stk = append(stk, makenum(c, w))
		} else {
			stk = operator(c, stk, op, nops, lastop, lastn, w, lastname)
		}
		lastop, lastn, lastname = op, nops, w
	}
	r := make([]*Listnode, len(stk))
	for i, n := range stk {
		r[i] = wordnode(big2string(n, radix))
	}
	return r
}

func exprOpts(c *Context, cmd []*Listnode) ([]*Listnode, int) {
	if len(cmd) == 0 {
		return nil, 10
	}
	w := cmd[0].word
	if len(w) < 2 || w[0] != '-' || (w[1] >= '0' && w[1] <= '9') {
		return cmd, 10
	}
	if w[1] != 'r' {
		c.fail("usage", "usage: expr [-r radix] [arg...]")
	}
	if len(w) > 2 {
		w = w[2:]
	} else {
		if len(cmd) < 2 {
			c.fail("usage", "usage: expr [-r radix] [arg...]")
		}
		cmd = cmd[1:]
		w = cmd[0].word
	}
	r := limboAtoi(w)
	if r <= 0 || r > 36 {
		c.fail("usage", "expr: invalid radix "+strconv.Itoa(r))
	}
	return cmd[1:], r
}

func operator(c *Context, stk []int64, op, nops, lastop, lastn int, opname, lastopname string) []int64 {
	args := make([]int64, nops)
	for i := nops - 1; i >= 0; i-- {
		if len(stk) == 0 {
			c.fail("empty stack", "expr: empty stack on op '"+opname+"'")
		}
		args[i] = stk[len(stk)-1]
		stk = stk[:len(stk)-1]
	}
	return oper(c, args, op, lastop, lastn, lastopname, stk)
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// oper applies op to args, first pushed first.
func oper(c *Context, args []int64, op, lastop, lastn int, lastopname string, stk []int64) []int64 {
	if op == opREP {
		if lastop == -1 || lastop == opSEQ || lastn != 2 {
			c.fail("usage", "expr: bad operator for rep")
		}
		if len(stk) < 2 {
			return stk
		}
		for len(stk) > 1 {
			stk = operator(c, stk, lastop, 2, -1, -1, lastopname, "")
		}
		return stk
	}
	var n1, n2 int64
	n1 = args[0]
	if len(args) > 1 {
		n2 = args[1]
	}
	var r int64
	switch op {
	case opEQ:
		r = b2i(n1 == n2)
	case opNEQ:
		r = b2i(n1 != n2)
	case opGT:
		r = b2i(n1 > n2)
	case opLT:
		r = b2i(n1 < n2)
	case opGE:
		r = b2i(n1 >= n2)
	case opLE:
		r = b2i(n1 <= n2)
	case opPLUS:
		r = n1 + n2
	case opMINUS:
		r = n1 - n2
	case opNOT:
		r = b2i(n1 != 0)
	case opDIVIDE, opMOD:
		if n2 == 0 {
			c.fail("divide by zero", "expr: division by zero")
		}
		if op == opDIVIDE {
			r = n1 / n2
		} else {
			r = n1 % n2
		}
	case opTIMES:
		r = n1 * n2
	case opAND:
		r = n1 & n2
	case opOR:
		r = n1 | n2
	case opXOR:
		r = n1 ^ n2
	case opUMINUS:
		r = -n1
	case opBNOT:
		r = ^n1
	case opSHL:
		r = n1 << uint64(int32(n2)&63)
	case opSHR:
		r = n1 >> uint64(int32(n2)&63)
	case opSEQ:
		incr := int64(1)
		if n2 < n1 {
			incr = -1
		}
		for ; n1 != n2; n1 += incr {
			stk = append(stk, n1)
		}
		return append(stk, n1)
	}
	return append(stk, r)
}

func makenum(c *Context, s string) int64 {
	if s == "" || (s[0] != '-' && (s[0] < '0' || s[0] > '9')) {
		c.fail("usage", "expr: unknown operator '"+s+"'")
	}
	t := s
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	radix := 10
	for i := 0; i < len(s) && i < 3; i++ {
		if s[i] == 'r' {
			radix = limboAtoi(s)
			s = s[i+1:]
			break
		}
	}
	if radix == 10 {
		return limboAtob(t)
	}
	if radix == 0 || radix > 36 {
		c.fail("usage", "expr: bad number "+t)
	}
	var n int64
	rb := int64(radix)
	for i := 0; i < len(s); i++ {
		d := s[i]
		switch {
		case '0' <= d && d <= '9':
			n = n*rb + int64(d-'0')
		case 'a' <= d && int(d) < 'a'+radix-10:
			n = n*rb + int64(d-'a'+10)
		case 'A' <= d && int(d) < 'A'+radix-10:
			n = n*rb + int64(d-'A'+10)
		default:
			i = len(s)
		}
	}
	if neg {
		return -n
	}
	return n
}

func big2string(n int64, radix int) string {
	neg := n < 0
	u := uint64(n)
	if neg {
		u = uint64(-n)
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
