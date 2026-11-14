// Command 9660srv serves an ISO 9660 CD-ROM image read-only (Inferno
// 9660srv, see dossrv(4)). It understands plain ISO 9660, High Sierra,
// Joliet, Rock Ridge (PX modes and NM names) and the Plan 9 directory
// extensions.
//
// A guest is single-threaded and mount waits for the server's answers, so
// the server runs as a second prog of 9660srv (hidden first argument
// "@serve") on one end of a pipe, and the first mounts the other end.
// With -s the server talks on standard output and standard input is
// mounted instead, as in 9660srv.b.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	serveFlag  = "@serve"
	sectorsize = 2048
	maxname    = 256

	enonexist = "file does not exist"
	eperm     = "permission denied"
	ebadfid   = "invalid fid"
	efidinuse = "fid already in use"
	enotdir   = "not a directory"
	esyntax   = "file name syntax"

	maxfdata = 8192
	maxrpc   = styx.IOHDRSZ + maxfdata

	voldesc = 16 // sector number of the first volume descriptor
)

var (
	devname  string
	chatty   bool
	progname = "9660srv"
	noplan9  bool
	nojoliet bool
	norock   bool
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: %s [-rabc] [-9JR] [-s] cd_device dir\n", progname)
	sys.Exits("fail:usage")
}

func fatal(p string) {
	os.Stderr.WriteString("9660srv: " + p + "\n")
	sys.Exits("fail:error")
}

func main() {
	server := len(os.Args) > 1 && os.Args[1] == serveFlag
	args := os.Args
	if server {
		args = append([]string{args[0]}, args[2:]...)
	}
	if len(args) > 0 {
		progname = args[0]
	}
	mountopt := sys.MREPL
	copt := 0
	stdio := false
	var opts []string
	a := arg.New(args)
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'v', 'D':
			chatty = true
			opts = append(opts, "-D")
		case 'r':
			mountopt = sys.MREPL
		case 'a':
			mountopt = sys.MAFTER
		case 'b':
			mountopt = sys.MBEFORE
		case 'c':
			copt = sys.MCREATE
		case 's':
			stdio = true
		case '9':
			noplan9 = true
			opts = append(opts, "-9")
		case 'J':
			nojoliet = true
			opts = append(opts, "-J")
		case 'R':
			norock = true
			opts = append(opts, "-R")
		default:
			usage()
		}
	}
	argv := a.Argv()
	if len(argv) < 2 {
		usage()
	}
	devname = argv[0]
	mountpt := argv[1]
	if server {
		fileserve(os.NewFile(0, "styx"))
		return
	}
	var mfd, sfd int
	if stdio {
		mfd, sfd = 0, 1
	} else {
		p, err := sys.Pipe()
		if err != nil {
			fatal("can't create pipe: " + sys.Errstr())
		}
		mfd, sfd = p[0], p[1]
	}
	sargv := append([]string{progname, serveFlag}, opts...)
	sargv = append(sargv, "--", devname, mountpt)
	if _, err := sys.Spawn("/bin/9660srv.wasm", sargv, sys.NEWFD|sys.FORKNS,
		[][2]int{{0, sfd}, {2, 2}}); err != nil {
		fatal("can't start server: " + sys.Errstr())
	}
	if !stdio {
		sys.Close(sfd)
	}
	if err := sys.Mount(mfd, -1, mountpt, mountopt|copt, ""); err != nil {
		fmt.Fprintf(os.Stderr, "%s: mount %s %s failed: %s\n", progname, devname, mountpt, sys.Errstr())
	}
}

func chat(s string) {
	if chatty {
		os.Stderr.WriteString(s)
	}
}

func fileserve(conn *os.File) {
	srv := styxservers.New(conn, nil, 0)
	for {
		t, err := srv.Next()
		if err != nil {
			if err != io.EOF {
				fatal("mount read error: " + err.Error())
			}
			chat("server end of file\n")
			return
		}
		chat(styxservers.Text(t) + "...")
		var r *styx.Fcall
		e := ""
		switch t.Type {
		case styx.Tversion:
			msize, v := styxservers.Compatible(t, maxrpc, styx.Version)
			srv.Msize = msize
			r = &styx.Fcall{Type: styx.Rversion, Msize: msize, Version: v}
		case styx.Tauth:
			e = "authentication not required"
		case styx.Tflush:
			r = &styx.Fcall{Type: styx.Rflush}
		case styx.Tattach:
			r, e = rattach(t)
		case styx.Twalk:
			r, e = rwalk(t)
		case styx.Topen:
			r, e = ropen(t)
		case styx.Tcreate:
			e = rcreate(t)
		case styx.Tread:
			r, e = rread(t)
		case styx.Twrite, styx.Twstat:
			e = eperm
		case styx.Tclunk:
			r, e = rclunk(t)
		case styx.Tremove:
			if _, e = rclunk(t); e == "" {
				e = eperm
			}
		case styx.Tstat:
			r, e = rstat(t)
		default:
			fatal(fmt.Sprintf("invalid T-message tag: %d", t.Type))
		}
		if e != "" {
			r = &styx.Fcall{Type: styx.Rerror, Ename: e}
		}
		r.Tag = t.Tag
		chat(styxservers.Text(r) + "\n")
		if err := srv.Reply(r); err != nil {
			fatal("connection write error: " + sys.Errstr())
		}
	}
}

const (
	oread = 1 << iota
	owrite
	orclose
	omodes = 3
)

type xfs struct {
	d       *os.File
	issusp  bool
	suspoff int
	isplan9 bool
	isrock  bool
	rootqid styx.Qid
}

type xfile struct {
	xf    *xfs
	flags int
	qid   styx.Qid
	ptr   *isofile
}

// drec is a directory record.
type drec struct {
	reclen   int
	attrlen  int
	addr     int
	size     int
	time     uint32
	flags    int
	unitsize int
	gapsize  int
	vseqno   int
	name     []byte
	data     []byte // system use area
}

type isofile struct {
	fmt     byte // 'z' ISO, 'r' High Sierra, 'J' Joliet, '9' Plan 9
	blksize int
	offset  int // byte offset reading the directory
	doffset int // Styx offset reading the directory
	d       *drec
}

var fids = map[uint32]*xfile{}

func findfid(fid uint32) *xfile {
	f := fids[fid]
	if f == nil {
		chat("unassigned fid")
	}
	return f
}

func cleanfid(fid uint32) *xfile {
	if fids[fid] != nil {
		chat("fid in use")
		return nil
	}
	f := &xfile{}
	fids[fid] = f
	return f
}

func clunkfid(fid uint32) { delete(fids, fid) }

func iso(f *xfile) *isofile {
	if f.ptr == nil {
		f.ptr = &isofile{d: &drec{}}
	}
	return f.ptr
}

func rattach(t *styx.Fcall) (*styx.Fcall, string) {
	dname := devname
	if t.Aname != "" {
		dname = t.Aname
	}
	fd, err := sys.Open(dname, sys.OREAD)
	if err != nil {
		return nil, dname + ": can't open: " + sys.Errstr()
	}
	xf := &xfs{d: os.NewFile(uintptr(fd), dname), rootqid: styx.Qid{Type: styx.QTDIR}}
	root := cleanfid(t.Fid)
	if root == nil {
		xf.d.Close()
		return nil, efidinuse
	}
	root.qid = styx.Qid{Type: styx.QTDIR}
	root.xf = xf
	if e := attach(root); e != "" {
		clunkfid(t.Fid)
		xf.d.Close()
		return nil, e
	}
	xf.rootqid = root.qid
	return &styx.Fcall{Type: styx.Rattach, Qid: root.qid}, ""
}

func walk1(f *xfile, name string) string {
	if f.qid.Type&styx.QTDIR == 0 {
		return enotdir
	}
	switch name {
	case ".":
		return ""
	case "..":
		if f.qid.Path == f.xf.rootqid.Path {
			return ""
		}
		return walkup(f)
	}
	return walk(f, name)
}

func (f *xfile) save() *xfile {
	s := *f
	p := *iso(f)
	s.ptr = &p
	return &s
}

func (f *xfile) restore(s *xfile) {
	f.flags = s.flags
	f.qid = s.qid
	f.ptr = s.ptr
}

func rwalk(t *styx.Fcall) (*styx.Fcall, string) {
	f := findfid(t.Fid)
	if f == nil {
		return nil, ebadfid
	}
	var nf, sf *xfile
	if t.Newfid != t.Fid {
		nf = cleanfid(t.Newfid)
		if nf == nil {
			return nil, efidinuse
		}
		*nf = *f.save()
		f = nf
	} else {
		sf = f.save()
	}
	var qids []styx.Qid
	for i, name := range t.Wname {
		if e := walk1(f, name); e != "" {
			if nf != nil {
				clunkfid(t.Newfid)
			} else {
				f.restore(sf)
			}
			if i == 0 {
				return nil, e
			}
			return &styx.Fcall{Type: styx.Rwalk, Wqid: qids}, ""
		}
		qids = append(qids, f.qid)
	}
	return &styx.Fcall{Type: styx.Rwalk, Wqid: qids}, ""
}

func openflags(mode int) int {
	flags := 0
	switch mode &^ (sys.OTRUNC | sys.ORCLOSE) {
	case sys.OREAD:
		flags = oread
	case sys.OWRITE:
		flags = owrite
	case sys.ORDWR:
		flags = oread | owrite
	}
	if mode&sys.ORCLOSE != 0 {
		flags |= orclose
	}
	return flags
}

func ropen(t *styx.Fcall) (*styx.Fcall, string) {
	f := findfid(t.Fid)
	if f == nil {
		return nil, ebadfid
	}
	if f.flags&omodes != 0 {
		return nil, "open on open file"
	}
	if t.Mode != sys.OREAD {
		return nil, eperm
	}
	ip := iso(f)
	ip.offset = 0
	ip.doffset = 0
	f.flags = openflags(int(t.Mode))
	return &styx.Fcall{Type: styx.Ropen, Qid: f.qid, Iounit: maxfdata}, ""
}

func rcreate(t *styx.Fcall) string {
	if t.Name == "." || t.Name == ".." {
		return esyntax
	}
	f := findfid(t.Fid)
	switch {
	case f == nil:
		return ebadfid
	case f.flags&omodes != 0:
		return "create on open file"
	case f.qid.Type&styx.QTDIR == 0:
		return "create in non-directory"
	}
	return eperm
}

func rread(t *styx.Fcall) (*styx.Fcall, string) {
	f := findfid(t.Fid)
	if f == nil {
		return nil, ebadfid
	}
	if f.flags&oread == 0 {
		return nil, "file not opened for reading"
	}
	if int64(t.Offset) < 0 {
		return nil, "negative offset or count"
	}
	count := min(int(t.Count), maxfdata)
	if f.qid.Type&styx.QTDIR != 0 {
		return &styx.Fcall{Type: styx.Rread, Data: readdir(f, int(t.Offset), count)}, ""
	}
	b, e := read(f, int(t.Offset), count)
	if e != "" {
		return nil, e
	}
	return &styx.Fcall{Type: styx.Rread, Data: b}, ""
}

func rclunk(t *styx.Fcall) (*styx.Fcall, string) {
	if findfid(t.Fid) == nil {
		return nil, ebadfid
	}
	clunkfid(t.Fid)
	return &styx.Fcall{Type: styx.Rclunk}, ""
}

func rstat(t *styx.Fcall) (*styx.Fcall, string) {
	f := findfid(t.Fid)
	if f == nil {
		return nil, ebadfid
	}
	ip := iso(f)
	d := &styx.Dir{}
	rzdir(f.xf, d, ip.fmt, ip.d)
	d.Qid.Vers = f.qid.Vers
	if d.Qid.Path == f.xf.rootqid.Path {
		d.Qid.Path = 0
		d.Qid.Type = styx.QTDIR
	}
	return &styx.Fcall{Type: styx.Rstat, Stat: styx.MarshalDir(nil, d)}, ""
}

// getblock reads sector addr of the device.
func getblock(xf *xfs, addr int) []byte {
	if addr < 0 {
		return nil
	}
	b := make([]byte, sectorsize)
	if n, _ := xf.d.ReadAt(b, int64(addr)*sectorsize); n != sectorsize {
		return nil
	}
	return b
}

func attach(root *xfile) string {
	var format byte
	blksize := 0
	haveplan9 := false
	dp := &drec{}
scan:
	for a := voldesc; a < voldesc+100; a++ {
		v := getblock(root.xf, a)
		if v == nil {
			return "can't read volume descriptor"
		}
		switch {
		case string(v[0:7]) == "\x01CD001\x01": // ISO primary
			format = 'z'
			convM2Drec(v[156:], dp, false)
			blksize = l16(v[128:])
			chat(fmt.Sprintf("iso, blksize=%d...", blksize))
			haveplan9 = string(v[8:8+6]) == "PLAN 9"
			if haveplan9 {
				if noplan9 {
					chat("ignoring plan9")
					haveplan9 = false
				} else {
					format = '9'
					chat("plan9 iso...")
				}
			}
		case string(v[8:8+7]) == "\x01CDROM\x01": // High Sierra
			format = 'r'
			convM2Drec(v[180:], dp, true)
			blksize = l16(v[136:])
			chat(fmt.Sprintf("high sierra, blksize=%d...", blksize))
		case !haveplan9 && !nojoliet && string(v[0:7]) == "\x02CD001\x01":
			q := v[88:]
			if q[0] == 0x25 && q[1] == 0x2F && (q[2] == 0x40 || q[2] == 0x43 || q[2] == 0x45) {
				format = 'J'
				convM2Drec(v[156:], dp, false)
				if blksize != l16(v[128:]) {
					fmt.Fprintf(os.Stderr, "9660srv: warning: suspicious Joliet block size: %d\n", l16(v[128:]))
				}
				chat("joliet...")
			}
		case v[0] == 0xFF:
			break scan
		}
	}
	if format == 0 {
		return "CD format not recognised"
	}
	if blksize > sectorsize {
		return "blocksize too big"
	}
	root.xf.isplan9 = haveplan9
	root.ptr = &isofile{fmt: format, blksize: blksize, d: dp}
	root.qid = styx.Qid{Path: uint64(dp.addr), Type: styx.QTDIR}
	d := &drec{}
	if getdrec(root, d) {
		s := d.data
		n := len(s)
		if n >= 7 && s[0] == 'S' && s[1] == 'P' && s[2] == 7 && s[3] == 1 && s[4] == 0xBE && s[5] == 0xEF {
			root.xf.issusp = true
			root.xf.suspoff = int(s[6])
			n -= root.xf.suspoff
			s = s[min(root.xf.suspoff, len(s)):]
		susp:
			for n >= 4 && len(s) >= 4 {
				l := int(s[2])
				switch {
				case s[0] == 'E' && s[1] == 'R':
					if len(s) >= 18 && s[4] == 10 && string(s[8:18]) == "RRIP_1991A" {
						root.xf.isrock = true
					}
					break susp
				case s[0] == 'C' && s[1] == 'E' && s[2] >= 28:
					s, n = getcontin(root.xf, s)
					continue
				case s[0] == 'R' && s[1] == 'R':
					if !norock {
						root.xf.isrock = true
					}
					break susp
				case s[0] == 'S' && s[1] == 'T':
					break susp
				}
				if l < 4 || l > len(s) {
					break
				}
				s = s[l:]
				n -= l
			}
		}
	}
	if root.xf.isrock {
		chat("Rock Ridge...")
	}
	root.ptr.offset = 0
	root.ptr.doffset = 0
	return ""
}

func walkup(f *xfile) string {
	pf := &xfile{}
	ppf := &xfile{}
	if e := opendotdot(f, pf); e != "" {
		return "can't open pf: " + e
	}
	paddr := iso(pf).d.addr
	if iso(f).d.addr == paddr {
		return ""
	}
	if e := opendotdot(pf, ppf); e != "" {
		return "can't open ppf: " + e
	}
	for {
		d := &drec{}
		if !getdrec(ppf, d) {
			break
		}
		if d.addr == paddr {
			newdrec(f, d)
			f.qid = styx.Qid{Path: uint64(paddr), Type: styx.QTDIR}
			return ""
		}
	}
	return "can't find addr of .."
}

func walk(f *xfile, name string) string {
	ip := iso(f)
	if !f.xf.isplan9 {
		i := strings.IndexByte(name, ';')
		if i < 0 {
			i = len(name)
		}
		if i >= maxname {
			i = maxname - 1
		}
		name = name[:i]
	}
	if chatty {
		chat(fmt.Sprintf("%d \"%s\"...", len(name), name))
	}
	ip.offset = 0
	for {
		d := &drec{}
		if !getdrec(f, d) {
			break
		}
		dir := &styx.Dir{}
		rzdir(f.xf, dir, ip.fmt, d)
		if name != dir.Name {
			continue
		}
		newdrec(f, d)
		f.qid = dir.Qid
		return ""
	}
	return enonexist
}

func readdir(f *xfile, offset, count int) []byte {
	ip := iso(f)
	if offset < ip.doffset {
		ip.offset = 0
		ip.doffset = 0
	}
	var buf []byte
	for len(buf) < count {
		dr := &drec{}
		if !getdrec(f, dr) {
			break
		}
		if len(dr.name) == 1 && (dr.name[0] == 0 || dr.name[0] == 1) {
			continue
		}
		d := &styx.Dir{}
		rzdir(f.xf, d, ip.fmt, dr)
		d.Qid.Vers = f.qid.Vers
		a := styx.MarshalDir(nil, d)
		if ip.doffset < offset {
			ip.doffset += len(a)
			continue
		}
		if len(buf)+len(a) > count {
			// 9660srv.b loses this entry; back up so the next read gets it.
			ip.offset -= dr.reclen + dr.reclen&1
			break
		}
		buf = append(buf, a...)
	}
	ip.doffset += len(buf)
	return buf
}

func read(f *xfile, offset, count int) ([]byte, string) {
	ip := iso(f)
	if offset >= ip.d.size {
		return nil, ""
	}
	if offset+count > ip.d.size {
		count = ip.d.size - offset
	}
	addr := (ip.d.addr+ip.d.attrlen)*ip.blksize + offset
	o := addr % sectorsize
	addr /= sectorsize
	if chatty {
		chat(fmt.Sprintf("d.addr=0x%x, addr=0x%x, o=0x%x...", ip.d.addr, addr, o))
	}
	n := sectorsize - o
	var buf []byte
	for count > 0 {
		n = min(n, count)
		p := getblock(f.xf, addr)
		if p == nil {
			return nil, "i/o error"
		}
		buf = append(buf, p[o:o+n]...)
		count -= n
		addr++
		o = 0
		n = sectorsize
	}
	return buf, ""
}

func newdrec(f *xfile, dp *drec) {
	x := iso(f)
	f.ptr = &isofile{fmt: x.fmt, blksize: x.blksize, d: dp}
}

// getdrec reads the next directory record of f into d.
func getdrec(f *xfile, d *drec) bool {
	if f.ptr == nil {
		return false
	}
	ip := f.ptr
	for ip.offset < ip.d.size {
		addr := (ip.d.addr+ip.d.attrlen)*ip.blksize + ip.offset
		boff := addr % sectorsize
		if boff > sectorsize-34 {
			ip.offset += sectorsize - boff
			continue
		}
		p := getblock(f.xf, addr/sectorsize)
		if p == nil {
			return false
		}
		if nb := int(p[boff]); nb >= 34 {
			convM2Drec(p[boff:], d, ip.fmt == 'r')
			ip.offset += nb + nb&1
			return true
		}
		ip.offset += sectorsize - boff
	}
	return false
}

func getcontin(xf *xfs, a []byte) ([]byte, int) {
	bn := l32(a[4:])
	off := l32(a[12:])
	n := l32(a[20:])
	p := getblock(xf, bn)
	if p == nil || off < 0 || n < 0 || off+n > len(p) {
		return nil, 0
	}
	return p[off : off+n], n
}

func opendotdot(f, pf *xfile) string {
	d := &drec{}
	ip := iso(f)
	ip.offset = 0
	if !getdrec(f, d) {
		return "opendotdot: getdrec(.) failed"
	}
	if len(d.name) != 1 || d.name[0] != 0 {
		return "opendotdot: no . entry"
	}
	if d.addr != ip.d.addr {
		return "opendotdot: bad . address"
	}
	d = &drec{}
	if !getdrec(f, d) {
		return "opendotdot: getdrec(..) failed"
	}
	if len(d.name) != 1 || d.name[0] != 1 {
		return "opendotdot: no .. entry"
	}
	pf.xf = f.xf
	pf.ptr = &isofile{fmt: ip.fmt, blksize: ip.blksize, d: d}
	return ""
}

func rzdir(fs *xfs, d *styx.Dir, format byte, dp *drec) {
	const (
		hmode = 1 << iota
		hname
	)
	have := 0
	d.Qid = styx.Qid{Path: uint64(dp.addr), Type: styx.QTFILE}
	n := len(dp.name)
	switch {
	case n == 1 && dp.name[0] == 0:
		d.Name = "."
		have |= hname
	case n == 1 && dp.name[0] == 1:
		d.Name = ".."
		have |= hname
	case n == 1:
		d.Name = string(rune(tolower(dp.name[0])))
	case format == 'J':
		var r []rune
		for i := 0; i+1 < n; i += 2 {
			r = append(r, rune(dp.name[i])<<8|rune(dp.name[i+1]))
		}
		d.Name = string(r)
	default:
		if n >= maxname {
			n = maxname - 1
		}
		var r []rune
		for i := 0; i < n && dp.name[i] != '\r'; i++ {
			r = append(r, rune(tolower(dp.name[i])))
		}
		d.Name = string(r)
	}
	if fs.isplan9 && dp.reclen > 34+len(dp.name) && plan9ext(d, dp.data) {
		// mode, owner, group and perhaps name came from the extension
	} else {
		d.Mode = 0o444
		switch format {
		case 'z':
			if fs.isrock {
				d.GID = "ridge"
			} else {
				d.GID = "iso"
			}
		case 'r':
			d.GID = "sierra"
		case 'J':
			d.GID = "joliet"
		default:
			d.GID = "???"
		}
		if dp.flags&2 != 0 {
			d.Qid.Type = styx.QTDIR
			d.Mode |= styx.DMDIR | 0o111
		}
		d.UID = "cdrom"
		if i := strings.IndexByte(d.Name, ';'); i >= 0 {
			d.Name = d.Name[:i]
		}
		n = len(dp.data) - fs.suspoff
		if fs.isrock && n >= 4 {
			s := dp.data[fs.suspoff:]
			nm := false
			for n >= 4 && len(s) >= 4 && have != hname|hmode {
				l := int(s[2])
				if l < 4 || l > len(s) {
					break
				}
				switch {
				case s[0] == 'P' && s[1] == 'X' && s[3] == 1 && l >= 12:
					mode := uint32(l32(s[4:12]))
					d.Mode = mode & 0o777
					if mode&0o170000 == 0o040000 {
						d.Mode |= styx.DMDIR
						d.Qid.Type = styx.QTDIR
					}
					have |= hmode
				case s[0] == 'N' && s[1] == 'M' && s[3] == 1 && l >= 5:
					if flags := int(s[4]); flags&^1 == 0 {
						if !nm {
							d.Name = string(s[5:l])
							nm = true
						} else {
							d.Name += string(s[5:l])
						}
						if flags == 0 {
							have |= hname
						}
					}
				case s[0] == 'C' && s[1] == 'E' && s[2] >= 28:
					s, n = getcontin(fs, s)
					continue
				case s[0] == 'S' && s[1] == 'T':
					n = 0
					continue
				}
				n -= l
				s = s[l:]
			}
		}
	}
	d.Length = 0
	if d.Mode&styx.DMDIR == 0 {
		d.Length = uint64(dp.size)
	}
	d.Atime = dp.time
	d.Mtime = dp.time
}

// plan9ext takes name, owner, group and mode from a Plan 9 system use
// area, and reports whether it was well formed.
func plan9ext(d *styx.Dir, s []byte) bool {
	get := func(l int) (string, int, bool) {
		if l >= len(s) || l+1+int(s[l]) > len(s) {
			return "", l, false
		}
		n := int(s[l])
		return string(s[l+1 : l+1+n]), l + 1 + n, true
	}
	name, l, ok1 := get(0)
	uid, l, ok2 := get(l)
	gid, l, ok3 := get(l)
	if l&1 != 0 {
		l++
	}
	if !ok1 || !ok2 || !ok3 || l+4 > len(s) {
		return false
	}
	if name != "" {
		d.Name = name
	}
	d.UID, d.GID = uid, gid
	d.Mode = uint32(l32(s[l:]))
	if d.Mode&styx.DMDIR != 0 {
		d.Qid.Type = styx.QTDIR
	}
	return true
}

func convM2Drec(a []byte, d *drec, highsierra bool) {
	d.reclen = int(a[0])
	d.attrlen = int(a[1])
	d.addr = l32(a[2:10])
	d.size = l32(a[10:18])
	d.time = gtime(a[18:24])
	if highsierra {
		d.flags = int(a[24])
		d.unitsize, d.gapsize, d.vseqno = 0, 0, 0
	} else {
		d.flags = int(a[25])
		d.unitsize = int(a[26])
		d.gapsize = int(a[27])
		d.vseqno = l32(a[28:32])
	}
	n := int(a[32])
	d.name = append([]byte(nil), a[33:33+n]...)
	n += 33
	if n&1 != 0 {
		n++
	}
	d.data = nil
	if syslen := d.reclen - n; syslen > 0 && n+syslen <= len(a) {
		d.data = append([]byte(nil), a[n:n+syslen]...)
	}
}

var dmsize = [12]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

func dysize(y int) int {
	if y%4 == 0 {
		return 366
	}
	return 365
}

// gtime converts a recording date (years since 1900, month, day, hour,
// minute, second) to seconds since the epoch, ignoring the zone, as
// 9660srv.b does.
func gtime(p []byte) uint32 {
	y, mo, d := int(p[0]), int(p[1]), int(p[2])
	h, m, s := int(p[3]), int(p[4]), int(p[5])
	if y < 70 || mo < 1 || mo > 12 || d < 1 || d > dmsize[mo-1] || h > 23 || m > 59 || s > 59 {
		return 0
	}
	y += 1900
	t := 0
	for i := 1970; i < y; i++ {
		t += dysize(i)
	}
	if dysize(y) == 366 && mo >= 3 {
		t++
	}
	for mo--; mo > 0; {
		mo--
		t += dmsize[mo]
	}
	t += d - 1
	t = 24*t + h
	t = 60*t + m
	t = 60*t + s
	return uint32(t)
}

func l16(p []byte) int {
	v := int(p[1])<<8 | int(p[0])
	if v >= 0x8000 {
		v -= 0x10000
	}
	return v
}

func l32(p []byte) int {
	return int(int32(uint32(p[3])<<24 | uint32(p[2])<<16 | uint32(p[1])<<8 | uint32(p[0])))
}

func tolower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c - 'A' + 'a'
	}
	return c
}
