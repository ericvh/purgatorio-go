// Package core is the portable Inferno kernel: channels, devices, name
// spaces, file descriptor and environment groups, and progs. It reaches
// the host only through package platform, so the hosted emu and the
// bare-metal kernel run this same code.
package core

import "strings"

// Cleanname returns the shortest lexically equivalent path, by Plan 9 and
// Inferno rules: repeated slashes collapse, "." elements go, and ".."
// removes the previous element (or is dropped at the root). A device
// prefix such as "#c" or "#Uspec" is the root of its path.
func Cleanname(path string) string {
	prefix, rest, isdev := SplitDevice(path)
	rooted := isdev || strings.HasPrefix(rest, "/")
	var out []string
	for _, e := range strings.Split(rest, "/") {
		switch e {
		case "", ".":
		case "..":
			if len(out) > 0 && out[len(out)-1] != ".." {
				out = out[:len(out)-1]
			} else if !rooted {
				out = append(out, "..")
			}
		default:
			out = append(out, e)
		}
	}
	var s strings.Builder
	if isdev {
		s.WriteString(prefix)
		if len(out) > 0 {
			s.WriteByte('/')
		}
	} else if rooted {
		s.WriteByte('/')
	}
	s.WriteString(strings.Join(out, "/"))
	if s.Len() == 0 {
		return "."
	}
	return s.String()
}

// SplitDevice splits "#cspec/rest" into ("#cspec", "/rest", true); other
// paths return ("", path, false).
func SplitDevice(path string) (prefix, rest string, isdev bool) {
	if !strings.HasPrefix(path, "#") {
		return "", path, false
	}
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i], path[i:], true
	}
	return path, "", true
}

// Elements returns the non-empty elements of a cleaned path, without any
// device prefix.
func Elements(path string) []string {
	_, rest, _ := SplitDevice(path)
	var out []string
	for _, e := range strings.Split(rest, "/") {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// Absolute makes path absolute against dot and cleans it; "#" paths stay
// as they are, cleaned.
func Absolute(path, dot string) string {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "#") {
		return Cleanname(path)
	}
	return Cleanname(dot + "/" + path)
}

// Quote quotes s as Inferno's %q does when it is empty or holds white space
// or a quote: wrapped in single quotes, with quotes doubled.
func Quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r'") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
