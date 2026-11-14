// Command dossrv serves a DOS/Windows FAT12 or FAT16 file system held in
// a file or device, read and write, with long file names (Inferno
// dossrv(4)). It serves Styx on a pipe and mounts the other end on the
// mount point (/n/dos by default); with -s it serves its standard output
// instead.
//
// A guest is single-threaded and mount needs answers from the server
// before it returns, so dossrv runs the server as a second prog, dossrv
// -s with the pipe as its standard output, then mounts and exits.
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

const (
	verbose = 1 << iota
	styxMess
	fatInfo
	clusterInfo
)

var (
	arg0    = "dossrv"
	deffile string
	pflag   bool
	debug   int
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: %s [-v] [-s] [-F] [-c] [-S secpertrack] [-f devicefile] [-m mountpoint]\n", arg0)
	sys.Exits("fail:usage")
}

func main() {
	srvfile := "/n/dos"
	sectors := 0
	stdin := false
	a := arg.New(os.Args)
	if p := a.Progname(); p != "" {
		arg0 = p
	}
	var flags []string // passed on to the server
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'v':
			if debug&styxMess != 0 {
				debug |= verbose
			}
			debug |= styxMess
			flags = append(flags, "-v")
		case 'F':
			debug |= fatInfo
			flags = append(flags, "-F")
		case 'c':
			debug |= clusterInfo
			iodebug = true
			flags = append(flags, "-c")
		case 'S':
			s := a.Arg()
			if s == "" || s[0] < '0' || s[0] > '9' {
				usage()
			}
			sectors = atoi(s)
		case 's':
			stdin = true
		case 'f':
			deffile = a.Arg()
			if deffile == "" {
				usage()
			}
		case 'm':
			srvfile = a.Arg()
			if srvfile == "" {
				usage()
			}
		case 'p':
			pflag = true
			flags = append(flags, "-p")
		default:
			usage()
		}
	}
	a.Argv()
	if deffile == "" || !stdin && srvfile == "" {
		usage()
	}
	if stdin {
		iotrackinit(sectors)
		dossetup()
		serve(os.NewFile(1, "styx"))
		return
	}
	p, err := sys.Pipe()
	if err != nil {
		fatal("can't create pipe: " + sys.Errstr())
	}
	args := append([]string{arg0, "-s", "-S", strconv.Itoa(sectors), "-f", deffile}, flags...)
	if _, err := sys.Spawn("/bin/dossrv.wasm", args, sys.NEWFD, [][2]int{{1, p[1]}, {2, 2}}); err != nil {
		fatal("can't start server: " + sys.Errstr())
	}
	sys.Close(p[1])
	if err := sys.Mount(p[0], -1, srvfile, sys.MREPL|sys.MCREATE, deffile); err != nil {
		fatal("mount " + srvfile + ": " + sys.Errstr())
	}
}

func fatal(e string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", arg0, e)
	sys.Exits("fail:error")
}

func chat(s string) {
	if debug != 0 {
		os.Stderr.WriteString(s)
	}
}

func panicf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "dosfs: panic: %s\n", fmt.Sprintf(format, a...))
	if pflag {
		select {} // hang here, for debugging
	}
	sys.Exits("fail:panic")
}

// The Styx server.

const (
	enevermind = iota
	eformat
	eio
	enomem
	enonexist
	enotdir
	enofid
	efidopen
	efidinuse
	eexist
	eperm
	enofilsys
	eauth
	econtig
	efull
	eopen
	ephase
)

var errmsg = []string{
	enevermind: "never mind",
	eformat:    "unknown format",
	eio:        "I/O error",
	enomem:     "server out of memory",
	enonexist:  "file does not exist",
	enotdir:    "not a directory",
	enofid:     "no such fid",
	efidopen:   "fid already open",
	efidinuse:  "fid in use",
	eexist:     "file exists",
	eperm:      "permission denied",
	enofilsys:  "no file system device specified",
	eauth:      "authentication failed",
	econtig:    "out of contiguous disk space",
	efull:      "file system full",
	eopen:      "invalid open mode",
	ephase:     "phase error -- directory entry not found",
}

func e(n int) *styx.Fcall {
	if n < 0 || n >= len(errmsg) {
		return &styx.Fcall{Type: styx.Rerror, Ename: "it's thermal problems"}
	}
	return &styx.Fcall{Type: styx.Rerror, Ename: errmsg[n]}
}

func rerror(s string) *styx.Fcall { return &styx.Fcall{Type: styx.Rerror, Ename: s} }

const (
	oread   = 1
	owrite  = 2
	orclose = 4
	omodes  = 3
)

// xfile is a client fid.
type xfile struct {
	fid   uint32
	flags int
	qid   styx.Qid
	xf    *xfs
	ptr   *dosptr
}

var xfiles = map[uint32]*xfile{}

// getxfile returns the fid, or nil if it is unknown or its device has
// gone away.
func getxfile(fid uint32) *xfile {
	f := xfiles[fid]
	if f != nil && f.xf != nil && f.xf.dev == nil {
		return nil
	}
	return f
}

// newxfile returns fid made fresh.
func newxfile(fid uint32) *xfile {
	if f := xfiles[fid]; f != nil {
		clean(f)
	}
	f := &xfile{fid: fid, ptr: newdosptr()}
	xfiles[fid] = f
	return f
}

func clunkxfile(fid uint32) {
	if f := xfiles[fid]; f != nil {
		delete(xfiles, fid)
		clean(f)
	}
}

func clean(f *xfile) {
	f.ptr = nil
	if f.xf != nil {
		refxfs(f.xf, -1)
		f.xf = nil
	}
	f.flags = 0
	f.qid = styx.Qid{}
}

func serve(conn *os.File) {
	srv := styxservers.New(conn, nil, 0)
	if debug&styxMess != 0 {
		srv.Trace = os.Stderr
	}
	for {
		t, err := srv.Next()
		if err != nil {
			break
		}
		var r *styx.Fcall
		switch t.Type {
		case styx.Tversion:
			msize, version := styxservers.Compatible(t, styxservers.MAXRPC, styx.Version)
			srv.Msize = msize
			r = &styx.Fcall{Type: styx.Rversion, Msize: msize, Version: version}
		case styx.Tauth:
			r = rerror("authentication not required")
		case styx.Tflush:
			r = &styx.Fcall{Type: styx.Rflush}
		case styx.Tattach:
			r = rattach(t)
		case styx.Twalk:
			r = rwalk(t)
		case styx.Topen:
			r = ropen(t)
		case styx.Tcreate:
			r = rcreate(t)
		case styx.Tread:
			r = rread(t)
		case styx.Twrite:
			r = rwrite(t)
		case styx.Tclunk:
			r = rclunk(t)
		case styx.Tremove:
			r = rremove(t)
		case styx.Tstat:
			r = rstat(t)
		case styx.Twstat:
			r = rwstat(t)
		default:
			panicf("Styx mtype")
		}
		r.Tag = t.Tag
		// Write back now, not just on clunk as dossrv.b does: the
		// emu's mount driver clunks asynchronously, so an unmount can
		// end the conversation before the last clunks arrive.
		sync()
		if srv.Reply(r) != nil {
			panicf("mount write")
		}
	}
	sync()
	if debug&styxMess != 0 {
		chat("server EOF\n")
	}
}

func rattach(t *styx.Fcall) *styx.Fcall {
	root := newxfile(t.Fid)
	aname := t.Aname
	if aname == "" {
		aname = deffile
	}
	xf, ec := getxfs(aname)
	root.xf = xf
	if xf == nil {
		clunkxfile(t.Fid)
		return rerror(ec)
	}
	if !xf.fmt && dosfs(xf) < 0 {
		clunkxfile(t.Fid)
		return e(eformat)
	}
	root.qid = styx.Qid{Type: styx.QTDIR}
	xf.rootqid = root.qid
	return &styx.Fcall{Type: styx.Rattach, Qid: root.qid}
}

func clone(ofl *xfile, newfid uint32) *xfile {
	nfl := newxfile(newfid)
	*nfl = *ofl
	nfl.fid = newfid
	refxfs(nfl.xf, 1)
	if ofl.ptr != nil {
		dp := *ofl.ptr
		dp.p = nil
		nfl.ptr = &dp
	}
	return nfl
}

func walk1(f *xfile, name string) *styx.Fcall {
	if f.qid.Type&styx.QTDIR == 0 {
		if debug != 0 {
			chat(fmt.Sprintf("qid.path=0x%x...", f.qid.Path))
		}
		return e(enotdir)
	}
	switch name {
	case ".":
		return nil
	case "..":
		if f.qid.Path == f.xf.rootqid.Path {
			return nil
		}
		r, dp := walkup(f)
		if r < 0 {
			return e(enonexist)
		}
		f.ptr = dp
		f.qid.Type = styx.QTDIR
		if dp.addr == 0 {
			f.qid.Path = f.xf.rootqid.Path
		} else {
			f.qid.Path = qidpath(dp)
		}
		return nil
	}
	if getfile(f) < 0 {
		return e(enonexist)
	}
	r, dp := searchdir(f, []rune(name), false, true)
	putfile(f)
	if r < 0 {
		return e(enonexist)
	}
	f.ptr = dp
	f.qid.Path = qidpath(dp)
	f.qid.Type = styx.QTFILE
	if dp.addr == 0 {
		f.qid.Path = f.xf.rootqid.Path
	} else if dp.entry()[11]&dDir != 0 {
		f.qid.Type = styx.QTDIR
	}
	putfile(f)
	return nil
}

func rwalk(t *styx.Fcall) *styx.Fcall {
	f := getxfile(t.Fid)
	if f == nil {
		return e(enofid)
	}
	var nf *xfile
	if t.Newfid != t.Fid {
		nf = clone(f, t.Newfid)
		f = nf
	}
	qids := []styx.Qid{}
	savedqid, savedptr := f.qid, f.ptr
	for i, name := range t.Wname {
		if r := walk1(f, name); r != nil {
			f.qid, f.ptr = savedqid, savedptr
			if nf != nil {
				clunkxfile(t.Newfid)
			}
			if i == 0 {
				return r
			}
			return &styx.Fcall{Type: styx.Rwalk, Wqid: qids}
		}
		qids = append(qids, f.qid)
	}
	return &styx.Fcall{Type: styx.Rwalk, Wqid: qids}
}

func ropen(t *styx.Fcall) *styx.Fcall {
	f := getxfile(t.Fid)
	if f == nil {
		return e(enofid)
	}
	if f.flags&omodes != 0 {
		return e(efidopen)
	}
	omode := 0
	dp := f.ptr
	if dp.paddr != 0 && t.Mode&sys.ORCLOSE != 0 {
		// The parent of a file to be removed must be writable.
		p := getsect(f.xf, dp.paddr)
		if p == nil {
			return e(eio)
		}
		attr := p.buf[dp.poffset+11]
		putsect(p)
		if attr&drOnly != 0 {
			return e(eperm)
		}
		omode |= orclose
	} else if t.Mode&sys.ORCLOSE != 0 {
		omode |= orclose
	}
	if getfile(f) < 0 {
		return e(enonexist)
	}
	var attr byte = dDir
	if dp.addr != 0 {
		attr = dp.entry()[11]
	}
	switch t.Mode & 7 {
	case sys.OREAD, sys.OEXEC:
		omode |= oread
	case sys.ORDWR, sys.OWRITE:
		if t.Mode&7 == sys.ORDWR {
			omode |= oread
		}
		omode |= owrite
		if attr&(drOnly|dDir) != 0 {
			putfile(f)
			return e(eperm)
		}
	default:
		putfile(f)
		return e(eopen)
	}
	if t.Mode&sys.OTRUNC != 0 {
		if attr&(dDir|drOnly) != 0 {
			putfile(f)
			return e(eperm)
		}
		if truncfile(f) < 0 {
			putfile(f)
			return e(eio)
		}
	}
	f.flags |= omode
	putfile(f)
	return &styx.Fcall{Type: styx.Ropen, Qid: f.qid, Iounit: styxservers.MAXFDATA}
}

// newentry returns a cleared directory entry named "." with the time
// set to now.
func newentry() []byte {
	nd := make([]byte, dosdirsize)
	copy(nd, ".          ")
	puttime(nd)
	return nd
}

func shortname(sname []rune) []rune {
	return append(append(append([]rune{}, sname[:8]...), '.'), sname[8:11]...)
}

func mkdentry(xf *xfs, ndp *dosptr, name, sname []rune, islong bool, nattr byte, start, length []byte) int {
	ndp.p = getsect(xf, ndp.addr)
	if ndp.p == nil {
		return eio
	}
	if islong {
		if r := putlongname(xf, ndp, name, sname); r < 0 {
			putsect(ndp.p)
			if r == -2 {
				return efull
			}
			return eio
		}
	}
	nd := newentry()
	nd[11] = nattr
	copy(nd[26:28], start)
	copy(nd[28:32], length)
	if islong {
		putname(shortname(sname), nd)
	} else {
		putname(name, nd)
	}
	copy(ndp.entry(), nd)
	ndp.p.flags |= bmod
	return 0
}

// newshort finds a short name for long name name that is not in use in
// directory f.
func newshort(f *xfile, name []rune) []rune {
	for i := 1; ; i++ {
		sname := long2short(name, i)
		r, dp := searchdir(f, sname, false, false)
		if r < 0 {
			return sname
		}
		putsect(dp.p)
	}
}

func rcreate(t *styx.Fcall) *styx.Fcall {
	f := getxfile(t.Fid)
	if f == nil {
		return e(enofid)
	}
	if f.flags&omodes != 0 {
		return e(efidopen)
	}
	if getfile(f) < 0 {
		return e(eio)
	}
	pdp := f.ptr
	var pd []byte
	var attr byte = dDir
	if pdp.addr != 0 {
		pd = pdp.entry()
		attr = pd[11]
	}
	if attr&dDir == 0 || attr&drOnly != 0 {
		putfile(f)
		return e(eperm)
	}
	omode := 0
	if t.Mode&sys.ORCLOSE != 0 {
		omode |= orclose
	}
	switch t.Mode & 7 {
	case sys.OREAD, sys.OEXEC:
		omode |= oread
	case sys.OWRITE, sys.ORDWR:
		if t.Mode&7 == sys.ORDWR {
			omode |= oread
		}
		omode |= owrite
		if t.Perm&styx.DMDIR != 0 {
			putfile(f)
			return e(eperm)
		}
	default:
		putfile(f)
		return e(eopen)
	}
	if t.Name == "." || t.Name == ".." {
		putfile(f)
		return e(eperm)
	}
	name := []rune(t.Name)
	r, ndp := searchdir(f, name, true, true)
	if r < 0 {
		putfile(f)
		if r == -2 {
			return e(efull)
		}
		return e(eexist)
	}
	var sname []rune
	islong := name2de(name) > 0
	if islong {
		sname = newshort(f, name)
	}
	bp := f.xf.bp
	start := 0
	if t.Perm&styx.DMDIR != 0 {
		start = falloc(f.xf)
		if start <= 0 {
			putfile(f)
			return e(efull)
		}
	}
	// Now we're committed.
	if pd != nil {
		puttime(pd)
		pdp.p.flags |= bmod
	}
	f.ptr = ndp
	ndp.p = getsect(f.xf, ndp.addr)
	if ndp.p == nil || islong && putlongname(f.xf, ndp, name, sname) < 0 {
		putsect(pdp.p)
		if ndp.p != nil {
			putsect(ndp.p)
		}
		return e(eio)
	}
	nd := newentry()
	if t.Perm&0o222 == 0 {
		nd[11] |= drOnly
	}
	putstart(nd, start)
	if islong {
		putname(shortname(sname), nd)
	} else {
		putname(name, nd)
	}
	f.qid.Path = qidpath(ndp)
	if t.Perm&styx.DMDIR != 0 {
		nd[11] |= dDir
		f.qid.Type |= styx.QTDIR
		xp := getsect(f.xf, clust2sect(bp, start))
		if xp == nil {
			putfile(f)
			putsect(pdp.p)
			return e(eio)
		}
		xd := append([]byte{}, nd...)
		copy(xd, ".          ")
		copy(xp.buf, xd)
		if pd != nil {
			xd = append([]byte{}, pd...)
		} else {
			xd = newentry()
			xd[11] = dDir
		}
		copy(xd, "..         ")
		copy(xp.buf[dosdirsize:], xd)
		xp.flags |= bmod
		putsect(xp)
	} else {
		f.qid.Type = styx.QTFILE
	}
	ndp.p.flags |= bmod
	copy(ndp.entry(), nd)
	putfile(f)
	putsect(pdp.p)
	f.flags |= omode
	return &styx.Fcall{Type: styx.Rcreate, Qid: f.qid, Iounit: styxservers.MAXFDATA}
}

func rread(t *styx.Fcall) *styx.Fcall {
	f := getxfile(t.Fid)
	if f == nil || f.flags&oread == 0 {
		return e(eio)
	}
	if getfile(f) < 0 {
		return e(eio)
	}
	count := min(int(t.Count), styxservers.MAXFDATA)
	var r int
	var data []byte
	if f.qid.Type&styx.QTDIR != 0 {
		r, data = readdir(f, int(t.Offset), count)
	} else {
		r, data = readfile(f, int(t.Offset), count)
	}
	putfile(f)
	if r < 0 {
		return e(eio)
	}
	return &styx.Fcall{Type: styx.Rread, Data: data[:r]}
}

func rwrite(t *styx.Fcall) *styx.Fcall {
	f := getxfile(t.Fid)
	if f == nil || f.flags&owrite == 0 {
		return e(eio)
	}
	if getfile(f) < 0 {
		return e(eio)
	}
	r := writefile(f, t.Data, int(t.Offset), len(t.Data))
	putfile(f)
	if r < 0 {
		if r == -2 {
			return e(efull)
		}
		return e(eio)
	}
	return &styx.Fcall{Type: styx.Rwrite, Count: uint32(r)}
}

func rclunk(t *styx.Fcall) *styx.Fcall {
	clunkxfile(t.Fid)
	sync()
	return &styx.Fcall{Type: styx.Rclunk}
}

// doremove marks dp's entry and its long name entries free.
func doremove(xf *xfs, dp *dosptr) {
	b := dp.p.buf
	b[dp.offset] = dosempty
	dp.p.flags |= bmod
	prevdo := dp.offset - dosdirsize
	for ; prevdo >= 0; prevdo -= dosdirsize {
		if b[prevdo+11] != dLong {
			break
		}
		b[prevdo] = dosempty
	}
	if prevdo < 0 && dp.prevaddr != -1 {
		p := getsect(xf, dp.prevaddr)
		if p == nil {
			return
		}
		for prevdo = xf.bp.sectsize - dosdirsize; prevdo >= 0; prevdo -= dosdirsize {
			if p.buf[prevdo+11] != dLong {
				break
			}
			p.buf[prevdo] = dosempty
			p.flags |= bmod
		}
		putsect(p)
	}
}

func rremove(t *styx.Fcall) *styx.Fcall {
	f := getxfile(t.Fid)
	if f == nil {
		return e(enofid)
	}
	fail := func(n int) *styx.Fcall {
		clunkxfile(t.Fid)
		sync()
		return e(n)
	}
	if f.ptr.addr == 0 {
		if debug != 0 {
			chat("root...")
		}
		return fail(eperm)
	}
	// The parent of the file must be writable.
	parp := getsect(f.xf, f.ptr.paddr)
	if parp == nil {
		return fail(eio)
	}
	pard := parp.buf[f.ptr.poffset : f.ptr.poffset+dosdirsize]
	if f.ptr.paddr != 0 && pard[11]&drOnly != 0 {
		putsect(parp)
		return fail(eperm)
	}
	if getfile(f) < 0 {
		putsect(parp)
		return fail(eio)
	}
	dattr := f.ptr.entry()[11]
	if dattr&dDir != 0 && emptydir(f) < 0 {
		putfile(f)
		putsect(parp)
		return fail(eperm)
	}
	if f.ptr.paddr == 0 && dattr&drOnly != 0 {
		putfile(f)
		putsect(parp)
		return fail(eperm)
	}
	doremove(f.xf, f.ptr)
	if f.ptr.paddr != 0 {
		puttime(pard)
		parp.flags |= bmod
	}
	putsect(parp)
	err := 0
	if truncfile(f) < 0 {
		err = eio
	}
	putfile(f)
	clunkxfile(t.Fid)
	sync()
	if err != 0 {
		return e(err)
	}
	return &styx.Fcall{Type: styx.Rremove}
}

func rstat(t *styx.Fcall) *styx.Fcall {
	f := getxfile(t.Fid)
	if f == nil {
		return e(enofid)
	}
	if getfile(f) < 0 {
		return e(eio)
	}
	dir := dostat(f)
	putfile(f)
	return &styx.Fcall{Type: styx.Rstat, Stat: styx.MarshalDir(nil, dir)}
}

// longbefore collects the long name entries before offset o in b.
func longbefore(b []byte, o int) ([]rune, int, bool) {
	var name []rune
	islong := false
	for o -= dosdirsize; o >= 0; o -= dosdirsize {
		if b[o+11] != dLong {
			break
		}
		islong = true
		name = append(name, getnamesect(b[o:o+dosdirsize])...)
	}
	return name, o, islong
}

func dostat(f *xfile) *styx.Dir {
	dp := f.ptr
	var dir *styx.Dir
	if dp.addr == 0 {
		dir = getdir(nil, 0, 0)
	} else {
		dir = getdir(dp.entry(), dp.addr, dp.offset)
	}
	name, prevdo, islong := longbefore(dp.p.buf, dp.offset)
	if dp.addr != 0 && prevdo < 0 && dp.prevaddr != -1 {
		// The long name runs back into the previous sector.
		if p := getsect(f.xf, dp.prevaddr); p != nil {
			more, _, l := longbefore(p.buf, f.xf.bp.sectsize)
			name = append(name, more...)
			islong = islong || l
			putsect(p)
		}
	}
	if islong && dp.addr != 0 {
		dir.Name = string(name)
	}
	return dir
}

func rwstat(t *styx.Fcall) *styx.Fcall {
	f := getxfile(t.Fid)
	if f == nil {
		return e(enofid)
	}
	if getfile(f) < 0 {
		return e(eio)
	}
	dp := f.ptr
	if dp.addr == 0 {
		putfile(f)
		return e(eperm)
	}
	wdir, err := styx.UnmarshalDir(t.Stat)
	if err != nil {
		putfile(f)
		return e(eperm)
	}
	dir := dostat(f)
	// Fields left as "don't change" take the file's values.
	if wdir.UID == "" {
		wdir.UID = dir.UID
	}
	if wdir.GID == "" {
		wdir.GID = dir.GID
	}
	if wdir.Name == "" {
		wdir.Name = dir.Name
	}
	if wdir.Mode == ^uint32(0) {
		wdir.Mode = dir.Mode
	}
	if wdir.Mtime == ^uint32(0) {
		wdir.Mtime = dir.Mtime
	}
	if dir.UID != wdir.UID || dir.GID != wdir.GID {
		putfile(f)
		return e(eperm)
	}
	changes := dir.Mtime != wdir.Mtime || (dir.Mode^wdir.Mode)&0o777 != 0
	if wdir.Mode&7 != (wdir.Mode>>3)&7 || wdir.Mode&7 != (wdir.Mode>>6)&7 {
		putfile(f)
		return e(eperm)
	}
	if dir.Name != wdir.Name {
		// The parent must be writable; a read-only file in the root
		// directory cannot be renamed.
		parp := getsect(f.xf, dp.paddr)
		if parp == nil {
			putfile(f)
			return e(eio)
		}
		pardattr := parp.buf[dp.poffset+11]
		d := dp.entry()
		if dp.paddr != 0 && pardattr&drOnly != 0 || dp.paddr == 0 && d[11]&drOnly != 0 {
			putsect(parp)
			putfile(f)
			return e(eperm)
		}
		oaddr, ooffset := dp.addr, dp.offset
		start := append([]byte{}, d[26:28]...)
		length := append([]byte{}, d[28:32]...)
		attr := d[11]
		// Release the file to look in the parent, then remove the old
		// entry.
		putfile(f)
		pf := *f
		pf.ptr = &dosptr{addr: dp.paddr, offset: dp.poffset, prevaddr: -1, naddr: -1, p: parp}
		if wdir.Name == "." || wdir.Name == ".." {
			putsect(parp)
			return e(eperm)
		}
		nname := []rune(wdir.Name)
		if r, xdp := searchdir(&pf, nname, false, true); r >= 0 {
			self := xdp.addr == oaddr && xdp.offset == ooffset
			putsect(xdp.p)
			if !self {
				putsect(parp)
				return e(eexist)
			}
		}
		var sname []rune
		islong := name2de(nname) > 0
		if islong {
			sname = newshort(&pf, nname)
		} else {
			b, x := dosname(nname)
			sname = append(b, x...)
		}
		if getfile(f) < 0 {
			putsect(parp)
			return e(eio)
		}
		doremove(f.xf, dp)
		putfile(f)
		// Look for a slot again: the old one may do, and a long name
		// may need the next sector set up.
		r, ndp := searchdir(&pf, sname, true, islong)
		if r < 0 {
			putsect(parp)
			return e(ephase)
		}
		if r := mkdentry(pf.xf, ndp, nname, sname, islong, attr, start, length); r != 0 {
			putsect(parp)
			return e(r)
		}
		putsect(parp)
		// Move other fids on the same file, if it moved.
		dp = ndp
		f.ptr = dp
		f.qid.Path = qidpath(dp)
		if oaddr != dp.addr || ooffset != dp.offset {
			dosptrreloc(f, dp, oaddr, ooffset)
		}
		changes = true
	}
	if changes {
		putdir(dp.entry(), wdir.Mode, wdir.Mtime)
		dp.p.flags |= bmod
	}
	putfile(f)
	sync()
	return &styx.Fcall{Type: styx.Rwstat}
}

// dosptrreloc points the other fids on the entry at addr, offset at dp.
func dosptrreloc(f *xfile, dp *dosptr, addr, offset int) {
	for _, p := range xfiles {
		xdp := p.ptr
		if p != f && p.xf == f.xf && xdp != nil && xdp.addr == addr && xdp.offset == offset {
			*xdp = *dp
			xdp.p = nil
			p.qid.Path = qidpath(xdp)
		}
	}
}
