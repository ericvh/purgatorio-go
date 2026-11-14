// Package core is the portable Inferno kernel: channels, devices, name
// spaces, file descriptor and environment groups, and progs. It reaches
// the host only through package platform.
package core

import "strings"

// Cleanname returns the shortest equivalent of a slash-separated path,
// by Plan 9 and Inferno lexical rules: repeated slashes collapse, "."
// elements go, and ".." removes the previous element (or is dropped at the
// root). Device paths such as "#c/cons" keep their leading element, and
// ".." never climbs above it.
func Cleanname(name string) string {
	if name == "" {
		return "."
	}
	rooted := name[0] == '/'
	var dev string
	rest := name
	if name[0] == '#' {
		if i := strings.IndexByte(name, '/'); i >= 0 {
			dev, rest = name[:i], name[i+1:]
		} else {
			return name
		}
		rooted = true
	}
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
	p := strings.Join(out, "/")
	switch {
	case dev != "":
		if p == "" {
			return dev
		}
		return dev + "/" + p
	case rooted:
		return "/" + p
	case p == "":
		return "."
	}
	return p
}
