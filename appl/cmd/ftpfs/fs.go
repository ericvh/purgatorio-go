package main

import (
	"bufio"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	nfiles = 128
	chsyml = 0x40000000 // a symbolic link, until we know what it is
	anon   = "anon"
)

// node is a remote file or directory we know about.
type node struct {
	dir      styx.Dir
	uniq     uint64
	parent   *node
	children []*node // newest first, as ftpfs.b links them
	file     *file
	depth    int
	remname  string
	cached   bool
	valid    bool
}

// file is the cached contents of a remote file.
type file struct {
	data         []byte
	inuse, dirty bool
	atime        int
	node         *node
}

var (
	npath       uint64 = 1
	files       []*file
	fileatime   int
	remroot     *node
	remdir      *node // the server's current directory
	remrootpath string
	tmnow       *daytime.Tm
)

func newnode(parent *node, name string) *node {
	n := &node{remname: name, uniq: npath}
	npath++
	n.dir.Name = name
	n.dir.Atime = uint32(daytime.Now())
	if parent != nil {
		n.parent = parent
		parent.children = append([]*node{n}, parent.children...)
		n.depth = parent.depth + 1
	} else {
		n.parent = n
		n.valid = true
		n.dir.UID = anon
		n.dir.GID = anon
		n.dir.Mtime = n.dir.Atime
	}
	return n
}

func (p *node) extendpath(elem string) *node {
	for _, n := range p.children {
		if n.dir.Name == elem {
			return n
		}
	}
	return newnode(p, elem)
}

func (n *node) markcached() {
	n.cached = true
	n.dir.Atime = uint32(daytime.Now())
}

func (n *node) uncache() {
	if n.fileisdirty() {
		n.createfile()
	}
	n.filefree()
	n.cached = false
}

// uncachedir drops the clean cached files in directory p, but child's.
func (p *node) uncachedir(child *node) {
	if p == nil || p == child {
		return
	}
	for _, sp := range p.children {
		if sp != child && sp.file != nil && !sp.file.dirty {
			sp.filefree()
			sp.cached = false
		}
	}
}

func (n *node) invalidate() {
	n.uncachedir(nil)
	for _, c := range n.children {
		c.cached = false
		c.invalidate()
		c.valid = false
	}
}

func (n *node) fixsymbolic() {
	if n.changedir() == 0 {
		n.dir.Mode |= styx.DMDIR
	}
	n.dir.Mode &^= chsyml
}

func (n *node) qid() styx.Qid {
	if n.dir.Mode&styx.DMDIR != 0 {
		return styx.Qid{Path: n.uniq, Type: styx.QTDIR}
	}
	return styx.Qid{Path: n.uniq, Type: styx.QTFILE}
}

func (n *node) stat() []byte {
	d := n.dir
	d.Qid = n.qid()
	return styx.MarshalDir(nil, &d)
}

// fileget gives n a cache entry, reusing the oldest clean one when
// there are too many.
func (n *node) fileget() *file {
	if n.file != nil {
		return n.file
	}
	var f, o *file
	for _, x := range files {
		if !x.inuse {
			f = x
			break
		}
		if !x.dirty && (o == nil || o.atime > x.atime) {
			o = x
		}
	}
	if f == nil {
		if len(files) == nfiles && o != nil {
			o.node.uncache()
			f = o
		} else {
			f = &file{}
			files = append([]*file{f}, files...)
		}
	}
	n.file = f
	f.node = n
	f.atime = fileatime
	fileatime++
	f.inuse = true
	f.dirty = false
	f.data = nil
	return f
}

func (n *node) fileread(off, c int) []byte {
	f := n.file
	if f == nil || off >= len(f.data) {
		return nil
	}
	return f.data[off:min(off+c, len(f.data))]
}

func (n *node) filewrite(b []byte, off int) int {
	f := n.fileget()
	if end := off + len(b); end > len(f.data) {
		f.data = append(f.data, make([]byte, end-len(f.data))...)
	}
	copy(f.data[off:], b)
	return len(b)
}

func (n *node) filefree() {
	f := n.file
	if f == nil {
		return
	}
	f.data = nil
	f.inuse = false
	f.dirty = false
	n.file = nil
}

func (n *node) fileclean() {
	if n.file != nil {
		n.file.dirty = false
	}
}

func (n *node) fileisdirty() bool { return n.file != nil && n.file.dirty }

func (n *node) filedirty() { n.fileget().dirty = true }

// Directory listings.

const months = "janfebmaraprmayjunjulaugsepoctnovdec"

func cracktime(month, day, year, hms string) uint32 {
	if tmnow == nil {
		tmnow = daytime.Local(daytime.Now())
	}
	tm := *tmnow
	if month[0] >= '0' && month[0] <= '9' {
		tm.Mon = atoi(month) - 1
		if tm.Mon < 0 || tm.Mon > 11 {
			tm.Mon = 5
		}
	} else if len(month) >= 3 {
		m := strings.ToLower(month[:3])
		for i := 0; i < 36; i += 3 {
			if m == months[i:i+3] {
				tm.Mon = i / 3
				break
			}
		}
	}
	tm.Mday = atoi(day)
	if hms != "" {
		h, z := hms, ""
		if i := strings.IndexAny(hms, "apAP"); i >= 0 {
			h, z = hms[:i], hms[i:]
		}
		a, b, hasmin := strings.Cut(h, ":")
		tm.Hour = atoi(a)
		if hasmin {
			c, d, hassec := strings.Cut(b, ":")
			tm.Min = atoi(c)
			if hassec {
				tm.Sec = atoi(d)
			}
		}
		if z != "" && (z[0] == 'p' || z[0] == 'P') {
			tm.Hour += 12
		}
	}
	if year != "" {
		tm.Year = atoi(year)
		if tm.Year >= 1900 {
			tm.Year -= 1900
		}
	} else if tm.Mon > tmnow.Mon || tm.Mon == tmnow.Mon && tm.Mday > tmnow.Mday+1 {
		tm.Year--
	}
	return uint32(daytime.Tm2epoch(&tm))
}

func crackmode(p string) uint32 {
	var flags uint32
	switch len(p) {
	case 10: // unix and new plan 9
		switch p[0] {
		case 'l':
			return chsyml | 0o777
		case 'd':
			flags = styx.DMDIR
		}
		p = p[1:10]
	case 11: // old plan 9
		if p[0] == 'l' {
			flags = styx.DMDIR
		}
		p = p[2:11]
	default:
		return styx.DMDIR | 0o777
	}
	var mode uint32
	for i := 0; i < 9; i += 3 {
		mode <<= 3
		if p[i] == 'r' {
			mode |= 4
		}
		if p[i+1] == 'w' {
			mode |= 2
		}
		switch p[i+2] {
		case 'x', 's', 'S':
			mode |= 1
		}
	}
	return mode | flags
}

// crackdir parses a line of a LIST reply.
func crackdir(p string) (string, styx.Dir) {
	var d styx.Dir
	f := tokenize(p, " \t\r\n")
	n := len(f)
	if n > 2 && f[n-2] == "->" {
		n -= 2
	}
	var ln string
	timeof := func(mon, day, yt string) uint32 {
		if strings.Contains(yt, ":") {
			return cracktime(mon, day, "", yt)
		}
		return cracktime(mon, day, yt, "")
	}
	switch n {
	case 8: // ls -l
		ln = f[7]
		d.UID, d.GID = f[2], f[2]
		d.Mode = crackmode(f[0])
		d.Length = uint64(atoi(f[3]))
		d.Atime = timeof(f[4], f[5], f[6])
	case 9: // ls -lg
		ln = f[8]
		d.UID, d.GID = f[2], f[3]
		d.Mode = crackmode(f[0])
		d.Length = uint64(atoi(f[4]))
		d.Atime = timeof(f[5], f[6], f[7])
	case 10: // plan 9
		ln = f[9]
		d.UID, d.GID = f[3], f[4]
		d.Mode = crackmode(f[0])
		d.Length = uint64(atoi(f[5]))
		d.Atime = timeof(f[6], f[7], f[8])
	case 4: // NT
		ln = f[3]
		d.UID, d.GID = anon, anon
		if f[2] == "<DIR>" {
			d.Mode = styx.DMDIR | 0o777
		} else {
			d.Mode = 0o666
			d.Length = uint64(atoi(f[2]))
		}
		if l := tokenize(f[0], "/-"); len(l) == 3 {
			d.Atime = cracktime(l[0], l[1], f[2], f[1])
		}
	case 1: // ls
		ln = f[0]
		d.UID, d.GID = anon, anon
		d.Mode = 0o777
	default:
		return "", d
	}
	if ln == "." || ln == ".." {
		return "", d
	}
	d.Mtime = d.Atime
	d.Name = ln
	return ln, d
}

var longls = true

func (n *node) readdir() int {
	if n.changedir() < 0 {
		return -1
	}
	transfer = true
	defer func() { transfer = false }()
	var f *os.File
	for {
		if longls {
			if f = data("LIST -la"); f == nil {
				longls = false
				continue
			}
		} else if f = data("LIST"); f == nil {
			return seterr(errclass, enosuchfile)
		}
		break
	}
	b := bufio.NewReader(f)
	for {
		s, err := b.ReadString('\n')
		if s != "" {
			if debug > 0 {
				print(s)
			}
			if l, d := crackdir(s); l != "" {
				p := n.extendpath(l)
				p.dir = d
				p.valid = true
			}
		}
		if err != nil {
			break
		}
	}
	f.Close()
	if r, _ := getreply(false); r != success {
		return seterr(errclass, enosuchfile)
	}
	return 0
}

func (n *node) readfile() int {
	if n.parent.changedir() < 0 {
		return -1
	}
	transfer = true
	defer func() { transfer = false }()
	f := data("RETR " + n.remname)
	if f == nil {
		return seterr(errclass, enosuchfile)
	}
	buf := make([]byte, 8192)
	off := 0
	var rerr error
	for {
		c, err := f.Read(buf)
		if c <= 0 {
			if err != nil && err.Error() != "EOF" {
				rerr = err
			}
			break
		}
		n.filewrite(buf[:c], off)
		off += c
	}
	f.Close()
	if rerr != nil {
		return seterr(errclass, eioerror)
	}
	if off == 0 {
		n.filewrite(nil, 0)
	}
	if s, _ := getreply(false); s != success {
		return seterr(s, enosuchfile)
	}
	return off
}

func path(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case a[len(a)-1] == '/':
		return a + b
	}
	return a + "/" + b
}

func (n *node) pathname() string {
	s := ""
	for n != n.parent {
		s = path(n.remname, s)
		n = n.parent
	}
	return path(remrootpath, s)
}

func (n *node) changedir() int {
	if n == remdir {
		return 0
	}
	d := remrootpath
	if n.depth != 0 {
		d = n.pathname()
	}
	remdir.uncachedir(nil)
	r := sendrequest("CWD "+d, false)
	if r == success {
		r, _ = getreply(false)
	}
	if r == success {
		remdir = n
		return 0
	}
	return seterr(r, enosuchfile)
}

func (n *node) docreate() int {
	transfer = true
	defer func() { transfer = false }()
	f := data("STOR " + n.remname)
	if f == nil {
		return -1
	}
	defer f.Close()
	off := 0
	for {
		b := n.fileread(off, 8192)
		if len(b) == 0 {
			break
		}
		if _, err := f.Write(b); err != nil {
			return -1
		}
		off += len(b)
	}
	return off
}

func (n *node) createfile() int {
	if n.parent.changedir() < 0 {
		return -1
	}
	off := n.docreate()
	if off < 0 {
		return -1
	}
	if r, _ := getreply(false); r != success {
		return -1
	}
	return off
}

func (n *node) action(cmd string) int {
	if n.parent.changedir() < 0 {
		return -1
	}
	r := sendrequest(cmd+" "+n.dir.Name, false)
	if r == success {
		r, _ = getreply(false)
	}
	if r != success {
		return seterr(errclass, epermission)
	}
	return 0
}
