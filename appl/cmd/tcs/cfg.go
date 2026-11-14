package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// The character set file uses Inferno's cfg(2) syntax: a record starts
// on a line that does not begin with white space and continues on the
// lines that do. Each line is a tuple of name=value attributes.

type attr struct{ name, value string }

type tuple []attr

func (t tuple) lookup(name string) string {
	for _, a := range t {
		if a.name == name {
			return a.value
		}
	}
	return ""
}

type record []tuple

// lookup returns the value of the first attribute called name and the
// tuple holding it.
func (r record) lookup(name string) (string, tuple) {
	for _, t := range r {
		for _, a := range t {
			if a.name == name {
				return a.value, t
			}
		}
	}
	return "", nil
}

// entry is every record whose first attribute has one name, with that
// attribute's value.
type entry struct {
	value string
	rec   record
}

type cfg struct {
	keys  []string
	table map[string][]entry
}

func readcfg(path string) (*cfg, string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "bad path: " + sys.Errstr()
	}
	defer f.Close()
	c := &cfg{table: map[string][]entry{}}
	var recs []record
	var r record
	b := bufio.NewReader(f)
	for lnum := 1; ; lnum++ {
		line, rerr := b.ReadString('\n')
		if line == "" && rerr != nil {
			break
		}
		t, e := parseline(line)
		if e != "" {
			return nil, fmt.Sprintf("%s:%d: %s", path, lnum, e)
		}
		if t == nil {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' && r != nil {
			recs = append(recs, r)
			r = nil
		}
		r = append(r, t)
	}
	if r != nil {
		recs = append(recs, r)
	}
	for _, r := range recs {
		name, value := r[0][0].name, r[0][0].value
		if _, ok := c.table[name]; !ok {
			c.keys = append(c.keys, name)
		}
		c.table[name] = append(c.table[name], entry{value, r})
	}
	return c, ""
}

// parseline splits a line into attributes: name=value, or a bare word
// with no value. Values may be quoted with ' or ", doubling the quote to
// include it; # starts a comment. As in cfg(2), an empty string counts
// as absent.
func parseline(s string) (tuple, string) {
	var attrs tuple
	var quote rune
	word, last, name := "", "", ""
	rs := []rune(s)
loop:
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(rs) && rs[i+1] == quote {
					word += string(quote)
					i++
				} else {
					quote = 0
				}
			} else {
				word += string(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '#':
			break loop
		case ' ', '\t', '\n', '\r':
			if word == "" {
				continue
			}
			if last != "" {
				attrs = append(attrs, attr{last, ""})
			}
			last, word = word, ""
			if name != "" {
				attrs = append(attrs, attr{name, last})
				name, last = "", ""
			}
		case '=':
			if last == "" {
				last, word = word, ""
			}
			if word != "" {
				attrs = append(attrs, attr{last, ""})
				last, word = word, ""
			}
			if last == "" {
				return nil, "empty name"
			}
			name, last = last, ""
		default:
			word += string(c)
		}
	}
	if quote != 0 {
		return nil, "missing quote"
	}
	if last == "" {
		last = word
	}
	if name == "" {
		name, last = last, ""
	}
	if name != "" {
		attrs = append(attrs, attr{name, last})
	}
	return attrs, ""
}

// alias finds the record for a character set, following one level of
// renaming.
func (c *cfg) alias(cs string) (record, string) {
	for i := 0; i < 2; i++ {
		e := c.table[cs]
		if len(e) == 0 {
			return nil, "unknown charset " + cs
		}
		if e[0].value != "" {
			cs = e[0].value
			continue
		}
		return e[0].rec, ""
	}
	return nil, "too man aliases for " + cs + "\n"
}

type csinfo struct {
	name, desc string
	btos, stob bool
}

// enum lists the character sets with converters, in file order.
func (c *cfg) enum() []csinfo {
	var l []csinfo
	for _, cs := range c.keys {
		e := c.table[cs]
		if e[0].value != "" {
			continue
		}
		b, _ := e[0].rec.lookup("btos")
		s, _ := e[0].rec.lookup("stob")
		if b == "" && s == "" {
			continue
		}
		desc, _ := e[0].rec.lookup("desc")
		if desc == "" {
			desc = cs
		}
		l = append(l, csinfo{cs, desc, b != "", s != ""})
	}
	return l
}

// aliases returns a character set's description and its names, the
// standard one first; on error the first result is the message.
func (c *cfg) aliases(cs string) (string, []string) {
	rec, err := c.alias(strings.ToLower(cs))
	if err != "" {
		return err, nil
	}
	cs = rec[0][0].name
	desc, _ := rec.lookup("desc")
	if desc == "" {
		desc = cs
	}
	al := []string{cs}
	for _, name := range c.keys {
		if c.table[name][0].value == cs {
			al = append(al, name)
		}
	}
	return desc, al
}
