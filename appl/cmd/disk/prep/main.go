// Command prep edits the Plan 9 partition table of a disk partition
// (Inferno disk/prep, prep(8)). The table is text in the partition's
// second sector, a line "part name start end" for each subpartition, in
// sectors. prep runs the partition editor (see package pedit) on
// standard input; -r makes it read only. -p prints the ctl messages that
// give the table to sd(3); -w writes it and exits. -b starts from an
// empty table, and -a name lays out the named partitions automatically
// on an empty one. -s sets the sector size and -f takes the disk as a
// plain file.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/disks"
	"github.com/ericvh/purgatorio-go/sdk/pedit"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	blank    bool
	file     bool
	doauto   bool
	printflg bool
	opart    []*pedit.Part
	osecbuf  []byte
	rdonly   bool
	dowrite  bool
	edit     *pedit.Edit
)

type auto struct {
	name     string
	min, max int64
	weight   int
	alloc    bool
	size     int64
}

const (
	kb = 1024
	mb = kb * kb
	gb = kb * mb
)

// The layout order on disk.
var autos = []auto{
	{"9fat", 10 * mb, 100 * mb, 10, false, 0},
	{"nvram", 512, 512, 1, false, 0},
	{"fscfg", 512, 512, 1, false, 0},
	{"fs", 200 * mb, 0, 10, false, 0},
	{"fossil", 200 * mb, 0, 4, false, 0},
	{"arenas", 500 * mb, 0, 20, false, 0},
	{"isect", 25 * mb, 0, 1, false, 0},
	{"other", 200 * mb, 0, 4, false, 0},
	{"swap", 100 * mb, 512 * mb, 1, false, 0},
	{"cache", 50 * mb, 1 * gb, 2, false, 0},
}

func main() {
	edit = pedit.Mk("sector")
	edit.Add = cmdadd
	edit.Del = func(e *pedit.Edit, p *pedit.Part) string { return e.Delpart(p) }
	edit.Okname = cmdokname
	edit.Sum = cmdsum
	edit.Write = func(e *pedit.Edit) string { wrpart(e); return "" }

	secsize := 0
	a := arg.New(os.Args)
	a.SetUsage("disk/prep [-bfprw] [-a partname]... [-s sectorsize] /dev/sdC0/plan9")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			p := a.EArg()
			i := 0
			for ; i < len(autos); i++ {
				if p == autos[i].name {
					if autos[i].alloc {
						fmt.Fprintf(os.Stderr, "you said -a %s more than once.\n", p)
						a.Usage()
					}
					autos[i].alloc = true
					break
				}
			}
			if i == len(autos) {
				fmt.Fprintf(os.Stderr, "don't know how to create automatic partition %s\n", p)
				a.Usage()
			}
			doauto = true
		case 'b':
			blank = true
		case 'f':
			file = true
		case 'p':
			printflg = true
			rdonly = true
		case 'r':
			rdonly = true
		case 's':
			secsize, _ = strconv.Atoi(a.EArg())
		case 'w':
			dowrite = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) != 1 {
		a.Usage()
	}
	mode := sys.ORDWR
	if rdonly {
		mode = sys.OREAD
	}
	disk := disks.Open(args[0], mode, file)
	if disk == nil {
		fmt.Fprintf(os.Stderr, "cannot open disk: %s\n", sys.Errstr())
		pedit.Exits("opendisk")
	}
	if secsize != 0 {
		disk.Secsize = secsize
		disk.Secs = disk.Size / int64(secsize)
	}
	edit.End = disk.Secs
	checkfat(disk)
	osecbuf = make([]byte, disk.Secsize)
	edit.Disk = disk
	if !blank {
		rdpart(edit)
	}
	opart = append([]*pedit.Part(nil), edit.Part...)
	if printflg {
		edit.Runcmd("P")
		pedit.Exits("")
	}
	if doauto {
		autopart(edit)
	}
	if dowrite {
		edit.Runcmd("w")
		pedit.Exits("")
	}
	edit.Runcmd("p")
	for {
		fmt.Fprintf(os.Stderr, ">>> ")
		edit.Runcmd(edit.Getline())
	}
}

func cmdsum(e *pedit.Edit, p *pedit.Part, a, b int64) {
	c := ' '
	name := "empty"
	if p != nil {
		if p.Changed {
			c = '\''
		}
		name = p.Name
	}
	sz := (b - a) * int64(e.Disk.Secsize)
	suf := "B "
	div := int64(1)
	switch {
	case sz >= gb:
		suf, div = "GB", gb
	case sz >= mb:
		suf, div = "MB", mb
	case sz >= kb:
		suf, div = "KB", kb
	}
	w := e.Disk.Width
	if div == 1 {
		fmt.Printf("%c %-12s %*d %-*d (%d sectors, %d %s)\n", c, name, w, a, w, b, b-a, sz, suf)
	} else {
		fmt.Printf("%c %-12s %*d %-*d (%d sectors, %d.%.2d %s)\n", c, name, w, a, w, b, b-a,
			sz/div, (sz%div)*100/div, suf)
	}
}

func cmdadd(e *pedit.Edit, name string, start, end int64) string {
	if start < 2 && name == "9fat" {
		return "overlaps with the pbs and/or the partition table"
	}
	return e.Addpart(mkpart(name, start, end, true))
}

func isfrog(c byte) bool { return c < 0x20 || c == ' ' || c == '/' || c == 0x7f }

func cmdokname(_ *pedit.Edit, elem string) string {
	for i := 0; i < len(elem); i++ {
		if isfrog(elem[i]) {
			return "bad character in name"
		}
	}
	return ""
}

func mkpart(name string, start, end int64, changed bool) *pedit.Part {
	return &pedit.Part{Name: name, Ctlname: name, Start: start, End: end, Changed: changed}
}

// rdpart reads the table from the partition's second sector.
func rdpart(e *pedit.Edit) {
	d := e.Disk
	d.Fd.Seek(int64(d.Secsize), io.SeekStart)
	if n, _ := io.ReadFull(d.Fd, osecbuf); n != d.Secsize {
		return
	}
	tab := string(osecbuf)
	if i := strings.IndexByte(tab, 0); i >= 0 {
		tab = tab[:i]
	}
	if !strings.HasPrefix(tab, "part") {
		fmt.Fprintf(os.Stderr, "no plan9 partition table found\n")
		return
	}
	waserr := false
	for _, line := range strings.FieldsFunc(tab, func(r rune) bool { return r == '\n' }) {
		if !strings.HasPrefix(line, "part") {
			waserr = true
			continue
		}
		f := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' })
		if len(f) != 4 || f[0] != "part" {
			waserr = true
			continue
		}
		a, _ := strconv.ParseInt(f[2], 10, 64)
		b, _ := strconv.ParseInt(f[3], 10, 64)
		if a >= b {
			waserr = true
			continue
		}
		if err := e.Addpart(mkpart(f[1], a, b, false)); err != "" {
			fmt.Fprintf(os.Stderr, "?%s: not continuing\n", err)
			pedit.Exits("partition")
		}
	}
	if waserr {
		fmt.Fprintf(os.Stderr, "syntax error reading partition\n")
	}
}

func autopart(e *pedit.Edit) {
	if len(e.Part) > 0 {
		if doauto {
			fmt.Fprintf(os.Stderr, "partitions already exist; not repartitioning\n")
		}
		return
	}
	secs := e.Disk.Secs
	secsize := int64(e.Disk.Secsize)
	for {
		totw := 0
		for i := range autos {
			if autos[i].alloc && autos[i].size == 0 {
				totw += autos[i].weight
			}
		}
		if totw == 0 {
			break
		}
		if secs <= 0 {
			fmt.Fprintf(os.Stderr, "ran out of disk space during autopartition.\n")
			return
		}
		futz := false
		for i := range autos {
			if !autos[i].alloc || autos[i].size != 0 {
				continue
			}
			s := secs * int64(autos[i].weight) / int64(totw)
			if s < autos[i].min/secsize {
				autos[i].size = autos[i].min / secsize
				secs -= autos[i].size
				futz = true
				break
			}
		}
		if futz {
			continue
		}
		for i := range autos {
			if !autos[i].alloc || autos[i].size != 0 {
				continue
			}
			s := secs * int64(autos[i].weight) / int64(totw)
			if autos[i].max != 0 && s > autos[i].max/secsize {
				autos[i].size = autos[i].max / secsize
				secs -= autos[i].size
				futz = true
				break
			}
		}
		if futz {
			continue
		}
		for i := range autos {
			if !autos[i].alloc || autos[i].size != 0 {
				continue
			}
			s := secs * int64(autos[i].weight) / int64(totw)
			autos[i].size = s
			secs -= autos[i].size
			totw -= autos[i].weight
		}
	}
	for _, a := range autos {
		if a.alloc {
			fmt.Printf("%s %d\n", a.name, a.size)
		}
	}
	var s int64
	for _, a := range autos {
		if !a.alloc {
			continue
		}
		if err := e.Addpart(mkpart(a.name, s, s+a.size, true)); err != "" {
			fmt.Fprintf(os.Stderr, "addpart %s: %s\n", a.name, err)
		}
		s += a.size
	}
}

func restore(e *pedit.Edit, ctl *os.File) {
	offset := e.Disk.Offset
	fmt.Fprintf(os.Stderr, "attempting to restore partitions to previous state\n")
	if off, err := e.Disk.Wfd.Seek(int64(e.Disk.Secsize), io.SeekStart); err != nil || off != int64(e.Disk.Secsize) {
		fmt.Fprintf(os.Stderr, "cannot restore: error seeking on disk: %s\n", sys.Errstr())
		pedit.Exits("inconsistent")
	}
	if n, _ := e.Disk.Wfd.Write(osecbuf); n != e.Disk.Secsize {
		fmt.Fprintf(os.Stderr, "cannot restore: couldn't write old partition table to disk: %s\n", sys.Errstr())
		pedit.Exits("inconsistent")
	}
	if ctl != nil {
		for _, p := range e.Part {
			fmt.Fprintf(ctl, "delpart %s", p.Name)
		}
		for _, p := range opart {
			if _, err := fmt.Fprintf(ctl, "part %s %d %d", p.Name, p.Start+offset, p.End+offset); err != nil {
				fmt.Fprintf(os.Stderr, "restored disk partition table but not kernel table; reboot\n")
				pedit.Exits("inconsistent")
			}
		}
	}
	pedit.Exits("restored")
}

func wrpart(e *pedit.Edit) {
	d := e.Disk
	secbuf := make([]byte, d.Secsize)
	n := 0
	for _, p := range e.Part {
		a := fmt.Sprintf("part %s %d %d\n", p.Name, p.Start, p.End)
		if n+len(a) > d.Secsize {
			fmt.Fprintf(os.Stderr, "partition table bigger than sector (%d bytes)\n", d.Secsize)
			pedit.Exits("overflow")
		}
		copy(secbuf[n:], a)
		n += len(a)
	}
	if off, err := d.Wfd.Seek(int64(d.Secsize), io.SeekStart); err != nil || off != int64(d.Secsize) {
		fmt.Fprintf(os.Stderr, "error seeking to %d on disk: %s\n", d.Secsize, sys.Errstr())
		pedit.Exits("seek")
	}
	if k, _ := d.Wfd.Write(secbuf); k != d.Secsize {
		fmt.Fprintf(os.Stderr, "error writing partition table to disk: %s\n", sys.Errstr())
		restore(e, nil)
	}
	if d.Ctlfd != nil && !e.Ctldiff(d.Ctlfd) {
		fmt.Fprintf(os.Stderr, "?warning: partitions could not be updated in devsd\n")
	}
}

// checkfat refuses to write over a FAT boot sector where the table goes,
// as when editing a whole bootable disk.
func checkfat(d *disks.Disk) {
	buf := make([]byte, 32)
	if off, err := d.Fd.Seek(int64(d.Secsize), io.SeekStart); err != nil || off != int64(d.Secsize) {
		return
	}
	if n, _ := io.ReadFull(d.Fd, buf); n < len(buf) {
		return
	}
	if buf[0] != 0xEB || buf[1] != 0x3C || buf[2] != 0x90 {
		return
	}
	fmt.Fprintf(os.Stderr, "there's a fat partition where the\n"+
		"plan9 partition table would go.\n"+
		"if you really want to overwrite it, zero\n"+
		"the second sector of the disk and try again\n")
	pedit.Exits("fat partition")
}
