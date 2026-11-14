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
