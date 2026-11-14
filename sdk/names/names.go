// Package names manipulates path names as Inferno's Names module does
// (appl/lib/names.b, names(2)).
package names

import "strings"

// Cleanname rewrites name to compress repeated slashes, remove "."
// elements and interpret "..", lexically. A leading "#x" device name is
// the root of its path. The empty name is ".".
func Cleanname(name string) string {
	if name == "" {
		return "."
	}
	rooted := 0
	if name[0] == '/' {
		rooted = 1
	}
	p := rooted
	if name[0] == '#' {
		if len(name) < 2 {
			return name
		}
		// The character after # is the device name, whatever it is.
		for p += 2; p < len(name); p++ {
			if name[p] == '/' {
				break
			}
		}
		rooted = p
	}
	dotdot := rooted
	out := name[:rooted]
	for p < len(name) {
		q := p
		for p < len(name) && name[p] != '/' {
			p++
		}
		n := name[q:p]
		p++
		switch n {
		case "", ".":
		case "..":
			if len(out) > dotdot {
				q = len(out) - 1
				for q > dotdot && out[q] != '/' {
					q--
				}
				out = out[:q]
			} else if rooted == 0 {
				if out != "" {
					out += "/.."
				} else {
					out += ".."
				}
				dotdot = len(out)
			}
		default:
			if rooted > 1 || len(out) > rooted {
				out += "/"
			}
			out += n
		}
	}
	if out == "" {
		return "."
	}
	return out
}

// Dirname returns everything before the last slash, "/" for a name in
// the root, or "" when name has no slash.
func Dirname(name string) string {
	i := strings.LastIndexByte(name, '/')
	if i < 0 {
		return ""
	}
	if i > 0 {
		return name[:i]
	}
	return "/"
}

// Basename returns the last element of name, less suffix if it ends so.
func Basename(name, suffix string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if suffix != "" && len(name) >= len(suffix) && strings.HasSuffix(name, suffix) {
		return name[:len(name)-len(suffix)]
	}
	return name
}
