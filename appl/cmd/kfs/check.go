package main

import (
	"fmt"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/quote"
)

// The file system check, as kfs.b's: it walks the tree from the root,
// marking each block and qid it reaches, then checks or rebuilds the
// free list and reports what it found.

// check flags
const (
	crdall = 1 << iota // read all files
	ctag               // rebuild tags
	cpfile             // print files
	cpdir              // print directories
	cfree              // rebuild the free list
	cream              // clear all bad tags
	cbad               // clear all bad blocks
	ctouch             // touch old dir and indir
	cquiet             // report just nasty things
)

const (
	maxdepth = 100
	maxname  = 4000
)

type bmap struct {
	lo, hi            int
	bits              []byte
	nbad, ndup, nmark int
}

func newmap(lo, hi int) *bmap {
	return &bmap{lo: lo, hi: hi, bits: make([]byte, (hi-lo+7)>>3)}
}

func (m *bmap) mark(i int) string {
	if i < m.lo || i >= m.hi {
		m.nbad++
		return "out of range"
	}
	i -= m.lo
	b := byte(1 << (i & 7))
	i >>= 3
	if m.bits[i]&b != 0 {
		m.ndup++
		return "dup"
	}
	m.bits[i] |= b
	m.nmark++
	return ""
}

type checker struct {
	amap, qmap *bmap
	name       string
	nfiles     int
	maxq       int
	mod        int
	flags      int
	depth      int
	maxdepth   int
}

// checkflags parses the console's check flags, or returns -1.
func checkflags(s string) int {
	f := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'r':
			f |= crdall
		case 't':
			f |= ctag
		case 'P':
			f |= cpfile
		case 'p':
			f |= cpdir
		case 'f':
			f |= cfree
		case 'c':
			f |= cream
		case 'd':
			f |= cbad
		case 'w':
			f |= ctouch
		case 'q':
			f |= cquiet
		case 'v':
		default:
			return -1
		}
	}
	return f
}

func check(flags int) {
	c := &checker{flags: flags}
	c.check()
}

func (c *checker) check() {
	p := c.xtag(superaddr, tsuper, qpsuper)
	if p == nil {
		cprint("bad superblock")
		return
	}
	a := p.data
	sb := &superb{iob: p, fstart: get4(a, ofstart), fsize: get4(a, ofsize), tfree: get4(a, otfree),
		qidgen: get4(a, oqidgen), fsok: get4(a, ofsok), fbuf: a[super1size:]}
	fstart, fsize := sb.fstart, sb.fsize
	if fstart != 1 {
		cprint("invalid superblock")
		sb.put()
		return
	}
	if fsize < fstart || fsize > wrensize() {
		cprint("invalid size in superblock")
		sb.put()
		return
	}
	c.amap = newmap(fstart, fsize)
	nqid := sb.qidgen + 100
	if nqid > 1024*1024*8 {
		nqid = 1024 * 1024 * 8
	}
	if nqid < 64*1024 {
		nqid = 64 * 1024
	}
	c.qmap = newmap(0, nqid)
	c.amark(superaddr)
	if c.flags&cquiet == 0 {
		cprint("checking file system: main")
	}
	if d := c.maked(rootaddr, 0, qproot); d != nil {
		c.amark(rootaddr)
		if c.fsck(d) != 0 {
			c.modd(rootaddr, 0, d)
		}
		c.depth--
		if c.depth != 0 {
			cprint("depth not zero on return")
		}
	}
	if sb.qidgen < c.maxq {
		cprint(fmt.Sprintf("qid generator low path=%d maxq=%d", sb.qidgen, c.maxq))
	}
	nqbad := c.qmap.nbad + c.qmap.ndup
	c.qmap = nil
	ndup := c.amap.ndup
	nused := c.amap.nmark
	c.amap.ndup, c.amap.nmark = 0, 0
	if c.flags&cfree != 0 {
		c.name = "free list"
		c.mkfreelist(sb)
		sb.qidgen = c.maxq
		p.settag(tsuper, qpnone)
	} else {
		c.ckfreelist(sb)
	}
	nbad := c.amap.nbad
	nfdup := c.amap.ndup
	nfree := c.amap.nmark
	if c.mod != 0 {
		cprint("file system was modified")
		p.settag(tsuper, qpnone)
	}
	if c.flags&cquiet == 0 {
		cprint(fmt.Sprintf("%8d files", c.nfiles))
		cprint(fmt.Sprintf("%8d blocks in the file system", fsize-fstart))
		cprint(fmt.Sprintf("%8d used blocks", nused))
		cprint(fmt.Sprintf("%8d free blocks", sb.tfree))
	}
	if c.flags&cfree == 0 {
		if nfree != sb.tfree {
			cprint(fmt.Sprintf("%8d free blocks found", nfree))
		}
		if nfdup != 0 {
			cprint(fmt.Sprintf("%8d blocks duplicated in the free list", nfdup))
		}
		if n := fsize - fstart - nused - nfree; n != 0 {
			cprint(fmt.Sprintf("%8d missing blocks", n))
		}
	}
	if ndup != 0 {
		cprint(fmt.Sprintf("%8d address duplications", ndup))
	}
	if nbad != 0 {
		cprint(fmt.Sprintf("%8d bad block addresses", nbad))
	}
	if nqbad != 0 {
		cprint(fmt.Sprintf("%8d bad qids", nqbad))
	}
	if c.flags&cquiet == 0 {
		cprint(fmt.Sprintf("%8d maximum qid path", c.maxq))
	}
	c.missing(sb)
	sb.put()
}

func (c *checker) touch(a int) int {
	if c.flags&ctouch != 0 && a != 0 {
		if p := getbuf(a, bread|bmod); p != nil {
			p.put()
		}
		return 1
	}
	return 0
}

func (c *checker) checkdir(a, qpath int) int {
	ns := len(c.name)
	dmod := c.touch(a)
	for i := 0; i < dirperbuf; i++ {
		nd := c.maked(a, i, qpath)
		if nd == nil {
			break
		}
		if c.fsck(nd) != 0 {
			c.modd(a, i, nd)
			dmod++
		}
		c.depth--
		c.name = c.name[:ns]
	}
	c.name = c.name[:ns]
	return dmod
}

func (c *checker) checkindir(a int, d *dentry, qpath int) int {
	dmod := c.touch(a)
	p := c.xtag(a, tind1, qpath)
	if p == nil {
		return dmod
	}
	for i := 0; i < indperbuf; i++ {
		a = get4(p.data, i*4)
		if a == 0 {
			continue
		}
		if c.amark(a) {
			if c.flags&cbad != 0 {
				put4(p.data, i*4, 0)
				p.flags |= bmod
			}
			continue
		}
		if d.mode&ddir != 0 {
			dmod += c.checkdir(a, qpath)
		} else if c.flags&crdall != 0 {
			c.xread(a, qpath)
		}
	}
	p.put()
	return dmod
}

func (c *checker) fsck(d *dentry) int {
	c.depth++
	if c.depth >= c.maxdepth {
		c.maxdepth = c.depth
		if c.maxdepth >= maxdepth {
			cprint("max depth exceeded: " + c.name)
			return 0
		}
	}
	dmod := 0
	if d.mode&dalloc == 0 {
		return 0
	}
	c.nfiles++
	ns := len(c.name)
	i := utf8.RuneCountInString(d.name)
	if i >= namelen {
		cprint(fmt.Sprintf("%s.name (%s) not terminated", quote.Quote(c.name), quote.Quote(d.name)))
		return 0
	}
	ns += i
	if ns >= maxname {
		cprint(fmt.Sprintf("%s.name (%s) name too large", quote.Quote(c.name), quote.Quote(d.name)))
		return 0
	}
	c.name += d.name
	if d.mode&ddir != 0 {
		if ns > 1 {
			c.name += "/"
		}
		if c.flags&cpdir != 0 {
			cprint(c.name)
		}
	} else if c.flags&cpfile != 0 {
		cprint(c.name)
	}
	qpath := int(d.qid.Path) &^ qpdir
	c.qmark(qpath)
	if qpath > c.maxq {
		c.maxq = qpath
	}
	for i := 0; i < ndblock; i++ {
		a := get4(d.buf, odblock+i*4)
		if a == 0 {
			continue
		}
		if c.amark(a) {
			put4(d.buf, odblock+i*4, 0)
			dmod++
			continue
		}
		if d.mode&ddir != 0 {
			dmod += c.checkdir(a, qpath)
		} else if c.flags&crdall != 0 {
			c.xread(a, qpath)
		}
	}
	if a := get4(d.buf, oiblock); a != 0 {
		if c.amark(a) {
			put4(d.buf, oiblock, 0)
			dmod++
		} else {
			dmod += c.checkindir(a, d, qpath)
		}
	}
	a := get4(d.buf, odiblock)
	if a != 0 && c.amark(a) {
		put4(d.buf, odiblock, 0)
		return dmod + 1
	}
	dmod += c.touch(a)
	if p := c.xtag(a, tind2, qpath); p != nil {
		for i := 0; i < indperbuf; i++ {
			a = get4(p.data, i*4)
			if a == 0 {
				continue
			}
			if c.amark(a) {
				if c.flags&cbad != 0 {
					put4(p.data, i*4, 0)
					p.flags |= bmod
				}
				continue
			}
			dmod += c.checkindir(a, d, qpath)
		}
		p.put()
	}
	return dmod
}

func (c *checker) ckfreelist(sb *superb) {
	c.name = "free list"
	cprint("check " + c.name)
	fb := sb.fbuf
	var p *iobuf
	lo, hi := 0, 0
	for {
		n := get4(fb, 0)
		if n < 0 || n > feperbuf {
			cprint(fmt.Sprintf("check: nfree bad %d", n))
			break
		}
		for i := 1; i < n; i++ {
			a := get4(fb, 4+i*4)
			if a != 0 && !c.fmark(a) {
				if lo == 0 || lo > a {
					lo = a
				}
				if hi == 0 || hi < a {
					hi = a
				}
			}
		}
		a := get4(fb, 4)
		if a == 0 || c.fmark(a) {
			break
		}
		if lo == 0 || lo > a {
			lo = a
		}
		if hi == 0 || hi < a {
			hi = a
		}
		if p != nil {
			p.put()
		}
		if p = c.xtag(a, tfree, qpnone); p == nil {
			break
		}
		fb = p.data
	}
	if p != nil {
		p.put()
	}
	cprint(fmt.Sprintf("lo = %d; hi = %d", lo, hi))
}

// mkfreelist makes the free list from scratch.
func (c *checker) mkfreelist(sb *superb) {
	copy(sb.fbuf, emptyblock[:(feperbuf+1)*4])
	sb.tfree = 0
	put4(sb.fbuf, 0, 1)
	for a := sb.fsize - sb.fstart - 1; a >= 0; a-- {
		i := a >> 3
		if i < 0 || i >= len(c.amap.bits) {
			continue
		}
		b := byte(1 << (a & 7))
		if c.amap.bits[i]&b != 0 {
			continue
		}
		addfree(sb.fstart+a, sb)
		c.amap.bits[i] |= b
	}
	sb.iob.flags |= bmod
}

// maked copies a directory entry so that its block need not be held.
func (c *checker) maked(a, s, qpath int) *dentry {
	p := c.xtag(a, tdir, qpath)
	if p == nil {
		return nil
	}
	d := getdentry(p, s)
	if d == nil {
		p.put()
		return nil
	}
	d.buf = append([]byte(nil), d.buf[:dentrysize]...)
	d.iob = nil
	p.put()
	return d
}

func (c *checker) modd(a, s int, d1 *dentry) {
	if c.flags&cbad == 0 {
		return
	}
	p := getbuf(a, bread)
	d := getdentry(p, s)
	if d == nil {
		if p != nil {
			p.put()
		}
		return
	}
	copy(d.buf[:dentrysize], d1.buf)
	p.flags |= bmod
	p.put()
}

func (c *checker) xread(a, qpath int) {
	if p := c.xtag(a, tfile, qpath); p != nil {
		p.put()
	}
}

func (c *checker) xtag(a, tag, qpath int) *iobuf {
	if a == 0 {
		return nil
	}
	p := getbuf(a, bread)
	if p == nil {
		cprint(fmt.Sprintf("check: \"%s\": xtag: p null", c.name))
		if c.flags&(cream|ctag) != 0 {
			if p = getbuf(a, bmod); p != nil {
				copy(p.data, emptyblock)
				p.settag(tag, qpath)
				c.mod++
				return p
			}
		}
		return nil
	}
	if p.checktag(tag, qpath) != 0 {
		cprint(fmt.Sprintf("check: \"%s\": xtag: checktag", c.name))
		if c.flags&cream != 0 {
			copy(p.data, emptyblock)
		}
		if c.flags&(cream|ctag) != 0 {
			p.settag(tag, qpath)
			c.mod++
		}
	}
	return p
}

func (c *checker) amark(a int) bool {
	if e := c.amap.mark(a); e != "" {
		cprint(fmt.Sprintf("check: \"%s\": %s %d", c.name, e, a))
		return e != "dup" // dup blocks are not cleared: rm might repair them
	}
	return false
}

func (c *checker) fmark(a int) bool {
	if e := c.amap.mark(a); e != "" {
		cprint(fmt.Sprintf("check: \"%s\": %s %d", c.name, e, a))
		return true
	}
	return false
}

func (c *checker) missing(sb *superb) {
	n := 0
	for a := sb.fsize - sb.fstart - 1; a >= 0; a-- {
		if c.amap.bits[a>>3]&byte(1<<(a&7)) == 0 {
			cprint(fmt.Sprintf("missing: %d", sb.fstart+a))
			n++
		}
		if n > 10 {
			cprint(" ...")
			break
		}
	}
}

func (c *checker) qmark(qpath int) {
	if e := c.qmap.mark(qpath); e != "" {
		if c.qmap.nbad+c.qmap.ndup < 20 {
			cprint(fmt.Sprintf("check: \"%s\": qid %s 0x%x", c.name, e, qpath))
		}
	}
}
