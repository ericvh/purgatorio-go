// Command dbfs serves the records of a text file as numbered files
// (Inferno dbfs(4)). Records end with an empty line. Opening new makes a
// record; a write replaces a record's data and rewrites the file;
// removing a record's file deletes it.
//
// A guest is single-threaded, and mount must have answers from the
// server before it returns, so the server runs as a second prog, dbfs
// with the internal option -s and the pipe as its standard input, as
// memfs does. The first prog reads the file to report errors, then
// mounts the other end of the pipe.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	qdir = iota
	qnew
	qdata
)

const eremoved = "file removed"

type record struct {
	id   int // file number in the directory
	x    int // index in the file
	vers int
	data []byte // nil (or empty, as Limbo's nil array) when removed
}

type database struct {
	name    string
	records []*record
	dirty   int
	vers    int
}

var (
	stderr = os.Stderr
	db     *database
	user   string
)

func usage() {
	fmt.Fprintf(stderr, "Usage: dbfs [-a|-b|-ac|-bc] [-D] file mountpoint\n")
	sys.Exits("fail:usage")
}

func main() {
	a := arg.New(os.Args)
	flags := sys.MREPL
	copt, empty, trace, srv := false, false, false, false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			flags = sys.MAFTER
		case 'b':
			flags = sys.MBEFORE
		case 'c':
			copt = true
		case 'e':
			empty = true
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
		serve(args[0], trace)
		return
	}
	if len(args) != 2 {
		usage()
	}
	if copt {
		flags |= sys.MCREATE
	}
	file, mountpt := args[0], args[1]
	data, err := os.ReadFile(file)
	if err != nil && empty {
		if _, serr := sys.Stat(file); serr != nil {
			var fd int
			if fd, err = sys.Create(file, sys.OREAD, 0o600); err == nil {
				sys.Close(fd)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "dbfs: can't open %s: %s\n", file, sys.Errstr())
		sys.Exits("fail:open")
	}
	dbread(data)
	p, err := sys.Pipe()
	if err != nil {
		fmt.Fprintf(stderr, "dbfs: can't create pipe: %s\n", sys.Errstr())
		sys.Exits("fail:pipe")
	}
	sargs := []string{"dbfs", "-s"}
	if trace {
		sargs = append(sargs, "-D")
	}
	sargs = append(sargs, file)
	if _, err := sys.Spawn("/bin/dbfs.wasm", sargs, sys.NEWFD|sys.FORKNS,
		[][2]int{{0, p[1]}, {1, 1}, {2, 2}}); err != nil {
		fmt.Fprintf(stderr, "dbfs: can't start server: %s\n", sys.Errstr())
		sys.Exits("fail:pipe")
	}
	sys.Close(p[1])
	if _, err := sys.Mount(p[0], -1, mountpt, flags, ""); err != nil {
		fmt.Fprintf(stderr, "dbfs: mount failed: %s\n", sys.Errstr())
		sys.Exits("fail:mount")
	}
}

func rf(f string) string {
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	return string(b)
}

// dbread splits the file into records: a record is (.+\n)*\n.
func dbread(data []byte) []*record {
	var recs []*record
	var cur []byte
	in := bufio.NewReader(bytes.NewReader(data))
	for {
		s, err := in.ReadString('\n')
		if s == "" && err != nil {
			if cur != nil {
				recs = append(recs, &record{data: cur})
			}
			break
		}
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		if s == "\n" {
			recs = append(recs, &record{data: cur})
			cur = nil
			continue
		}
		cur = append(cur, s...)
	}
	for i, r := range recs {
		r.id, r.x = i, i
	}
	return recs
}

func (db *database) sync() error {
	if db.dirty == 0 {
		return nil
	}
	fd, err := sys.Create(db.name, sys.OWRITE, 0o666)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), db.name)
	defer f.Close()
	b := bufio.NewWriter(f)
	for _, r := range db.records {
		if len(r.data) > 0 {
			b.Write(r.data)
			b.WriteByte('\n')
		}
	}
	if err := b.Flush(); err != nil {
		return err
	}
	db.dirty = 0
	return nil
}

func (db *database) findrec(id int) *record {
	for _, r := range db.records {
		if len(r.data) > 0 && r.id == id {
			return r
		}
	}
	return nil
}

func (db *database) newrec() *record {
	n := len(db.records)
	r := &record{id: n, x: n}
	db.records = append(db.records, r)
	db.vers++
	return r
}

func (r *record) qid() styx.Qid {
	return styx.Qid{Path: qpath(r.x, qdata), Vers: uint32(r.vers), Type: styx.QTFILE}
}

func qpath(w, q int) uint64 { return uint64(w<<8 | q) }
func typ(path uint64) int   { return int(path & 0xFF) }
func fileno(path uint64) int {
	return int(path>>8) & 0xFFFFFF
}

func serve(file string, trace bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "dbfs: can't open %s: %s\n", file, sys.Errstr())
		sys.Exits("fail:open")
	}
	db = &database{name: file, records: dbread(data)}
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
		c := srv.GetFid(t.Fid)
		if c == nil || typ(c.Path) != qnew {
			srv.Open(t)
			return
		}
		if c.Uname != user {
			srv.Error(t, string(styxservers.Eperm))
			return
		}
		mode := styxservers.OpenMode(int(t.Mode))
		if mode < 0 {
			srv.Error(t, string(styxservers.Ebadarg))
			return
		}
		q := db.newrec().qid()
		c.Open(mode, q)
		srv.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: q, Iounit: srv.Iounit()})
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
		srv.Reply(styxservers.ReadBytes(t, rec(c.Path).data))
	case styx.Twrite:
		c, err := srv.CanWrite(t)
		if c == nil {
			srv.Error(t, err.Error())
			return
		}
		if e := checkrec(t.Data); e != "" {
			srv.Error(t, e)
			return
		}
		r := rec(c.Path)
		if r == nil {
			srv.Error(t, eremoved)
			return
		}
		r.data = append([]byte(nil), t.Data...)
		r.vers++
		db.dirty++
		if db.sync() != nil {
			srv.Error(t, sys.Errstr())
			return
		}
		srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
	case styx.Tremove:
		c := srv.GetFid(t.Fid)
		if c == nil || c.Qtype&styx.QTDIR != 0 || typ(c.Path) != qdata {
			srv.Remove(t)
			return
		}
		if r := rec(c.Path); r != nil {
			r.data = nil
		}
		db.dirty++
		srv.DelFid(c)
		if db.sync() != nil {
			srv.Error(t, sys.Errstr())
			return
		}
		srv.Reply(&styx.Fcall{Type: styx.Rremove, Tag: t.Tag})
	default:
		srv.Default(t)
	}
}

// rec is the record for a data file's path, or nil.
func rec(path uint64) *record {
	if n := fileno(path); n < len(db.records) {
		return db.records[n]
	}
	return nil
}

// checkrec accepts data that is (.+\n)*, without a final empty line.
func checkrec(data []byte) string {
	for b := data; len(b) > 0; {
		i := bytes.IndexByte(b, '\n')
		if i < 0 || i == 0 || !utf8.Valid(b[:i+1]) {
			return "partial or malformed record"
		}
		b = b[i+1:]
	}
	return ""
}

func mkdir(q styx.Qid, name string, length int, perm uint32) *styx.Dir {
	if q.Type&styx.QTDIR != 0 {
		perm |= styx.DMDIR
	}
	return &styx.Dir{Qid: q, Mode: perm, Name: name, UID: user, GID: user, MUID: "", Length: uint64(length)}
}

func dirgen(p uint64) (*styx.Dir, error) {
	switch typ(p) {
	case qdir:
		return mkdir(styx.Qid{Path: qpath(0, qdir), Vers: uint32(db.vers), Type: styx.QTDIR}, "/", 0, 0o700), nil
	case qnew:
		return mkdir(styx.Qid{Path: qpath(0, qnew)}, "new", 0, 0o600), nil
	}
	n := fileno(p)
	if n >= len(db.records) {
		return nil, styxservers.Enotfound
	}
	r := db.records[n]
	if len(r.data) == 0 {
		return nil, styxservers.Enotfound
	}
	return mkdir(r.qid(), strconv.Itoa(r.id), len(r.data), 0o600), nil
}

// dirslot is the index of the nth record that holds data, or -1.
func dirslot(n int) int {
	for i, r := range db.records {
		if len(r.data) > 0 {
			if n == 0 {
				return i
			}
			n--
		}
	}
	return -1
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
	default:
		if name == "" || name[0] < '0' || name[0] > '9' {
			return nil, styxservers.Enotfound
		}
		r := db.findrec(atoi(name))
		if r == nil {
			return nil, styxservers.Enotfound
		}
		p = qpath(r.x, qdata)
	}
	return dirgen(p)
}

func (nav) Readdir(p uint64, off, count int) []*styx.Dir {
	if p != qdir {
		return nil
	}
	var ds []*styx.Dir
	i := off
	if i == 0 {
		d, _ := dirgen(qpath(0, qnew))
		ds = append(ds, d)
		count--
	} else {
		i-- // entry 0 is new
	}
	for ; count > 0; count-- {
		j := dirslot(i)
		if j < 0 {
			break
		}
		d, _ := dirgen(qpath(j, qdata))
		ds = append(ds, d)
		i++
	}
	return ds
}

// atoi is Limbo's int of a string: the leading digits.
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
