package lethe

import "strings"

const (
	// MaxPath is the longest path.
	MaxPath = 1024
	// MaxElem is the longest element.
	MaxElem = 255
)

// ValidPath reports whether p is a Lethe path: absolute, with no empty,
// "." or ".." element, no NUL, and no trailing / except the root.
func ValidPath(p string) bool {
	if p == "/" {
		return true
	}
	if len(p) > MaxPath || !strings.HasPrefix(p, "/") || strings.IndexByte(p, 0) >= 0 {
		return false
	}
	for _, e := range strings.Split(p[1:], "/") {
		if e == "" || e == "." || e == ".." || len(e) > MaxElem {
			return false
		}
	}
	return true
}

// ValidElem reports whether name can be one element of a path.
func ValidElem(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= MaxElem &&
		!strings.ContainsAny(name, "/\x00")
}

// Within reports whether p is prefix or below it.
func Within(p, prefix string) bool {
	return prefix == "/" || p == prefix ||
		(strings.HasPrefix(p, prefix) && len(p) > len(prefix) && p[len(prefix)] == '/')
}

// Join joins dir and the element name.
func Join(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

// Ancestors are the directories above p, from the root down, without p.
func Ancestors(p string) []string {
	if p == "/" {
		return nil
	}
	var out []string
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			if i == 0 {
				out = append(out, "/")
			} else {
				out = append(out, p[:i])
			}
		}
	}
	return out
}
