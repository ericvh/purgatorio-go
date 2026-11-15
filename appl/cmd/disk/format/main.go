// Command format writes a FAT file system, or only a boot sector, on a
// disk partition or image file (Inferno disk/format, prep(8)). -d makes
// the FAT file system, holding the files named after the disk in its
// root; -b writes boot code from bootblock into the boot sector (-d uses
// a default one that says the disk is not bootable). -t gives the disk
// type: 3½HD (the default for a file), 3½DD, 3½QD, 5¼HD, 5¼DD or hard;
// -c the cluster size in sectors; -l the volume label; -r the number of
// reserved sectors. -f makes the file if it does not exist. -x leaves
// out the checks that guard against formatting a whole disk or a
// partition table.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/disks"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// ftype is a floppy type (all MFM).
type ftype struct {
	name    string
	bytes   int // per sector
	sectors int // per track
	heads   int
	tracks  int
	media   int // media descriptor byte
	cluster int // default cluster size
}

var floppytype = []ftype{
	{"3½HD", 512, 18, 2, 80, 0xf0, 1},
	{"3½DD", 512, 9, 2, 80, 0xf9, 2},
	{"3½QD", 512, 36, 2, 80, 0xf9, 2},
	{"5¼HD", 512, 15, 2, 80, 0xf9, 1},
	{"5¼DD", 512, 9, 2, 40, 0xfd, 2},
	{"hard", 512, 0, 0, 0, 0xf8, 4},
}

// Offsets in the DOS boot sector.
const (
	dbMagic      = 0
	dbVersion    = 3
	dbSectsize   = 11
	dbClustsize  = 13
	dbNresrv     = 14
	dbNfats      = 16
	dbRootsize   = 17
	dbVolsize    = 19
	dbMediadesc  = 21
	dbFatsize    = 22
	dbTrksize    = 24
	dbNheads     = 26
	dbNhidden    = 28
	dbBigvolsize = 32
	dbDriveno    = 36
	dbBootsig    = 38
	dbVolid      = 39
	dbLabel      = 43
	dbType       = 54

	dbVersionsize = 8
	dbLabelsize   = 11
	dbTypesize    = 8
	dbSize        = 62
)

// Offsets in a DOS directory entry.
const (
	ddName   = 0
	ddExt    = 8
	ddAttr   = 11
	ddTime   = 22
	ddDate   = 24
	ddStart  = 26
	ddLength = 28

	ddNamesize = 8
	ddExtsize  = 3
	ddSize     = 32

	dsystem = 0x04
)

// bootprog is the boot sector's default code.
var bootprog = func() []byte {
	b := make([]byte, 512)
	copy(b, []byte{0xEB, 0x3C, 0x90})
	copy(b[0x3E:], []byte{
		0xFA, 0xFC, 0x8C, 0xC8, 0x8E, 0xD8, 0x8E, 0xD0,
		0xBC, 0x00, 0x7C, 0xBE, 0x77, 0x7C, 0xE8, 0x19,
		0x00, 0x33, 0xC0, 0xCD, 0x16, 0xBB, 0x40, 0x00,
		0x8E, 0xC3, 0xBB, 0x72, 0x00, 0xB8, 0x34, 0x12,
		0x26, 0x89, 0x07, 0xEA, 0x00, 0x00, 0xFF, 0xFF,
		0xEB, 0xD6, 0xAC, 0x0A, 0xC0, 0x74, 0x09, 0xB4,
		0x0E, 0xBB, 0x07, 0x00, 0xCD, 0x10, 0xEB, 0xF2,
		0xC3})
	copy(b[0x77:], "Not a bootable disc or disc error\r\nPress almost any key to reboot...")
	b[0x1FE], b[0x1FF] = 0x55, 0xAA
	return b
}()

var (
	clustersize int
	fat         []byte
	fatbits     int
	fatsecs     int
	fatlast     int // last cluster allocated
	clusters    int
	volsecs     int
	root        []byte
	rootsecs    int
	rootfiles   int
	chatty      bool
	xflag       bool
	nresrv      = 1
	dos         bool
	fflag       bool
	file        string
	pbs         string
	typ         string
)

const (
	sof = 1 // start of file
	eof = 2 // end of file
)

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "format: %s\n", s)
	if fflag && file != "" {
		sys.Remove(file)
	}
	sys.Exits("fail:error")
}

func main() {
	writepbs := false
	label := []byte("CYLINDRICAL")
	a := arg.New(os.Args)
	a.SetUsage("disk/format [-df] [-b bootblock] [-c csize] [-l label] [-r nresrv] [-t type] disk [files ...]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'b':
			pbs = a.EArg()
			writepbs = true
		case 'd':
			dos = true
			writepbs = true
		case 'c':
			clustersize, _ = strconv.Atoi(a.EArg())
		case 'f':
			fflag = true
		case 'l':
			l := []byte(a.EArg())
			if len(l) > dbLabelsize {
				l = l[:dbLabelsize]
			}
			label = make([]byte, dbLabelsize)
			for i := range label {
				label[i] = ' '
			}
			copy(label, l)
		case 'r':
			nresrv, _ = strconv.Atoi(a.EArg())
		case 't':
			typ = a.EArg()
		case 'v':
			chatty = true
		case 'x':
			xflag = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	dev := args[0]
	disk := disks.Open(dev, sys.ORDWR, false)
	if disk == nil {
		if fflag {
			if fd, err := sys.Create(dev, sys.ORDWR, 0o666); err == nil {
				sys.Close(fd)
				disk = disks.Open(dev, sys.ORDWR, false)
			}
		}
		if disk == nil {
			fatal(fmt.Sprintf("opendisk %s: %s", quote.Quote(dev), sys.Errstr()))
		}
	}
	if disk.Dtype == "file" {
		fflag = true
	}
	if typ == "" {
		switch disk.Dtype {
		case "file":
			typ = "3½HD"
		case "floppy":
			disk.Ctlfd.Seek(0, io.SeekStart)
			buf := make([]byte, 10)
			n, _ := disk.Ctlfd.Read(buf)
			if n <= 0 || n >= 10 {
				fatal("reading floppy type")
			}
			typ = string(buf[:n])
		case "sd":
			typ = "hard"
		default:
			typ = "unknown"
		}
	}
	if !fflag && disk.Dtype == "floppy" {
		if _, err := fmt.Fprintf(disk.Ctlfd, "format %s", typ); err != nil {
			fatal(fmt.Sprintf("formatting floppy as %s: %s", typ, sys.Errstr()))
		}
	}
	if disk.Dtype != "floppy" && !xflag {
		sanitycheck(disk)
	}
	dosfs(dos, writepbs, disk, label, args[1:], false)
	dosfs(dos, writepbs, disk, label, args[1:], true)
	fmt.Printf("used %d bytes\n", int64(fatlast)*int64(clustersize)*int64(disk.Secsize))
}

// sanitycheck refuses to format a partition holding a Plan 9 partition
// table without -r 2, or a whole sd(3) disk.
func sanitycheck(disk *disks.Disk) {
	buf := make([]byte, 512)
	bad := false
	if dos && nresrv < 2 {
		if off, err := disk.Fd.Seek(int64(disk.Secsize), io.SeekStart); err == nil && off == int64(disk.Secsize) {
			if n, _ := io.ReadFull(disk.Fd, buf); n >= 5 && string(buf[:5]) == "part " {
				fmt.Fprintf(os.Stderr, "there's a plan9 partition on the disk\n"+
					"and you didn't specify -r 2 (or greater).\n"+
					"either specify -r 2 or -x to disable this check.\n")
				bad = true
			}
		}
	}
	if disk.Dtype == "sd" && disk.Offset == 0 {
		fmt.Fprintf(os.Stderr, "you're attempting to format your disk (/dev/sdXX/data)\n"+
			"rather than a partition such as /dev/sdXX/9fat;\n"+
			"this is probably a mistake.  specify -x to disable this check.\n")
		bad = true
	}
	if bad {
		sys.Exits("fail:failed disk sanity check")
	}
}

// getdriveno is the BIOS drive number of the disk: 0x80 for sdC0, 0x81
// for sdC1, and so on.
func getdriveno(disk *disks.Disk) int {
	if disk.Dtype != "sd" {
		return 0x80
	}
	name, err := sys.Fd2path(int(disk.Fd.Fd()))
	if err != nil || len(name) < 3 {
		return 0x80
	}
	b := []byte(name)
	if b[0] == '#' && b[1] == 'S' {
		b[1] = '/'
	}
	for p := b; len(p) >= 4; p = p[1:] {
		if p[0] == 's' && p[1] == 'd' && (p[2] == 'C' || p[2] == 'D') && (p[3] == '0' || p[3] == '1') {
			return 0x80 + int(p[2]-'C')*2 + int(p[3]-'0')
		}
	}
	return 0x80
}

func writen(f *os.File, buf []byte) int {
	tot := 0
	for tot < len(buf) {
		m := len(buf) - tot
		if m > 8192 {
			m = 8192
		}
		if n, err := f.Write(buf[tot : tot+m]); n != m || err != nil {
			break
		}
		tot += m
	}
	return tot
}

func dosfs(dofat, dopbs bool, disk *disks.Disk, label []byte, args []string, commit bool) {
	if !dofat && !dopbs {
		return
	}
	i := 0
	for ; i < len(floppytype); i++ {
		if typ == floppytype[i].name {
			break
		}
	}
	if i == len(floppytype) {
		fatal("unknown floppy type " + quote.Quote(typ))
	}
	t := floppytype[i]
	if t.sectors == 0 && typ == "hard" {
		t.sectors = disk.S
		t.heads = disk.H
		t.tracks = disk.C
	}
	if t.sectors == 0 && dofat {
		fatal(fmt.Sprintf("cannot format fat with type %s: geometry unknown", typ))
	}
	if fflag {
		disk.Size = int64(t.bytes * t.sectors * t.heads * t.tracks)
		disk.Secsize = t.bytes
		disk.Secs = disk.Size / int64(disk.Secsize)
	}
	secsize := disk.Secsize
	length := disk.Size

	// Make the disk full size if it is a file.
	if fflag && disk.Dtype == "file" {
		d, err := sys.Fstat(int(disk.Wfd.Fd()))
		if err != nil {
			fatal("fstat disk: " + sys.Errstr())
		}
		if commit && int64(d.Length) < disk.Size {
			if _, err := disk.Wfd.Seek(disk.Size-1, io.SeekStart); err != nil {
				fatal("seek to 9: " + sys.Errstr())
			}
			if _, err := disk.Wfd.Write([]byte{'9'}); err != nil {
				fatal("writing 9: " + sys.Errstr())
			}
		}
	}

	buf := make([]byte, secsize)
	if _, err := disk.Fd.Seek(0, io.SeekStart); err != nil {
		fatal("seek to boot sector: " + sys.Errstr())
	}
	if commit {
		if n, _ := io.ReadFull(disk.Fd, buf); n != secsize {
			fatal("reading boot sector: " + sys.Errstr())
		}
	}
	if dofat {
		for j := 0; j < dbSize; j++ {
			buf[j] = 0
		}
	}
	b := buf
	b[dbMagic+0] = 0xEB
	b[dbMagic+1] = 0x3C
	b[dbMagic+2] = 0x90
	copy(b[dbVersion:dbVersion+dbVersionsize], "Plan9.00")

	// The boot code starts at 0x3E, where the jump goes.
	if dopbs {
		pbsbuf := make([]byte, secsize)
		var npbs int
		if pbs != "" {
			f, err := os.Open(pbs)
			if err != nil {
				fatal(fmt.Sprintf("open %s: %s", pbs, sys.Errstr()))
			}
			npbs, err = f.Read(pbsbuf)
			f.Close()
			if err != nil && err != io.EOF {
				fatal(fmt.Sprintf("read %s: %s", pbs, sys.Errstr()))
			}
			if npbs > secsize-2 {
				fatal("boot block too large")
			}
		} else {
			copy(pbsbuf, bootprog)
			npbs = len(bootprog)
		}
		if npbs <= 0x3E {
			fmt.Fprintf(os.Stderr, "warning: pbs too small\n")
		} else {
			copy(buf[0x3E:], pbsbuf[0x3E:npbs])
		}
	}

	// The FAT BIOS parameter block.
	if dofat {
		if commit {
			fmt.Printf("Initializing FAT file system\n")
			fmt.Printf("type %s, %d tracks, %d heads, %d sectors/track, %d bytes/sec\n",
				t.name, t.tracks, t.heads, t.sectors, secsize)
		}
		if clustersize == 0 {
			clustersize = t.cluster
		}
		// The number of FAT bits depends on the space left after the
		// FATs, and the FATs' size on the number of clusters: iterate.
		for fatbits = 12; ; {
			volsecs = int(length / int64(secsize))
			clusters = 0
			for j := 0; ; j++ {
				fatsecs = (fatbits*clusters + 8*secsize - 1) / (8 * secsize)
				rootsecs = volsecs / 200
				rootfiles = rootsecs * (secsize / ddSize)
				if rootfiles > 512 {
					rootfiles = 512
					rootsecs = rootfiles / (secsize / ddSize)
				}
				data := nresrv + 2*fatsecs + (rootfiles*ddSize+secsize-1)/secsize
				newclusters := 2 + (volsecs-data)/clustersize
				if newclusters == clusters {
					break
				}
				clusters = newclusters
				if j > 10 {
					fatal(fmt.Sprintf("can't decide how many clusters to use (%d? %d?)", clusters, newclusters))
				}
				if chatty {
					fmt.Printf("clusters %d\n", clusters)
				}
				if clusters <= 1 {
					fatal("disk too small")
				}
			}
			if chatty {
				fmt.Printf("try %d fatbits => %d clusters of %d\n", fatbits, clusters, clustersize)
			}
			if clusters < 4087 || fatbits > 12 {
				break
			}
			fatbits = 16
		}
		if clusters >= 65527 {
			fatal("disk too big; implement fat32")
		}
		putshort(b[dbSectsize:], secsize)
		b[dbClustsize] = byte(clustersize)
		putshort(b[dbNresrv:], nresrv)
		b[dbNfats] = 2
		putshort(b[dbRootsize:], rootfiles)
		if volsecs < 1<<16 {
			putshort(b[dbVolsize:], volsecs)
		}
		b[dbMediadesc] = byte(t.media)
		putshort(b[dbFatsize:], fatsecs)
		putshort(b[dbTrksize:], t.sectors)
		putshort(b[dbNheads:], t.heads)
		putlong(b[dbNhidden:], int(disk.Offset))
		putlong(b[dbBigvolsize:], volsecs)

		// The extended BIOS parameter block.
		dno := 0
		if t.media == 0xF8 {
			dno = getdriveno(disk)
		}
		if chatty {
			fmt.Printf("driveno = %x\n", dno)
		}
		b[dbDriveno] = byte(dno)
		b[dbBootsig] = 0x29
		x := int(disk.Offset + int64(b[dbNfats])*int64(fatsecs) + int64(nresrv))
		putlong(b[dbVolid:], x)
		if chatty {
			fmt.Printf("volid = %x\n", x)
		}
		copy(b[dbLabel:dbLabel+dbLabelsize], label)
		r := fmt.Sprintf("FAT%d    ", fatbits)
		copy(b[dbType:dbType+dbTypesize], r)
	}
	b[secsize-2] = disks.Magic0
	b[secsize-1] = disks.Magic1
	if commit {
		if _, err := disk.Wfd.Seek(0, io.SeekStart); err != nil {
			fatal("seek to boot sector: " + sys.Errstr() + "\n")
		}
		if n, _ := disk.Wfd.Write(b); n != secsize {
			fatal("writing to boot sector: " + sys.Errstr())
		}
	}
	if !dofat {
		return
	}

	// The FAT, the root directory, then the files.
	fat = make([]byte, fatsecs*secsize)
	fat[0] = byte(t.media)
	fat[1] = 0xff
	fat[2] = 0xff
	if fatbits == 16 {
		fat[3] = 0xff
	}
	fatlast = 1
	root = make([]byte, rootsecs*secsize)
	if _, err := disk.Wfd.Seek(int64((nresrv+2*fatsecs+rootsecs)*secsize), io.SeekStart); err != nil {
		fatal("seek to files: " + sys.Errstr())
	}
	p := 0
	for _, name := range args {
		if p >= rootsecs*secsize {
			fatal("too many files in root")
		}
		f, err := os.Open(name)
		if err != nil {
			fatal(fmt.Sprintf("open %s: %s", name, sys.Errstr()))
		}
		d, err := sys.Fstat(int(f.Fd()))
		if err != nil {
			fatal(fmt.Sprintf("stat %s: %s", name, sys.Errstr()))
		}
		if d.Length >= 0x7FFFFFFF {
			fatal(fmt.Sprintf("file %s too big (%d bytes)", name, d.Length))
		}
		if commit {
			fmt.Printf("Adding file %s, length %d\n", name, d.Length)
		}
		x := 0
		length = int64(d.Length)
		if length > 0 {
			// Read the whole file, rounded up to a cluster, and
			// write it to the files area.
			csize := int64(secsize * clustersize)
			length = (length + csize - 1) / csize * csize
			fbuf := make([]byte, length)
			if nr, _ := io.ReadFull(f, fbuf[:d.Length]); nr != int(d.Length) {
				if nr >= 0 {
					sys.Werrstr("short read")
				}
				fatal(fmt.Sprintf("read %s: %s", name, sys.Errstr()))
			}
			if commit && writen(disk.Wfd, fbuf) != len(fbuf) {
				fatal(fmt.Sprintf("write %s: %s", name, sys.Errstr()))
			}
			// Allocate the clusters where the file went.
			length /= csize
			x = clustalloc(sof)
			for n := 0; n < int(length)-1; n++ {
				clustalloc(0)
			}
			clustalloc(eof)
		}
		f.Close()
		fmt.Fprintf(os.Stderr, "add %s at clust %x\n", d.Name, x)
		addrname(root[p:], d, name, x)
		p += ddSize
	}

	if commit {
		if _, err := disk.Wfd.Seek(int64(nresrv*secsize), io.SeekStart); err != nil {
			fatal("seek to fat #1: " + sys.Errstr())
		}
		if _, err := disk.Wfd.Write(fat); err != nil {
			fatal("writing fat #1: " + sys.Errstr())
		}
		if _, err := disk.Wfd.Write(fat); err != nil {
			fatal("writing fat #2: " + sys.Errstr())
		}
		if _, err := disk.Wfd.Write(root); err != nil {
			fatal("writing root: " + sys.Errstr())
		}
	}
}

// clustalloc allocates a cluster and links the last one to it.
func clustalloc(flag int) int {
	if flag != sof {
		x := fatlast + 1
		if flag == eof {
			x = 0xffff
		}
		if fatbits == 12 {
			x &= 0xfff
			o := 3 * fatlast / 2
			if fatlast&1 != 0 {
				fat[o] = byte(int(fat[o])&0x0f | x<<4)
				fat[o+1] = byte(x >> 4)
			} else {
				fat[o] = byte(x)
				fat[o+1] = byte(int(fat[o+1])&0xf0 | (x>>8)&0x0F)
			}
		} else {
			o := 2 * fatlast
			fat[o] = byte(x)
			fat[o+1] = byte(x >> 8)
		}
	}
	if flag == eof {
		return 0
	}
	fatlast++
	if fatlast >= clusters {
		fatal(fmt.Sprintf("data does not fit on disk (%d %d)", fatlast, clusters))
	}
	return fatlast
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c + 'A' - 'a'
	}
	return c
}

func putname(p string, buf []byte) {
	for i := 0; i < ddNamesize+ddExtsize; i++ {
		buf[ddName+i] = ' '
	}
	for i := 0; i < ddNamesize && i < len(p) && p[i] != '.'; i++ {
		buf[ddName+i] = upper(p[i])
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			ext := p[i+1:]
			for j := 0; j < ddExtsize && j < len(ext); j++ {
				buf[ddExt+j] = upper(ext[j])
			}
			break
		}
	}
}

func puttime(buf []byte) {
	t := daytime.Local(daytime.Now())
	x := t.Hour<<11 | t.Min<<5 | t.Sec>>1
	buf[ddTime+0] = byte(x)
	buf[ddTime+1] = byte(x >> 8)
	x = (t.Year-80)<<9 | (t.Mon+1)<<5 | t.Mday
	buf[ddDate+0] = byte(x)
	buf[ddDate+1] = byte(x >> 8)
}

func addrname(buf []byte, dir *sys.Dir, name string, start int) {
	s := name
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			s = s[i+1:]
			break
		}
	}
	putname(s, buf)
	if s == "9load" {
		buf[ddAttr] = dsystem
	} else {
		buf[ddAttr] = 0
	}
	puttime(buf)
	buf[ddStart+0] = byte(start)
	buf[ddStart+1] = byte(start >> 8)
	buf[ddLength+0] = byte(dir.Length)
	buf[ddLength+1] = byte(dir.Length >> 8)
	buf[ddLength+2] = byte(dir.Length >> 16)
	buf[ddLength+3] = byte(dir.Length >> 24)
}

func putshort(b []byte, v int) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

func putlong(b []byte, v int) {
	putshort(b, v)
	putshort(b[2:], v>>16)
}
