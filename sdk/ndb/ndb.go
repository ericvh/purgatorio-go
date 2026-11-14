// Package ndb reads attribute data bases (attrdb(6), ndb(6)) as
// Inferno's Attrdb module does (appl/lib/attrdb.b), and makes the hash
// files of ndb/mkhash in the format of Inferno's Attrhash (and Plan 9's
// ndb). An entry starts on a line at the left margin and continues on
// the following lines that start with a blank; each line holds
// attr=value pairs or lone attributes, and # starts a comment.
package ndb

import (
	"bytes"
	"os"
)

// Attr is one attribute and its value ("" for a lone attribute).
type Attr struct {
	Attr, Val string
}

// Tuples is one line of an entry.
type Tuples struct {
	Pairs []Attr
}

// HasAttr reports whether the line has attribute attr.
func (t *Tuples) HasAttr(attr string) bool {
	for _, a := range t.Pairs {
		if a.Attr == attr {
			return true
		}
	}
	return false
}

// HasPair reports whether the line has attr=value.
func (t *Tuples) HasPair(attr, value string) bool {
	for _, a := range t.Pairs {
		if a.Attr == attr && a.Val == value {
			return true
		}
	}
	return false
}

// Find returns the line's attr pairs, in order.
func (t *Tuples) Find(attr string) []Attr {
	var r []Attr
	for _, a := range t.Pairs {
		if a.Attr == attr {
			r = append(r, a)
		}
	}
	return r
}

// Match is a line of an entry and some of its pairs.
type Match struct {
	Line  *Tuples
	Attrs []Attr
}

// Entry is one data base entry.
type Entry struct {
	Lines []*Tuples
}

// Find returns the lines that have attr, with their attr pairs. As in
// Inferno, the pairs of each line come in reverse order.
func (e *Entry) Find(attr string) []Match {
	var r []Match
	for _, l := range e.Lines {
		if ra := l.Find(attr); ra != nil {
			rev := make([]Attr, len(ra))
			for i, a := range ra {
				rev[len(ra)-1-i] = a
			}
			r = append(r, Match{l, rev})
		}
	}
	return r
}

// FindFirst returns the value of the first attr pair in the entry.
func (e *Entry) FindFirst(attr string) string {
	for _, l := range e.Lines {
		for _, a := range l.Pairs {
			if a.Attr == attr {
				return a.Val
			}
		}
	}
	return ""
}

// FindPair returns the lines that have attr=value.
func (e *Entry) FindPair(attr, value string) []*Tuples {
	var r []*Tuples
	for _, l := range e.Lines {
		if l.HasPair(attr, value) {
			r = append(r, l)
		}
	}
	return r
}

// FindByAttr returns the lines that have rattr with their rattr pairs:
// first the lines that also have attr=value, then the others.
func (e *Entry) FindByAttr(attr, value, rattr string) []Match {
	var match, other []Match
	for _, l := range e.Lines {
		var ra []Attr
		m := false
		for _, a := range l.Pairs {
			if a.Attr == attr && a.Val == value {
				m = true
			}
			if a.Attr == rattr {
				ra = append(ra, a)
			}
		}
		if ra != nil {
			if m {
				match = append(match, Match{l, ra})
			} else {
				other = append(other, Match{l, ra})
			}
		}
	}
	return append(match, other...)
}

// Error is a syntax error in a line.
type Error string

func (e Error) Error() string { return string(e) }

// ParseLine parses one line. It returns nil and no error for a line
// with no pairs.
func ParseLine(s string) (*Tuples, error) {
	var attrs []Attr
	var quote rune
	var word, lastword, name []rune
	r := []rune(s)
Line:
	for i := 0; i < len(r); i++ {
		c := r[i]
		if quote != 0 {
			if c == quote {
				if i+1 >= len(r) || r[i+1] != quote {
					quote = 0
					continue
				}
				i++
			}
			word = append(word, c)
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '#':
			break Line
		case ' ', '\t', '\n':
			if word == nil {
				continue
			}
			if lastword != nil {
				attrs = append(attrs, Attr{string(lastword), ""})
			}
			lastword, word = word, nil
			if name != nil {
				attrs = append(attrs, Attr{string(name), string(lastword)})
				name, lastword = nil, nil
			}
		case '=':
			if lastword == nil {
				lastword, word = word, nil
			}
			if word != nil {
				attrs = append(attrs, Attr{string(lastword), ""})
				lastword, word = word, nil
			}
			if lastword == nil {
				return nil, Error("empty name")
			}
			name, lastword = lastword, nil
		default:
			word = append(word, c)
		}
	}
	if quote != 0 {
		return nil, Error("missing quote")
	}
	if lastword == nil {
		lastword = word
	}
	if name == nil {
		name, lastword = lastword, nil
	}
	if name != nil {
		attrs = append(attrs, Attr{string(name), string(lastword)})
	}
	if attrs == nil {
		return nil, nil
	}
	return &Tuples{attrs}, nil
}

// File is one data base file, read whole.
type File struct {
	Name  string
	Mtime uint32
	data  []byte
}

// OpenFile reads the file at path.
func OpenFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(f); err != nil {
		return nil, err
	}
	return &File{Name: path, Mtime: uint32(fi.ModTime().Unix()), data: b.Bytes()}, nil
}

// OpenString makes a File of data.
func OpenString(data string) *File { return &File{data: []byte(data)} }

func isentry(l []byte) bool {
	return len(l) > 0 && l[0] != '\t' && l[0] != '\n' && l[0] != ' ' && l[0] != '#'
}

// ReadEntry reads from offset to the first entry that has attr (attr=value
// with useval), or the next entry when attr is empty. It returns the
// entry, whether it matched, and the offset to continue from. When
// nothing matches the entry is nil.
func (f *File) ReadEntry(offset int, attr, value string, useval bool) (*Entry, bool, int) {
	pos := min(max(offset, 0), len(f.data))
	gets := func() []byte {
		if pos >= len(f.data) {
			return nil
		}
		i := bytes.IndexByte(f.data[pos:], '\n')
		if i < 0 {
			i = len(f.data) - pos - 1
		}
		l := f.data[pos : pos+i+1]
		pos += i + 1
		return l
	}
	match := attr == ""
	for l := gets(); l != nil; l = gets() {
		for isentry(l) {
			var lines []*Tuples
			for {
				offset = pos
				if t, _ := ParseLine(string(l)); t != nil {
					lines = append(lines, t)
					if !match {
						if useval {
							match = t.HasPair(attr, value)
						} else {
							match = t.HasAttr(attr)
						}
					}
				}
				l = gets()
				if l == nil || isentry(l) {
					break
				}
			}
			if match && lines != nil {
				return &Entry{lines}, true, offset
			}
		}
	}
	return nil, false, pos
}

// DB is a data base: one or more files searched in order.
type DB struct {
	Files []*File
}

// Open opens the data base at path and, when it has a database entry,
// the files that entry names, in their order; path itself is searched
// first unless the entry names it.
func Open(path string) (*DB, error) {
	df, err := OpenFile(path)
	if err != nil {
		return nil, err
	}
	db := &DB{[]*File{df}}
	e, _ := db.FindPair(nil, "database", "")
	if e == nil {
		return db, nil
	}
	var files []string
	for _, m := range e.Find("file") {
		for _, a := range m.Line.Find("file") {
			files = append(files, a.Val)
		}
	}
	if files == nil {
		return db, nil
	}
	var dbs []*File
	self := df
	for _, name := range files {
		if name == path && self != nil {
			dbs = append(dbs, self)
			self = nil
		} else if f, err := OpenFile(name); err == nil {
			dbs = append(dbs, f)
		}
	}
	if self != nil {
		dbs = append([]*File{self}, dbs...)
	}
	db.Files = dbs
	return db, nil
}

// Ptr is a position in a data base search; nil starts at the beginning.
type Ptr struct {
	file   int
	offset int
}

func (db *DB) next(p *Ptr, attr, value string, useval bool) (*Entry, *Ptr) {
	if p == nil {
		p = &Ptr{}
	}
	for p.file < len(db.Files) {
		e, match, next := db.Files[p.file].ReadEntry(p.offset, attr, value, useval)
		if match {
			return e, &Ptr{p.file, next}
		}
		if e == nil {
			p = &Ptr{p.file + 1, 0}
		} else {
			p = &Ptr{p.file, next}
		}
	}
	return nil, p
}

// Find returns the next entry with attribute attr.
func (db *DB) Find(p *Ptr, attr string) (*Entry, *Ptr) { return db.next(p, attr, "", false) }

// FindPair returns the next entry with attr=value.
func (db *DB) FindPair(p *Ptr, attr, value string) (*Entry, *Ptr) {
	return db.next(p, attr, value, true)
}

// FindByAttr returns the next entry with attr=value that also has rattr.
func (db *DB) FindByAttr(p *Ptr, attr, value, rattr string) (*Entry, *Ptr) {
	for {
		var e *Entry
		e, p = db.next(p, attr, value, true)
		if e == nil || e.Find(rattr) != nil {
			return e, p
		}
	}
}
