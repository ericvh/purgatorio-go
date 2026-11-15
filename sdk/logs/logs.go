// Package logs reads and writes replica logs, the Plan 9 style records of
// changes to a file tree that updatelog writes and applylog and mergelog
// read (Inferno's appl/cmd/install/logs.b).
//
// Each line of a log is
//
//	time gen action path serverpath mode uid gid mtime length [digest]
//
// where action is a (add), c (contents), d (delete) or m (metadata), and
// a serverpath of "-" is the same as path.
package logs

import (
	"bufio"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Sort keys.
const (
	Byseq = iota
	Byname
)

// Entry is a log entry, or the state of a file built from them.
type Entry struct {
	Seq        int64 // time<<32 | gen
	Action     byte
	Path       string
	Serverpath string
	X          int
	D          sys.Dir
	Contents   []string // MD5 digests of the contents, most recent first
}

// Read reads the next entry; at end of file it returns nil and "".
func Read(in *bufio.Reader) (*Entry, string) {
	s, err := in.ReadString('\n')
	if s == "" && err != nil {
		return nil, ""
	}
	s = strings.TrimSuffix(s, "\n")
	e := &Entry{X: -1}
	fields := quote.Unquoted(s)
	if len(fields) < 10 || len(fields[2]) != 1 {
		return nil, "bad log entry: " + quote.Quote(s)
	}
	e.Action = fields[2][0]
	switch e.Action {
	case 'a', 'c', 'd', 'm':
	default:
		return nil, "bad log entry: " + quote.Quote(s)
	}
	time, ok1 := bigof(fields[0], 10)
	gen, ok2 := bigof(fields[1], 10)
	e.Seq = time<<32 | gen
	if fields[4] == "-" {
		fields[4] = fields[3]
	}
	e.Path = fields[3]
	e.Serverpath = fields[4]
	e.D = sys.NullDir()
	mode, ok3 := bigof(fields[5], 8)
	e.D.Mode = uint32(mode)
	e.D.Qid.Type = uint8(e.D.Mode >> 24)
	e.D.UID = fields[6]
	if e.D.UID == "-" {
		e.D.UID = ""
	}
	e.D.GID = fields[7]
	if e.D.GID == "-" {
		e.D.GID = ""
	}
	mtime, ok4 := bigof(fields[8], 10)
	e.D.Mtime = uint32(mtime)
	length, ok5 := bigof(fields[9], 10)
	e.D.Length = uint64(length)
	if !(ok1 && ok2 && ok3 && ok4 && ok5) {
		return nil, "invalid integer field in log entry " + quote.Quote(s)
	}
	if len(fields) > 10 {
		e.Contents = []string{fields[10]}
	}
	return e, ""
}

func bigof(s string, base int) (int64, bool) {
	v, err := strconv.ParseInt(s, base, 64)
	if err != nil {
		u, err2 := strconv.ParseUint(s, base, 64)
		return int64(u), err2 == nil
	}
	return v, true
}

// Mkpath joins root and name.
func Mkpath(root, name string) string {
	if root != "" && root[len(root)-1] != '/' && (name == "" || name[0] != '/') {
		return root + "/" + name
	}
	return root + name
}

func (e *Entry) contents() string {
	var b strings.Builder
	for _, c := range e.Contents {
		b.WriteString(" " + c)
	}
	return b.String()
}

func (e *Entry) action() byte {
	if e.Action == 0 {
		return '?'
	}
	return e.Action
}

// Text is the entry for debugging.
func (e *Entry) Text() string {
	return fmt.Sprintf("%d %d %s [%d] %c m=%o l=%d t=%d c=%s", e.Seq>>32, e.Seq&0xFFFFFFFF,
		quote.Quote(e.Path), e.X, e.action(), e.D.Mode, int64(e.D.Length), e.D.Mtime, quote.Quote(e.contents()))
}

// Sumtext is a summary of the entry's action.
func (e *Entry) Sumtext() string {
	switch e.Action {
	case 'a', 'm':
		return fmt.Sprintf("%c %s %o %s %s %d", e.Action, quote.Quote(e.Path), e.D.Mode,
			quote.Quote(e.D.UID), quote.Quote(e.D.GID), e.D.Mtime)
	case 'd', 'c':
		return fmt.Sprintf("%c %s", e.Action, quote.Quote(e.Path))
	}
	return "? " + quote.Quote(e.Path)
}

// Dbtext is the entry as a database line.
func (e *Entry) Dbtext() string {
	return fmt.Sprintf("%d %d %s - %o %s %s %d %d%s", e.Seq>>32, e.Seq&0xFFFFFFFF, quote.Quote(e.Path),
		e.D.Mode, quote.Quote(e.D.UID), quote.Quote(e.D.GID), e.D.Mtime, int64(e.D.Length), e.contents())
}

// Logtext is the entry as a log line.
func (e *Entry) Logtext() string {
	sf := e.Serverpath
	if sf == "" || sf == e.Path {
		sf = "-"
	}
	return fmt.Sprintf("%d %d %c %s %s %o %s %s %d %d%s", e.Seq>>32, e.Seq&0xFFFFFFFF, e.action(),
		quote.Quote(e.Path), quote.Quote(sf), e.D.Mode, quote.Quote(e.D.UID), quote.Quote(e.D.GID),
		e.D.Mtime, int64(e.D.Length), e.contents())
}

// Remove marks the entry deleted.
func (e *Entry) Remove() { e.Action = 'd' }

// Removed reports whether it is deleted.
func (e *Entry) Removed() bool { return e.Action == 'd' }

// Update applies log entry n to the state e.
func (e *Entry) Update(n *Entry) {
	if n == nil {
		return
	}
	if n.Action == 'd' {
		e.Contents = nil
	} else {
		e.D = n.D
	}
	if n.Action != 'm' || e.Action == 'd' {
		e.Action = n.Action
	}
	e.Serverpath = n.Serverpath
	e.Contents = append(append([]string(nil), n.Contents...), e.Contents...)
	if n.Seq > e.Seq {
		e.Seq = n.Seq
	}
}

// Db is a set of file states.
type Db struct {
	Name  string
	State []*Entry
	ht    map[string]*Entry
}

// New makes an empty Db.
func New(name string) *Db { return &Db{Name: name, ht: map[string]*Entry{}} }

// Look finds name's entry.
func (db *Db) Look(name string) *Entry { return db.ht[name] }

// Entry adds an entry for name.
func (db *Db) Entry(seq int64, name string, d sys.Dir) *Entry {
	e := &Entry{Action: 'a', Seq: seq, Path: name, D: d, X: len(db.State)}
	db.State = append(db.State, e)
	db.ht[name] = e
	return e
}

// Sort sorts the entries by key, stably.
func (db *Db) Sort(key int) {
	sort.SliceStable(db.State, func(i, j int) bool {
		if key == Byname {
			return db.State[i].Path < db.State[j].Path
		}
		return db.State[i].Seq < db.State[j].Seq
	})
}
