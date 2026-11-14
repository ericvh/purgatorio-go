// Command archfs mounts an archive made by mkfs -a read-only (Inferno
// archfs(4)).
//
// The archive is a sequence of header lines, "name mode uid gid mtime
// length", each followed by length bytes of data, and ends with the line
// "end of archive".
//
// A guest is single-threaded and mount waits for the server's answers, so
// archfs reads the archive to check it, then starts a second prog of
// itself (hidden first argument "@serve") holding the archive and one end
// of a pipe, and mounts the other end.
package main

import (
	"bufio"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	serveFlag = "@serve"

	einuse    = "fid already in use"
	ebadfid   = "bad fid"
	eopen     = "fid already opened"
	enotfound = "file does not exist"
	enotdir   = "not a directory"
	eperm     = "permission denied"

	uid = "inferno"
	gid = "inferno"

	maxfdata = 8192
	maxrpc   = styx.IOHDRSZ + maxfdata
)

type dir struct {
	d       styx.Dir
	offset  int64
	parent  *dir
	child   *dir
	sibling *dir
}

type fid struct {
	open bool
	dir  *dir
}

var (
	debug  bool
	skip   bool
	mtpt   = "/mnt/arch"
	root   *dir
	qidgen uint64
	arch   *os.File
	fids   = map[uint32]*fid{}
	now    uint32
)

func fatal(e string) {
	os.Stderr.WriteString("archfs: " + e + "\n")
	sys.Exits("fail:error")
}

func main() {
	server := len(os.Args) > 1 && os.Args[1] == serveFlag
	args := os.Args
	if server {
		args = append([]string{args[0]}, args[2:]...)
	}
	a := arg.New(args)
	a.SetUsage("archfs [-ab] [-m mntpt] archive [prefix ...]")
	flags := sys.MREPL
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'D':
			debug = true
		case 'a':
			flags = sys.MAFTER
		case 'b':
			flags = sys.MBEFORE
		case 'm':
			mtpt = a.EArg()
		case 's':
			skip = true
		default:
			a.Usage()
		}
	}
	argv := a.Argv()
	if len(argv) == 0 {
		a.Usage()
	}
	now = uint32(time.Now().Unix())
	root = newdir(basename(mtpt), uid, gid, 0o555|styx.DMDIR, now)
	root.parent = root
	if server {
		arch = os.NewFile(5, argv[0])
		readarch(argv[0], argv[1:])
		serve(os.NewFile(0, "styx"))
		return
	}
	fd, err := sys.Open(argv[0], sys.OREAD)
	if err != nil {
		fatal("cannot open " + argv[0] + ": " + sys.Errstr())
	}
	arch = os.NewFile(uintptr(fd), argv[0])
	readarch(argv[0], argv[1:])
	p, err := sys.Pipe()
	if err != nil {
		fatal("can't create pipe")
	}
	sargv := []string{"archfs", serveFlag}
	if debug {
		sargv = append(sargv, "-D")
	}
	if skip {
		sargv = append(sargv, "-s")
	}
	sargv = append(sargv, "-m", mtpt, "--")
	sargv = append(sargv, argv...)
	if _, err := sys.Spawn("/bin/archfs.wasm", sargv, sys.NEWFD|sys.NEWNS,
		[][2]int{{0, p[1]}, {2, 2}, {5, fd}}); err != nil {
		fatal("can't start server: " + sys.Errstr())
	}
	sys.Close(p[1])
	if err := sys.Mount(p[0], -1, mtpt, flags, ""); err != nil {
		fatal("cannot mount archive on " + mtpt + ": " + sys.Errstr())
	}
}

func reply(w io.Writer, r *styx.Fcall) {
	if debug {
		os.Stderr.WriteString("-> " + styxservers.Text(r) + "\n")
	}
	b, err := styx.Marshal(nil, r)
	if err != nil {
		return
	}
	w.Write(b)
}

func rerror(w io.Writer, t *styx.Fcall, e string) {
	reply(w, &styx.Fcall{Type: styx.Rerror, Tag: t.Tag, Ename: e})
}

func mapfid(n uint32) (*fid, string) {
	if n == styx.NOFID {
		return nil, ebadfid
	}
	f := fids[n]
	if f == nil {
		return nil, ebadfid
	}
	if f.dir == nil {
		return nil, enotfound
	}
	return f, ""
}

func newfid(n uint32) *fid {
	if n == styx.NOFID || fids[n] != nil {
		return nil
	}
	f := &fid{}
	fids[n] = f
	return f
}

func serve(conn *os.File) {
	r := bufio.NewReaderSize(conn, maxrpc)
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err != io.EOF {
				fatal("read error on styx server")
			}
			return
		}
		n := styx.Size(hdr[:])
		if n < 7 || n > maxrpc {
			fatal("read error on styx server")
		}
		msg := make([]byte, n)
		copy(msg, hdr[:])
		if _, err := io.ReadFull(r, msg[4:]); err != nil {
			fatal("read error on styx server")
		}
		m, err := styx.Unmarshal(msg, maxrpc)
		if err != nil || m.Type%2 != 0 {
			fatal("read error on styx server")
		}
		if debug {
			os.Stderr.WriteString("<- " + styxservers.Text(m) + "\n")
		}
		handle(conn, m)
	}
}

func handle(w io.Writer, m *styx.Fcall) {
	switch m.Type {
	case styx.Tversion:
		msize, v := styxservers.Compatible(m, maxrpc, styx.Version)
		reply(w, &styx.Fcall{Type: styx.Rversion, Tag: m.Tag, Msize: msize, Version: v})
	case styx.Tauth:
		rerror(w, m, "authentication not required")
	case styx.Tflush:
		reply(w, &styx.Fcall{Type: styx.Rflush, Tag: m.Tag})
	case styx.Twalk:
		f, e := mapfid(m.Fid)
		if e != "" {
			rerror(w, m, e)
			return
		}
		if f.open {
			rerror(w, m, eopen)
			return
		}
		d := f.dir
		var qids []styx.Qid
		for k, name := range m.Wname {
			if d.d.Mode&styx.DMDIR == 0 {
				if k == 0 {
					rerror(w, m, enotdir)
					return
				}
				break
			}
			d = lookup(d, name)
			if d == nil {
				if k == 0 {
					rerror(w, m, enotfound)
					return
				}
				break
			}
			qids = append(qids, d.d.Qid)
		}
		if len(qids) == len(m.Wname) {
			if m.Newfid != m.Fid {
				f = newfid(m.Newfid)
				if f == nil {
					rerror(w, m, einuse)
					return
				}
			}
			f.dir = d
		}
		reply(w, &styx.Fcall{Type: styx.Rwalk, Tag: m.Tag, Wqid: qids})
	case styx.Topen:
		f, e := mapfid(m.Fid)
		if e != "" {
			rerror(w, m, e)
			return
		}
		if m.Mode != sys.OREAD {
			rerror(w, m, eperm)
			return
		}
		f.open = true
		reply(w, &styx.Fcall{Type: styx.Ropen, Tag: m.Tag, Qid: f.dir.d.Qid, Iounit: maxfdata})
	case styx.Tread:
		f, e := mapfid(m.Fid)
		if e != "" {
			rerror(w, m, e)
			return
		}
		reply(w, &styx.Fcall{Type: styx.Rread, Tag: m.Tag, Data: read(f.dir, int64(m.Offset), int(m.Count))})
	case styx.Tclunk:
		if _, e := mapfid(m.Fid); e != "" {
			rerror(w, m, e)
			return
		}
		delete(fids, m.Fid)
		reply(w, &styx.Fcall{Type: styx.Rclunk, Tag: m.Tag})
	case styx.Tstat:
		f, e := mapfid(m.Fid)
		if e != "" {
			rerror(w, m, e)
			return
		}
		reply(w, &styx.Fcall{Type: styx.Rstat, Tag: m.Tag, Stat: styx.MarshalDir(nil, &f.dir.d)})
	case styx.Tattach:
		f := newfid(m.Fid)
		if f == nil {
			rerror(w, m, einuse)
			return
		}
		f.dir = root
		reply(w, &styx.Fcall{Type: styx.Rattach, Tag: m.Tag, Qid: root.d.Qid})
	case styx.Tcreate, styx.Twrite, styx.Tremove, styx.Twstat:
		rerror(w, m, eperm)
	default:
		fatal("unknown styx message")
	}
}

func readarch(name string, prefixes []string) {
	br := bufio.NewReader(io.NewSectionReader(arch, 0, 1<<62))
	var off int64
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			fatal("premature end of archive")
		}
		off += int64(len(line))
		if line == "end of archive\n" {
			break
		}
		fs := tokenize(line, " \t\n")
		if len(fs) != 6 {
			fatal("too few fields in file header")
		}
		name := fs[0]
		mode := uint32(toint(fs[1], 8))
		mtime := uint32(toint(fs[4], 10))
		length := toint(fs[5], 10)
		dataoff := off
		off += length
		if _, err := br.Discard(int(length)); err != nil {
			fatal("premature end of archive")
		}
		if len(prefixes) > 0 && !selected(name, prefixes) {
			if skip {
				return
			}
			continue
		}
		d := mkdir(name, mode, mtime, fs[2], fs[3], false)
		if d != nil && mode&styx.DMDIR == 0 {
			d.d.Length = uint64(length)
			d.offset = dataoff
		}
	}
}

func selected(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if fileprefix(p, s) {
			return true
		}
	}
	return false
}

func fileprefix(prefix, s string) bool {
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	return len(s) == len(prefix) || s[len(prefix)] == '/'
}

func basename(f string) string {
	if i := strings.LastIndexByte(f, '/'); i >= 0 {
		return f[i+1:]
	}
	return f
}

// split takes the first element off a path that starts with a slash.
func split(p string) (string, string) {
	if p == "" || p[0] != '/' {
		fatal("p0 not / in split")
	}
	p = strings.TrimLeft(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i:]
	}
	return p, ""
}

func read(d *dir, offset int64, n int) []byte {
	if d.d.Mode&styx.DMDIR != 0 {
		return readdir(d, int(offset), n)
	}
	return readfile(d, offset, n)
}

func readdir(d *dir, o, n int) []byte {
	k := 0
	var b []byte
	for s := d.child; s != nil; s = s.sibling {
		e := styx.MarshalDir(nil, &s.d)
		if k < o {
			k += len(e)
			continue
		}
		if len(b)+len(e) > n {
			break
		}
		b = append(b, e...)
	}
	return b
}

func readfile(d *dir, offset int64, n int) []byte {
	if offset+int64(n) > int64(d.d.Length) {
		n = int(int64(d.d.Length) - offset)
	}
	if n <= 0 || offset < 0 {
		return nil
	}
	a := make([]byte, n)
	if k, _ := arch.ReadAt(a, d.offset+offset); k != n {
		fatal("premature eof")
	}
	return a
}

func mkdir(f string, mode, mtime uint32, u, g string, existsok bool) *dir {
	if f == "/" {
		return nil
	}
	d := newdir(basename(f), u, g, mode, mtime)
	addfile(d, f, existsok)
	return d
}

func addfile(d *dir, path string, existsok bool) {
	opath := path
	prev := root
	var p *dir
	basedir := ""
	for path != "" {
		var elem string
		elem, path = split(path)
		basedir += "/" + elem
		op := prev
		p = lookup(prev, elem)
		if path == "" {
			if p != nil {
				if !existsok && p.d.Mode&styx.DMDIR == 0 {
					os.Stderr.WriteString("addfile: " + opath + " already there")
				}
				return
			}
			if prev.child == nil {
				prev.child = d
			} else {
				s := prev.child
				for s.sibling != nil {
					s = s.sibling
				}
				s.sibling = d
			}
			d.parent = prev
		} else if p == nil {
			mkdir(basedir, 0o775|styx.DMDIR, now, uid, gid, true)
			if p = lookup(op, elem); p == nil {
				fatal("bad file system")
			}
		}
		prev = p
	}
}

func lookup(p *dir, f string) *dir {
	if p.d.Mode&styx.DMDIR == 0 {
		fatal("not a directory in lookup")
	}
	switch f {
	case ".":
		return p
	case "..":
		return p.parent
	}
	for d := p.child; d != nil; d = d.sibling {
		if d.d.Name == f {
			return d
		}
	}
	return nil
}

func newdir(name, u, g string, mode, mtime uint32) *dir {
	d := &dir{d: styx.Dir{Name: name, UID: u, GID: g, Mode: mode, Atime: mtime, Mtime: mtime,
		Qid: styx.Qid{Path: qidgen, Type: uint8(mode >> 24)}}}
	qidgen++
	return d
}

// tokenize splits s at runs of the delimiters, as Sys->tokenize does.
func tokenize(s, delim string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(delim, r) })
}

// toint parses the leading digits of s in base, as String->toint does.
func toint(s string, base int64) int64 {
	s = strings.TrimLeft(s, " \t")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	var v int64
	for _, c := range s {
		dv := int64(-1)
		switch {
		case c >= '0' && c <= '9':
			dv = int64(c - '0')
		case c >= 'a' && c <= 'z':
			dv = int64(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			dv = int64(c-'A') + 10
		}
		if dv < 0 || dv >= base {
			break
		}
		v = v*base + dv
	}
	if neg {
		v = -v
	}
	return v
}
