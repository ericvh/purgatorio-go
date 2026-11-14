package main

import (
	"strconv"
	"strings"
)

// stringModule is Inferno's string shell module (sh-string(1)).
type stringModule struct{}

var stringmod Module = &stringModule{}

func (m *stringModule) initbuiltin(c *Context) string {
	c.addbuiltin("prefix", m)
	c.addbuiltin("in", m)
	for _, n := range []string{"splitl", "splitr", "drop", "take", "splitstrl", "splitstrr",
		"tolower", "toupper", "len", "alen", "slice", "fields", "padl", "padr"} {
		c.addsbuiltin(n, m)
	}
	return ""
}

func (m *stringModule) whatis(*Context, string, int) string { return "" }

func (m *stringModule) runbuiltin(c *Context, argv []*Listnode, last bool) string {
	switch argv[0].word {
	case "prefix":
		a, b := earg2("prefix", c, argv)
		if !strings.HasPrefix(b, a) {
			return "false"
		}
	case "in":
		a, b := earg2("in", c, argv)
		if a == "" || !in([]rune(a)[0], b) {
			return "false"
		}
	}
	return ""
}

func (m *stringModule) runsbuiltin(c *Context, argv []*Listnode) []*Listnode {
	switch name := argv[0].word; name {
	case "splitl":
		return mk2(splitl(earg2(name, c, argv)))
	case "splitr":
		return mk2(splitr(earg2(name, c, argv)))
	case "drop":
		return mk1(drop(earg2(name, c, argv)))
	case "take":
		return mk1(take(earg2(name, c, argv)))
	case "splitstrl":
		a, b := earg2(name, c, argv)
		if b == "" {
			return mk2("", a)
		}
		if i := strings.Index(a, b); i >= 0 {
			return mk2(a[:i], a[i:])
		}
		return mk2(a, "")
	case "splitstrr":
		a, b := earg2(name, c, argv)
		if b == "" {
			return mk2(a, "")
		}
		if i := strings.LastIndex(a, b); i >= 0 {
			return mk2(a[:i+len(b)], a[i+len(b):])
		}
		return mk2("", a)
	case "tolower":
		return mk1(asciimap(earg1("tolower", c, argv), 'A', 'Z', 'a'-'A'))
	case "toupper":
		return mk1(asciimap(earg1("tolower", c, argv), 'a', 'z', 'A'-'a'))
	case "len":
		return mk1(strconv.Itoa(len([]rune(earg1("len", c, argv)))))
	case "alen":
		return mk1(strconv.Itoa(len(earg1("alen", c, argv))))
	case "slice":
		return sbuiltinSlice(c, argv)
	case "fields":
		return sbuiltinFields(c, argv)
	case "padl":
		return sbuiltinPad(c, argv, -1)
	case "padr":
		return sbuiltinPad(c, argv, 1)
	}
	return nil
}

func splitl(s, cl string) (string, string) {
	r := []rune(s)
	for j := range r {
		if in(r[j], cl) {
			return string(r[:j]), string(r[j:])
		}
	}
	return s, ""
}

func splitr(s, cl string) (string, string) {
	r := []rune(s)
	for j := len(r) - 1; j >= 0; j-- {
		if in(r[j], cl) {
			return string(r[:j+1]), string(r[j+1:])
		}
	}
	return "", s
}

func drop(s, cl string) string {
	r := []rune(s)
	for j := range r {
		if !in(r[j], cl) {
			return string(r[j:])
		}
	}
	return ""
}

func take(s, cl string) string {
	r := []rune(s)
	for j := range r {
		if !in(r[j], cl) {
			return string(r[:j])
		}
	}
	return s
}

func asciimap(s string, lo, hi rune, d rune) string {
	r := []rune(s)
	for i, c := range r {
		if c >= lo && c <= hi {
			r[i] = c + d
		}
	}
	return string(r)
}

func sbuiltinPad(c *Context, argv []*Listnode, dir int) []*Listnode {
	if len(argv) < 2 || !isnum(argv[1].word) {
		c.fail("usage", "usage: "+argv[0].word+" n [arg...]")
	}
	n := limboAtoi(argv[1].word) * dir
	s := squash(words(argv[2:]), " ")
	if w := len([]rune(s)); n < 0 && w < -n {
		s += strings.Repeat(" ", -n-w)
	} else if n > 0 && w < n {
		s = strings.Repeat(" ", n-w) + s
	}
	return mk1(s)
}

func sbuiltinFields(c *Context, argv []*Listnode) []*Listnode {
	argv = argv[1:]
	if len(argv) != 2 {
		c.fail("usage", "usage: fields cl s")
	}
	cl := word(argv[0])
	s := []rune(word(argv[1]))
	var r []*Listnode
	n := 0
	for i := range s {
		if in(s[i], cl) {
			r = append(r, wordnode(string(s[n:i])))
			n = i + 1
		}
	}
	return append(r, wordnode(string(s[n:])))
}

func sbuiltinSlice(c *Context, argv []*Listnode) []*Listnode {
	argv = argv[1:]
	if len(argv) != 3 || !isnum(argv[0].word) || (argv[1].word != "end" && !isnum(argv[1].word)) {
		c.fail("usage", "usage: slice start end arg")
	}
	n1 := limboAtoi(argv[0].word)
	s := []rune(word(argv[2]))
	n2 := len(s)
	if argv[1].word != "end" {
		n2 = limboAtoi(argv[1].word)
	}
	if n2 > len(s) {
		n2 = len(s)
	}
	if n1 > len(s) {
		n1 = len(s)
	}
	r := ""
	if n2 > n1 {
		r = string(s[n1:n2])
	}
	return mk1(r)
}

func earg2(cmd string, c *Context, argv []*Listnode) (string, string) {
	argv = argv[1:]
	if len(argv) != 2 {
		c.fail("usage", "usage: "+cmd+" arg1 arg2")
	}
	return word(argv[0]), word(argv[1])
}

func earg1(cmd string, c *Context, argv []*Listnode) string {
	if len(argv) != 2 {
		c.fail("usage", "usage: "+cmd+" arg")
	}
	return word(argv[1])
}

func mk1(s string) []*Listnode    { return []*Listnode{wordnode(s)} }
func mk2(a, b string) []*Listnode { return []*Listnode{wordnode(a), wordnode(b)} }
