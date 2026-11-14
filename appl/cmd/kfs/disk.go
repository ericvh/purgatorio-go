package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/styx"
)

// The disk format is Plan 9's kfs, as Inferno's kfs.b keeps it: blocks
// of RBUFSIZE bytes, each ending in a tag; block 0 holds the magic,
// block 1 the super block with the free list, block 2 the root
// directory's entry.

const (
	maxbufsize  = 16 * 1024
	namelen     = 28 // with the terminating zero
	ndblock     = 6
	maxfilesize = 0x7fffffff

	superaddr = 1
	rootaddr  = 2

	qpdir   = 1 << 31
	qpnone  = 0
	qproot  = 1
	qpsuper = 2

	// mode bits on disk
	dalloc = 0x8000
	ddir   = 0x4000
	dapnd  = 0x2000
	dlock  = 0x1000
	dread  = 4
	dwrite = 2
	dexec  = 1

	tagsize = 2 + 2 + 4
)

// block tags
const (
	tnone = iota
	tsuper
	tdir
	tind1
	tind2
	tfile
	tfree
	tbuck
	tvirgo
	tcache
)

// flags of getbuf
const (
	bread  = 1 << iota // read the block if not cached
	bprobe             // return nil if not cached
	bmod               // the block is modified
	bimm               // write it when put
)

// Dentry.mod bits
const (
	uname = 1 << iota
	uids
	umode
	uqid
	usize
	utime
)

// File.open bits
const (
	fread = 1 << iota
	fwrite
	fremov
	fwstat
)

// Directory entry layout.
const (
	oname      = 0
	ouid       = oname + namelen
	ogid       = ouid + 2
	omode      = ogid + 2
	omuid      = omode + 2
	opath      = omuid + 2
	overs      = opath + 4
	osize      = overs + 4
	odblock    = osize + 4
	oiblock    = odblock + ndblock*4
	odiblock   = oiblock + 4
	oatime     = odiblock + 4
	omtime     = oatime + 4
	dentrysize = omtime + 4
)

// Super block layout.
const (
	ofstart    = 0
	ofsize     = ofstart + 4
	otfree     = ofsize + 4
	oqidgen    = otfree + 4
	ofsok      = oqidgen + 4
	ororaddr   = ofsok + 4
	olast      = ororaddr + 4
	onext      = olast + 4
	super1size = onext + 4
)

var (
	rbufsize, bufsize, dirperbuf, indperbuf, indperbuf2, feperbuf int

	emptyblock []byte
	disk       *os.File
	readonly   bool
	noatime    bool
	badmagic   bool
)

const wmagic = "kfs wren device\n"

var le = binary.LittleEndian

func get2(a []byte, o int) int  { return int(le.Uint16(a[o:])) }
func get2s(a []byte, o int) int { return int(int16(le.Uint16(a[o:]))) }
func get4(a []byte, o int) int  { return int(int32(le.Uint32(a[o:]))) }
func put2(a []byte, o, v int)   { le.PutUint16(a[o:], uint16(v)) }
func put4(a []byte, o, v int)   { le.PutUint32(a[o:], uint32(v)) }

func now() int { return int(time.Now().Unix()) }

func setsizes() {
	bufsize = rbufsize - tagsize
	dirperbuf = bufsize / dentrysize
	indperbuf = bufsize / 4
	indperbuf2 = indperbuf * indperbuf
	feperbuf = (bufsize - super1size - 4) / 4
	emptyblock = make([]byte, rbufsize)
}

// The block cache. A guest is single-threaded, so buffers need no locks:
// a block held twice is the same buffer. Writes go through when a
// modified buffer is put, as in kfs.b.

type iobuf struct {
	addr  int
	data  []byte
	flags int
	ref   int
	used  int
}

var (
	cache    = map[int]*iobuf{}
	cacheuse int
)

const ncache = 64

func getbuf(addr, flags int) *iobuf {
	cacheuse++
	if p := cache[addr]; p != nil {
		p.flags |= flags
		p.ref++
		p.used = cacheuse
		return p
	}
	if flags == bprobe {
		return nil
	}
	if len(cache) >= ncache {
		var old *iobuf
		for _, p := range cache {
			if p.ref == 0 && (old == nil || p.used < old.used) {
				old = p
			}
		}
		if old != nil {
			delete(cache, old.addr)
		}
	}
	p := &iobuf{addr: addr, data: make([]byte, rbufsize), flags: flags, ref: 1, used: cacheuse}
	if flags&bread != 0 {
		if _, err := disk.ReadAt(p.data, int64(addr)*int64(rbufsize)); err != nil {
			eprint(fmt.Sprintf("error reading block %d: %v", addr, err))
			return nil
		}
	}
	cache[addr] = p
	return p
}

func (p *iobuf) put() {
	if p.flags&bmod != 0 {
		p.flags |= bimm
	}
	if p.flags&bimm != 0 {
		if _, err := disk.WriteAt(p.data, int64(p.addr)*int64(rbufsize)); err != nil {
			panicf("error writing block %d: %v", p.addr, err)
		}
		p.flags &^= bmod | bimm
	}
	p.ref--
}

func tagname(t int) string {
	names := []string{"Tnone", "Tsuper", "Tdir", "Tind1", "Tind2", "Tfile", "Tfree", "Tbuck", "Tvirgo", "Tcache"}
	if t >= 0 && t < len(names) {
		return names[t]
	}
	return strconv.Itoa(t)
}

func (p *iobuf) checktag(tag, qpath int) int {
	t, path := get2(p.data, bufsize+2), get4(p.data, bufsize+4)
	if t != tag {
		eprint(fmt.Sprintf("	tag = %s; expected %s; addr = %d\n", tagname(t), tagname(tag), p.addr))
		return 2
	}
	if qpath != qpnone {
		qpath &^= qpdir
		if qpath != path {
			if qpath == path&^qpdir {
				return 0
			}
			eprint(fmt.Sprintf("	tag/path = %x; expected %s/%x\n", path, tagname(tag), qpath))
			return 1
		}
	}
	return 0
}

func (p *iobuf) settag(tag, qpath int) {
	a := p.data[bufsize:]
	put2(a, 0, 0)
	put2(a, 2, tag)
	if qpath != qpnone {
		put4(a, 4, qpath&^qpdir)
	}
	p.flags |= bmod
}

// The super block.

type superb struct {
	iob                          *iobuf
	fstart, fsize, tfree, qidgen int
	fsok                         int
	fbuf                         []byte // nfree, free[]: aliased into the block
}

func getsuper(flags int) *superb {
	p := getbuf(superaddr, flags)
	if p == nil {
		return nil
	}
	if p.checktag(tsuper, qpsuper) != 0 {
		p.put()
		return nil
	}
	a := p.data
	return &superb{iob: p, fstart: get4(a, ofstart), fsize: get4(a, ofsize), tfree: get4(a, otfree),
		qidgen: get4(a, oqidgen), fsok: get4(a, ofsok), fbuf: a[super1size:]}
}

func (s *superb) touched() { s.iob.flags |= bmod }

func (s *superb) put() {
	if s.iob == nil {
		return
	}
	if s.iob.flags&bmod != 0 {
		a := s.iob.data
		put4(a, ofstart, s.fstart)
		put4(a, ofsize, s.fsize)
		put4(a, otfree, s.tfree)
		put4(a, oqidgen, s.qidgen)
		put4(a, ofsok, s.fsok)
	}
	s.iob.put()
	s.iob = nil
}

func superok(set int) int {
	sb := getsuper(bread | bmod | bimm)
	if sb == nil {
		return 0
	}
	ok := sb.fsok
	sb.fsok = set
	sb.touched()
	sb.put()
	return ok
}

// Directory entries.

type dentry struct {
	name  string
	uid   int
	gid   int
	muid  int
	mode  int
	qid   styx.Qid
	size  int64
	atime int
	mtime int

	iob *iobuf
	buf []byte
	mod int
}

func mkqid(path, vers, mode int) styx.Qid {
	q := styx.Qid{Path: uint64(uint32(path) &^ qpdir), Vers: uint32(vers)}
	if mode&ddir != 0 {
		q.Type |= styx.QTDIR
	}
	if mode&dapnd != 0 {
		q.Type |= styx.QTAPPEND
	}
	if mode&dlock != 0 {
		q.Type |= styx.QTEXCL
	}
	return q
}

func unpackdentry(a []byte) *dentry {
	i := 0
	for i < namelen && a[i] != 0 {
		i++
	}
	d := &dentry{name: string(a[:i]), uid: get2s(a, ouid), gid: get2s(a, ogid), mode: get2(a, omode),
		muid: get2(a, omuid), size: int64(uint32(get4(a, osize))), atime: get4(a, oatime), mtime: get4(a, omtime)}
	d.qid = mkqid(get4(a, opath), get4(a, overs), d.mode)
	return d
}

func getdentry(p *iobuf, slot int) *dentry {
	if p == nil {
		return nil
	}
	buf := p.data[(slot%dirperbuf)*dentrysize:]
	d := unpackdentry(buf)
	d.iob = p
	d.buf = buf
	return d
}

func getdentrya(addr, slot, qpath, mode int) (*dentry, string) {
	p := getbuf(addr, mode)
	if p == nil || p.checktag(tdir, qpath) != 0 {
		if p != nil {
			p.put()
		}
		return nil, ealloc
	}
	d := getdentry(p, slot)
	if d == nil || d.mode&dalloc == 0 {
		p.put()
		return nil, ealloc
	}
	return d, ""
}

func getdentryf(f *file, mode int) (*dentry, string) {
	d, e := getdentrya(f.addr, f.slot, qpnone, mode)
	if e != "" {
		return nil, e
	}
	if f.qid.Path != d.qid.Path || f.qid.Type&styx.QTDIR != d.qid.Type&styx.QTDIR {
		d.put()
		return nil, eqid
	}
	return d, ""
}

func (d *dentry) change(f int) { d.mod |= f }

func (d *dentry) update() {
	f := d.mod
	d.mod = 0
	if d.iob == nil || d.iob.flags&bmod == 0 {
		if f != 0 {
			panicf("Dentry.update")
		}
		return
	}
	a := d.buf
	if f&uname != 0 {
		b := []byte(d.name)
		for i := 0; i < namelen; i++ {
			if i < len(b) {
				a[i] = b[i]
			} else {
				a[i] = 0
			}
		}
	}
	if f&uids != 0 {
		put2(a, ouid, d.uid)
		put2(a, ogid, d.gid)
	}
	if f&umode != 0 {
		put2(a, omode, d.mode)
	}
	if f&uqid != 0 {
		path := int(d.qid.Path)
		if d.mode&ddir != 0 {
			path |= qpdir
		}
		put4(a, opath, path)
		put4(a, overs, int(d.qid.Vers))
	}
	if f&usize != 0 {
		put4(a, osize, int(d.size))
	}
	if f&utime != 0 {
		put4(a, omtime, d.mtime)
		put4(a, oatime, d.atime)
	}
	d.iob.flags |= bmod
}

func (d *dentry) access(f, uid int) {
	p := d.iob
	if p == nil || readonly {
		return
	}
	if f&(fwrite|fwstat) == 0 && noatime {
		return
	}
	if f&(fread|fwrite|fwstat) != 0 {
		d.atime = now()
		put4(d.buf, oatime, d.atime)
		p.flags |= bmod
	}
	if f&fwrite != 0 {
		d.mtime = now()
		put4(d.buf, omtime, d.mtime)
		d.muid = uid
		put2(d.buf, omuid, uid)
		d.qid.Vers++
		put4(d.buf, overs, int(d.qid.Vers))
		p.flags |= bmod
	}
}

// release drops the entry's block, to be got again when needed.
func (d *dentry) release() {
	if d.iob != nil {
		d.update()
		d.iob.put()
		d.iob = nil
		d.buf = nil
	}
}

func (d *dentry) put() {
	p := d.iob
	if p == nil || d.buf == nil {
		return
	}
	d.update()
	p.put()
	d.iob = nil
	d.buf = nil
}

func (d *dentry) getblk(a, tag int) *iobuf {
	addr := d.rel2abs(a, tag, false)
	if addr == 0 {
		return nil
	}
	return getbuf(addr, bread)
}

// getblk1 is getblk, releasing the entry's block first.
func (d *dentry) getblk1(a, tag int) *iobuf {
	addr := d.rel2abs(a, tag, true)
	if addr == 0 {
		return nil
	}
	return getbuf(addr, bread)
}

func (d *dentry) rel2abs(a, tag int, putb bool) int {
	if a < 0 {
		fmt.Println("Dentry.rel2abs: neg")
		return 0
	}
	p := d.iob
	if p == nil || d.buf == nil {
		panicf("nil iob")
	}
	data := d.buf
	qpath := int(d.qid.Path)
	if a < ndblock {
		addr := get4(data, odblock+a*4)
		if addr == 0 && tag != 0 {
			addr = balloc(tag, qpath)
			put4(data, odblock+a*4, addr)
			p.flags |= bmod | bimm
		}
		if putb {
			d.release()
		}
		return addr
	}
	a -= ndblock
	if a < indperbuf {
		addr := get4(data, oiblock)
		if addr == 0 && tag != 0 {
			addr = balloc(tind1, qpath)
			put4(data, oiblock, addr)
			p.flags |= bmod | bimm
		}
		if putb {
			d.release()
		}
		return indfetch(qpath, addr, a, tind1, tag)
	}
	a -= indperbuf
	if a < indperbuf2 {
		addr := get4(data, odiblock)
		if addr == 0 && tag != 0 {
			addr = balloc(tind2, qpath)
			put4(data, odiblock, addr)
			p.flags |= bmod | bimm
		}
		if putb {
			d.release()
		}
		addr = indfetch(qpath, addr, a/indperbuf, tind2, tind1)
		return indfetch(qpath, addr, a%indperbuf, tind1, tag)
	}
	if putb {
		d.release()
	}
	fmt.Println("Dentry.buf: trip indirect")
	return 0
}

func indfetch(path, addr, a, itag, tag int) int {
	if addr == 0 {
		return 0
	}
	bp := getbuf(addr, bread)
	if bp == nil {
		fmt.Println("ind fetch bp = nil")
		return 0
	}
	if bp.checktag(itag, path) != 0 {
		fmt.Println("ind fetch tag")
		bp.put()
		return 0
	}
	addr = get4(bp.data, a*4)
	if addr == 0 && tag != 0 {
		addr = balloc(tag, path)
		if addr != 0 {
			put4(bp.data, a*4, addr)
			bp.flags |= bmod | bimm
			bp.settag(itag, path)
		}
	}
	bp.put()
	return addr
}

func balloc(tag, qpath int) int {
	sb := getsuper(bread | bmod)
	if sb == nil {
		panicf("balloc: super block")
	}
	n := get4(sb.fbuf, 0)
	n--
	sb.tfree--
	if n < 0 || n >= feperbuf {
		panicf("balloc: bad freelist")
	}
	a := get4(sb.fbuf, 4+n*4)
	if n == 0 {
		if a == 0 {
			sb.tfree = 0
			sb.touched()
			sb.put()
			return 0
		}
		bp := getbuf(a, bread)
		if bp == nil || bp.checktag(tfree, qpnone) != 0 {
			if bp != nil {
				bp.put()
			}
			sb.put()
			return 0
		}
		copy(sb.fbuf, bp.data[:(feperbuf+1)*4])
		sb.touched()
		bp.put()
	} else {
		put4(sb.fbuf, 0, n)
		sb.touched()
	}
	bp := getbuf(a, bmod)
	copy(bp.data, emptyblock)
	bp.settag(tag, qpath)
	if tag == tind1 || tag == tind2 || tag == tdir {
		bp.flags |= bimm
	}
	bp.put()
	sb.put()
	return a
}

func bfree(addr, d int) {
	if addr == 0 {
		return
	}
	if d > 0 {
		d--
		if p := getbuf(addr, bread); p != nil {
			for i := indperbuf - 1; i >= 0; i-- {
				bfree(get4(p.data, i*4), d)
			}
			p.put()
		}
	}
	// stop outstanding i/o
	if p := getbuf(addr, bprobe); p != nil {
		p.flags &^= bmod | bimm
		p.put()
	}
	s := getsuper(bread | bmod)
	if s == nil {
		panicf("bfree: super block")
	}
	addfree(addr, s)
	s.put()
}

func addfree(addr int, sb *superb) {
	if addr >= sb.fsize {
		fmt.Printf("addfree: bad addr %d\n", addr)
		return
	}
	n := get4(sb.fbuf, 0)
	if n < 0 || n > feperbuf {
		panicf("addfree: bad freelist")
	}
	if n >= feperbuf {
		p := getbuf(addr, bmod)
		if p == nil {
			panicf("addfree: Iobuf.get")
		}
		copy(p.data, sb.fbuf[:(1+feperbuf)*4])
		copy(sb.fbuf, emptyblock[:(1+feperbuf)*4])
		p.settag(tfree, qpnone)
		p.put()
		n = 0
	}
	put4(sb.fbuf, 4+n*4, addr)
	put4(sb.fbuf, 0, n+1)
	sb.tfree++
	if addr >= sb.fsize {
		sb.fsize = addr + 1
	}
	sb.touched()
}

func qidpathgen() int {
	sb := getsuper(bread | bmod)
	if sb == nil {
		panicf("qidpathgen: super block")
	}
	sb.qidgen++
	path := sb.qidgen
	sb.touched()
	sb.put()
	return path
}

func (d *dentry) trunc(uid int) {
	p := d.iob
	data := d.buf
	bfree(get4(data, odiblock), 2)
	put4(data, odiblock, 0)
	bfree(get4(data, oiblock), 1)
	put4(data, oiblock, 0)
	for i := ndblock - 1; i >= 0; i-- {
		bfree(get4(data, odblock+i*4), 0)
		put4(data, odblock+i*4, 0)
	}
	d.size = 0
	d.change(usize)
	p.flags |= bmod | bimm
	d.access(fwrite, uid)
	d.update()
}

// The device.

func wrenream() {
	if rbufsize%512 != 0 {
		panicf("kfs: bad buffersize(%d): restart a multiple of 512", rbufsize)
	}
	if rbufsize > maxbufsize {
		panicf("kfs: bad buffersize(%d): must be at most %d", rbufsize, maxbufsize)
	}
	fmt.Printf("kfs: reaming the file system using %d byte blocks\n", rbufsize)
	buf := make([]byte, rbufsize)
	copy(buf[256:], fmt.Sprintf("%s%d\n", wmagic, rbufsize))
	if _, err := disk.WriteAt(buf, 0); err != nil {
		panicf("can't ream disk")
	}
}

func wreninit() bool {
	buf := make([]byte, maxbufsize)
	n, _ := disk.ReadAt(buf, 0)
	if n < len(buf) {
		return false
	}
	badmagic = false
	rbufsize = 1024
	if string(buf[256:256+len(wmagic)]) != wmagic {
		badmagic = true
		return false
	}
	s := string(buf[256+len(wmagic) : 256+len(wmagic)+12])
	if i := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		s = s[:i]
	}
	rbufsize, _ = strconv.Atoi(s)
	if rbufsize <= 0 || rbufsize%512 != 0 || rbufsize > maxbufsize {
		fatal("bad block size")
	}
	return true
}

func wrenread(addr int, a []byte) bool {
	n, _ := disk.ReadAt(a, int64(addr)*int64(rbufsize))
	return n != len(a)
}

func wrentag(buf []byte, tag, qpath int) bool {
	return get2(buf, bufsize+2) != tag || qpath&^qpdir != get4(buf, bufsize+4)
}

func wrencheck() bool {
	if badmagic {
		return true
	}
	buf := make([]byte, rbufsize)
	if wrenread(superaddr, buf) || wrentag(buf, tsuper, qpsuper) ||
		wrenread(rootaddr, buf) || wrentag(buf, tdir, qproot) {
		return true
	}
	return unpackdentry(buf).mode&dalloc == 0
}

func wrensize() int {
	fi, err := disk.Stat()
	if err != nil {
		return -1
	}
	return int(fi.Size() / int64(rbufsize))
}

func rootream(addr int) {
	p := getbuf(addr, bmod|bimm)
	copy(p.data, emptyblock)
	p.settag(tdir, qproot)
	d := getdentry(p, 0)
	d.name = "/"
	d.uid = -1
	d.gid = -1
	d.mode = dalloc | ddir | (dread|dwrite|dexec)<<6 | (dread|dwrite|dexec)<<3 | (dread | dwrite | dexec)
	d.qid = styx.Qid{Path: qproot, Type: styx.QTDIR}
	d.atime = now()
	d.mtime = d.atime
	d.change(^0)
	d.access(fread|fwrite, -1)
	d.update()
	p.put()
}

func superream(addr int) {
	fsize := wrensize()
	if fsize <= 0 {
		panicf("file system device size")
	}
	p := getbuf(addr, bmod|bimm)
	copy(p.data, emptyblock)
	p.settag(tsuper, qpsuper)
	sb := &superb{iob: p, fstart: 1, fsize: fsize, qidgen: 10, fbuf: p.data[super1size:]}
	put4(sb.fbuf, 0, 1) // nfree = 1
	for i := fsize - 1; i >= addr+2; i-- {
		addfree(i, sb)
	}
	sb.put()
}

func dir9p2(d *dentry) styx.Dir {
	dir := styx.Dir{Name: d.name, UID: uidtostr(d.uid), GID: uidtostr(d.gid), MUID: uidtostr(d.muid),
		Qid: d.qid, Mode: uint32(d.mode & 0o777), Atime: uint32(d.atime), Mtime: uint32(d.mtime), Length: uint64(d.size)}
	if d.mode&ddir != 0 {
		dir.Mode |= styx.DMDIR
	}
	if d.mode&dapnd != 0 {
		dir.Mode |= styx.DMAPPEND
	}
	if d.mode&dlock != 0 {
		dir.Mode |= styx.DMEXCL
	}
	return dir
}
