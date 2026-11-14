package main

import (
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/regex"
)

// look3 is button 3 (look.b): open the file named by the text, or
// search for the text in the body. With the window's event file open
// and !external, it is sent there instead.
func look3(t *Text, q0, q1 int, external bool) {
	oq0, oq1 := q0, q1
	expanded := false
	if q0 == q1 {
		if t.q1 > t.q0 && t.q0 <= q0 && q0 <= t.q1 {
			q0, q1 = t.q0, t.q1
		} else {
			q0, q1 = t.expand(q0, q1, true)
			expanded = true
		}
	}
	if q0 == q1 {
		return
	}
	r := t.file.Read(q0, q1)
	if !external && t.w != nil && t.w.nopen[qwEvent] > 0 {
		c := 'l'
		if t.what == Body {
			c = 'L'
		}
		f := 0
		name := string(r)
		if i := strings.IndexByte(name, ':'); i >= 0 {
			name = name[:i]
		}
		isfile := false
		if name != "" {
			full := name
			if !strings.HasPrefix(full, "/") {
				full = path.Join(t.w.dir(), full)
			}
			if g.row.lookfile(full) != nil {
				f = 1 // acme can do it without loading a file
			}
			if _, err := os.Stat(full); err == nil {
				isfile = true
			}
		}
		if expanded {
			f |= 2
		}
		if isfile {
			f |= 4
		}
		t.w.event("M", c, oq0, oq1, f, t.file.Read(oq0, oq1))
		if expanded {
			t.w.event("M", c, q0, q1, f&^2, r)
		}
		return
	}
	s := string(r)
	if openlook(t, s) {
		return
	}
	// Search the body for the text: from a body selection onwards,
	// round the end.
	ft := t
	if t.what != Body && t.w != nil {
		ft = &t.w.body
	}
	if t.what == Body {
		ft.setselect(q0, q1)
	}
	if search(ft, r) {
		ft.show(ft.q0, ft.q1, true)
	}
}

// openlook opens s if it names a file (with an optional :address); it
// reports whether it did.
func openlook(t *Text, s string) bool {
	name, addr := s, ""
	if i := strings.IndexByte(s, ':'); i >= 0 {
		name, addr = s[:i], s[i+1:]
	}
	if name == "" {
		// Just an address, in this window.
		if t.w != nil && addr != "" {
			return gotoaddr(&t.w.body, addr)
		}
		return false
	}
	dir := g.wdir
	if t.w != nil {
		dir = t.w.dir()
	}
	full := name
	if !strings.HasPrefix(name, "/") {
		full = path.Join(dir, name)
	}
	if w := g.row.lookfile(full); w != nil {
		if addr != "" {
			gotoaddr(&w.body, addr)
		} else {
			w.body.show(w.body.q0, w.body.q1, true)
		}
		return true
	}
	if _, err := os.Stat(full); err != nil {
		return false
	}
	c := t.col
	if c == nil {
		c = g.row.col[len(g.row.col)-1]
	}
	openfile(c, full, addr)
	return true
}

// openfile opens a window on name in column c and goes to addr.
func openfile(c *Column, name, addr string) *Window {
	if !strings.HasPrefix(name, "/") {
		name = path.Join(g.wdir, name)
	}
	name = path.Clean(name)
	if w := g.row.lookfile(name); w != nil {
		if addr != "" {
			gotoaddr(&w.body, addr)
		}
		return w
	}
	w := c.add(nil, nil, -1)
	if _, err := os.Stat(name); err == nil {
		loadfile(w, name)
	} else {
		w.setname(name)
	}
	if addr != "" {
		gotoaddr(&w.body, addr)
	}
	return w
}

// gotoaddr selects and shows an address in t.
func gotoaddr(t *Text, addr string) bool {
	q0, q1, err := address(t, t.q0, t.q1, addr)
	if err != "" {
		warning(nil, "address: "+err+"\n")
		return false
	}
	t.show(q0, q1, true)
	return true
}

// search finds r in t after the selection, wrapping round, and selects
// it.
func search(t *Text, r []rune) bool {
	n := t.file.Nc()
	if len(r) == 0 || len(r) > n {
		return false
	}
	b := t.file.b
	match := func(q int) bool {
		if q+len(r) > n {
			return false
		}
		for i, c := range r {
			if b[q+i] != c {
				return false
			}
		}
		return true
	}
	for i := 0; i <= n; i++ {
		q := (t.q1 + i) % (n + 1)
		if match(q) {
			t.setselect(q, q+len(r))
			return true
		}
	}
	return false
}

// lookcmd is the Look command: search for its argument, or the
// selection.
func lookcmd(et, t, argt *Text, arg string) {
	if et.w == nil {
		return
	}
	var r []rune
	if arg != "" {
		r = []rune(arg)
	} else {
		r = et.file.Read(et.q0, et.q1)
	}
	if search(&et.w.body, r) {
		et.w.body.show(et.w.body.q0, et.w.body.q1, true)
	}
}

// address evaluates an acme address (sam's syntax: #n, n, /re/, ?re?,
// $, ., 0, a,b, a;b, a+b, a-b) against t with dot q0 to q1, and
// returns its range or an error.
func address(t *Text, q0, q1 int, s string) (int, int, string) {
	a := &addrparser{t: t, s: []rune(s)}
	r0, r1, ok := a.compound(q0, q1)
	if a.err != "" {
		return q0, q1, a.err
	}
	if !ok || a.i < len(a.s) {
		return q0, q1, "bad address syntax"
	}
	return r0, r1, ""
}

type addrparser struct {
	t   *Text
	s   []rune
	i   int
	err string
}

func (a *addrparser) peek() rune {
	if a.i < len(a.s) {
		return a.s[a.i]
	}
	return 0
}

// compound is a1 [,;] a2.
func (a *addrparser) compound(q0, q1 int) (int, int, bool) {
	r0, r1, ok := a.simple(q0, q1)
	c := a.peek()
	if c != ',' && c != ';' {
		return r0, r1, ok
	}
	a.i++
	if !ok {
		r0, r1 = 0, 0
	}
	d0, d1 := q0, q1
	if c == ';' {
		d0, d1 = r0, r1
	}
	_, s1, ok2 := a.compound(d0, d1)
	if !ok2 {
		s1 = a.t.file.Nc()
	}
	if s1 < r0 {
		a.err = "addresses out of order"
	}
	return r0, s1, true
}

// simple is a basic address followed by + and - offsets.
func (a *addrparser) simple(q0, q1 int) (int, int, bool) {
	r0, r1, ok := a.basic(q0, q1, 1)
	for {
		c := a.peek()
		if c != '+' && c != '-' {
			return r0, r1, ok
		}
		a.i++
		dir := 1
		if c == '-' {
			dir = -1
		}
		if !ok {
			r0, r1 = q0, q1
		}
		n := a.peek()
		if n == 0 || n == '+' || n == '-' || n == ',' || n == ';' {
			// A bare + or - is a line.
			r0, r1, _ = a.line(r0, r1, dir, 1)
		} else {
			b0, b1, ok2 := a.basicRel(r0, r1, dir)
			if !ok2 {
				return r0, r1, false
			}
			r0, r1 = b0, b1
		}
		ok = true
	}
}

// basicRel is a basic address relative to dot in direction dir.
func (a *addrparser) basicRel(q0, q1, dir int) (int, int, bool) {
	c := a.peek()
	switch {
	case c >= '0' && c <= '9':
		n := a.number()
		return a.line(q0, q1, dir, n)
	case c == '#':
		a.i++
		n := a.number()
		if dir > 0 {
			q := min(q1+n, a.t.file.Nc())
			return q, q, true
		}
		q := max(q0-n, 0)
		return q, q, true
	case c == '/' || c == '?':
		return a.basic(q0, q1, dir)
	}
	return q0, q1, false
}

// basic is #n, n, /re/, ?re?, $, . or 0.
func (a *addrparser) basic(q0, q1, dir int) (int, int, bool) {
	t := a.t
	nc := t.file.Nc()
	c := a.peek()
	switch {
	case c == '#':
		a.i++
		n := a.number()
		if n > nc {
			a.err = "address out of range"
			return 0, 0, false
		}
		return n, n, true
	case c >= '0' && c <= '9':
		n := a.number()
		return a.lineaddr(0, 0, 1, n, true)
	case c == '$':
		a.i++
		return nc, nc, true
	case c == '.':
		a.i++
		return q0, q1, true
	case c == '/' || c == '?':
		a.i++
		d := c
		var pat []rune
		for a.i < len(a.s) && a.s[a.i] != d {
			if a.s[a.i] == '\\' && a.i+1 < len(a.s) && a.s[a.i+1] == d {
				a.i++
			}
			pat = append(pat, a.s[a.i])
			a.i++
		}
		if a.i < len(a.s) {
			a.i++
		}
		back := c == '?'
		if dir < 0 {
			back = !back
		}
		return a.regexp(string(pat), q0, q1, back)
	}
	return q0, q1, false
}

func (a *addrparser) number() int {
	j := a.i
	for a.i < len(a.s) && a.s[a.i] >= '0' && a.s[a.i] <= '9' {
		a.i++
	}
	n, _ := strconv.Atoi(string(a.s[j:a.i]))
	return n
}

// line returns line n: absolutely, or after (dir 1) or before (dir -1)
// dot, as sam's lineaddr does.
func (a *addrparser) line(q0, q1, dir, n int) (int, int, bool) {
	return a.lineaddr(q0, q1, dir, n, false)
}

func (a *addrparser) lineaddr(q0, q1, dir, l int, abs bool) (int, int, bool) {
	t := a.t
	nc := t.file.Nc()
	rng := func() (int, int, bool) { a.err = "address out of range"; return 0, 0, false }
	if dir >= 0 {
		var p, r0 int
		if l == 0 {
			if abs || q1 == 0 {
				return 0, 0, true
			}
			r0 = q1
			p = q1
		} else {
			n := 0
			if abs || q1 == 0 {
				p, n = 0, 1
			} else {
				p = q1 - 1
				if t.readc(p) == '\n' {
					n = 1
				}
				p++
			}
			for n < l {
				if p >= nc {
					return rng()
				}
				if t.readc(p) == '\n' {
					n++
				}
				p++
			}
			r0 = p
		}
		for p < nc {
			c := t.readc(p)
			p++
			if c == '\n' {
				break
			}
		}
		return r0, p, true
	}
	p := q0
	r1 := q0
	if l != 0 {
		for n := 0; n < l; {
			if p == 0 {
				if n++; n != l {
					return rng()
				}
			} else {
				c := t.readc(p - 1)
				if c != '\n' {
					p--
				} else if n++; n != l {
					p--
				}
			}
		}
		r1 = p
		if p > 0 {
			p--
		}
	}
	for p > 0 && t.readc(p-1) != '\n' {
		p--
	}
	return p, r1, true
}

// regexp finds pat after (or before) dot, wrapping round.
func (a *addrparser) regexp(pat string, q0, q1 int, back bool) (int, int, bool) {
	re, e := regex.Compile(pat, 0)
	if e != "" {
		a.err = e
		return 0, 0, false
	}
	b := a.t.file.b
	n := len(b)
	find := func(s int) (int, int, bool) {
		m := re.ExecuteSE(b, s, n, s == 0 || b[s-1] == '\n', true)
		if len(m) == 0 || m[0].Beg < 0 {
			return 0, 0, false
		}
		return m[0].Beg, m[0].End, true
	}
	if !back {
		if r0, r1, ok := find(q1); ok && !(r0 == r1 && r0 == q1 && q0 == q1 && q1 < n) {
			return r0, r1, true
		}
		if r0, r1, ok := find(0); ok {
			return r0, r1, true
		}
		a.err = "no match for regexp"
		return 0, 0, false
	}
	// Backwards: the last match starting before q0, wrapping.
	best0, best1, found := -1, -1, false
	for s := 0; s < n; {
		r0, r1, ok := find(s)
		if !ok {
			break
		}
		if r0 < q0 {
			best0, best1, found = r0, r1, true
		} else if found {
			break
		}
		s = max(r0+1, r1)
	}
	if !found {
		for s := 0; s < n; {
			r0, r1, ok := find(s)
			if !ok {
				break
			}
			best0, best1, found = r0, r1, true
			s = max(r0+1, r1)
		}
	}
	if !found {
		a.err = "no match for regexp"
		return 0, 0, false
	}
	return best0, best1, true
}
