// Package regex is Inferno's Regex module (appl/lib/regex.b, regex(2)):
// the regular expressions of regexp(6), matched leftmost-longest.
//
// The syntax is Plan 9's: alternation |, concatenation, closures * + ?,
// grouping ( ), any character ., classes [abc] [a-z] [^a-z], anchors ^
// and $, and \ to quote a metacharacter (\n is a newline). There are no
// counted repetitions, no Perl classes such as \d, and no non-greedy
// operators. Matching is over runes.
package regex

// Node kinds above the rune range.
const (
	kALT = 1<<16 + iota
	kCAT
	kDOT
	kSET
	kHAT
	kDOL
	kNUL
	kPCLO
	kCLO
	kOPT
	kLPN
	kRPN
)

const (
	nilRex  = -1 // no next state: the match is complete
	noneRex = -2 // unset
	bad     = 1 << 16
	huge    = 1<<31 - 1
)

type set struct {
	neg     bool
	ascii   [4]uint32
	unicode [][2]rune
}

func (s *set) member(c rune) bool {
	if c < 128 {
		return (s.ascii[c/32]>>(c%32))&1 != 0 != s.neg
	}
	for _, r := range s.unicode {
		if c >= r[0] && c <= r[1] {
			return !s.neg
		}
	}
	return s.neg
}

type rex struct {
	kind  int
	left  int
	right int
	set   *set
	pno   int
}

// Re is a compiled regular expression.
type Re struct {
	rex   []rex
	start int
	pno   int
}

type restr struct {
	s []rune
	i int
	n int // characters left; -1 on error
}

func (s *restr) peek() int {
	if s.n <= 0 {
		return bad
	}
	return int(s.s[s.i])
}

func (s *restr) next() int {
	if s.n <= 0 {
		return bad
	}
	s.n--
	s.i++
	return int(s.s[s.i-1])
}

func (re *Re) add(kind, left, right int, st *set, pno int) int {
	re.rex = append(re.rex, rex{kind, left, right, st, pno})
	return len(re.rex) - 1
}

func (re *Re) alt(s *restr) int {
	left := re.cat(s)
	if left == nilRex || s.peek() != '|' {
		return left
	}
	s.next()
	right := re.alt(s)
	if right == nilRex {
		return nilRex
	}
	return re.add(kALT, left, right, nil, 0)
}

func (re *Re) cat(s *restr) int {
	left := re.dup(s)
	if left == nilRex {
		return left
	}
	right := re.cat(s)
	if right == nilRex {
		return left
	}
	return re.add(kCAT, left, right, nil, 0)
}

func (re *Re) dup(s *restr) int {
	switch s.peek() {
	case bad, ')', ']', '|', '?', '*', '+':
		return nilRex
	}
	var prim int
	switch kind := s.next(); kind {
	case '(':
		if re.pno < 0 {
			if s.peek() == ')' {
				s.next()
				prim = re.add(kNUL, noneRex, noneRex, nil, 0)
			} else {
				prim = re.alt(s)
				if prim == nilRex || s.next() != ')' {
					s.n = -1
				}
			}
		} else {
			re.pno++
			pno := re.pno
			lp := re.add(kLPN, noneRex, noneRex, nil, pno)
			rp := re.add(kRPN, noneRex, noneRex, nil, pno)
			if s.peek() == ')' {
				s.next()
				prim = re.add(kCAT, lp, rp, nil, 0)
			} else {
				prim = re.alt(s)
				if prim == nilRex || s.next() != ')' {
					s.n = -1
				} else {
					prim = re.add(kCAT, prim, rp, nil, 0)
					prim = re.add(kCAT, lp, prim, nil, 0)
				}
			}
		}
	case '[':
		prim = re.add(kSET, noneRex, noneRex, newset(s), 0)
	default:
		switch kind {
		case '.':
			kind = kDOT
		case '^':
			kind = kHAT
		case '$':
			kind = kDOL
		}
		prim = re.add(esc(s, kind), noneRex, noneRex, nil, 0)
	}
	var kind int
	switch s.peek() {
	case '*':
		kind = kCLO
	case '+':
		kind = kPCLO
	case '?':
		kind = kOPT
	default:
		return prim
	}
	s.next()
	return re.add(kind, prim, noneRex, nil, 0)
}

func esc(s *restr, c int) int {
	if c == '\\' {
		c = s.next()
		switch c {
		case bad:
			s.n = -1
		case 'n':
			c = '\n'
		}
	}
	return c
}

// walk links each node to the next state of the machine.
func (re *Re) walk(r, succ int) {
	if r == noneRex {
		return
	}
	x := re.rex[r]
	switch x.kind {
	case kALT:
		re.walk(x.left, succ)
		re.walk(x.right, succ)
		return
	case kCAT:
		re.walk(x.left, x.right)
		re.walk(x.right, succ)
		re.rex[r] = re.rex[x.left]
		return
	case kCLO, kPCLO:
		end := re.add(kOPT, r, succ, nil, 0) // the loop back
		re.walk(x.left, end)
	case kOPT:
		re.walk(x.left, succ)
	}
	re.rex[r].right = succ
}

func newset(s *restr) *set {
	st := &set{}
	if s.peek() == '^' {
		st.neg = true
		s.next()
	}
	for s.n > 0 {
		c1 := s.next()
		if c1 == ']' {
			return st
		}
		c1 = esc(s, c1)
		c2 := c1
		if s.peek() == '-' {
			s.next()
			c2 = s.next()
			if c2 == ']' {
				break
			}
			c2 = esc(s, c2)
			if c2 < c1 {
				break
			}
		}
		for ; c1 <= c2; c1++ {
			if c1 < 128 {
				st.ascii[c1/32] |= 1 << (c1 % 32)
			} else {
				st.unicode = append(st.unicode, [2]rune{rune(c1), rune(c2)})
				break
			}
		}
	}
	s.n = -1
	return nil
}

// Compile compiles e. With flag 1, parenthesized subexpressions are
// reported by Execute. On error it returns Inferno's diagnostic.
func Compile(e string, flag int) (*Re, string) {
	if e == "" {
		return nil, "missing expression"
	}
	r := []rune(e)
	s := &restr{s: r, n: len(r)}
	re := &Re{pno: flag&1 - 1}
	re.start = re.alt(s)
	if re.start == nilRex || s.n != 0 {
		return nil, "invalid regular expression"
	}
	re.walk(re.start, nilRex)
	if re.pno < 0 {
		re.pno = 0
	}
	return re, ""
}

// Range is a match of [Beg, End); (-1, -1) is no match.
type Range struct{ Beg, End int }

type gaz struct{ pno, beg, end int }

type trace struct {
	cre int
	beg int
	gaz []gaz // newest first
}

type queue struct {
	ptr int
	q   []trace
}

// Execute matches re against s and returns nil, or the leftmost longest
// match followed by one range for each subexpression. Positions are
// rune indexes.
func (re *Re) Execute(s []rune) []Range {
	return re.ExecuteSE(s, -1, -1, true, true)
}

// ExecuteSE matches within s[s0:s1]; bol and eol say whether s0 and s1
// are at the start and end of a line, for ^ and $. Negative s0 and s1
// mean the start and end of s.
func (re *Re) ExecuteSE(s []rune, s0, s1 int, bol, eol bool) []Range {
	if re == nil {
		return nil
	}
	if s0 < 0 {
		s0 = 0
	}
	if s1 < 0 {
		s1 = len(s)
	}
	var g []gaz
	beg, end := -1, -1
	todo1 := &queue{q: make([]trace, len(re.rex))}
	todo2 := &queue{q: make([]trace, len(re.rex))}
	for i := s0; i <= s1; i++ {
		small2 := huge // earliest possible match if advance
		if beg == -1 { // no leftmost match yet
			todo1.q[todo1.ptr] = trace{re.start, i, nil}
			todo1.ptr++
		}
		for k := 0; k < todo1.ptr; k++ {
			q := todo1.q[k]
			x := re.rex[q.cre]
			next1, next2 := noneRex, noneRex
			switch x.kind {
			case kNUL:
				next1 = x.right
			case kDOT:
				if i < len(s) && s[i] != '\n' {
					next2 = x.right
				}
			case kHAT:
				if i == s0 && bol {
					next1 = x.right
				}
			case kDOL:
				if i == s1 && eol {
					next1 = x.right
				}
			case kSET:
				if i < len(s) && x.set.member(s[i]) {
					next2 = x.right
				}
			case kCAT, kPCLO:
				next1 = x.left
			case kALT, kCLO, kOPT:
				next1 = x.right
				k = insert(x.left, q.beg, q.gaz, todo1, k)
			case kLPN:
				next1 = x.right
				q.gaz = append([]gaz{{x.pno, i, -1}}, q.gaz...)
			case kRPN:
				next1 = x.right
				for _, r := range q.gaz {
					if x.pno == r.pno && r.end == -1 {
						q.gaz = append([]gaz{{r.pno, r.beg, i}}, q.gaz...)
						break
					}
				}
			default:
				if i < len(s) && x.kind == int(s[i]) {
					next2 = x.right
				}
			}
			if next1 != noneRex {
				if next1 != nilRex {
					k = insert(next1, q.beg, q.gaz, todo1, k)
				} else if better(q.beg, i, beg, end) {
					g, beg, end = q.gaz, q.beg, i
				}
			}
			if next2 != noneRex {
				if next2 != nilRex {
					if q.beg < small2 {
						small2 = q.beg
					}
					insert(next2, q.beg, q.gaz, todo2, 0)
				} else if better(q.beg, i+1, beg, end) {
					g, beg, end = q.gaz, q.beg, i+1
				}
			}
		}
		if beg != -1 && beg < small2 { // nothing better possible
			break
		}
		todo1, todo2 = todo2, todo1
		todo2.ptr = 0
	}
	if beg == -1 {
		return nil
	}
	result := make([]Range, re.pno+1)
	result[0] = Range{beg, end}
	for i := 1; i < len(result); i++ {
		result[i] = Range{-1, -1}
	}
	for _, x := range g {
		if result[x.pno].Beg == -1 && (x.beg|x.end) != -1 {
			result[x.pno] = Range{x.beg, x.end}
		}
	}
	return result
}

func better(newbeg, newend, oldbeg, oldend int) bool {
	return oldbeg == -1 || newbeg < oldbeg || newbeg == oldbeg && newend > oldend
}

// insert queues state next, unless it is queued already with an earlier
// start; k is the caller's cursor in todo, adjusted for the move.
func insert(next, tbeg int, tgaz []gaz, todo *queue, k int) int {
	j := 0
	for ; j < todo.ptr; j++ {
		if todo.q[j].cre == next {
			if todo.q[j].beg <= tbeg {
				return k
			}
			break
		}
	}
	if j < k {
		k--
	}
	if j < todo.ptr {
		todo.ptr--
	}
	for ; j < todo.ptr; j++ {
		todo.q[j] = todo.q[j+1]
	}
	todo.q[todo.ptr] = trace{next, tbeg, tgaz}
	todo.ptr++
	return k
}
