package main

import (
	"github.com/ericvh/purgatorio-go/sdk/filepat"
	"github.com/ericvh/purgatorio-go/sdk/regex"
)

// A value is a singleton s, or a list l, or neither (null); x says the
// singleton is a file name pattern.

// glob expands a file name pattern; one matching nothing stands for
// itself.
func glob(s string) (string, []string) {
	if l := filepat.Expand(s); len(l) > 0 {
		return "", l
	}
	return s, nil
}

// match reports whether regular expression s2 matches in s1.
func match(s1, s2 string) bool {
	re, _ := regex.Compile(s2, 0)
	if re == nil {
		return false
	}
	return re.Execute([]rune(s1)) != nil
}

// match2 matches two values; two lists never match.
func match2(s1 string, l1 []string, s2 string, l2 []string) bool {
	switch {
	case s1 != "":
		if s2 != "" {
			return match(s1, s2)
		}
		for _, s := range l2 {
			if match(s1, s) {
				return true
			}
		}
	case l1 != nil:
		if s2 == "" {
			return false
		}
		for _, s := range l1 {
			if match(s, s2) {
				return true
			}
		}
	case s2 != "":
		return match("", s2)
	case l2 != nil:
		for _, s := range l2 {
			if match("", s) {
				return true
			}
		}
	default:
		return true
	}
	return false
}

func eqlist(l1, l2 []string) bool {
	if len(l1) != len(l2) {
		return false
	}
	for i := range l1 {
		if l1[i] != l2[i] {
			return false
		}
	}
	return true
}

func (c *Cmd) evaleq(e *Env) bool {
	s1, l1, _ := c.left.eeval2(e)
	s2, l2, _ := c.right.eeval2(e)
	if s1 != "" {
		return s1 == s2
	}
	if l1 != nil {
		return eqlist(l1, l2)
	}
	return s2 == "" && l2 == nil
}

func (c *Cmd) evalmatch(e *Env) bool {
	s1, l1, _ := c.left.eeval2(e)
	s2, l2, _ := c.right.eeval2(e)
	return match2(s1, l1, s2, l2)
}

func (i *Item) caret(e *Env) (string, []string, int) {
	s1, l1, x1 := i.left.ieval2(e)
	s2, l2, x2 := i.right.ieval2(e)
	return caret(s1, l1, x1, s2, l2, x2)
}

// caret concatenates two values: a singleton distributes, two lists
// join pairwise, padded with null strings.
func caret(s1 string, l1 []string, x1 int, s2 string, l2 []string, x2 int) (string, []string, int) {
	var l []string
	switch {
	case s1 != "":
		if s2 != "" {
			return s1 + s2, nil, x1 | x2
		}
		if l2 == nil {
			return s1, nil, x1
		}
		for _, s := range l2 {
			l = append(l, s1+s)
		}
	case s2 != "":
		if l1 == nil {
			return s2, nil, x2
		}
		for _, s := range l1 {
			l = append(l, s+s2)
		}
	case l1 != nil:
		if l2 == nil {
			return "", l1, 0
		}
		for i := 0; i < len(l1) || i < len(l2); i++ {
			var a, b string
			if i < len(l1) {
				a = l1[i]
			}
			if i < len(l2) {
				b = l2[i]
			}
			l = append(l, a+b)
		}
	case l2 != nil:
		return "", l2, 0
	}
	return "", l, 0
}
