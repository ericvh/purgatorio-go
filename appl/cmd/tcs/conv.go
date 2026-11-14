package main

import "unicode/utf8"

// A btos converts bytes to runes and reports how many bytes it used; an
// incomplete sequence at the end is left for the next call unless flush
// is set. A stob converts runes to bytes; nil ends the output.
type btos interface {
	btos(b []byte, flush bool) ([]rune, int)
}

type stob interface {
	stob(r []rune) []byte
}

type utf8btos struct{}

// btos takes every complete rune. When flushing, a partial sequence is
// dropped, as Inferno's converter does.
func (utf8btos) btos(b []byte, flush bool) ([]rune, int) {
	if flush {
		return nil, 0
	}
	var r []rune
	i := 0
	for i < len(b) && utf8.FullRune(b[i:]) {
		c, n := utf8.DecodeRune(b[i:])
		r = append(r, c)
		i += n
	}
	return r, i
}

type utf8stob struct{}

func (utf8stob) stob(r []rune) []byte { return []byte(string(r)) }

type utf16btos struct {
	started bool
	big     bool
}

// btos honours a byte order mark at the start and otherwise uses the
// configured order.
func (u *utf16btos) btos(b []byte, flush bool) ([]rune, int) {
	i := 0
	if !u.started && len(b) >= 2 {
		u.started = true
		switch uint16(b[0])<<8 | uint16(b[1]) {
		case 0xfeff:
			u.big = true
			i = 2
		case 0xfffe:
			u.big = false
			i = 2
		}
	}
	nb := len(b) &^ 1
	var r []rune
	for ; i < nb; i += 2 {
		if u.big {
			r = append(r, rune(b[i])<<8|rune(b[i+1]))
		} else {
			r = append(r, rune(b[i])|rune(b[i+1])<<8)
		}
	}
	if flush && i < len(b) {
		r = append(r, utf8.RuneError)
	}
	return r, i
}

type utf16stob struct {
	started     bool
	big, header bool
}

func (u *utf16stob) stob(r []rune) []byte {
	if !u.started {
		u.started = true
		if u.header {
			r = append([]rune{0xfeff}, r...)
		}
	}
	b := make([]byte, 0, 2*len(r))
	for _, c := range r {
		if u.big {
			b = append(b, byte(c>>8), byte(c))
		} else {
			b = append(b, byte(c), byte(c>>8))
		}
	}
	return b
}

type cpbtos struct{ cp []rune }

func (c *cpbtos) btos(b []byte, flush bool) ([]rune, int) {
	r := make([]rune, len(b))
	for i, x := range b {
		r[i] = c.cp[x]
	}
	return r, len(b)
}

// cpstob maps runes back through a code page; others become '?'.
type cpstob map[rune]byte

func (m cpstob) stob(r []rune) []byte {
	b := make([]byte, len(r))
	for i, c := range r {
		x, ok := m[c]
		if !ok {
			x = '?'
		}
		b[i] = x
	}
	return b
}
