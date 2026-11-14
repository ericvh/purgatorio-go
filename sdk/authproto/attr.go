package authproto

import "strings"

// Attribute lists, as factotum's ctl and rpc take them: words separated
// by blanks, quoted with ' ('' for a quote), each name=value.

// Attr is one attribute.
type Attr struct{ Name, Value string }

// Tokenize splits s into words; ok is false for an unclosed quote.
func Tokenize(s string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	have, quoted := false, false
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		if quoted {
			if c == '\'' {
				if i+1 < len(r) && r[i+1] == '\'' {
					i++
					cur.WriteRune('\'')
				} else {
					quoted = false
				}
			} else {
				cur.WriteRune(c)
			}
			continue
		}
		switch c {
		case ' ', '\t', '\n', '\r':
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		case '\'':
			quoted, have = true, true
		default:
			cur.WriteRune(c)
			have = true
		}
	}
	if quoted {
		return nil, false
	}
	if have {
		out = append(out, cur.String())
	}
	return out, true
}

// Quote quotes s when it is empty or holds a blank or a quote.
func Quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r'") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ParseAttrs parses an attribute list.
func ParseAttrs(s string) ([]Attr, bool) {
	w, ok := Tokenize(s)
	if !ok {
		return nil, false
	}
	a := make([]Attr, 0, len(w))
	for _, x := range w {
		n, v, _ := strings.Cut(x, "=")
		a = append(a, Attr{Name: n, Value: v})
	}
	return a, true
}

// Get is the value of name in a, and whether it is there.
func Get(a []Attr, name string) (string, bool) {
	for _, x := range a {
		if x.Name == name {
			return x.Value, true
		}
	}
	return "", false
}
