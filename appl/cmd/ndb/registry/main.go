// Command ndb/registry is the service registry (Inferno registry(4)). It
// serves Styx on its standard input, and is mounted on /mnt/registry:
//
//	new	opened and written with a service name (and attribute value
//		pairs) to register a service; it lasts while new stays open
//		unless it has the attribute persist
//	index	all services, one per line: name, then attributes and values
//	find	written with attribute value pairs (* matches any value), and
//		read for the services that have all of them
//	event	each read returns when the registry changes
//	name	each service: read for its line, written with attribute value
//		pairs to change them, removed (by its owner) to unregister it
//
// With -f, services are loaded from the ndb(6) file's service= entries.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/ndb"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	Qroot = iota
	Qnew
	Qindex
	Qevent
	Qfind
	Qsvc

	shift = 4
	mask  = 0xF

	egreg = "buggy program!"
)

type service struct {
	id     int
	owner  string
	name   string
	atime  uint32
	mtime  uint32
	vers   uint32
	fid    uint32 // the fid that created it (NOFID if static)
	attrs  [][2]string
	static bool
}

type filter struct {
	id    uint32
	attrs [][2]string
}

type event struct {
	id   uint32
	vers int
	m    *styx.Fcall // outstanding read
}

var (
	services  []*service
	filters   []*filter
	events    []*event
	idseq     int
	rootvers  int
	now       uint32
	startdate uint32
	srv       *styxservers.Server
)

func main() {
	dbfile := ""
	a := arg.New(os.Args)
	a.SetUsage("ndb/registry [-f initdb]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'f':
			dbfile = a.EArg()
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	sys.Pctl(sys.FORKNS, nil)
	startdate = uint32(time.Now().Unix())
	now = startdate
	if dbfile != "" {
		db, err := ndb.Open(dbfile)
		if err != nil {
			os.Stderr.WriteString("registry: can't open " + dbfile + ": " + sys.Errstr() + "\n")
			sys.Exits("fail:error")
		}
		dbload(db)
	}
	sys.Detach()
	srv = styxservers.New(os.NewFile(0, "styx"), nav{}, Qroot)
	for {
		t, err := srv.Next()
		if err != nil {
			return
		}
		now = uint32(time.Now().Unix())
		if e := handle(t); e != "" {
			srv.Error(t, e)
		}
	}
}

func handle(t *styx.Fcall) string {
	switch t.Type {
	case styx.Topen:
		fid, mode, _, err := srv.CanOpen(t)
		if err != nil {
			return err.Error()
		}
		if fid.Qtype&styx.QTDIR != 0 {
			srv.Default(t)
			return ""
		}
		open(t, fid, mode)
	case styx.Tread:
		fid, err := srv.CanRead(t)
		if err != nil {
			return err.Error()
		}
		if fid.Qtype&styx.QTDIR != 0 {
			srv.Read(t)
			return ""
		}
		return read(t, fid)
	case styx.Twrite:
		fid, err := srv.CanWrite(t)
		if err != nil {
			return err.Error()
		}
		if e := write(t, fid); e != "" {
			return e
		}
		srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
	case styx.Tclunk:
		if c := srv.Clunk(t); c != nil {
			clunk(c)
		}
	case styx.Tremove:
		fid, _, err := srv.CanRemove(t)
		if fid == nil {
			if fid = srv.GetFid(t.Fid); fid == nil {
				return err.Error()
			}
		}
		srv.DelFid(fid) // always clunked, even on error
		if err != nil {
			return err.Error()
		}
		if e := remove(fid); e != "" {
			return e
		}
		srv.Reply(&styx.Fcall{Type: styx.Rremove, Tag: t.Tag})
	case styx.Tflush:
		for _, e := range events {
			if e.m != nil && e.m.Tag == t.Oldtag {
				e.m = nil
				break
			}
		}
		srv.Default(t)
	default:
		srv.Default(t)
	}
	return ""
}

func open(t *styx.Fcall, fid *styxservers.Fid, mode int) {
	q := styx.Qid{Path: fid.Path, Type: fid.Qtype}
	switch fid.Path & mask {
	case Qnew:
		svc := newservice(fid.Uname)
		svc.fid = fid.Fid
		q = styx.Qid{Path: uint64(svc.id<<shift | Qsvc)}
	case Qevent:
		events = append(events, &event{id: fid.Fid, vers: rootvers})
	}
	fid.Open(mode, q)
	srv.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: q})
}

func read(t *styx.Fcall, fid *styxservers.Fid) string {
	path := fid.Path
	data, _ := fid.Data.([]byte)
	switch path & mask {
	case Qindex:
		if data == nil || t.Offset == 0 {
			data = indexdata(-1, styx.NOFID)
		}
	case Qfind:
		if data == nil || t.Offset == 0 {
			data = indexdata(-1, fid.Fid)
		}
	case Qsvc:
		if data == nil || t.Offset == 0 {
			if svc := findservice(int(path >> shift)); svc != nil {
				svc.atime = now
			}
			data = indexdata(int(path>>shift), styx.NOFID)
		}
	case Qevent:
		e := findevent(fid.Fid)
		if e.vers == rootvers {
			if e.m != nil {
				return "concurrent read for event fid"
			}
			e.m = t
			return ""
		}
		e.vers = rootvers
		t.Offset = 0
		srv.Reply(styxservers.ReadStr(t, fmt.Sprintf("%8.8d\n", rootvers)))
		return ""
	default:
		return egreg
	}
	fid.Data = data
	srv.Reply(styxservers.ReadBytes(t, data))
	return ""
}

func write(t *styx.Fcall, fid *styxservers.Fid) string {
	path := fid.Path
	switch path & mask {
	case Qsvc:
		svc := findservice(int(path >> shift))
		if svc == nil {
			return egreg
		}
		toks := quote.Unquoted(string(t.Data))
		if toks == nil {
			return "bad syntax"
		}
		// the first write names the service (possibly with attributes)
		if svc.name == "" {
			if err := svcnameok(toks[0]); err != "" {
				return err
			}
			svc.name = toks[0]
			toks = toks[1:]
		}
		if len(toks)%2 != 0 {
			return "odd attribute/value pairs"
		}
		svc.mtime = now
		svc.vers++
		for ; len(toks) > 0; toks = toks[2:] {
			svc.set(toks[0], toks[1])
		}
		rootvers++
		post(rootvers)
	case Qfind:
		toks := quote.Unquoted(string(t.Data))
		if len(toks)%2 != 0 {
			return "odd attribute/value pairs"
		}
		f := findfilter(fid.Fid)
		if len(toks) != 0 {
			var a [][2]string
			for ; len(toks) > 0; toks = toks[2:] {
				a = append(a, [2]string{toks[0], toks[1]})
			}
			if f == nil {
				f = &filter{id: fid.Fid}
				filters = append(filters, f)
			}
			f.attrs = a
		} else if f != nil {
			removefilter(f)
		}
	default:
		return egreg
	}
	return ""
}

func clunk(fid *styxservers.Fid) {
	path := fid.Path
	switch path & mask {
	case Qsvc:
		svc := findservice(int(path >> shift))
		if svc != nil && !svc.static && svc.fid == fid.Fid && (fid.Mode&styxservers.ORCLOSE != 0 || atoi(svc.get("persist")) == 0) {
			removeservice(svc)
			if svc.name != "" { // otherwise there's no visible change
				rootvers++
				post(rootvers)
			}
		}
	case Qevent:
		for i, e := range events {
			if e.id == fid.Fid {
				events = append(events[:i], events[i+1:]...)
				break
			}
		}
	case Qfind:
		if f := findfilter(fid.Fid); f != nil {
			removefilter(f)
		}
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func remove(fid *styxservers.Fid) string {
	if fid.Path&mask == Qsvc {
		if svc := findservice(int(fid.Path >> shift)); svc != nil && fid.Uname == svc.owner {
			removeservice(svc)
			rootvers++
			post(rootvers)
			return ""
		}
	}
	return "permission denied"
}

func svcnameok(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 32 || c == '/' || c == 0x7f {
			return "bad character in service name"
		}
	}
	switch s {
	case "new", "event", "find", "index", "":
		return "bad service name"
	}
	for _, svc := range services {
		if svc.name == s {
			return "duplicate service name"
		}
	}
	return ""
}

func indexdata(id int, filterid uint32) []byte {
	var f *filter
	if filterid != styx.NOFID {
		f = findfilter(filterid)
	}
	var b strings.Builder
	for _, svc := range services {
		if svc.name == "" {
			continue
		}
		if id == -1 {
			if f != nil && !f.match(svc.attrs) {
				continue
			}
		} else if svc.id != id {
			continue
		}
		b.WriteString(quote.Quote(svc.name))
		for _, a := range svc.attrs {
			b.WriteString(" " + quote.Quote(a[0]) + " " + quote.Quote(a[1]))
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// nav answers for the tree: the root and its files.
type nav struct{}

func (nav) Stat(path uint64) (*styx.Dir, error) { return dirgen(path) }

func (nav) Walk(parent uint64, name string) (*styx.Dir, error) {
	if parent&mask != Qroot {
		if name == ".." {
			return dirgen(Qroot)
		}
		return nil, styxservers.Enotdir
	}
	switch name {
	case "..":
		return dirgen(Qroot)
	case "new":
		return dirgen(Qnew)
	case "index":
		return dirgen(Qindex)
	case "event":
		return dirgen(Qevent)
	case "find":
		return dirgen(Qfind)
	}
	for _, svc := range services {
		if svc.name == name {
			return dirgen(uint64(svc.id<<shift | Qsvc))
		}
	}
	return nil, styxservers.Enotfound
}

func (nav) Readdir(path uint64, offset, count int) []*styx.Dir {
	if path&mask != Qroot {
		return nil
	}
	d := []uint64{Qnew, Qindex, Qfind, Qevent}
	for _, svc := range services {
		if svc.name != "" {
			d = append(d, uint64(svc.id<<shift|Qsvc))
		}
	}
	var out []*styx.Dir
	for i := offset; i < len(d) && len(out) < count; i++ {
		if dir, err := dirgen(d[i]); dir != nil {
			out = append(out, dir)
		} else {
			fmt.Fprintf(os.Stderr, "registry: bad qid %#x: %s\n", d[i], err)
		}
	}
	return out
}

func dirgen(path uint64) (*styx.Dir, error) {
	var name string
	var perm uint32
	var svc *service
	switch path & mask {
	case Qroot:
		name, perm = ".", 0o777|styx.DMDIR
	case Qnew:
		name, perm = "new", 0o666
	case Qindex:
		name, perm = "index", 0o444
	case Qevent:
		name, perm = "event", 0o444
	case Qfind:
		name, perm = "find", 0o666
	case Qsvc:
		if svc = findservice(int(path >> shift)); svc == nil {
			return nil, styxservers.Enotfound
		}
		name, perm = svc.name, 0o644
	default:
		return nil, styxservers.Enotfound
	}
	d := &styx.Dir{Qid: styx.Qid{Path: path}, Mode: perm, Name: name}
	if perm&styx.DMDIR != 0 {
		d.Qid.Type = styx.QTDIR
	}
	if svc != nil {
		d.UID, d.GID = svc.owner, svc.owner
		d.Atime, d.Mtime = svc.atime, svc.mtime
		d.Qid.Vers = svc.vers
	} else {
		d.UID, d.GID = "registry", "registry"
		d.Atime, d.Mtime = startdate, startdate
		if path == Qroot {
			d.Qid.Vers = uint32(rootvers)
		}
	}
	return d, nil
}

func newservice(owner string) *service {
	svc := &service{id: idseq, owner: owner, atime: now, mtime: now}
	idseq++
	services = append(services, svc)
	return svc
}

func findservice(id int) *service {
	for _, s := range services {
		if s.id == id {
			return s
		}
	}
	return nil
}

// removeservice takes svc out, moving the last service into its slot,
// as Inferno's Service.remove does.
func removeservice(svc *service) {
	for i, s := range services {
		if s == svc {
			last := len(services) - 1
			services[i] = services[last]
			services[last] = nil
			services = services[:last]
			return
		}
	}
}

func (svc *service) get(attr string) string {
	for _, a := range svc.attrs {
		if a[0] == attr {
			return a[1]
		}
	}
	return ""
}

// set sets attr, a new attribute going first, as Inferno's list does.
func (svc *service) set(attr, val string) {
	for i, a := range svc.attrs {
		if a[0] == attr {
			svc.attrs[i][1] = val
			return
		}
	}
	svc.attrs = append([][2]string{{attr, val}}, svc.attrs...)
}

func findfilter(id uint32) *filter {
	if id == styx.NOFID {
		return nil
	}
	for _, f := range filters {
		if f.id == id {
			return f
		}
	}
	return nil
}

func removefilter(f *filter) {
	for i, g := range filters {
		if g == f {
			filters = append(filters[:i], filters[i+1:]...)
			return
		}
	}
}

func (f *filter) match(attrs [][2]string) bool {
next:
	for _, q := range f.attrs {
		for _, a := range attrs {
			if a[0] == q[0] && (q[1] == "*" || a[1] == q[1]) {
				continue next
			}
		}
		return false
	}
	return true
}

func findevent(id uint32) *event {
	for _, e := range events {
		if e.id == id {
			return e
		}
	}
	return nil
}

func post(vers int) {
	s := fmt.Sprintf("%8.8d\n", vers)
	for _, e := range events {
		if e.vers < vers && e.m != nil {
			srv.Reply(styxservers.ReadStr(e.m, s))
			e.vers = vers
			e.m = nil
		}
	}
}

func dbload(db *ndb.DB) {
	var p *ndb.Ptr
	for {
		var e *ndb.Entry
		if e, p = db.Find(p, "service"); e == nil {
			break
		}
		name := e.FindFirst("service")
		if name == "" || svcnameok(name) != "" {
			continue
		}
		svc := newservice("registry")
		svc.name = name
		svc.fid = styx.NOFID
		svc.static = true
		for _, l := range e.Lines {
			for _, a := range l.Pairs {
				if a.Attr != "service" {
					svc.set(a.Attr, a.Val)
				}
			}
		}
	}
}
