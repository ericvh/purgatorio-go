package main

import (
	"io"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// The environment is /env, one file per variable, holding the value as
// ${quote} would write it (Inferno's env(2) as sh.y uses it).

const envdir = "/env/"

func nameok(v string) bool { return v != "" && !strings.Contains(v, "/") }

func envgetall() [][2]string {
	fd, err := sys.Open(envdir, sys.OREAD)
	if err != nil {
		return nil
	}
	var names []string
	for {
		d, err := sys.Dirread(fd)
		if err != nil || len(d) == 0 {
			break
		}
		for _, e := range d {
			names = append(names, e.Name)
		}
	}
	closefd(fd)
	var vl [][2]string
	for i := len(names) - 1; i >= 0; i-- {
		vl = append(vl, [2]string{names[i], getenv(names[i])})
	}
	return vl
}

func getenv(v string) string {
	if !nameok(v) {
		return ""
	}
	fd, err := sys.Open(envdir+v, sys.OREAD)
	if err != nil {
		return ""
	}
	defer closefd(fd)
	b, _ := io.ReadAll(fdreader(fd))
	return string(b)
}

func setenv(name string, val []*Listnode) {
	if !nameok(name) {
		return
	}
	s := quoted(val, true)
	if s == "" {
		_ = sys.Remove(envdir + name)
		return
	}
	fd, err := sys.Create(envdir+name, sys.OWRITE, 0o600)
	if err != nil {
		return
	}
	_ = writefd(fd, s)
	closefd(fd)
}

func envstringtoval(v string) []*Listnode { return stringlist(unquoted(v)) }

// unquoted splits s into words as String->unquoted does: blanks separate
// words, and single quotes quote, a doubled quote standing for one.
func unquoted(s string) []string {
	var args []string
	r := []rune(s)
	var word []rune
	inquote := false
	j := len(r)
	for j > 0 {
		c := r[j-1]
		if c == ' ' || c == '\t' || c == '\n' {
			j--
			continue
		}
		i := j - 1
		for ; i >= 0; i-- {
			c = r[i]
			if (c == ' ' || c == '\t' || c == '\n') && !inquote {
				break
			}
			if c == '\'' {
				word = append(append([]rune(nil), r[i+1:j]...), word...)
				j = i
				if !inquote || i == 0 || r[i-1] != '\'' {
					inquote = !inquote
				} else {
					i--
				}
			}
		}
		w := append(append([]rune(nil), r[i+1:j]...), word...)
		args = append([]string{string(w)}, args...)
		word = nil
		j = i
	}
	if inquote {
		return unquoted(s + "'")
	}
	return args
}
