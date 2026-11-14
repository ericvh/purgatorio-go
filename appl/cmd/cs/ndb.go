package main

import (
	"os"
	"strings"
)

// A small reader for the attribute data base format of attrdb(6), as much
// as cs needs: entries start at the left margin and continue on indented
// lines; each line holds attr=value pairs (or a lone attr); # starts a
// comment; values may be quoted.

type pair struct{ attr, val string }

type tuples []pair // one line

type entry []tuples

type db struct {
	files   []string
	entries []entry
}

// parseline parses one line of pairs.
func parseline(s string) (tuples, string) {
	var t tuples
	var word strings.Builder
	have := false
	var quote byte
	flush := func() {
		if !have {
			return
		}
		w := word.String()
		word.Reset()
		have = false
		if a, v, ok := strings.Cut(w, "\x00"); ok {
			t = append(t, pair{a, v})
		} else {
			t = append(t, pair{w, ""})
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				if i+1 >= len(s) || s[i+1] != quote {
					quote = 0
					continue
				}
				i++
			}
			word.WriteByte(c)
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			have = true
		case '#':
			flush()
			return t, ""
		case ' ', '\t', '\n', '\r':
			flush()
		case '=':
			if !have || strings.Contains(word.String(), "\x00") {
				return nil, "bad attribute syntax"
			}
			word.WriteByte(0)
		default:
			word.WriteByte(c)
			have = true
		}
	}
	if quote != 0 {
		return nil, "missing quote"
	}
	flush()
	return t, ""
}

func parse(data string) []entry {
	var es []entry
	var cur entry
	for _, line := range strings.Split(data, "\n") {
		t, _ := parseline(line)
		if len(t) == 0 {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			if cur != nil {
				es = append(es, cur)
			}
			cur = nil
		}
		cur = append(cur, t)
	}
	if cur != nil {
		es = append(es, cur)
	}
	return es
}

// opendb reads path and, if it has a database entry, the files that
// entry names, as attrdb's Db.open does.
func opendb(path string) (*db, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d := &db{files: []string{path}, entries: parse(string(b))}
	for _, e := range d.entries {
		if !e.has("database", "") {
			continue
		}
		var files []string
		for _, t := range e {
			for _, p := range t {
				if p.attr == "file" {
					files = append(files, p.val)
				}
			}
		}
		if len(files) == 0 {
			break
		}
		nd := &db{}
		self := false
		for _, f := range files {
			if f == path && !self {
				self = true
				nd.files = append(nd.files, path)
				nd.entries = append(nd.entries, d.entries...)
			} else if b, err := os.ReadFile(f); err == nil {
				nd.files = append(nd.files, f)
				nd.entries = append(nd.entries, parse(string(b))...)
			}
		}
		if !self {
			nd.files = append([]string{path}, nd.files...)
			nd.entries = append(d.entries, nd.entries...)
		}
		return nd, nil
	}
	return d, nil
}

func (d *db) append(o *db) *db {
	if d == nil {
		return o
	}
	if o == nil {
		return d
	}
	return &db{files: append(d.files, o.files...), entries: append(d.entries, o.entries...)}
}

func (e entry) has(attr, val string) bool {
	for _, t := range e {
		if t.has(attr, val) {
			return true
		}
	}
	return false
}

func (t tuples) has(attr, val string) bool {
	for _, p := range t {
		if p.attr == attr && p.val == val {
			return true
		}
	}
	return false
}

// findbyattr returns the values of rattr in entry e, those on lines that
// also have attr=val first, as Dbentry.findbyattr orders them.
func (e entry) findbyattr(attr, val, rattr string) []string {
	var match, other []string
	for _, t := range e {
		m := t.has(attr, val)
		for _, p := range t {
			if p.attr == rattr {
				if m {
					match = append(match, p.val)
				} else {
					other = append(other, p.val)
				}
			}
		}
	}
	return append(match, other...)
}

// lookup returns the values of rattr in the entries that have attr=val,
// without repeats.
func (d *db) lookup(attr, val, rattr string) []string {
	if d == nil {
		return nil
	}
	var rl []string
	seen := map[string]bool{}
	for _, e := range d.entries {
		if !e.has(attr, val) {
			continue
		}
		for _, v := range e.findbyattr(attr, val, rattr) {
			if !seen[v] {
				seen[v] = true
				rl = append(rl, v)
			}
		}
	}
	return rl
}

// first returns the first value of rattr on a line with attr=val, and
// whether that line also has attribute flag.
func (d *db) first(attr, val, rattr, flag string) (string, bool, bool) {
	if d == nil {
		return "", false, false
	}
	for _, e := range d.entries {
		if !e.has(attr, val) {
			continue
		}
		for _, t := range e {
			m := t.has(attr, val)
			for _, p := range t {
				if m && p.attr == rattr {
					f := false
					for _, q := range t {
						f = f || q.attr == flag
					}
					return p.val, f, true
				}
			}
		}
	}
	return "", false, false
}
