// Command rawdbfs serves records of any bytes as numbered files, as
// dbfs does, but keeps them in a file of its own format that it changes
// in place rather than rewriting (Inferno rawdbfs, dbfs(4)). -x adds the
// index and stats files, -l lets one fid at a time open a record for
// writing, and -u cmd runs cmd with sh whenever the data change.
//
// The file is a sequence of blocks, each a 10-byte header "%4d %4d\n"
// giving the block's size and the length of the data in it (-1 for a
// free block), then the block. When freed blocks are merged, rawdbfs
// keeps each merged block under 10000 bytes so its header stays 10
// bytes; Inferno's can write a longer header there and damage the file.
//
// The server runs as a second prog with the internal option -s, as in
// dbfs.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	qdir = iota
	qnew
	qdata
	qindex
	qstats
)

const (
	headlen = 10
	minsize = 20
	maxsize = 9999 // the largest size a 4-digit header holds

	eremoved = "file removed"
	egreg    = "thermal problems"
	elocked  = "open/create -- file is locked"
)

type record struct {
	id      int // file number in the directory (if allocated)
	offset  int // start of data
	count   int // length of block (excluding header)
	datalen int // length of data (-1 if block is free)
	vers    int
}

type lock struct {
	qpath uint64
	fid   uint32
}

type database struct {
	file     *os.File
	records  []*record
	maxid    int
	locking  bool
	locks    []lock
	indexing bool
	stats    bool
	index    int
	sreads   int
	swrites  int
	screates int
	sremoves int
	updcmd   string
	vers     int
}

var (
	stderr = os.Stderr
	db     *database
	user   string
)

func usage() {
	fmt.Fprintf(stderr, "Usage: dbfs [-abcelrxD][-u cmd] file mountpoint\n")
	sys.Exits("fail:usage")
}

func main() {
	a := arg.New(os.Args)
	flags := sys.MREPL
	copt, empty, trace, srv := false, false, false, false
	locking, indexing := false, false
	updcmd := ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			flags = sys.MAFTER
		case 'b':
			flags = sys.MBEFORE
		case 'r':
			flags = sys.MREPL
		case 'c':
			copt = true
		case 'e':
			empty = true
		case 'l':
			locking = true
		case 'u':
			if updcmd = a.Arg(); updcmd == "" {
				usage()
			}
		case 'x':
			indexing = true
		case 'D':
			trace = true
		case 's': // internal: serve standard input
			srv = true
		default:
			usage()
		}
	}
	args := a.Argv()
	if srv {
		if len(args) != 1 {
			usage()
		}
		serve(args[0], locking, indexing, updcmd, trace)
		return
	}
	if len(args) != 2 {
		usage()
	}
	if copt {
		flags |= sys.MCREATE
	}
	file, mountpt := args[0], args[1]
	fd, err := sys.Open(file, sys.ORDWR)
	if err != nil && empty {
		if _, serr := sys.Stat(file); serr != nil {
			fd, err = sys.Create(file, sys.ORDWR, 0o600)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "dbfs: can't open %s: %s\n", file, sys.Errstr())
		sys.Exits("fail:cannot open file")
	}
	f := os.NewFile(uintptr(fd), file)
	if _, err := build(f); err != "" {
		fmt.Fprintf(stderr, "dbfs: can't read %s: %s\n", file, err)
		sys.Exits("fail:cannot read db")
	}
	f.Close()
	p, perr := sys.Pipe()
	if perr != nil {
		fmt.Fprintf(stderr, "dbfs: can't create pipe: %s\n", sys.Errstr())
		sys.Exits("fail:pipe")
	}
	sargs := []string{"rawdbfs", "-s"}
	if trace {
		sargs = append(sargs, "-D")
	}
	if locking {
		sargs = append(sargs, "-l")
	}
	if indexing {
		sargs = append(sargs, "-x")
	}
	if updcmd != "" {
		sargs = append(sargs, "-u", updcmd)
	}
	sargs = append(sargs, file)
	if _, err := sys.Spawn("/bin/rawdbfs.wasm", sargs, sys.NEWFD|sys.FORKNS,
		[][2]int{{0, p[1]}, {1, 1}, {2, 2}}); err != nil {
		fmt.Fprintf(stderr, "dbfs: can't start server: %s\n", sys.Errstr())
		sys.Exits("fail:pipe")
	}
	sys.Close(p[1])
	if err := sys.Mount(p[0], -1, mountpt, flags, ""); err != nil {
		fmt.Fprintf(stderr, "dbfs: mount failed: %s\n", sys.Errstr())
		sys.Exits("fail:bad mount")
	}
}

func rf(f string) string {
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	return string(b)
}

func serve(file string, locking, indexing bool, updcmd string, trace bool) {
	fd, err := sys.Open(file, sys.ORDWR)
	if err != nil {
		fmt.Fprintf(stderr, "dbfs: can't open %s: %s\n", file, sys.Errstr())
		sys.Exits("fail:cannot open file")
	}
	recs, e := build(os.NewFile(uintptr(fd), file))
	if e != "" {
		fmt.Fprintf(stderr, "dbfs: can't read %s: %s\n", file, e)
		sys.Exits("fail:cannot read db")
	}
	db = &database{file: os.NewFile(uintptr(fd), file), records: recs, maxid: len(recs),
		locking: locking, indexing: indexing, stats: indexing, index: -1, updcmd: updcmd}
	if user = rf("/dev/user"); user == "" {
		user = "inferno"
	}
	srv := styxservers.New(os.NewFile(0, "styx"), nav{}, qpath(0, qdir))
	if trace {
		srv.Trace = stderr
	}
	for {
		t, err := srv.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintf(stderr, "dbfs: fatal read error: %s\n", err)
			}
			return
		}
		handle(srv, t)
	}
}

func handle(srv *styxservers.Server, t *styx.Fcall) {
	switch t.Type {
	case styx.Topen:
		open(srv, t)
	case styx.Tread:
		c, err := srv.CanRead(t)
		if c == nil {
			srv.Error(t, err.Error())
			return
		}
		if c.Qtype&styx.QTDIR != 0 {
			srv.Read(t)
			return
		}
		switch typ(c.Path) {
		case qindex:
			if db.index < 0 {
				srv.Error(t, string(styxservers.Eperm))
				return
			}
			if t.Offset > 0 {
				srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag})
				return
			}
			db.index++
			srv.Reply(short(t, strconv.Itoa(db.index)))
		case qstats:
			if t.Offset > 0 {
				srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag})
				return
			}
			srv.Reply(short(t, fmt.Sprintf("%d %d %d %d", db.sreads, db.swrites, db.screates, db.sremoves)))
		case qdata:
			recno := id2recno(fileno(c.Path))
			if recno == -1 {
				srv.Error(t, eremoved)
			} else {
				srv.Reply(styxservers.ReadBytes(t, db.read(recno)))
			}
		default:
			srv.Error(t, egreg)
		}
	case styx.Twrite:
		c, err := srv.CanWrite(t)
		if c == nil {
			srv.Error(t, err.Error())
			return
		}
		if !db.ownlock(c) { // open has checked
			srv.Error(t, elocked)
			return
		}
		switch typ(c.Path) {
		case qindex:
			if db.index >= 0 {
				srv.Error(t, string(styxservers.Eperm))
				return
			}
			db.index = atoi(string(t.Data))
			srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
		case qdata:
			recno := id2recno(fileno(c.Path))
			if recno == -1 {
				srv.Error(t, "phase error")
				return
			}
			changed := true
			if db.updcmd != "" {
				changed = string(t.Data) != string(db.read(recno))
			}
			if changed && db.write(recno, t.Data) == -1 {
				srv.Error(t, sys.Errstr())
				return
			}
			if changed {
				db.updated() // run the command before the reply
			}
			srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
		default:
			srv.Error(t, string(styxservers.Eperm))
		}
	case styx.Tclunk:
		if c := srv.GetFid(t.Fid); c != nil {
			db.unlock(c)
		}
		srv.Clunk(t)
	case styx.Tremove:
		c := srv.GetFid(t.Fid)
		if c != nil {
			db.unlock(c)
		}
		if c == nil || c.Qtype&styx.QTDIR != 0 || typ(c.Path) != qdata {
			srv.Remove(t)
			return
		}
		if recno := id2recno(fileno(c.Path)); recno == -1 {
			srv.Error(t, "phase error")
		} else {
			db.remove(recno)
			db.updated()
			srv.Reply(&styx.Fcall{Type: styx.Rremove, Tag: t.Tag})
		}
		srv.DelFid(c)
	default:
		srv.Default(t)
	}
}

// short replies with s, cut to the count asked for.
func short(t *styx.Fcall, s string) *styx.Fcall {
	b := []byte(s)
	if int(t.Count) < len(b) {
		b = b[:t.Count]
	}
	return &styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: b}
}

func id2recno(id int) int {
	for i, r := range db.records {
		if r.datalen >= 0 && r.id == id {
			return i
		}
	}
	return -1
}

func open(srv *styxservers.Server, t *styx.Fcall) {
	c, mode, d, err := srv.CanOpen(t)
	if c == nil {
		srv.Error(t, err.Error())
		return
	}
	if typ(c.Path) == qnew {
		if c.Uname != user {
			srv.Error(t, string(styxservers.Eperm))
			return
		}
		r := db.create(nil)
		if r == nil {
			srv.Error(t, "create -- i/o error")
			return
		}
		d, _ = dirgen(qpath(r.id, qdata))
	}
	c.Open(mode, d.Qid)
	if db.locking && typ(c.Path) == qdata && t.Mode&(sys.OWRITE|sys.ORDWR) != 0 && !db.lock(c) {
		c.IsOpen = false
		srv.Error(t, elocked)
		return
	}
	srv.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: d.Qid, Iounit: srv.Iounit()})
}

func mkdir(q styx.Qid, name string, length int, perm uint32) *styx.Dir {
	if q.Type&styx.QTDIR != 0 {
		perm |= styx.DMDIR
	}
	return &styx.Dir{Qid: q, Mode: perm, Name: name, UID: user, GID: user, Length: uint64(length)}
}

func dirgen(p uint64) (*styx.Dir, error) {
	switch typ(p) {
	case qdir:
		return mkdir(styx.Qid{Path: qpath(0, qdir), Vers: uint32(db.vers), Type: styx.QTDIR}, "/", 0, 0o700), nil
	case qnew:
		return mkdir(styx.Qid{Path: qpath(0, qnew)}, "new", 0, 0o600), nil
	case qindex:
		return mkdir(styx.Qid{Path: qpath(0, qindex)}, "index", 0, 0o600), nil
	case qstats:
		return mkdir(styx.Qid{Path: qpath(0, qstats)}, "stats", 0, 0o400), nil
	}
	n := id2recno(fileno(p))
	if n < 0 {
		return nil, styxservers.Enotfound
	}
	r := db.records[n]
	return mkdir(r.qid(), strconv.Itoa(r.id), max(r.datalen, 0), 0o600), nil
}

type nav struct{}

func (nav) Stat(p uint64) (*styx.Dir, error) { return dirgen(p) }

func (nav) Walk(p uint64, name string) (*styx.Dir, error) {
	if p != qdir {
		return nil, styxservers.Enotdir
	}
	switch name {
	case "..":
	case "new":
		p = qpath(0, qnew)
	case "stats", "index":
		if !db.indexing {
			return nil, styxservers.Enotfound
		}
		p = qpath(0, qstats)
		if name == "index" {
			p = qpath(0, qindex)
		}
	default:
		if name == "" || name[0] < '0' || name[0] > '9' {
			return nil, styxservers.Enotfound
		}
		p = qpath(atoi(name), qdata)
	}
	return dirgen(p)
}

func (nav) Readdir(p uint64, off, count int) []*styx.Dir {
	if p != qdir {
		return nil
	}
	ents := []uint64{qpath(0, qnew)}
	if db.indexing {
		ents = append(ents, qpath(0, qindex))
	}
	if db.stats {
		ents = append(ents, qpath(0, qstats))
	}
	for _, r := range db.records {
		if r.datalen >= 0 {
			ents = append(ents, qpath(r.id, qdata))
		}
	}
	var ds []*styx.Dir
	for i := off; i < len(ents) && count > 0; i++ {
		if d, _ := dirgen(ents[i]); d != nil {
			ds = append(ds, d)
			count--
		}
	}
	return ds
}

func qpath(w, q int) uint64 { return uint64(w<<8 | q) }
func typ(path uint64) int   { return int(path & 0xFF) }
func fileno(path uint64) int {
	return int(path>>8) & 0xFFFFFF
}

func (r *record) qid() styx.Qid {
	return styx.Qid{Path: qpath(r.id, qdata), Vers: uint32(r.vers), Type: styx.QTFILE}
}

// build reads the block headers of f.
func build(f *os.File) ([]*record, string) {
	var recs []*record
	offset := 0
	d := make([]byte, headlen)
	for {
		if n, _ := f.ReadAt(d, int64(offset)); n < headlen {
			break
		}
		s := string(d)
		r := &record{id: len(recs)}
		var ok1, ok2 bool
		r.count, s, ok1 = toint(s)
		r.datalen, s, ok2 = toint(s)
		if !ok1 || !ok2 || s != "\n" {
			return nil, fmt.Sprintf("found bad header '%s'\n", d)
		}
		r.offset = offset + headlen
		offset += r.count + headlen
		recs = append(recs, r)
	}
	return recs, ""
}

// toint is String->toint in base 10: white space, a sign and digits.
func toint(s string) (int, string, bool) {
	s = strings.TrimLeft(s, " \t\n")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	i, n := 0, 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		n = -n
	}
	return n, s[i:], i > 0
}

// atoi is Limbo's int of a string: white space, a sign and digits.
func atoi(s string) int {
	n, _, _ := toint(s)
	return n
}

func (db *database) write(recno int, data []byte) int {
	db.swrites++
	r := db.records[recno]
	r.vers++
	if len(data) <= r.count {
		if r.count-len(data) >= headlen+minsize {
			db.splitrec(recno, len(data))
		}
		if db.writerec(recno, data) < 0 {
			return -1
		}
		return 0
	}
	db.freerec(recno)
	n := db.allocrec(len(data))
	if n == -1 {
		return -1 // as in Inferno, the old data are lost
	}
	db.records[n].id = r.id
	return db.write(n, data)
}

func (db *database) create(data []byte) *record {
	db.screates++
	db.vers++
	n := db.allocrec(len(data))
	if n < 0 {
		return nil
	}
	if db.write(n, data) < 0 {
		db.freerec(n)
		return nil
	}
	r := db.records[n]
	r.id = db.maxid
	db.maxid++
	return r
}

func (db *database) read(recno int) []byte {
	db.sreads++
	r := db.records[recno]
	if r.datalen <= 0 {
		return nil
	}
	d := make([]byte, r.datalen)
	if n, _ := db.file.ReadAt(d, int64(r.offset)); n != r.datalen {
		fmt.Fprintf(stderr, "dbfs: only read %d bytes (expected %d)\n", n, r.datalen)
		return nil
	}
	return d
}

func (db *database) remove(recno int) {
	db.sremoves++
	db.vers++
	db.freerec(recno)
}

// updated runs the -u command with sh and waits for it.
func (db *database) updated() {
	if db.updcmd == "" {
		return
	}
	me, _ := sys.Pctl(0, nil)
	w, err := sys.Open("/prog/"+strconv.Itoa(me)+"/wait", sys.OREAD)
	if err != nil {
		return
	}
	wait := os.NewFile(uintptr(w), "wait")
	defer wait.Close()
	pid, err := sys.Spawn("/bin/sh.wasm", []string{"sh", "-c", db.updcmd}, sys.NEWFD,
		[][2]int{{1, 1}, {2, 2}})
	if err != nil {
		fmt.Fprintf(stderr, "dbfs: can't run %s: %s\n", db.updcmd, sys.Errstr())
		return
	}
	msg := make([]byte, 256)
	for {
		n, err := wait.Read(msg)
		if err != nil || n == 0 || strings.HasPrefix(string(msg[:n]), strconv.Itoa(pid)+" ") {
			break
		}
	}
}

func (db *database) lock(c *styxservers.Fid) bool {
	if typ(c.Path) != qdata || !db.locking {
		return true
	}
	for _, l := range db.locks {
		if l.qpath == c.Path {
			return l.fid == c.Fid
		}
	}
	db.locks = append(db.locks, lock{c.Path, c.Fid})
	return true
}

func (db *database) unlock(c *styxservers.Fid) {
	if typ(c.Path) != qdata || !db.locking {
		return
	}
	ls := db.locks[:0]
	for _, l := range db.locks {
		if l.qpath != c.Path || l.fid != c.Fid {
			ls = append(ls, l)
		}
	}
	db.locks = ls
}

func (db *database) ownlock(c *styxservers.Fid) bool {
	if typ(c.Path) != qdata || !db.locking {
		return true
	}
	for _, l := range db.locks {
		if l.qpath == c.Path {
			return l.fid == c.Fid
		}
	}
	return false
}

// freerec frees a record and merges it with the free blocks around it,
// keeping each merged block small enough for its header.
func (db *database) freerec(recno int) {
	recs := db.records
	recs[recno].datalen = -1
	f := recno
	for f > 0 && recs[f-1].datalen == -1 {
		f--
	}
	e := recno + 1
	for e < len(recs) && recs[e].datalen == -1 {
		e++
	}
	out := append([]*record(nil), recs[:f]...)
	var g *record
	for _, r := range recs[f:e] {
		if g != nil && g.count+headlen+r.count <= maxsize {
			g.count += headlen + r.count
			continue
		}
		if g != nil {
			db.writeheader(g)
		}
		g = r
		out = append(out, g)
	}
	db.writeheader(g)
	db.records = append(out, recs[e:]...)
}

func (db *database) splitrec(recno, pos int) {
	r := db.records[recno]
	nr := &record{id: -1, offset: r.offset + pos + headlen, count: r.count - headlen - pos, datalen: -1}
	r.count = pos
	db.records = append(db.records[:recno+1], append([]*record{nr}, db.records[recno+1:]...)...)
	db.writeheader(nr)
}

func (db *database) writerec(recno int, data []byte) int {
	r := db.records[recno]
	r.datalen = len(data)
	if db.writeheader(r) < 0 {
		return -1
	}
	if _, err := db.file.WriteAt(data, int64(r.offset)); err != nil {
		return -1
	}
	return 0
}

func (db *database) writeheader(r *record) int {
	h := fmt.Sprintf("%4d %4d\n", r.count, r.datalen)
	if _, err := db.file.WriteAt([]byte(h), int64(r.offset-headlen)); err != nil {
		fmt.Fprintf(stderr, "dbfs: error writing header (id %d, offset %d, count %d, datalen %d): %s\n",
			r.id, r.offset, r.count, r.datalen, sys.Errstr())
		return -1
	}
	return 0
}

// allocrec finds or makes a block of at least nb bytes; it does not mark
// it allocated.
func (db *database) allocrec(nb int) int {
	nb = max(nb, minsize)
	best, n := -1, -1
	for i, r := range db.records {
		if r.datalen == -1 {
			if avail := r.count - nb; avail >= 0 && (n == -1 || avail < best) {
				best, n = avail, i
			}
		}
	}
	if n != -1 {
		return n
	}
	offset := 0
	if nr := len(db.records); nr > 0 {
		offset = db.records[nr-1].offset + db.records[nr-1].count
	}
	if _, err := db.file.WriteAt(make([]byte, nb+headlen), int64(offset)); err != nil {
		fmt.Fprintf(stderr, "dbfs: write of new entry failed: %s\n", sys.Errstr())
		return -1
	}
	db.records = append(db.records, &record{id: -1, offset: offset + headlen, count: nb, datalen: -1})
	return len(db.records) - 1
}
