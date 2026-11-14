// Package quote formats strings as Inferno's %q verb does: a string that
// is empty or holds a space, a control character or a quote is put in
// single quotes, with each quote doubled.
package quote

import "strings"

// Quote returns s as %q prints it.
func Quote(s string) string {
	if !needs(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func needs(s string) bool {
	if s == "" {
		return true
	}
	for _, c := range s {
		if c <= ' ' || c == '\'' {
			return true
		}
	}
	return false
}

// Unquoted splits s into words as String->unquoted does: words are
// separated by white space, and single quotes, with a doubled quote for a
// quote, keep spaces in a word.
func Unquoted(s string) []string {
	var out []string
	var cur strings.Builder
	in, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			if in && i+1 < len(s) && s[i+1] == '\'' {
				cur.WriteByte('\'')
				i++
			} else {
				in = !in
			}
			have = true
		case !in && (c == ' ' || c == '\t' || c == '\n'):
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}
