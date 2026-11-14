// Command kfs is the disk file server (Inferno kfs(4)): it serves Styx
// on its standard input for the Plan 9 kfs file system held in file,
// usually a disk partition such as /dev/sdC0/data. -r reams (formats)
// file first, with blocks of -b bytes (1024 by default); a freshly
// reamed file system allows any wstat and write until it is served
// again. -R serves it read only, -A does not update access times, -W
// allows any wstat and -P any write. Mount it with
//
//	mount -c {kfs /dev/sdC0/data} /n/kfs
//
// Not done: the console (-n name and kfscmd(8)), and the check (fsck)
// that kfs.b runs on a file system that was not shut down cleanly; -c
// is accepted. The users file, /adm/users, is read at start.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

// Errors from the file server, as kfs.b's.
const (
	eaccess   = "access permission denied"
	ealloc    = "phase error -- directory entry not allocated"
	eauthmsg  = "kfs: authentication not required"
	ebadspc   = "attach -- bad specifier"
	ebroken   = "close/read/write -- lock is broken"
	ecount    = "read/write -- count too big"
	edir1     = "walk -- in a non-directory"
	edir2     = "create -- in a non-directory"
	edot      = "create -- . and .. illegal names"
	eempty    = "remove -- directory not empty"
	eentry    = "directory entry not found"
	eexist    = "create -- file exists"
	efid      = "unknown fid"
	efidinuse = "fid already in use"
	efull     = "file system full"
	elocked   = "open/create -- file is locked"
	emode     = "open/create -- unknown mode"
	ename     = "create/wstat -- bad character in file name"
	enotd     = "wstat -- attempt to change directory"
	enotg     = "wstat -- not in group"
	enotl     = "wstat -- attempt to change length"
	enotm     = "wstat -- unknown type/mode"
	enotu     = "wstat -- not owner"
	eoffset   = "read/write -- offset negative"
	eopen     = "read/write -- on non open fid"
	ephase    = "phase error -- cannot happen"
	eqid      = "phase error -- qid does not match"
	eqidmode  = "wstat -- qid.qtype/dir.mode mismatch"
	eronly    = "file system read only"
	ersc      = "it's russ's fault.  bug him."
	etoolong  = "name too long"
	etoobig   = "write -- file size limit"
)

const (
	maxrpc = 8192 + styx.IOHDRSZ
	tlockt = 5 * 60 // seconds an exclusive-use lock lasts
)

var (
	debug                  bool
	wstatallow, writeallow bool
	conschan               *channel
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("kfs [-r [-b bufsize]] [-cADPRW] [-n name] kfsfile")
	bsize := 1024
	ream := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'c':
		case 'r':
			ream = true
		case 'b':
			bsize, _ = strconv.Atoi(a.EArg())
		case 'D':
			debug = true
		case 'P':
			writeallow = true
		case 'W':
			wstatallow = true
		case 'R':
			readonly = true
		case 'A':
			noatime = true
		case 'n':
			a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) != 1 {
		a.Usage()
	}
	if e := start(args[0], ream, bsize); e != "" {
		fatal(e)
	}
	serve()
}

// start opens the file system in path, reaming it first with blocks of
// bsize bytes if asked, and reads the users file.
func start(path string, ream bool, bsize int) string {
	conschan = &channel{fids: map[uint32]*file{}, msize: maxrpc, cons: true}
	cache = map[int]*iobuf{}
	users = nil
	mode := os.O_RDWR
	if readonly {
		mode = os.O_RDONLY
	}
	var err error
	if disk, err = os.OpenFile(path, mode, 0); err != nil {
		return fmt.Sprintf("can't open %s: %s", path, sys.Errstr())
	}
	if ream {
		if bsize <= 0 || bsize%512 != 0 || bsize > maxbufsize {
			return fmt.Sprintf("invalid block size %d", bsize)
		}
		rbufsize = bsize
		wrenream()
	} else if !wreninit() {
		return "kfs magic in trouble"
	}
	setsizes()
	if ream {
		superream(superaddr)
		rootream(rootaddr)
		wstatallow, writeallow = true, true
	}
	if wrencheck() {
		return "kfs super/root in trouble"
	}
	if !ream && !readonly && superok(0) == 0 {
		fmt.Println("kfs needs check")
	}
	d, e := getdentrya(rootaddr, 0, qproot, bread)
	if d != nil && d.mode&ddir == 0 {
		e = "not a directory"
	}
	if e != "" {
		return "bad root: " + e
	}
	d.put()
	consattach()
	userinit()
	return ""
}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "kfs: %s\n", s)
	sys.Exits("fail:error")
}

func panicf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "kfs: panic: "+f+"\n", a...)
	sys.Exits("panic")
}

func eprint(s string) { fmt.Printf("kfs: %s\n", s) }
func cprint(s string) { eprint(s) }

// serve answers Styx on standard input until the mount goes.
func serve() {
	srv := styxservers.New(os.NewFile(0, "styx"), nil, 0)
	if debug {
		srv.Trace = os.Stderr
	}
	cp := &channel{fids: map[uint32]*file{}}
	for {
		t, err := srv.Next()
		if err != nil {
			if debug {
				fmt.Fprintf(os.Stderr, "kfs: %v\n", err)
			}
			break
		}
		r := apply(cp, t)
		if t.Type == styx.Tversion && r.Type == styx.Rversion {
			srv.Msize = r.Msize
		}
		r.Tag = t.Tag
		if err := srv.Reply(r); err != nil {
			break
		}
	}
	if !readonly {
		superok(1)
	}
}

// channel is one conversation; the console's has its own.
type channel struct {
	fids  map[uint32]*file
	msize uint32
	cons  bool
}

type wpath struct {
	up   *wpath
	addr int
	slot int
}

type tlock struct {
	time  int
	qpath int
	file  *file
}

var tlocks []*tlock

type file struct {
	qid     styx.Qid
	wpath   *wpath
	tlock   *tlock
	addr    int
	slot    int
	fid     uint32
	uid     int
	open    int
	doffset uint64
	dslot   int
}

func (cp *channel) getfid(fid uint32, create bool) *file {
	if fid == styx.NOFID {
		return nil
	}
	f := cp.fids[fid]
	if create {
		if f != nil {
			return nil
		}
		f = &file{fid: fid, uid: none}
		cp.fids[fid] = f
		return f
	}
	return f
}

func (cp *channel) putfid(f *file) { delete(cp.fids, f.fid) }

func rerr(s string) *styx.Fcall { return &styx.Fcall{Type: styx.Rerror, Ename: s} }

func apply(cp *channel, t *styx.Fcall) *styx.Fcall {
	switch t.Type {
	case styx.Tversion:
		return rversion(cp, t)
	case styx.Tauth:
		return rerr(eauthmsg)
	case styx.Tflush:
		return &styx.Fcall{Type: styx.Rflush}
	case styx.Tattach:
		return rattach(cp, t)
	case styx.Twalk:
		return rwalk(cp, t)
	case styx.Topen:
		return ropen(cp, t)
	case styx.Tcreate:
		return rcreate(cp, t)
	case styx.Tread:
		return rread(cp, t)
	case styx.Twrite:
		return rwrite(cp, t)
	case styx.Tclunk:
		return rclunk(cp, t)
	case styx.Tremove:
		return rremove(cp, t)
	case styx.Tstat:
		return rstat(cp, t)
	case styx.Twstat:
		return rwstat(cp, t)
	}
	return rerr("Styx mtype")
}

// The console's conversation, used to read /adm/users.

const consfid1, consfid2 = 1, 2

func consattach() {
	apply(conschan, &styx.Fcall{Type: styx.Tattach, Fid: consfid1, Afid: styx.NOFID, Uname: "adm"})
}

func consread(names ...string) ([]byte, string) {
	r := apply(conschan, &styx.Fcall{Type: styx.Twalk, Fid: consfid1, Newfid: consfid2, Wname: names})
	if r.Type == styx.Rerror {
		return nil, r.Ename
	}
	if len(r.Wqid) != len(names) {
		apply(conschan, &styx.Fcall{Type: styx.Tclunk, Fid: consfid2})
		return nil, eexist
	}
	defer apply(conschan, &styx.Fcall{Type: styx.Tclunk, Fid: consfid2})
	if r = apply(conschan, &styx.Fcall{Type: styx.Topen, Fid: consfid2, Mode: sys.OREAD}); r.Type == styx.Rerror {
		cprint("open: " + r.Ename)
		return nil, r.Ename
	}
	var buf []byte
	for {
		r = apply(conschan, &styx.Fcall{Type: styx.Tread, Fid: consfid2, Offset: uint64(len(buf)), Count: 8192})
		if r.Type == styx.Rerror {
			cprint("/adm/users read error: " + r.Ename)
			return nil, r.Ename
		}
		if len(r.Data) == 0 {
			return buf, ""
		}
		buf = append(buf, r.Data...)
	}
}

func rversion(cp *channel, t *styx.Fcall) *styx.Fcall {
	cp.msize = uint32(rbufsize + styx.IOHDRSZ)
	if cp.msize < maxrpc {
		cp.msize = maxrpc
	}
	msize, version := styxservers.Compatible(t, maxrpc, styx.Version)
	if msize < 256 {
		return rerr("message size too small")
	}
	return &styx.Fcall{Type: styx.Rversion, Msize: msize, Version: version}
}

func (f *file) access(d *dentry, m int) bool {
	if wstatallow {
		return false
	}
	// none gets only other permissions
	if f.uid != none {
		if f.uid == d.uid && (m<<6)&d.mode != 0 {
			return false
		}
		if ingroup(f.uid, d.gid) && (m<<3)&d.mode != 0 {
			return false
		}
	}
	// other access for everyone except members of group "noworld"
	if m&d.mode != 0 {
		// walk directories regardless, or noworld's directories could
		// not be reached
		if d.mode&ddir != 0 && m == dexec {
			return false
		}
		if !ingroup(f.uid, noworld) {
			return false
		}
	}
	return true
}

func rattach(cp *channel, t *styx.Fcall) *styx.Fcall {
	if t.Aname != "" && t.Aname != "main" {
		return rerr(ebadspc)
	}
	f := cp.getfid(t.Fid, true)
	if f == nil {
		return rerr(efidinuse)
	}
	p := getbuf(rootaddr, bread)
	if p == nil {
		cp.putfid(f)
		return rerr("can't access root block")
	}
	d := getdentry(p, 0)
	if d == nil || p.checktag(tdir, qproot) != 0 || d.mode&dalloc == 0 || d.mode&ddir == 0 {
		p.put()
		cp.putfid(f)
		return rerr(ealloc)
	}
	if f.access(d, dexec) {
		p.put()
		cp.putfid(f)
		return rerr(eaccess)
	}
	d.access(fread, f.uid)
	f.qid = d.qid
	f.addr = p.addr
	f.slot = 0
	f.open = 0
	if cp.cons {
		f.uid = -1
	} else {
		f.uid = strtouid(t.Uname)
	}
	f.wpath = nil
	p.put()
	return &styx.Fcall{Type: styx.Rattach, Qid: f.qid}
}

func clone(nf, f *file) {
	nf.qid = f.qid
	nf.wpath = f.wpath
	nf.addr = f.addr
	nf.slot = f.slot
	nf.uid = f.uid
	nf.open = f.open &^ fremov
}

func walkname(f *file, wname string) (string, styx.Qid) {
	// The file must not be open and must be a directory.
	if f.open != 0 {
		return emode, styx.Qid{}
	}
	d, e := getdentryf(f, bread)
	if d == nil {
		return e, styx.Qid{}
	}
	if d.mode&ddir == 0 {
		d.put()
		return edir1, styx.Qid{}
	}
	// The user must have permission to search the directory.
	if f.access(d, dexec) {
		d.put()
		return eaccess, styx.Qid{}
	}
	d.access(fread, f.uid)
	if wname == "." || wname == ".." && f.wpath == nil {
		d.put()
		return "", f.qid
	}
	var d1 *dentry
	var slot int
	if wname == ".." {
		d.put()
		addr := f.wpath.addr
		slot = f.wpath.slot
		if d1, e = getdentrya(addr, slot, qpnone, bread); d1 == nil {
			return e, styx.Qid{}
		}
		f.wpath = f.wpath.up
	} else {
	search:
		for addr := 0; ; addr++ {
			if d.iob == nil {
				if d, e = getdentryf(f, bread); d == nil {
					return e, styx.Qid{}
				}
			}
			p1 := d.getblk1(addr, 0)
			if p1 == nil || p1.checktag(tdir, int(d.qid.Path)) != 0 {
				if p1 != nil {
					p1.put()
				}
				return eentry, styx.Qid{}
			}
			for slot = 0; slot < dirperbuf; slot++ {
				d1 = getdentry(p1, slot)
				if d1.mode&dalloc == 0 || wname != d1.name {
					continue
				}
				f.wpath = &wpath{up: f.wpath, addr: f.addr, slot: f.slot}
				slot += dirperbuf * addr
				break search
			}
			p1.put()
		}
		d.put()
	}
	f.addr = d1.iob.addr
	f.slot = slot
	f.qid = d1.qid
	d1.put()
	return "", f.qid
}

func rwalk(cp *channel, t *styx.Fcall) *styx.Fcall {
	f := cp.getfid(t.Fid, false)
	if f == nil {
		return rerr(efid)
	}
	if f.open != 0 {
		return rerr(emode)
	}
	var nf, tf *file
	if t.Newfid != t.Fid {
		if nf = cp.getfid(t.Newfid, true); nf == nil {
			return rerr(efidinuse)
		}
	} else if len(t.Wname) != 0 {
		tf = &file{fid: styx.NOFID, uid: none}
		nf = tf
	} else {
		return &styx.Fcall{Type: styx.Rwalk}
	}
	clone(nf, f)
	var qids []styx.Qid
	var e string
	for _, name := range t.Wname {
		var q styx.Qid
		if e, q = walkname(nf, name); e != "" {
			break
		}
		qids = append(qids, q)
	}
	switch {
	case len(t.Wname) == 0:
		if e != "" {
			cp.putfid(nf)
		}
	case len(qids) < len(t.Wname):
		// Not all walked: drop nf, leave f alone; an error only if
		// nothing was walked.
		if nf != tf {
			cp.putfid(nf)
		}
		if len(qids) != 0 {
			e = ""
		}
	default:
		if nf == tf {
			f.qid = nf.qid
			f.wpath = nf.wpath
			f.addr = nf.addr
			f.slot = nf.slot
		}
	}
	if e != "" {
		return rerr(e)
	}
	return &styx.Fcall{Type: styx.Rwalk, Wqid: qids}
}

func isro() bool { return readonly }

func ropen(cp *channel, t *styx.Fcall) *styx.Fcall {
	wok := cp.cons || writeallow
	f := cp.getfid(t.Fid, false)
	if f == nil {
		return rerr(efid)
	}
	ro := isro() || writegroup != 0 && !ingroup(f.uid, writegroup)
	if t.Mode&sys.ORCLOSE != 0 {
		if ro {
			return rerr(eronly)
		}
		// check the parent directory of the file to be removed
		if f.wpath == nil || f.wpath.addr == f.addr {
			return rerr(ephase)
		}
		p := getbuf(f.wpath.addr, bread)
		if p == nil || p.checktag(tdir, qpnone) != 0 {
			if p != nil {
				p.put()
			}
			return rerr(ephase)
		}
		d := getdentry(p, f.wpath.slot)
		if d == nil || d.mode&dalloc == 0 {
			p.put()
			return rerr(ephase)
		}
		if f.access(d, dwrite) {
			p.put()
			return rerr(eaccess)
		}
		p.put()
	}
	d, e := getdentryf(f, bread)
	if d == nil {
		return rerr(e)
	}
	p := d.iob
	fail := func(s string) *styx.Fcall {
		p.put()
		return rerr(s)
	}
	qid := d.qid
	var fmod int
	switch t.Mode & 7 {
	case sys.OREAD:
		if f.access(d, dread) && !wok {
			return fail(eaccess)
		}
		fmod = fread
	case sys.OWRITE:
		if d.mode&ddir != 0 || f.access(d, dwrite) && !wok {
			return fail(eaccess)
		}
		if ro {
			return fail(eronly)
		}
		fmod = fwrite
	case sys.ORDWR:
		if d.mode&ddir != 0 || f.access(d, dread) && !wok || f.access(d, dwrite) && !wok {
			return fail(eaccess)
		}
		if ro {
			return fail(eronly)
		}
		fmod = fread | fwrite
	case sys.OEXEC:
		if d.mode&ddir != 0 || f.access(d, dexec) && !wok {
			return fail(eaccess)
		}
		fmod = fread
	default:
		return fail(emode)
	}
	if t.Mode&sys.OTRUNC != 0 {
		if d.mode&ddir != 0 || f.access(d, dwrite) && !wok {
			return fail(eaccess)
		}
		if ro {
			return fail(eronly)
		}
	}
	if d.mode&dlock != 0 {
		tl := tlocked(d)
		if tl == nil {
			return fail(elocked)
		}
		f.tlock = tl
		tl.file = f
	}
	if t.Mode&sys.ORCLOSE != 0 {
		fmod |= fremov
	}
	f.open = fmod
	if t.Mode&sys.OTRUNC != 0 && d.mode&dapnd == 0 {
		d.trunc(f.uid)
		qid.Vers = d.qid.Vers
	}
	p.put()
	return &styx.Fcall{Type: styx.Ropen, Qid: qid, Iounit: cp.msize - styx.IOHDRSZ}
}

func rcreate(cp *channel, t *styx.Fcall) *styx.Fcall {
	wok := cp.cons || writeallow
	f := cp.getfid(t.Fid, false)
	if f == nil {
		return rerr(efid)
	}
	if isro() || writegroup != 0 && !ingroup(f.uid, writegroup) {
		return rerr(eronly)
	}
	d, e := getdentryf(f, bread)
	if e != "" {
		return rerr(e)
	}
	p := d.iob
	fail := func(s string) *styx.Fcall {
		p.put()
		return rerr(s)
	}
	if d.mode&ddir == 0 {
		return fail(edir2)
	}
	if f.access(d, dwrite) && !wok {
		return fail(eaccess)
	}
	d.access(fread, f.uid)
	// The name must be valid and fit in a directory entry.
	l := checkname9p2(t.Name)
	if l == 0 {
		return fail(ename)
	}
	if l+1 > namelen {
		return fail(etoolong)
	}
	if t.Name == "." || t.Name == ".." {
		return fail(edot)
	}
	addr1, slot1 := 0, 0 // the first empty slot, if any
	for addr := 0; ; addr++ {
		p1 := d.getblk(addr, 0)
		if p1 == nil {
			if addr1 != 0 {
				break
			}
			p1 = d.getblk(addr, tdir)
		}
		if p1 == nil {
			return fail(efull)
		}
		if p1.checktag(tdir, int(d.qid.Path)) != 0 {
			p1.put()
			return fail(ephase)
		}
		for slot := 0; slot < dirperbuf; slot++ {
			d1 := getdentry(p1, slot)
			if d1.mode&dalloc == 0 {
				if addr1 == 0 {
					addr1 = p1.addr
					slot1 = slot + addr*dirperbuf
				}
				continue
			}
			if t.Name == d1.name {
				p1.put()
				return fail(eexist)
			}
		}
		p1.put()
	}
	var fmod int
	switch t.Mode & 7 {
	case sys.OEXEC, sys.OREAD: // only useful to make directories
		fmod = fread
	case sys.OWRITE:
		fmod = fwrite
	case sys.ORDWR:
		fmod = fread | fwrite
	default:
		return fail(emode)
	}
	perm := int(t.Perm)
	if t.Perm&styx.DMDIR != 0 && (t.Mode&sys.OTRUNC != 0 || t.Perm&styx.DMAPPEND != 0 || fmod&fwrite != 0) {
		return fail(eaccess)
	}

	path := qidpathgen()
	p1 := getbuf(addr1, bread|bimm|bmod)
	if p1 == nil {
		return fail(ephase)
	}
	d1 := getdentry(p1, slot1)
	if d1 == nil || p1.checktag(tdir, int(d.qid.Path)) != 0 || d1.mode&dalloc != 0 {
		p1.put()
		return fail(ephase)
	}
	d1.name = t.Name
	if cp.cons {
		d1.uid, d1.gid = -1, -1
	} else {
		d1.uid = f.uid
		d1.gid = d.gid
		perm &= d.mode | ^0o666
		if t.Perm&styx.DMDIR != 0 {
			perm &= d.mode | ^0o777
		}
	}
	d1.qid = styx.Qid{Path: uint64(path)}
	d1.mode = dalloc | perm&0o777
	if t.Perm&styx.DMDIR != 0 {
		d1.mode |= ddir
	}
	if t.Perm&styx.DMAPPEND != 0 {
		d1.mode |= dapnd
	}
	var tl *tlock
	if t.Perm&styx.DMEXCL != 0 {
		d1.mode |= dlock
		tl = tlocked(d1) // nil if out of locks
	}
	d1.access(fwrite, f.uid)
	d1.change(^0)
	d1.update()
	qid := mkqid(path, 0, d1.mode)
	p1.put()
	d.change(^0)
	d.access(fwrite, f.uid)
	d.update()
	p.put()

	// walk to the new entry
	f.wpath = &wpath{up: f.wpath, addr: f.addr, slot: f.slot}
	f.qid = qid
	f.tlock = tl
	if tl != nil {
		tl.file = f
	}
	if t.Mode&sys.ORCLOSE != 0 {
		fmod |= fremov
	}
	f.open = fmod
	f.addr = addr1
	f.slot = slot1
	return &styx.Fcall{Type: styx.Rcreate, Qid: qid, Iounit: cp.msize - styx.IOHDRSZ}
}

func dirread(cp *channel, t *styx.Fcall, f *file, d *dentry) *styx.Fcall {
	count := int(t.Count)
	offset := t.Offset
	if iounit := int(cp.msize - styx.IOHDRSZ); count > iounit {
		count = iounit
	}
	data := make([]byte, 0, count)
	// Go on from the last read if it ended where this starts;
	// otherwise scan from the beginning.
	var addr, slot int
	var start uint64
	if offset == f.doffset {
		addr = f.dslot / dirperbuf
		slot = f.dslot % dirperbuf
		start = offset
	}
dread:
	for {
		if d.iob == nil {
			var e string
			if d, e = getdentryf(f, bread); d == nil {
				return rerr(e)
			}
		}
		p1 := d.getblk1(addr, 0)
		if p1 == nil {
			break
		}
		if p1.checktag(tdir, qpnone) != 0 {
			p1.put()
			return rerr(ephase)
		}
		for ; slot < dirperbuf; slot++ {
			d1 := getdentry(p1, slot)
			if d1.mode&dalloc == 0 {
				continue
			}
			dir := dir9p2(d1)
			b := styx.MarshalDir(nil, &dir)
			n := len(b)
			if n > count-len(data) {
				p1.put()
				break dread
			}
			start += uint64(n)
			if start <= offset {
				continue
			}
			data = append(data, b...)
			offset += uint64(n)
		}
		p1.put()
		slot = 0
		addr++
	}
	f.doffset = offset
	f.dslot = slot + dirperbuf*addr
	d.put()
	return &styx.Fcall{Type: styx.Rread, Data: data}
}

func rread(cp *channel, t *styx.Fcall) *styx.Fcall {
	f := cp.getfid(t.Fid, false)
	if f == nil {
		return rerr(efid)
	}
	if f.open&fread == 0 {
		return rerr(eopen)
	}
	count := int64(t.Count)
	if count > int64(cp.msize-styx.IOHDRSZ) {
		return rerr(ecount)
	}
	offset := int64(t.Offset)
	if offset < 0 {
		return rerr(eoffset)
	}
	d, e := getdentryf(f, bread)
	if d == nil {
		return rerr(e)
	}
	if tl := f.tlock; tl != nil {
		tim := now()
		if tl.time < tim || tl.file != f {
			d.put()
			return rerr(ebroken)
		}
		tl.time = tim + tlockt // renew the lock
	}
	d.access(fread, f.uid)
	if d.mode&ddir != 0 {
		return dirread(cp, t, f, d)
	}
	if offset+count > d.size {
		count = d.size - offset
	}
	if count < 0 {
		count = 0
	}
	data := make([]byte, 0, count)
	for count > 0 {
		if d.iob == nil {
			if d, e = getdentryf(f, bread); d == nil {
				return rerr(e)
			}
		}
		addr := int(offset / int64(bufsize))
		o := int(offset % int64(bufsize))
		n := min(int64(bufsize-o), count)
		if p1 := d.getblk1(addr, 0); p1 != nil {
			if p1.checktag(tfile, qpnone) != 0 {
				p1.put()
				return rerr(ephase)
			}
			data = append(data, p1.data[o:o+int(n)]...)
			p1.put()
		} else {
			data = append(data, emptyblock[:n]...)
		}
		count -= n
		offset += n
	}
	d.put()
	return &styx.Fcall{Type: styx.Rread, Data: data}
}

func rwrite(cp *channel, t *styx.Fcall) *styx.Fcall {
	f := cp.getfid(t.Fid, false)
	if f == nil {
		return rerr(efid)
	}
	if f.open&fwrite == 0 {
		return rerr(eopen)
	}
	if isro() || writegroup != 0 && !ingroup(f.uid, writegroup) {
		return rerr(eronly)
	}
	count := len(t.Data)
	if count > int(cp.msize-styx.IOHDRSZ) {
		return rerr(ecount)
	}
	offset := int64(t.Offset)
	if offset < 0 {
		return rerr(eoffset)
	}
	d, e := getdentryf(f, bread|bmod)
	if d == nil {
		return rerr(e)
	}
	if tl := f.tlock; tl != nil {
		tim := now()
		if tl.time < tim || tl.file != f {
			d.put()
			return rerr(ebroken)
		}
		tl.time = tim + tlockt
	}
	d.access(fwrite, f.uid)
	if d.mode&dapnd != 0 {
		offset = d.size
	}
	end := offset + int64(count)
	if end > d.size {
		if end > maxfilesize {
			d.put()
			return rerr(etoobig)
		}
		d.size = end
		d.change(usize)
	}
	d.update()
	nwrite := 0
	for count > 0 {
		if d.iob == nil {
			if d, e = getdentryf(f, bread|bmod); d == nil {
				return rerr(e)
			}
		}
		addr := int(offset / int64(bufsize))
		o := int(offset % int64(bufsize))
		n := min(bufsize-o, count)
		qpath := int(d.qid.Path)
		p1 := d.getblk1(addr, tfile)
		if p1 == nil {
			return rerr(efull)
		}
		if p1.checktag(tfile, qpath) != 0 {
			p1.put()
			return rerr(ealloc)
		}
		copy(p1.data[o:], t.Data[nwrite:nwrite+n])
		p1.flags |= bmod
		p1.put()
		count -= n
		nwrite += n
		offset += int64(n)
	}
	d.put()
	return &styx.Fcall{Type: styx.Rwrite, Count: uint32(nwrite)}
}

func doremove(f *file, iscon bool) string {
	if isro() || !iscon && writegroup != 0 && !ingroup(f.uid, writegroup) {
		return eronly
	}
	// check permission on the parent directory
	if f.wpath == nil || f.wpath.addr == f.addr {
		return ephase
	}
	d1, e := getdentrya(f.wpath.addr, f.wpath.slot, qpnone, bread)
	if e != "" {
		return e
	}
	if !iscon && f.access(d1, dwrite) {
		d1.put()
		return eaccess
	}
	d1.access(fwrite, f.uid)
	d1.put()
	d, e := getdentryf(f, bread)
	if e != "" {
		return e
	}
	// a directory must be empty
	if d.mode&ddir != 0 {
		for addr := 0; ; addr++ {
			p1 := d.getblk(addr, 0)
			if p1 == nil {
				break
			}
			if p1.checktag(tdir, int(d.qid.Path)) != 0 {
				p1.put()
				d.put()
				return ephase
			}
			for slot := 0; slot < dirperbuf; slot++ {
				if getdentry(p1, slot).mode&dalloc != 0 {
					p1.put()
					d.put()
					return eempty
				}
			}
			p1.put()
		}
	}
	d.trunc(f.uid)
	copy(d.buf, emptyblock[:dentrysize])
	d.put()
	return ""
}

func clunk(cp *channel, f *file, remove, wok bool) string {
	if tl := f.tlock; tl != nil {
		if tl.file == f {
			tl.time = 0 // free the lock
		}
		f.tlock = nil
	}
	var e string
	if remove {
		e = doremove(f, wok)
	}
	f.open = 0
	f.wpath = nil
	cp.putfid(f)
	return e
}

func rclunk(cp *channel, t *styx.Fcall) *styx.Fcall {
	f := cp.getfid(t.Fid, false)
	if f == nil {
		return rerr(efid)
	}
	clunk(cp, f, f.open&fremov != 0, false)
	return &styx.Fcall{Type: styx.Rclunk}
}

func rremove(cp *channel, t *styx.Fcall) *styx.Fcall {
	f := cp.getfid(t.Fid, false)
	if f == nil {
		return rerr(efid)
	}
	if e := clunk(cp, f, true, cp.cons); e != "" {
		return rerr(e)
	}
	return &styx.Fcall{Type: styx.Rremove}
}

func rstat(cp *channel, t *styx.Fcall) *styx.Fcall {
	f := cp.getfid(t.Fid, false)
	if f == nil {
		return rerr(efid)
	}
	d, e := getdentryf(f, bread)
	if d == nil {
		return rerr(e)
	}
	dir := dir9p2(d)
	if d.qid.Path == qproot { // stat of root gives time
		dir.Atime = uint32(now())
	}
	d.put()
	b := styx.MarshalDir(nil, &dir)
	if len(b) > int(cp.msize-styx.IOHDRSZ) {
		return rerr(ersc)
	}
	return &styx.Fcall{Type: styx.Rstat, Stat: b}
}

func rwstat(cp *channel, t *styx.Fcall) *styx.Fcall {
	f := cp.getfid(t.Fid, false)
	if f == nil {
		return rerr(efid)
	}
	// none can do nothing unless in allow mode
	if f.uid == none && !wstatallow {
		return rerr(eaccess)
	}
	if isro() || writegroup != 0 && !ingroup(f.uid, writegroup) {
		return rerr(eronly)
	}
	dir, err := styx.UnmarshalDir(t.Stat)
	if err != nil {
		return rerr("bad stat")
	}
	// first the parent
	var p1 *iobuf
	var d1 *dentry
	fail := func(s string) *styx.Fcall {
		if p1 != nil {
			p1.put()
		}
		return rerr(s)
	}
	if f.wpath != nil {
		if p1 = getbuf(f.wpath.addr, bread); p1 == nil {
			return rerr(ephase)
		}
		d1 = getdentry(p1, f.wpath.slot)
		if d1 == nil || p1.checktag(tdir, qpnone) != 0 || d1.mode&dalloc == 0 {
			return fail(ephase)
		}
	}
	// now the file
	d, e := getdentryf(f, bread)
	if d == nil {
		return fail(e)
	}
	uid, gid := d.uid, d.gid
	if dir.UID != "" {
		uid = strtouid(dir.UID)
	}
	if dir.GID != "" {
		gid = strtouid(dir.GID)
	}
	if dir.Name == "" {
		dir.Name = d.name
	} else {
		l := checkname9p2(dir.Name)
		if l == 0 {
			d.put()
			return fail(ename)
		}
		if l+1 > namelen {
			d.put()
			return fail(etoolong)
		}
	}
	// The new mode: from the old entry when type and mode are both
	// defaults; from mode when type is default; from type and the old
	// mode when mode is default; else mode, which must agree with type.
	switch {
	case dir.Qid.Type == 0xff && dir.Mode == ^uint32(0):
		dir.Mode = uint32(d.mode & 0o777)
		if d.mode&dlock != 0 {
			dir.Mode |= styx.DMEXCL
		}
		if d.mode&dapnd != 0 {
			dir.Mode |= styx.DMAPPEND
		}
		if d.mode&ddir != 0 {
			dir.Mode |= styx.DMDIR
		}
	case dir.Qid.Type == 0xff:
	case dir.Mode == ^uint32(0):
		dir.Mode = uint32(dir.Qid.Type)<<24 | uint32(d.mode&0o777)
	case uint32(dir.Qid.Type) != dir.Mode>>24&0xff:
		d.put()
		return fail(eqidmode)
	}
	// unknown type or mode bits, or a change of the directory bit
	if dir.Mode&^(styx.DMDIR|styx.DMAPPEND|styx.DMEXCL|0o777) != 0 {
		d.put()
		return fail(enotm)
	}
	var mode uint32
	if d.mode&ddir != 0 {
		mode = styx.DMDIR
	}
	if (dir.Mode^mode)&styx.DMDIR != 0 {
		d.put()
		return fail(enotd)
	}
	if dir.Mtime == ^uint32(0) {
		dir.Mtime = uint32(d.mtime)
	}
	if dir.Length == ^uint64(0) {
		dir.Length = uint64(d.size)
	}
	// the length cannot be changed
	if dir.Length != uint64(d.size) {
		d.put()
		return fail(enotl)
	}
	// chown: only in allow mode
	if uid != d.uid && !wstatallow {
		d.put()
		return fail(enotu)
	}
	// chgrp: the owner and in the new group, or the leader of both
	if gid != d.gid && !(wstatallow || writeallow) &&
		!(d.uid == f.uid && ingroup(f.uid, gid)) &&
		!(leadgroup(f.uid, gid) && leadgroup(f.uid, d.gid)) {
		d.put()
		return fail(enotg)
	}
	// rename: write permission in the parent
	if d.name != dir.Name {
		// drop the entry, then check the new name is valid and unique
		d.put()
		if checkname9p2(dir.Name) == 0 || d1 == nil {
			return fail(ename)
		}
		if dir.Name == "." || dir.Name == ".." {
			return fail(edot)
		}
		for addr := 0; ; addr++ {
			p := d1.getblk(addr, 0)
			if p == nil {
				break
			}
			if p.checktag(tdir, int(d1.qid.Path)) != 0 {
				p.put()
				continue
			}
			for slot := 0; slot < dirperbuf; slot++ {
				dd := getdentry(p, slot)
				if dd.mode&dalloc != 0 && dir.Name == dd.name {
					p.put()
					return fail(eexist)
				}
			}
			p.put()
		}
		if d, _ = getdentryf(f, bread); d == nil {
			return fail(ephase)
		}
		if !(wstatallow || writeallow) && (d1 == nil || f.access(d1, dwrite)) {
			d.put()
			return fail(eaccess)
		}
	}
	// mode or time: the owner, or the leader of either group
	m := int(dir.Mode & 0o777)
	if dir.Mode&styx.DMAPPEND != 0 {
		m |= dapnd
	}
	if dir.Mode&styx.DMEXCL != 0 {
		m |= dlock
	}
	if (d.mtime != int(dir.Mtime) || (d.mode^m)&(dapnd|dlock|0o777) != 0) &&
		!wstatallow && d.uid != f.uid && !leadgroup(f.uid, gid) && !leadgroup(f.uid, d.gid) {
		d.put()
		return fail(enotu)
	}
	d.mtime = int(dir.Mtime)
	d.uid = uid
	d.gid = gid
	d.mode = m&(dapnd|dlock|0o777) | d.mode&(dalloc|ddir)
	d.name = dir.Name
	d.access(fwstat, f.uid)
	d.change(^0)
	d.put()
	if p1 != nil {
		p1.put()
	}
	return &styx.Fcall{Type: styx.Rwstat}
}

// tlocked takes the exclusive-use lock of d's file, or returns nil if
// another holds it.
func tlocked(d *dentry) *tlock {
	tim := now()
	path := int(d.qid.Path)
	var t1 *tlock
	for _, t := range tlocks {
		if t.qpath == path && t.time >= tim {
			return nil // it's locked
		}
		if t.file == nil || t1 == nil && t.time < tim {
			t1 = t
		}
	}
	t := t1
	if t == nil {
		t = &tlock{}
		tlocks = append(tlocks, t)
	}
	t.qpath = path
	t.time = tim + tlockt
	return t
}
