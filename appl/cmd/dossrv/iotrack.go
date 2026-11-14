package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

// The device is read and written a track at a time through a small cache
// of tracks, least recently used first out. A sector handed out by
// getsect stays valid until putsect; a track with sectors out is never
// reused.

const (
	bmod   = 1 << 0
	bstale = 1 << 2

	niobuf     = 20
	sectorsize = 512
)

// xfs is one external file system: the device file and its format.
type xfs struct {
	name    string
	qid     sys.Qid // of the device file
	refn    int     // attach count
	rootqid styx.Qid
	dev     *os.File
	fd      int
	fmt     bool // format read successfully
	offset  int  // byte offset of the file system in the device
	bp      *dosbpb
}

type track struct {
	xf    *xfs
	addr  int
	flags int
	refn  int
	have  []bool // sectors handed out since the track was claimed
	buf   []byte
}

type iosect struct {
	t     *track
	flags int
	buf   []byte
}

var (
	sect2trk = 9
	trksize  = 9 * sectorsize
	tracks   []*track // most recently used first
	xfslist  []*xfs
	iodebug  bool
)

func iotrackinit(sectors int) {
	if sectors <= 0 {
		sectors = 9
	}
	sect2trk = sectors
	trksize = sect2trk * sectorsize
	tracks = make([]*track, niobuf)
	for i := range tracks {
		tracks[i] = &track{have: make([]bool, sect2trk), buf: make([]byte, trksize)}
	}
}

func getsect(xf *xfs, addr int) *iosect  { return getiosect(xf, addr, true) }
func getosect(xf *xfs, addr int) *iosect { return getiosect(xf, addr, false) }

// getiosect returns sector addr; with rflag the sector is read from the
// device if the cache does not hold it.
func getiosect(xf *xfs, addr int, rflag bool) *iosect {
	toff := addr % sect2trk
	t := getiotrack(xf, addr-toff)
	if rflag && t.flags&bstale != 0 {
		if tread(t) < 0 {
			return nil
		}
		t.flags &^= bstale
	}
	t.refn++
	t.have[toff] = true
	return &iosect{t: t, buf: t.buf[toff*sectorsize : (toff+1)*sectorsize]}
}

func putsect(p *iosect) {
	t := p.t
	t.flags |= p.flags
	p.flags = 0
	t.refn--
	if t.refn < 0 {
		panicf("putsect: refcount")
	}
}

// getiotrack finds or claims the cache entry for the track at addr.
func getiotrack(xf *xfs, addr int) *track {
	if iodebug {
		chat(fmt.Sprintf("iotrack %d,%d...", xf.fd, addr))
	}
	idx := -1
	for i, t := range tracks {
		if t.addr == addr && t.xf == xf {
			idx = i
			break
		}
	}
	if idx < 0 {
		for i := len(tracks) - 1; i >= 0; i-- {
			if tracks[i].refn == 0 {
				idx = i
				break
			}
		}
		if idx < 0 {
			panicf("iotrack all ref'd")
		}
		t := tracks[idx]
		if t.flags&bmod != 0 {
			twrite(t)
		}
		purgetrack(t)
		t.addr = addr
		t.xf = xf
		t.flags = bstale
	}
	t := tracks[idx]
	copy(tracks[1:idx+1], tracks[:idx])
	tracks[0] = t
	return t
}

func purgetrack(t *track) {
	if t.refn != 0 {
		panicf("purgetrack")
	}
	clear(t.have)
}

func twrite(t *track) int {
	if iodebug {
		chat(fmt.Sprintf("[twrite %d...", t.addr))
	}
	if t.flags&bstale != 0 {
		all := true
		for _, h := range t.have {
			all = all && h
		}
		if !all {
			if tread(t) < 0 {
				if iodebug {
					chat("error]")
				}
				return -1
			}
		} else {
			t.flags &^= bstale
		}
	}
	if devwrite(t.xf, t.addr, t.buf) < 0 {
		if iodebug {
			chat("error]")
		}
		return -1
	}
	if iodebug {
		chat(" done]")
	}
	return 0
}

// tread reads a track, keeping any sectors already handed out.
func tread(t *track) int {
	if iodebug {
		chat(fmt.Sprintf("[tread %d...", t.addr))
	}
	any := false
	for _, h := range t.have {
		any = any || h
	}
	tbuf := t.buf
	if any {
		tbuf = make([]byte, trksize)
	}
	if devread(t.xf, t.addr, tbuf) < 0 {
		if iodebug {
			chat("error]")
		}
		return -1
	}
	if any {
		for i, h := range t.have {
			if !h {
				copy(t.buf[i*sectorsize:(i+1)*sectorsize], tbuf[i*sectorsize:])
			}
		}
	}
	if iodebug {
		chat("done]")
	}
	t.flags &^= bstale
	return 0
}

// purgebuf writes back and forgets every track of xf.
func purgebuf(xf *xfs) {
	for _, t := range tracks {
		if t.xf != xf {
			continue
		}
		if t.flags&bmod != 0 {
			twrite(t)
		}
		t.flags = bstale
		purgetrack(t)
	}
}

func sync() {
	for _, t := range tracks {
		if t.flags&bmod != 0 {
			twrite(t)
			t.flags &^= bmod
		}
	}
}

func deverror(name string, xf *xfs, addr, n, nret int, err error) int {
	if nret < 0 {
		if iodebug {
			chat(fmt.Sprintf("%s errstr=\"%v\"...", name, err))
		}
		xf.dev = nil
		return -1
	}
	if iodebug {
		chat(fmt.Sprintf("dev %d sector %d, %s: %d, should be %d\n", xf.fd, addr, name, nret, n))
	}
	panicf("%s", name)
	return -1
}

func devread(xf *xfs, addr int, buf []byte) int {
	if xf.dev == nil {
		return -1
	}
	if _, err := sys.Seek(xf.fd, int64(xf.offset+addr*sectorsize), 0); err != nil {
		return deverror("read", xf, addr, trksize, -1, err)
	}
	n, err := io.ReadFull(xf.dev, buf[:trksize])
	if n != trksize {
		if n == 0 && err != nil && err != io.EOF {
			n = -1
		}
		return deverror("read", xf, addr, trksize, n, err)
	}
	return 0
}

func devwrite(xf *xfs, addr int, buf []byte) int {
	if xf.dev == nil {
		return -1
	}
	if _, err := sys.Seek(xf.fd, int64(xf.offset+addr*sectorsize), 0); err != nil {
		return deverror("write", xf, addr, trksize, -1, err)
	}
	n, err := xf.dev.Write(buf[:trksize])
	if n != trksize {
		if n == 0 && err != nil {
			n = -1
		}
		return deverror("write", xf, addr, trksize, n, err)
	}
	return 0
}

// devcheck reads the first sector, to notice a changed or missing medium.
func devcheck(xf *xfs) int {
	if xf.dev == nil {
		return -1
	}
	buf := make([]byte, sectorsize)
	if _, err := sys.Seek(xf.fd, 0, 0); err != nil {
		xf.dev = nil
		return -1
	}
	if n, _ := io.ReadFull(xf.dev, buf); n != sectorsize {
		xf.dev = nil
		return -1
	}
	return 0
}

// getxfs returns the file system on device name, which may end in
// ":offset" to give the file system's start in sectors.
func getxfs(name string) (*xfs, string) {
	if name == "" {
		return nil, "no file system device specified"
	}
	offset := 0
	for i := 0; i < len(name); i++ {
		if name[i] == ':' {
			offset = atoi(name[i+1:])
			if offset < 0 {
				return nil, "invalid device offset to file system"
			}
			offset *= sectorsize
			name = name[:i]
			break
		}
	}
	fd, err := sys.Open(name, sys.ORDWR)
	if err != nil {
		if iodebug {
			chat(fmt.Sprintf("getxfs: open(%s) failed: %s\n", name, sys.Errstr()))
		}
		return nil, "can't open " + name + ": " + sys.Errstr()
	}
	dir, err := sys.Fstat(fd)
	if err != nil {
		e := sys.Errstr()
		sys.Close(fd)
		return nil, "can't stat " + name + ": " + e
	}
	var fxf *xfs
	for _, xf := range xfslist {
		if xf.refn == 0 {
			if fxf == nil {
				fxf = xf
			}
			continue
		}
		if xf.qid.Path != dir.Qid.Path || xf.qid.Vers != dir.Qid.Vers {
			continue
		}
		if xf.name != name || xf.dev == nil {
			continue
		}
		if devcheck(xf) < 0 {
			continue
		}
		if offset != 0 && xf.offset != offset {
			continue
		}
		if iodebug {
			chat(fmt.Sprintf("incref \"%s\", dev=%d...", xf.name, xf.fd))
		}
		xf.refn++
		sys.Close(fd)
		return xf, ""
	}
	if fxf == nil {
		fxf = &xfs{}
		xfslist = append(xfslist, fxf)
	}
	if iodebug {
		chat(fmt.Sprintf("alloc \"%s\", dev=%d...", name, fd))
	}
	*fxf = xfs{name: name, refn: 1, qid: dir.Qid, dev: os.NewFile(uintptr(fd), name), fd: fd, offset: offset}
	return fxf, ""
}

func refxfs(xf *xfs, delta int) {
	xf.refn += delta
	if xf.refn == 0 {
		if iodebug {
			chat(fmt.Sprintf("free \"%s\", dev=%d...", xf.name, xf.fd))
		}
		purgebuf(xf)
		if xf.dev != nil {
			xf.dev.Close()
			xf.dev = nil
		}
	}
}

// atoi is Limbo's int of a string: leading sign and digits, else 0.
func atoi(s string) int {
	n, i, neg := 0, 0, false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		n = -n
	}
	return n
}
