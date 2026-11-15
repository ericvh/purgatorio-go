// Package disks opens disks and disk partitions for the partitioning and
// formatting commands, and reads and writes PC partition table entries
// (Inferno's appl/lib/disks.b, from Plan 9's libdisk).
//
// A disk is an sd(3) partition (a file in a directory with a ctl file,
// such as /dev/sdC0/data), a floppy(3) disk, or a plain file, such as a
// disk image.
package disks

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Disk is an open disk.
type Disk struct {
	Prefix string
	Part   string
	Fd     *os.File
	Wfd    *os.File
	Ctlfd  *os.File
	Rdonly bool
	Dtype  string // "file", "sd" or "floppy"

	Secs    int64
	Secsize int
	Size    int64
	Offset  int64 // within the larger disk
	Width   int   // of the disk size in bytes as a decimal string
	C, H, S int   // geometry: cylinders, heads, sectors
	Chssrc  string
}

// PC partition table layout.
const (
	Toffset    = 446 // offset of the table in sector 0
	TentrySize = 2 + 2*3 + 4 + 4
	NTentry    = 4
	Magic0     = 0x55
	Magic1     = 0xAA
	Active     = 0x80 // the partition is active
	Type9      = 0x39 // Plan 9 and Inferno

	oactive = 0
	ostarth = 1
	otype   = 4
	oendh   = 5
	oends   = 6
	oxlba   = 8
	oxsize  = 12
	omagic  = NTentry * TentrySize
)

func readn(f *os.File, b []byte) int {
	n, _ := io.ReadFull(f, b)
	return n
}

// partitiongeometry takes the geometry from the partition table in
// sector 0, if every partition ends on the same head and sector.
func partitiongeometry(d *Disk) bool {
	buf := make([]byte, 512)
	t := buf[Toffset:]
	ok := false
	if raw, err := os.Open(d.Prefix + "data"); err == nil && d.Prefix != "" {
		ok = readn(raw, buf) == 512 && t[omagic] == Magic0 && t[omagic+1] == Magic1
		raw.Close()
	}
	if !ok {
		if _, err := d.Fd.Seek(0, io.SeekStart); err != nil || readn(d.Fd, buf) != 512 ||
			t[omagic] != Magic0 || t[omagic+1] != Magic1 {
			return false
		}
	}
	h, s := -1, -1
	for i := 0; i < NTentry*TentrySize; i += TentrySize {
		if t[i+otype] == 0 {
			continue
		}
		t[i+oends] &= 63
		if h == -1 {
			h, s = int(t[i+oendh]), int(t[i+oends])
		} else if h != int(t[i+oendh]) || s != int(t[i+oends]) {
			return false
		}
	}
	if h == -1 {
		return false
	}
	d.H = h + 1
	d.S = s
	if d.H*d.S == 0 {
		return false
	}
	d.C = int(d.Secs / int64(d.H*d.S))
	d.Chssrc = "part"
	return true
}

// drivergeometry uses the geometry the driver gave, as a BIOS would
// translate it.
func drivergeometry(d *Disk) bool {
	if d.C == 0 || d.H == 0 || d.S == 0 {
		return false
	}
	d.Chssrc = "disk"
	if d.C < 1024 {
		return true
	}
	if d.H == 15 {
		d.H = 255
		d.C /= 17
		return true
	}
	for m := 2; m*d.H < 256; m *= 2 {
		if d.C/m < 1024 {
			d.C /= m
			d.H *= m
			return true
		}
	}
	d.H, d.S = 255, 63
	d.C = int(d.Secs / int64(d.H*d.S))
	return true
}

var guess = [][2]int{{64, 32}, {64, 63}, {128, 63}, {255, 63}}

func guessgeometry(d *Disk) {
	d.Chssrc = "guess"
	for _, g := range guess {
		if int64(1024*g[0]*g[1]) >= d.Secs {
			d.H, d.S = g[0], g[1]
			d.C = int(d.Secs / int64(d.H*d.S))
			return
		}
	}
	d.H, d.S = 255, 63
	d.C = int(d.Secs / int64(d.H*d.S))
}

func findgeometry(d *Disk) {
	d.H, d.S, d.C = 0, 0, 0
	if !partitiongeometry(d) && !drivergeometry(d) {
		guessgeometry(d)
	}
}

func openfile(d *Disk) *Disk {
	db, err := sys.Fstat(int(d.Fd.Fd()))
	if err != nil {
		return nil
	}
	d.Secsize = 512
	d.Size = int64(db.Length)
	d.Secs = d.Size / int64(d.Secsize)
	d.Offset = 0
	findgeometry(d)
	return mkwidth(d)
}

func opensd(d *Disk) *Disk {
	b := bufio.NewReader(d.Ctlfd)
	for {
		p, err := b.ReadString('\n')
		if p == "" && err != nil {
			break
		}
		f := strings.Fields(strings.TrimSuffix(p, "\n"))
		if len(f) >= 3 && f[0] == "geometry" {
			d.Secsize, _ = strconv.Atoi(f[2])
			if len(f) >= 6 {
				d.C, _ = strconv.Atoi(f[3])
				d.H, _ = strconv.Atoi(f[4])
				d.S, _ = strconv.Atoi(f[5])
			}
		}
		if len(f) >= 4 && f[0] == "part" && f[1] == d.Part {
			d.Offset, _ = strconv.ParseInt(f[2], 10, 64)
			end, _ := strconv.ParseInt(f[3], 10, 64)
			d.Secs = end - d.Offset
		}
		if err != nil {
			break
		}
	}
	d.Size = d.Secs * int64(d.Secsize)
	if d.Size <= 0 {
		d.Part = ""
		d.Dtype = "file"
		return openfile(d)
	}
	findgeometry(d)
	return mkwidth(d)
}

func open(name string, mode int) *os.File {
	fd, err := sys.Open(name, mode)
	if err != nil {
		return nil
	}
	return os.NewFile(uintptr(fd), name)
}

// Open opens the disk name with mode (sys.OREAD or sys.ORDWR); with
// noctl it is taken as a plain file.
func Open(name string, mode int, noctl bool) *Disk {
	d := &Disk{Rdonly: mode == sys.OREAD}
	if d.Fd = open(name, sys.OREAD); d.Fd == nil {
		return nil
	}
	if mode != sys.OREAD {
		if d.Wfd = open(name, sys.OWRITE); d.Wfd == nil {
			d.Rdonly = true
		}
	}
	if noctl {
		d.Dtype = "file"
		return openfile(d)
	}
	if len(name) >= 7 {
		q := name[len(name)-7:]
		if q[0] == 'f' && q[1] == 'd' && isdigit(q[2]) && q[3:] == "disk" {
			if d.Ctlfd = open(name[:len(name)-4]+"ctl", sys.ORDWR); d.Ctlfd != nil {
				d.Prefix = name[:len(name)-4]
				d.Dtype = "floppy"
				return openfile(d)
			}
		}
	}
	d.Prefix = ""
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		d.Prefix = name[:i+1]
	}
	if d.Prefix != "" {
		if d.Ctlfd = open(d.Prefix+"ctl", sys.ORDWR); d.Ctlfd != nil {
			d.Dtype = "sd"
			d.Part = name[len(d.Prefix):]
			return opensd(d)
		}
	}
	d.Dtype = "file"
	return openfile(d)
}

func mkwidth(d *Disk) *Disk {
	d.Width = len(strconv.FormatInt(d.Size, 10))
	return d
}

func isdigit(c byte) bool { return c >= '0' && c <= '9' }

func putchs(d *Disk, p []byte, lba int64) {
	s := int(lba % int64(d.S))
	h := int(lba / int64(d.S) % int64(d.H))
	c := int(lba / (int64(d.S) * int64(d.H)))
	if c >= 1024 {
		c = 1023
		h = d.H - 1
		s = d.S - 1
	}
	p[0] = byte(h)
	p[1] = byte((s+1)&0x3F | (c>>2)&0xC0)
	p[2] = byte(c)
}

// PCpart is a PC partition table entry.
type PCpart struct {
	Active int
	Ptype  int
	Base   int64 // 0, or the first extended partition in the chain
	Offset int64 // from Base to the partition, in sectors
	Size   int64 // in sectors
}

// Bytes is the entry in the table's format.
func (p PCpart) Bytes(d *Disk) []byte {
	a := make([]byte, TentrySize)
	a[oactive] = byte(p.Active)
	a[otype] = byte(p.Ptype)
	putchs(d, a[ostarth:], p.Base+p.Offset)
	putchs(d, a[oendh:], p.Base+p.Offset+p.Size-1)
	putle32(a[oxlba:], p.Offset)
	putle32(a[oxsize:], p.Size)
	return a
}

// Extract reads a table entry.
func Extract(a []byte) PCpart {
	return PCpart{Active: int(a[oactive]), Ptype: int(a[otype]), Offset: getle32(a[oxlba:]), Size: getle32(a[oxsize:])}
}

func getle32(p []byte) int64 {
	return int64(p[3])<<24 | int64(p[2])<<16 | int64(p[1])<<8 | int64(p[0])
}

func putle32(p []byte, i int64) {
	p[0], p[1], p[2], p[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
}

// Chstext is a cylinder-head-sector address as text.
func Chstext(p []byte) string {
	h := int(p[0])
	c := int(p[2]) | (int(p[1])&0xC0)<<2
	s := int(p[1]) & 0x3F
	return strconv.Itoa(c) + "/" + strconv.Itoa(h) + "/" + strconv.Itoa(s)
}
