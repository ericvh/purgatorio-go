package main

import (
	"encoding/binary"
	"fmt"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

// The FAT format. A directory entry is handled in place, as the 32 bytes
// of the sector buffer that hold it.

const (
	fat12   = 0x01
	fat16   = 0x04
	fathuge = 0x06
	dmddo   = 0x54

	drOnly  = 0x01
	dHidden = 0x02
	dSystem = 0x04
	dVLabel = 0x08
	dDir    = 0x10
	dArch   = 0x20
	dLong   = drOnly | dHidden | dSystem | dVLabel
	dmLong  = dLong | dDir | dArch

	dosdirsize = 32
	dosempty   = 0xe5
	dosrunes   = 13

	fatresrv = 2
)

var le = binary.LittleEndian

// dosbpb is the BIOS parameter block, worked out.
type dosbpb struct {
	sectsize    int // bytes
	clustsize   int // sectors
	nresrv      int
	nfats       int
	rootsize    int // entries
	volsize     int // sectors
	mediadesc   int
	fatsize     int // sectors
	fatclusters int
	fatbits     int // 12 or 16
	fataddr     int // sector numbers
	rootaddr    int
	dataaddr    int
	freeptr     int // next free cluster candidate
}

// dosptr locates a file's directory entry and its parent's.
type dosptr struct {
	addr     int // sector of the file's entry
	offset   int
	paddr    int // of the parent's entry
	poffset  int
	iclust   int // ordinal within the file
	clust    int
	prevaddr int
	naddr    int
	p        *iosect
}

func newdosptr() *dosptr { return &dosptr{prevaddr: -1, naddr: -1} }

// entry returns the directory entry dp points at; dp.p must be held.
func (dp *dosptr) entry() []byte { return dp.p.buf[dp.offset : dp.offset+dosdirsize] }

func short(a []byte) int { return int(le.Uint16(a)) }
func long(a []byte) int  { return int(le.Uint32(a)) }

func getstart(d []byte) int { return short(d[26:28]) }

func putstart(d []byte, start int) { le.PutUint16(d[26:28], uint16(start)) }

func qidpath(dp *dosptr) uint64 {
	return uint64(dp.addr*(sectorsize/dosdirsize) + dp.offset/dosdirsize)
}

func isroot(addr int) bool { return addr == 0 }

var (
	nowt, nowt1 int
	tzoff       int
)

func dossetup() {
	nowt = daytime.Now()
	nowt1 = sys.Millisec()
	tzoff = daytime.Local(0).Tzoff
}

// dosfs reads xf's boot sector, looking through a partition table if
// there is one.
func dosfs(xf *xfs) int {
	mbroffset := 0
	var p *iosect
	i := 2
dmddoLoop:
	for {
		for i = 2; i > 0; i-- {
			p = getsect(xf, 0)
			if p == nil {
				return -1
			}
			b := p.buf
			if mbroffset == 0 && b[0] == 0xe9 {
				break
			}
			// A jump too short for a FAT boot sector: DOS 4.0's MBR jumps 8.
			if b[0] == 0xeb && b[2] == 0x90 && b[1] != 0x08 {
				break
			}
			if i < 2 || b[0x1fe] != 0x55 || b[0x1ff] != 0xaa {
				i = 0
				break
			}
			dp := 0x1be
			j := 4
			for ; j > 0; j-- {
				if debug != 0 {
					chat(fmt.Sprintf("0x%2.2x (%d,%d) 0x%2.2x (%d,%d) %d %d...",
						b[dp], b[dp+1], short(b[dp+2:]), b[dp+4], b[dp+5],
						short(b[dp+6:]), long(b[dp+8:]), long(b[dp+12:])))
				}
				// A disc manager partition: the real MBR is at sector 63.
				if b[dp+4] == dmddo {
					mbroffset = 63 * sectorsize
					putsect(p)
					purgebuf(xf)
					xf.offset += mbroffset
					continue dmddoLoop
				}
				if b[dp+4] == fat12 || b[dp+4] == fat16 || b[dp+4] == fathuge {
					break
				}
				dp += 16
			}
			if j <= 0 {
				if debug != 0 {
					chat("no active partition...")
				}
				putsect(p)
				return -1
			}
			offset := long(b[dp+8:]) * sectorsize
			putsect(p)
			purgebuf(xf)
			xf.offset = mbroffset + offset
		}
		break
	}
	if i <= 0 {
		if debug != 0 {
			chat("bad magic...")
		}
		putsect(p)
		return -1
	}
	b := p.buf
	if debug&fatInfo != 0 {
		bootdump(b)
	}
	bp := &dosbpb{}
	xf.bp = bp
	xf.fmt = true
	bp.sectsize = short(b[11:])
	bp.clustsize = int(b[13])
	bp.nresrv = short(b[14:])
	bp.nfats = int(b[16])
	bp.rootsize = short(b[17:])
	bp.volsize = short(b[19:])
	if bp.volsize == 0 {
		bp.volsize = long(b[32:])
	}
	bp.mediadesc = int(b[21])
	bp.fatsize = short(b[22:])
	if bp.sectsize != sectorsize || bp.clustsize == 0 || bp.fatsize == 0 {
		// Not a FAT12 or FAT16 file system this server can use.
		xf.fmt = false
		putsect(p)
		return -1
	}
	bp.fataddr = bp.nresrv
	bp.rootaddr = bp.fataddr + bp.nfats*bp.fatsize
	n := (bp.rootsize*dosdirsize + bp.sectsize - 1) / bp.sectsize
	bp.dataaddr = bp.rootaddr + n
	bp.fatclusters = fatresrv + (bp.volsize-bp.dataaddr)/bp.clustsize
	if bp.fatclusters < 4087 {
		bp.fatbits = 12
	} else {
		bp.fatbits = 16
	}
	bp.freeptr = 2
	if debug&fatInfo != 0 {
		chat(fmt.Sprintf("fatbits=%d (%d clusters)...", bp.fatbits, bp.fatclusters))
		for i := 0; i < bp.nfats; i++ {
			chat(fmt.Sprintf("fat %d: %d...", i, bp.fataddr+i*bp.fatsize))
		}
		chat(fmt.Sprintf("root: %d...", bp.rootaddr))
		chat(fmt.Sprintf("data: %d...", bp.dataaddr))
	}
	putsect(p)
	return 0
}

func bootdump(b []byte) {
	chat(fmt.Sprintf("magic: 0x%2.2x 0x%2.2x 0x%2.2x\n", b[0], b[1], b[2]))
	chat(fmt.Sprintf("version: \"%8.8s\"\n", b[3:11]))
	chat(fmt.Sprintf("sectsize: %d\n", short(b[11:])))
	chat(fmt.Sprintf("allocsize: %d\n", b[13]))
	chat(fmt.Sprintf("nresrv: %d\n", short(b[14:])))
	chat(fmt.Sprintf("nfats: %d\n", b[16]))
	chat(fmt.Sprintf("rootsize: %d\n", short(b[17:])))
	chat(fmt.Sprintf("volsize: %d\n", short(b[19:])))
	chat(fmt.Sprintf("mediadesc: 0x%2.2x\n", b[21]))
	chat(fmt.Sprintf("fatsize: %d\n", short(b[22:])))
	chat(fmt.Sprintf("trksize: %d\n", short(b[24:])))
	chat(fmt.Sprintf("nheads: %d\n", short(b[26:])))
	chat(fmt.Sprintf("nhidden: %d\n", long(b[28:])))
	chat(fmt.Sprintf("bigvolsize: %d\n", long(b[32:])))
	chat(fmt.Sprintf("driveno: %d\n", b[36]))
	chat(fmt.Sprintf("bootsig: 0x%2.2x\n", b[38]))
	chat(fmt.Sprintf("volid: 0x%8.8x\n", long(b[39:])))
	chat(fmt.Sprintf("label: \"%11.11s\"\n", b[43:54]))
}

func getfile(f *xfile) int {
	dp := f.ptr
	if dp.p != nil {
		panicf("getfile")
	}
	if dp.addr < 0 {
		panicf("getfile address")
	}
	p := getsect(f.xf, dp.addr)
	if p == nil {
		return -1
	}
	if !isroot(dp.addr) && f.qid.Path != qidpath(dp) {
		if debug != 0 {
			chat(fmt.Sprintf("qid mismatch f=0x%x d=0x%x...", f.qid.Path, qidpath(dp)))
		}
		putsect(p)
		return -1
	}
	dp.p = p
	return 0
}

func putfile(f *xfile) {
	dp := f.ptr
	if dp.p == nil {
		panicf("putfile")
	}
	putsect(dp.p)
	dp.p = nil
}

func clust2sect(bp *dosbpb, clust int) int { return bp.dataaddr + (clust-fatresrv)*bp.clustsize }

// fileclust returns the disk cluster of cluster iclust of f, allocating
// clusters as needed when cflag is set.
func fileclust(f *xfile, iclust int, cflag bool) int {
	dp := f.ptr
	if isroot(dp.addr) {
		return -1 // the old root directory is not in clusters
	}
	if dp.p == nil {
		panicf("fileclust")
	}
	d := dp.entry()
	start := getstart(d)
	if start == 0 {
		if !cflag {
			return -1
		}
		start = falloc(f.xf)
		if start <= 0 {
			return -1
		}
		puttime(d)
		putstart(d, start)
		dp.p.flags |= bmod
		dp.clust = 0
	}
	var clust, nskip int
	if dp.clust == 0 || iclust < dp.iclust {
		clust, nskip = start, iclust
	} else {
		clust, nskip = dp.clust, iclust-dp.iclust
	}
	if debug&clusterInfo != 0 && nskip > 0 {
		chat(fmt.Sprintf("clust %d, skip %d...", clust, nskip))
	}
	if clust <= 0 {
		return -1
	}
	if nskip > 0 {
		next := 0
		for ; nskip > 0; nskip-- {
			next = getfat(f.xf, clust)
			if debug&clusterInfo != 0 {
				chat(fmt.Sprintf(".%d", next))
			}
			if next <= 0 {
				if !cflag {
					break
				}
				next = falloc(f.xf)
				if next <= 0 {
					return -1
				}
				putfat(f.xf, clust, next)
			}
			clust = next
		}
		if next <= 0 {
			return -1
		}
		dp.clust = clust
		dp.iclust = iclust
	}
	if debug&clusterInfo != 0 {
		chat(fmt.Sprintf(" clust(%d)=0x%x...", iclust, clust))
	}
	return clust
}

// fileaddr returns the disk sector of sector isect of f.
func fileaddr(f *xfile, isect int, cflag bool) int {
	bp := f.xf.bp
	dp := f.ptr
	if isroot(dp.addr) {
		if isect*bp.sectsize >= bp.rootsize*dosdirsize {
			return -1
		}
		return bp.rootaddr + isect
	}
	clust := fileclust(f, isect/bp.clustsize, cflag)
	if clust < 0 {
		return -1
	}
	return clust2sect(bp, clust) + isect%bp.clustsize
}

// searchdir looks in directory f for name, matching long names when
// lflag is set; otherwise name is an 11-character short name. When
// creating (cflag), it fails if name exists and otherwise returns a free
// slot big enough for the name's long entries, extending the directory if
// needed; -2 means the disk is full.
func searchdir(f *xfile, name []rune, cflag, lflag bool) (int, *dosptr) {
	xf := f.xf
	bp := xf.bp
	addr1, addr2, prevaddr1, o1 := -1, -1, -1, 0
	dp := newdosptr()
	dp.paddr = f.ptr.addr
	dp.poffset = f.ptr.offset
	islong := false
	var buf []rune
	need := 1
	if lflag && cflag {
		need += name2de(name)
	}
	if !lflag {
		n := append(append(append([]rune{}, name[:8]...), '.'), name[8:11]...)
		i := len(n) - 1
		for i >= 0 && (n[i] == ' ' || n[i] == '.') {
			i--
		}
		name = n[:i+1]
	}
	addr := -1
	prevaddr := -1
	have := 0
	isect := 0
	for ; ; isect++ {
		prevaddr = addr
		addr = fileaddr(f, isect, cflag)
		if addr < 0 {
			break
		}
		p := getsect(xf, addr)
		if p == nil {
			break
		}
		for o := 0; o < bp.sectsize; o += dosdirsize {
			dattr := int(p.buf[o+11])
			dname0 := p.buf[o]
			if dname0 == 0 {
				if debug != 0 {
					chat("end dir(0)...")
				}
				putsect(p)
				if !cflag {
					return -1, nil
				}
				// addr1.o1 starts the free slots; addr2 is the second
				// sector, if the long name entries run past addr1's.
				if addr1 < 0 {
					addr1 = addr
					prevaddr1 = prevaddr
					o1 = o
				}
				nleft := (bp.sectsize - o) / dosdirsize
				if addr2 < 0 && nleft+have < need {
					addr2 = fileaddr(f, isect+1, cflag)
					if addr2 < 0 {
						if debug != 0 {
							chat("end dir(2)...")
						}
						return -2, nil
					}
				} else if addr2 < 0 {
					addr2 = addr
				}
				if addr2 == addr1 {
					addr2 = -1
				}
				dp.addr, dp.offset, dp.prevaddr, dp.naddr = addr1, o1, prevaddr1, addr2
				return 0, dp
			}
			if dname0 == dosempty {
				have++
				if addr1 == -1 {
					addr1 = addr
					o1 = o
					prevaddr1 = prevaddr
				}
				if addr2 == -1 && have >= need {
					addr2 = addr
				}
				continue
			}
			have = 0
			if addr2 == -1 {
				addr1 = -1
			}
			if dattr&dmLong == dLong {
				if !islong {
					buf = nil
				}
				islong = true
				buf = append(getnamesect(p.buf[o:o+dosdirsize]), buf...)
				continue
			}
			if dattr&dVLabel != 0 {
				islong = false
				continue
			}
			if !islong || !lflag {
				buf = getname(p.buf[o : o+dosdirsize])
			}
			islong = false
			if debug != 0 {
				chat(fmt.Sprintf("cmp: [%s] [%s]", string(buf), string(name)))
			}
			if !namematch(buf, name) {
				buf = nil
				continue
			}
			if cflag {
				putsect(p)
				return -1, nil
			}
			dp.addr, dp.prevaddr, dp.offset, dp.p = addr, prevaddr, o, p
			return 0, dp
		}
		putsect(p)
	}
	if !cflag {
		return -1, nil
	}
	// The end of the root directory, or of another on a cluster boundary.
	if addr1 < 0 {
		addr1 = fileaddr(f, isect, true)
		if addr1 < 0 {
			return -2, nil
		}
		prevaddr1 = prevaddr
		o1 = 0
	} else if addr2 < 0 && have < need {
		addr2 = fileaddr(f, isect, true)
		if addr2 < 0 {
			return -2, nil
		}
	}
	if addr2 == addr1 {
		addr2 = -1
	}
	dp.addr, dp.offset, dp.prevaddr, dp.naddr = addr1, o1, prevaddr1, addr2
	return 0, dp
}

// emptydir reports whether directory f holds no short entries but . and ..
func emptydir(f *xfile) int {
	for isect := 0; ; isect++ {
		addr := fileaddr(f, isect, false)
		if addr < 0 {
			break
		}
		p := getsect(f.xf, addr)
		if p == nil {
			return -1
		}
		for o := 0; o < f.xf.bp.sectsize; o += dosdirsize {
			dname0 := p.buf[o]
			dattr := p.buf[o+11]
			if dname0 == 0 {
				putsect(p)
				return 0
			}
			if dname0 == dosempty || dname0 == '.' || dattr&dVLabel != 0 {
				continue
			}
			putsect(p)
			return -1
		}
		putsect(p)
	}
	return 0
}

// readdir returns the directory entries of f from byte offset, as
// packed Dirs fitting in count bytes.
func readdir(f *xfile, offset, count int) (int, []byte) {
	xf := f.xf
	bp := xf.bp
	var buf []byte
	islong := false
	var longname []rune
	if count <= 0 {
		return 0, nil
	}
	for isect := 0; ; isect++ {
		addr := fileaddr(f, isect, false)
		if addr < 0 {
			break
		}
		p := getsect(xf, addr)
		if p == nil {
			return -1, nil
		}
		for o := 0; o < bp.sectsize; o += dosdirsize {
			e := p.buf[o : o+dosdirsize]
			dname0 := e[0]
			dattr := int(e[11])
			if dname0 == 0 {
				putsect(p)
				return len(buf), buf
			}
			if dname0 == dosempty {
				continue
			}
			if dname0 == '.' {
				if e[1] == ' ' || e[1] == 0 {
					continue
				}
				if e[1] == '.' && (e[2] == ' ' || e[2] == 0) {
					continue
				}
			}
			if dattr&dmLong == dLong {
				if !islong {
					longname = nil
				}
				longname = append(getnamesect(e), longname...)
				islong = true
				continue
			}
			if dattr&dVLabel != 0 {
				islong = false
				continue
			}
			dir := getdir(e, addr, o)
			if islong {
				dir.Name = string(longname)
				longname = nil
				islong = false
			}
			d := styx.MarshalDir(nil, dir)
			if offset > 0 {
				offset -= len(d)
				continue
			}
			if len(buf)+len(d) > count {
				putsect(p)
				return len(buf), buf
			}
			buf = append(buf, d...)
			if len(buf) >= count {
				putsect(p)
				return len(buf), buf
			}
		}
		putsect(p)
	}
	return len(buf), buf
}

// walkup finds the entry of f's parent's directory, and that of its
// parent, from the . and .. entries.
func walkup(f *xfile) (int, *dosptr) {
	bp := f.xf.bp
	dp := f.ptr
	ndp := newdosptr()
	ndp.addr = dp.paddr
	ndp.offset = dp.poffset
	if debug != 0 {
		chat(fmt.Sprintf("walkup: paddr=0x%x...", dp.paddr))
	}
	if dp.paddr == 0 {
		return 0, ndp
	}
	p := getsect(f.xf, dp.paddr)
	if p == nil {
		return -1, nil
	}
	start := getstart(p.buf[dp.poffset : dp.poffset+dosdirsize])
	putsect(p)
	if start == 0 {
		return -1, nil
	}
	// The parent's . must point at itself.
	p = getsect(f.xf, clust2sect(bp, start))
	if p == nil {
		return -1, nil
	}
	if p.buf[0] != '.' || p.buf[1] != ' ' || start != getstart(p.buf) {
		putsect(p)
		return -1, nil
	}
	// The parent's .. follows, with the start of the parent's parent.
	if p.buf[32] != '.' || p.buf[33] != '.' {
		putsect(p)
		return -1, nil
	}
	pstart := getstart(p.buf[dosdirsize:])
	putsect(p)
	if pstart == 0 {
		return 0, ndp
	}
	p = getsect(f.xf, clust2sect(bp, pstart))
	if p == nil {
		return -1, nil
	}
	if p.buf[0] != '.' || p.buf[1] != ' ' || pstart != getstart(p.buf) {
		putsect(p)
		return -1, nil
	}
	if p.buf[32] != '.' || p.buf[33] != '.' {
		putsect(p)
		return -1, nil
	}
	ppstart := getstart(p.buf[dosdirsize:])
	putsect(p)
	// Look through the parent's parent's parent for the parent's
	// parent's entry.
	ppclust := ppstart
	var k int
	if ppclust != 0 {
		k = clust2sect(bp, ppclust)
	} else {
		k = bp.rootaddr
	}
	p = getsect(f.xf, k)
	if p == nil {
		return -1, nil
	}
	if ppstart != 0 && (p.buf[0] != '.' || p.buf[1] != ' ' || ppstart != getstart(p.buf)) {
		putsect(p)
		return -1, nil
	}
	for so := 1; ; so++ {
		for o := 0; o < bp.sectsize; o += dosdirsize {
			c := p.buf[o]
			if c == 0 {
				putsect(p)
				return -1, nil
			}
			if c == dosempty {
				continue
			}
			if getstart(p.buf[o:]) == pstart {
				putsect(p)
				ndp.paddr = k
				ndp.poffset = o
				return 0, ndp
			}
		}
		if ppclust != 0 {
			if so%bp.clustsize == 0 {
				ppclust = getfat(f.xf, ppclust)
				if ppclust < 0 {
					putsect(p)
					return -1, nil
				}
			}
			k = clust2sect(bp, ppclust) + so%bp.clustsize
		} else {
			if so*bp.sectsize >= bp.rootsize*dosdirsize {
				putsect(p)
				return -1, nil
			}
			k = bp.rootaddr + so
		}
		putsect(p)
		p = getsect(f.xf, k)
		if p == nil {
			return -1, nil
		}
	}
}

func readfile(f *xfile, offset, count int) (int, []byte) {
	xf := f.xf
	bp := xf.bp
	dp := f.ptr
	length := long(dp.entry()[28:])
	if offset >= length {
		return 0, nil
	}
	if offset+count >= length {
		count = length - offset
	}
	buf := make([]byte, 0, count)
	isect := offset / bp.sectsize
	o := offset % bp.sectsize
	for count > 0 {
		addr := fileaddr(f, isect, false)
		isect++
		if addr < 0 {
			break
		}
		c := min(bp.sectsize-o, count)
		p := getsect(xf, addr)
		if p == nil {
			return -1, nil
		}
		buf = append(buf, p.buf[o:o+c]...)
		putsect(p)
		count -= c
		o = 0
	}
	return len(buf), buf
}

func writefile(f *xfile, buf []byte, offset, count int) int {
	xf := f.xf
	bp := xf.bp
	dp := f.ptr
	addr := 0
	rcnt := 0
	isect := offset / bp.sectsize
	o := offset % bp.sectsize
	for count > 0 {
		addr = fileaddr(f, isect, true)
		isect++
		if addr < 0 {
			break
		}
		c := min(bp.sectsize-o, count)
		var p *iosect
		if c == bp.sectsize {
			p = getosect(xf, addr)
		} else {
			p = getsect(xf, addr)
		}
		if p == nil {
			return -1
		}
		copy(p.buf[o:], buf[rcnt:rcnt+c])
		p.flags |= bmod
		putsect(p)
		count -= c
		rcnt += c
		o = 0
	}
	if rcnt <= 0 && addr < 0 {
		return -2
	}
	d := dp.entry()
	length := 0
	dlen := long(d[28:])
	if rcnt > 0 {
		length = offset + rcnt
	} else if dp.addr != 0 && dp.clust != 0 {
		c := bp.clustsize * bp.sectsize
		if dp.iclust > (dlen+c-1)/c {
			length = c * dp.iclust
		}
	}
	if length > dlen {
		le.PutUint32(d[28:], uint32(length))
	}
	puttime(d)
	dp.p.flags |= bmod
	return rcnt
}

// truncfile frees f's clusters and makes it empty.
func truncfile(f *xfile) int {
	xf := f.xf
	dp := f.ptr
	d := dp.entry()
	clust := getstart(d)
	putstart(d, 0)
	for clust > 0 {
		next := getfat(xf, clust)
		putfat(xf, clust, 0)
		clust = next
	}
	le.PutUint32(d[28:], 0)
	dp.iclust = 0
	dp.clust = 0
	dp.p.flags |= bmod
	return 0
}

// getdir converts the entry e at addr, offset to a Dir.
func getdir(e []byte, addr, offset int) *styx.Dir {
	d := &styx.Dir{UID: "dos", GID: "srv"}
	if e == nil || addr == 0 {
		d.Qid.Type = styx.QTDIR
		d.Mode = styx.DMDIR | 0o777
		return d
	}
	name := getname(e)
	for i, c := range name {
		if c >= 'A' && c <= 'Z' {
			name[i] = c - 'A' + 'a'
		}
	}
	d.Name = string(name)
	d.Qid.Path = uint64(addr*(sectorsize/dosdirsize) + offset/dosdirsize)
	dattr := e[11]
	if dattr&drOnly != 0 {
		d.Mode = 0o444
	} else {
		d.Mode = 0o666
	}
	d.Atime = uint32(gtime(e))
	d.Mtime = d.Atime
	if dattr&dDir != 0 {
		d.Qid.Type |= styx.QTDIR
		d.Mode |= styx.DMDIR | 0o111
	} else {
		d.Length = uint64(long(e[28:]))
	}
	if dattr&dSystem != 0 {
		d.Mode |= styx.DMEXCL
		d.Qid.Type |= styx.QTEXCL
	}
	return d
}

// putdir sets entry e's attributes and time from mode and mtime.
func putdir(e []byte, mode, mtime uint32) {
	if mode&2 != 0 {
		e[11] &^= drOnly
	} else {
		e[11] |= drOnly
	}
	if mode&styx.DMEXCL != 0 {
		e[11] |= dSystem
	} else {
		e[11] &^= dSystem
	}
	xputtime(e, int(mtime))
}

// getname returns the short name of entry e as NAME.EXT.
func getname(e []byte) []rune {
	var p []rune
	for i := 0; i < 8; i++ {
		c := rune(e[i])
		if c == 0 || c == ' ' {
			break
		}
		if i == 0 && c == 0x05 {
			c = 0xe5
		}
		p = append(p, c)
	}
	for i := 8; i < 11; i++ {
		c := rune(e[i])
		if c == 0 || c == ' ' {
			break
		}
		if i == 8 {
			p = append(p, '.')
		}
		p = append(p, c)
	}
	return p
}

// dosname splits p into a blank-padded upper case name and extension.
func dosname(p []rune) ([]rune, []rune) {
	name := []rune("        ")
	i := 0
	for ; i < len(p) && i < 8; i++ {
		c := p[i]
		if c >= 'a' && c <= 'z' {
			c += 'A' - 'a'
		} else if c == '.' {
			break
		}
		name[i] = c
	}
	ext := []rune("   ")
	for j := len(p) - 1; j >= i; j-- {
		if p[j] == '.' {
			q := 0
			for j++; j < len(p) && q < 3; j++ {
				c := p[j]
				if c >= 'a' && c <= 'z' {
					c += 'A' - 'a'
				}
				ext[q] = c
				q++
			}
			break
		}
	}
	return name, ext
}

// putname stores p as entry e's short name.
func putname(p []rune, e []byte) {
	if e[11]&dLong == dLong {
		panicf("putname of long name")
	}
	name, ext := dosname(p)
	for i, c := range name {
		e[i] = byte(c)
	}
	for i, c := range ext {
		e[8+i] = byte(c)
	}
}

// namematch compares names ignoring ASCII case.
func namematch(s1, s2 []rune) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i := range s1 {
		c, d := s1[i], s2[i]
		if c >= 'A' && c <= 'Z' {
			c -= 'A' - 'a'
		}
		if d >= 'A' && d <= 'Z' {
			d -= 'A' - 'a'
		}
		if c != d {
			return false
		}
	}
	return true
}

// Upper case is not a DOS character here: short names are read back in
// lower case, so a name with upper case is stored as a long name, which
// keeps its case.
func isdos(c rune) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case ' ', '$', '%', '"', '-', '_', '@', '~', '`', '!', '(', ')', '{', '}', '^', '#', '&':
		return true
	}
	return false
}

// name2de returns the number of long name entries p needs, or 0 if it is
// a valid short name.
func name2de(p []rune) int {
	ext, name := 0, 0
	end := len(p) - 1
	for end >= 0 && p[end] != '.' {
		ext++
		end--
	}
	if end > 0 {
		name = end
		for i := 0; i < end; i++ {
			if p[i] == '.' {
				return (len(p) + dosrunes - 1) / dosrunes
			}
		}
	} else {
		name = ext
		ext = 0
	}
	if name <= 8 && ext <= 3 && isvalidname(p) {
		return 0
	}
	return (len(p) + dosrunes - 1) / dosrunes
}

func isvalidname(s []rune) bool {
	dot := 0
	for i, c := range s {
		if c == '.' {
			dot++
			if dot > 1 || i == len(s)-1 {
				return false
			}
		} else if !isdos(c) {
			return false
		}
	}
	return true
}

// getnamesect returns the characters of long name entry e.
func getnamesect(e []byte) []rune {
	var s []rune
	for _, r := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
		for i := r[0]; i < r[1]; i += 2 {
			c := rune(short(e[i:]))
			if c == 0 {
				return s
			}
			s = append(s, c)
		}
	}
	return s
}

// long2short makes an 11-character short name for long name src, with
// tag number val.
func long2short(src []rune, val int) []rune {
	dst := []rune("           ")
	skip := 0
	ext := len(src) - 1
	for ext >= 0 && src[ext] != '.' {
		ext--
	}
	if ext < 0 {
		ext = len(src) - 1
	}
	upper := func(c rune) rune {
		if c >= 'a' && c <= 'z' {
			return c - 'a' + 'A'
		}
		return c
	}
	j := 0
	name := 0
	for ; name < ext && j < 8; name++ {
		c := src[name]
		if c != '.' && c != ' ' && c != '\t' {
			dst[j] = upper(c)
			j++
		} else {
			skip++
		}
	}
	j = 8
	for x := ext + 1; x < len(src) && j < 11; x++ {
		c := src[x]
		if c != ' ' && c != '\t' {
			dst[j] = upper(c)
			j++
		}
	}
	j = 1
	for i := val; i > 0; i /= 10 {
		j++
	}
	if 8-j < name {
		name = 8 - j
	} else {
		name -= skip
	}
	dst[name] = '~'
	for ; val > 0; val /= 10 {
		j--
		dst[name+j] = rune(val%10) + '0'
	}
	if debug != 0 {
		chat(fmt.Sprintf("returning dst [%s] src [%s]\n", string(dst), string(src)))
	}
	return dst
}

func getfat(xf *xfs, n int) int {
	bp := xf.bp
	if n < 2 || n >= bp.fatclusters {
		return -1
	}
	fb := bp.fatbits
	k := (fb * n) >> 3
	if k < 0 || k >= bp.fatsize*bp.sectsize {
		panicf("getfat")
	}
	sect := k/bp.sectsize + bp.fataddr
	o := k % bp.sectsize
	p := getsect(xf, sect)
	if p == nil {
		return -1
	}
	k = int(p.buf[o])
	o++
	if o >= bp.sectsize {
		putsect(p)
		p = getsect(xf, sect+1)
		if p == nil {
			return -1
		}
		o = 0
	}
	k |= int(p.buf[o]) << 8
	putsect(p)
	if fb == 12 {
		if n&1 != 0 {
			k >>= 4
		} else {
			k &= 0xfff
		}
	}
	if debug&fatInfo != 0 {
		chat(fmt.Sprintf("fat(0x%x)=0x%x...", n, k))
	}
	if k >= (1<<fb)-8 {
		return -1
	}
	return k
}

// putfat sets FAT entry n to val in every copy of the FAT.
func putfat(xf *xfs, n, val int) {
	bp := xf.bp
	if n < 2 || n >= bp.fatclusters {
		panicf("putfat n=%d", n)
	}
	k := (bp.fatbits * n) >> 3
	if k >= bp.fatsize*bp.sectsize {
		panicf("putfat")
	}
	for sect := k/bp.sectsize + bp.fataddr; sect < bp.rootaddr; sect += bp.fatsize {
		o := k % bp.sectsize
		p := getsect(xf, sect)
		if p == nil {
			continue
		}
		// next moves to the following sector when a 12-bit entry spans two.
		next := func() bool {
			o++
			if o >= bp.sectsize {
				p.flags |= bmod
				putsect(p)
				p = getsect(xf, sect+1)
				if p == nil {
					return false
				}
				o = 0
			}
			return true
		}
		switch bp.fatbits {
		case 12:
			if n&1 != 0 {
				p.buf[o] = p.buf[o]&0x0f | byte(val<<4)
				if !next() {
					continue
				}
				p.buf[o] = byte(val >> 4)
			} else {
				p.buf[o] = byte(val)
				if !next() {
					continue
				}
				p.buf[o] = p.buf[o]&0xf0 | byte((val>>8)&0x0f)
			}
		case 16:
			p.buf[o] = byte(val)
			p.buf[o+1] = byte(val >> 8)
		default:
			panicf("putfat fatbits")
		}
		p.flags |= bmod
		putsect(p)
	}
}

// falloc allocates and clears a cluster; 0 means the disk is full.
func falloc(xf *xfs) int {
	bp := xf.bp
	n := bp.freeptr
	for getfat(xf, n) != 0 {
		n++
		if n >= bp.fatclusters {
			n = fatresrv
		}
		if n == bp.freeptr {
			return 0
		}
	}
	bp.freeptr = n + 1
	if bp.freeptr >= bp.fatclusters {
		bp.freeptr = fatresrv
	}
	putfat(xf, n, 0xffff)
	k := clust2sect(bp, n)
	for i := 0; i < bp.clustsize; i++ {
		p := getosect(xf, k+i)
		if p == nil {
			return -1
		}
		clear(p.buf)
		p.flags = bmod
		putsect(p)
	}
	return n
}

// xputtime sets entry e's time to s, or now when s is 0.
func xputtime(e []byte, s int) {
	var t *daytime.Tm
	if s == 0 {
		t = daytime.Local((sys.Millisec()-nowt1)/1000 + nowt)
	} else {
		t = daytime.Local(s)
	}
	le.PutUint16(e[22:], uint16(t.Hour<<11|t.Min<<5|t.Sec>>1))
	le.PutUint16(e[24:], uint16((t.Year-80)<<9|(t.Mon+1)<<5|t.Mday))
}

func puttime(e []byte) { xputtime(e, 0) }

// gtime returns entry e's time; DOS keeps local time.
func gtime(e []byte) int {
	i := short(e[22:])
	tm := &daytime.Tm{Hour: i >> 11, Min: (i >> 5) & 63, Sec: (i & 31) << 1}
	i = short(e[24:])
	tm.Year = 80 + (i >> 9)
	tm.Mon = ((i >> 5) & 15) - 1
	tm.Mday = i & 31
	tm.Tzoff = tzoff
	return daytime.Tm2epoch(tm)
}

// putnamesect fills long name entry a with slot curslot of longname.
func putnamesect(longname []rune, curslot int, first bool, sum int, a []byte) {
	for i := range dosdirsize {
		a[i] = 0xff
	}
	if first {
		a[0] = byte(0x40 | curslot)
	} else {
		a[0] = byte(curslot)
	}
	a[11] = dLong
	a[12] = 0
	a[13] = byte(sum)
	a[26] = 0
	a[27] = 0
	n := len(longname)
	j := (curslot - 1) * dosrunes
	for _, r := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
		for i := r[0]; i < r[1]; i += 2 {
			c := 0
			if j < n {
				c = int(longname[j])
				j++
			}
			a[i] = byte(c)
			a[i+1] = byte(c >> 8)
			if c == 0 {
				return
			}
		}
	}
}

// putlongname writes the long name entries for name before ndp's slot,
// moving ndp to the slot for the short entry.
func putlongname(xf *xfs, ndp *dosptr, name, sname []rune) int {
	bp := xf.bp
	first := true
	sum := aliassum(sname)
	nds := (len(name) + dosrunes - 1) / dosrunes
	for ; nds > 0; nds-- {
		putnamesect(name, nds, first, sum, ndp.p.buf[ndp.offset:])
		first = false
		ndp.offset += dosdirsize
		if ndp.offset == bp.sectsize {
			if debug != 0 {
				chat(fmt.Sprintf("long name %s entry %d crossing sector, addr=%d, naddr=%d",
					string(name), nds, ndp.addr, ndp.naddr))
			}
			ndp.p.flags |= bmod
			putsect(ndp.p)
			ndp.p = nil
			ndp.prevaddr = ndp.addr
			ndp.addr = ndp.naddr
			ndp.naddr = -1
			if ndp.addr < 0 {
				return -1
			}
			ndp.p = getsect(xf, ndp.addr)
			if ndp.p == nil {
				return -1
			}
			ndp.offset = 0
		}
	}
	return 0
}

// aliassum is the checksum of an 11-character short name kept in its
// long name entries.
func aliassum(sname []rune) int {
	sum := 0
	for i := range 11 {
		sum = (((sum & 1) << 7) | ((sum & 0xfe) >> 1) + int(byte(sname[i]))) & 0xff
	}
	return sum
}
