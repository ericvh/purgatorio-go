// Package proto reads proto files, the descriptions of file trees that
// mkfs(8) and the install tools take (Inferno's appl/lib/fsproto.b and
// appl/cmd/install/proto.b; proto(6)).
//
// Each line names a file, indented one level deeper than its parent
// directory, then optionally its mode, owner, group and the file it is
// copied from:
//
//	name [mode [uid [gid [source]]]]
//
// A name of "+" stands for every file under the parent, recursively, "*"
// for the files and directories directly in it, and "%" for the files
// only. A name of $user is replaced by the user's name.
package proto

import (
	"bufio"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Warning levels, as install's Protocaller's.
const (
	Warn = iota
	Error
	Fatal
)

// Reader walks one proto file. Entry is called for each file, with the
// name it has in the new tree, the name of the file it is copied from,
// and that file's Dir with the mode the proto gives. Problem is called
// with each warning; it may be nil.
type Reader struct {
	// Install selects install/proto.b's dialect: no quoting of names,
	// no append and exclusive mode letters, and no environment
	// variables but $user. Otherwise it is fsproto.b's.
	Install bool
	Entry   func(newf, oldf string, d *sys.Dir)
	Problem func(level, line int, oldf, msg string)

	b       *bufio.Reader
	lines   []string
	next    int
	indent  int
	lineno  int
	newfile string
	oldfile string
	oldroot string
}

type file struct {
	new, elem, old string
	uid, gid       string
	mode           uint32
}

// ReadFile reads the proto file name, taking source files from below
// root, and returns the error if it cannot be opened.
func (r *Reader) ReadFile(name, root string) error {
	f, err := os.Open(name)
	if err != nil {
		return sys.Error(sys.Errstr())
	}
	defer f.Close()
	r.Read(f, root)
	return nil
}

// Read reads a proto from in.
func (r *Reader) Read(in io.Reader, root string) {
	r.b = bufio.NewReader(in)
	r.lines, r.next = nil, 0
	for {
		s, err := r.b.ReadString('\n')
		if s != "" {
			if !strings.HasSuffix(s, "\n") {
				s += "\n"
			}
			r.lines = append(r.lines, s)
		}
		if err != nil {
			break
		}
	}
	r.oldroot = root
	r.lineno = 0
	r.indent = 0
	r.walk(&file{}, -1)
}

func (r *Reader) problem(level int, msg string) {
	if r.Problem != nil {
		r.Problem(level, r.lineno, r.oldfile, msg)
	}
}

func (r *Reader) walk(me *file, level int) {
	child, fp := r.getfile(me)
	if child == nil {
		return
	}
	if child.elem == "+" || child.elem == "*" || child.elem == "%" {
		rec := child.elem == "+"
		filesonly := child.elem == "%"
		child.new = me.new
		r.setnames(child)
		r.mktree(child, rec, filesonly)
		child, fp = r.getfile(me)
	}
	for child != nil && r.indent > level {
		if r.mkfile(child) {
			r.walk(child, r.indent)
		}
		child, fp = r.getfile(me)
	}
	if child != nil {
		r.next = fp
		r.lineno--
	}
}

// ReadDir returns the entries of directory name sorted by name, with
// repeated names (from unions) left out, as Readdir's NAME|COMPACT.
func ReadDir(name string) ([]sys.Dir, error) {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return nil, err
	}
	defer sys.Close(fd)
	var all []sys.Dir
	seen := map[string]bool{}
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			return all, err
		}
		if len(ds) == 0 {
			break
		}
		for _, d := range ds {
			if !seen[d.Name] {
				seen[d.Name] = true
				all = append(all, d)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

func (r *Reader) mktree(me *file, rec, filesonly bool) {
	ds, err := ReadDir(r.oldfile)
	if err != nil && ds == nil {
		r.problem(Warn, "can't open "+r.oldfile+": "+sys.Errstr())
		return
	}
	child := *me
	for i := range ds {
		d := ds[i]
		if filesonly && d.Mode&sys.DMDIR != 0 {
			continue
		}
		child.new = Mkpath(me.new, d.Name)
		if me.old != "" {
			child.old = Mkpath(me.old, d.Name)
		}
		child.elem = d.Name
		r.setnames(&child)
		if r.copyfile(&child, &d) && rec {
			r.mktree(&child, rec, filesonly)
		}
	}
}

func (r *Reader) mkfile(f *file) bool {
	d, err := sys.Stat(r.oldfile)
	if err != nil {
		r.problem(Warn, "can't stat file "+r.oldfile+": "+sys.Errstr())
		r.skipdir()
		return false
	}
	return r.copyfile(f, d)
}

func (r *Reader) copyfile(f *file, d *sys.Dir) bool {
	d.Name = f.elem
	if f.mode != ^uint32(0) {
		if d.Mode&sys.DMDIR != f.mode&sys.DMDIR {
			r.problem(Warn, "inconsistent mode for "+f.new)
		} else {
			d.Mode = f.mode
		}
	}
	if r.Entry != nil {
		r.Entry(r.newfile, r.oldfile, d)
	}
	return d.Mode&sys.DMDIR != 0
}

func (r *Reader) setnames(f *file) {
	r.newfile = f.new
	switch {
	case f.old == "":
		r.oldfile = Mkpath(r.oldroot, f.new)
	case f.old[0] == '/':
		r.oldfile = Mkpath(r.oldroot, f.old)
	default:
		r.oldfile = f.old
	}
}

func (r *Reader) gets() (string, bool) {
	if r.next >= len(r.lines) {
		return "", false
	}
	s := r.lines[r.next]
	r.next++
	return s, true
}

// skipdir skips the lines that could be in the current directory.
func (r *Reader) skipdir() {
	if r.indent < 0 {
		return
	}
	level := r.indent
	for {
		r.indent = 0
		fp := r.next
		p, ok := r.gets()
		r.lineno++
		if !ok {
			r.indent = -1
			return
		}
		r.indent = indentation(p)
		if r.indent <= level {
			r.next = fp
			r.lineno--
			return
		}
	}
}

func indentation(p string) int {
	n := 0
	for _, c := range []byte(p) {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 8
		default:
			return n
		}
	}
	return n
}

func (r *Reader) getfile(old *file) (*file, int) {
	if r.indent < 0 {
		return nil, 0
	}
	fp := r.next
	var p string
	for {
		s, ok := r.gets()
		r.lineno++
		if !ok {
			r.indent = -1
			return nil, 0
		}
		r.indent = indentation(s)
		p = strings.TrimLeft(s, " \t")
		if p[0] != '\n' && p[0] != '#' {
			break
		}
	}
	f := &file{}
	var elem string
	elem, p = r.getname(p)
	f.new = Mkpath(old.new, elem)
	f.elem = f.new[strings.LastIndexByte(f.new, '/')+1:]
	if f.elem == "" {
		r.problem(Error, "can't find file name component of "+f.new)
	}
	f.mode, p = r.getmode(p)
	f.uid, p = r.getname(p)
	if f.uid == "" {
		f.uid = "-"
	}
	f.gid, p = r.getname(p)
	if f.gid == "" {
		f.gid = "-"
	}
	f.old = getpath(p)
	if f.old == "-" {
		f.old = ""
	}
	if f.old == "" && old.old != "" {
		f.old = Mkpath(old.old, elem)
	}
	r.setnames(f)
	return f, fp
}

func getpath(p string) string {
	p = strings.TrimLeft(p, " \t")
	n := strings.IndexAny(p, "\n \t")
	if n < 0 {
		return p
	}
	return p[:n]
}

func (r *Reader) getname(p string) (string, string) {
	p = strings.TrimLeft(p, " \t")
	var s strings.Builder
	quoted := false
	i := 0
	for ; i < len(p); i++ {
		c := p[i]
		if c == '\n' || (c == ' ' || c == '\t') && !quoted {
			break
		}
		if !r.Install {
			if quoted && c == '\'' && i+1 < len(p) && p[i+1] == '\'' {
				i++
			} else if c == '\'' {
				quoted = !quoted
				continue
			}
		}
		s.WriteByte(c)
	}
	name := s.String()
	if strings.HasPrefix(name, "$") {
		v := r.getenv(name[1:])
		if v == "" {
			if r.Install {
				r.problem(Error, "can't read environment variable "+v)
			} else {
				r.problem(Warn, "can't read environment variable "+v)
			}
		}
		name = v
	}
	return name, p[i:]
}

func (r *Reader) getenv(s string) string {
	if s == "user" {
		return readfile("/dev/user")
	}
	if r.Install {
		return ""
	}
	return readfile("/env/" + s)
}

func readfile(f string) string {
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	if len(b) > 256 {
		b = b[:256]
	}
	return string(b)
}

func (r *Reader) getmode(p string) (uint32, string) {
	var s string
	s, p = r.getname(p)
	if s == "" || s == "-" {
		return ^uint32(0), p
	}
	var m uint32
	if s[0] == 'd' {
		m |= sys.DMDIR
		s = s[1:]
	}
	if len(s) > 0 && s[0] == 'a' {
		if !r.Install {
			m |= sys.DMAPPEND
		}
		s = s[1:]
	}
	if len(s) > 0 && s[0] == 'l' {
		if !r.Install {
			m |= sys.DMEXCL
		}
		s = s[1:]
	}
	for i := 0; i < len(s) || i < 3; i++ {
		if i >= len(s) || s[i] < '0' || s[i] > '7' {
			r.problem(Warn, "bad mode specification "+s)
			return ^uint32(0), p
		}
	}
	v, _ := strconv.ParseUint(s, 8, 32)
	return m | uint32(v), p
}

// Mkpath joins prefix and elem with exactly one slash between them.
func Mkpath(prefix, elem string) string {
	slash1 := prefix != "" && prefix[len(prefix)-1] == '/'
	slash2 := elem != "" && elem[0] == '/'
	switch {
	case slash1 && slash2:
		return prefix + elem[1:]
	case !slash1 && !slash2:
		return prefix + "/" + elem
	}
	return prefix + elem
}
