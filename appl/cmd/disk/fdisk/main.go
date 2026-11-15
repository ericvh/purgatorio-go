// Command fdisk edits the PC partition table in a disk's master boot
// record, and the extended partition tables it chains to (Inferno
// disk/fdisk, fdisk(8)). Positions are in cylinders. It runs the
// partition editor (see package pedit) on standard input with its own
// commands: A name makes a partition active, t name [type] sets its
// type, and R restores the tables as they were and
// exits. Primary partitions are named pN and secondary ones sN.
//
// Options: -a adds a Plan 9 partition in the largest free space if there
// is none; -b starts from an empty table; -p prints the ctl messages
// that give the partitions to sd(3); -w writes the table and exits; -r
// is read only; -s sets the sector size; -f takes the disk as a plain
// file; -v is accepted.
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
	blank     bool
	dowrite   bool
	file      bool
	rdonly    bool
	doauto    bool
	mbroffset int64
	printflag bool
	sec2cyl   int64
	written   bool
	edit      *pedit.Edit
)

const (
	active = 0x80

	typeEmpty     = 0x00
	typeDMDDO     = 0x54
	typeEXTHUGE   = 0x0F
	typeLINUXEXT  = 0x85
	type9         = disks.Type9
	tableSize     = disks.TentrySize * disks.NTentry
	omagic        = tableSize
	typeEXTENDED  = 0x05
	partNameTypes = 256
)

type ptype struct{ desc, name string }

var types = map[int]ptype{
	0x00:  {"EMPTY", ""},
	0x01:  {"FAT12", "dos"},
	0x04:  {"FAT16", "dos"},
	0x0B:  {"FAT32", "dos"},
	0x0C:  {"FAT32LBA", "dos"},
	0x0F:  {"EXTHUGE", ""},
	0x1C:  {"IBMRECOVERY", "ibm"},
	0x05:  {"EXTENDED", ""},
	0x06:  {"FATHUGE", "dos"},
	0xFF:  {"BB", "bb"},
	0x02:  {"XENIX", "xenix"},
	0x03:  {"XENIX USR", "xenixusr"},
	0x07:  {"HPFS", "ntfs"},
	0x08:  {"AIXBOOT", "aixboot"},
	0x09:  {"AIXDATA", "aixdata"},
	0x0A:  {"OS/2BOOT", "os2boot"},
	0x16:  {"UNFORMATTED", ""},
	0x17:  {"HPFS2", "hpfs2"},
	0x52:  {"CPM0", "cpm0"},
	0x54:  {"DMDDO", "dmdd0"},
	0x56:  {"GB", "gb"},
	0x61:  {"SPEEDSTOR", "speedstor"},
	0x63:  {"SYSV386", "sysv386"},
	0x64:  {"NETWARE", "netware"},
	0x75:  {"PCIX", "pcix"},
	0x80:  {"MINIXV1.3", "minix13"},
	0x81:  {"MINIXV1.5", "minix15"},
	0x82:  {"LINUXSWAP", "linuxswap"},
	0x83:  {"LINUX", "linux"},
	0x85:  {"LINUXEXTENDED", ""},
	0x93:  {"AMOEBA", "amoeba"},
	0x94:  {"AMOEBABB", "amoebaboot"},
	0xA5:  {"BSD386", "bsd386"},
	0xB7:  {"BSDI", "bsdi"},
	0xB8:  {"BSDISWAP", "bsdiswap"},
	0xDA:  {"OTHER", "other"},
	0xDB:  {"CPM", "cpm"},
	0xDE:  {"DELLRECOVERY", "dell"},
	0xE1:  {"SPEEDSTOR12", "speedstor"},
	0xE4:  {"SPEEDSTOR16", "speedstor"},
	0xFE:  {"LANSTEP", "lanstep"},
	type9: {"PLAN9", "plan9"},
}

type dospart struct {
	p       *pedit.Part
	pc      *disks.PCpart
	primary bool
	lba     int64 // absolute address
	size    int64
}

type recov struct {
	table []byte
	lba   int64
}

var dosparts []*dospart

func main() {
	edit = pedit.Mk("cylinder")
	edit.Add = cmdadd
	edit.Del = func(e *pedit.Edit, p *pedit.Part) string { return e.Delpart(p) }
	edit.Okname = cmdokname
	edit.Ext = cmdext
	edit.Help = cmdhelp
	edit.Sum = cmdsum
	edit.Write = func(e *pedit.Edit) string { wrpart(e); return "" }
	edit.Printctl = cmdprintctl

	secsize := 0
	a := arg.New(os.Args)
	a.SetUsage("disk/fdisk [-abfprvw] [-s sectorsize] /dev/sdC0/data")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			doauto = true
		case 'b':
			blank = true
		case 'f':
			file = true
		case 'p':
			printflag = true
		case 'r':
			rdonly = true
		case 's':
			secsize, _ = strconv.Atoi(a.EArg())
		case 'v':
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
	edit.Disk = disks.Open(args[0], mode, file)
	if edit.Disk == nil {
		fmt.Fprintf(os.Stderr, "cannot open disk: %s\n", sys.Errstr())
		pedit.Exits("opendisk")
	}
	if secsize != 0 {
		edit.Disk.Secsize = secsize
		edit.Disk.Secs = edit.Disk.Size / int64(secsize)
	}
	sec2cyl = int64(edit.Disk.H * edit.Disk.S)
	edit.End = edit.Disk.Secs / sec2cyl
	findmbr(edit)
	if blank {
		edit.Changed = true
	} else {
		rdpart(edit, 0, 0)
	}
	if doauto {
		autopart(edit)
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "fdisk: exception %v\n", r)
			if written {
				recover1(edit)
			}
			pedit.Exits("exception")
		}
	}()
	if dowrite {
		edit.Runcmd("w")
	}
	if printflag {
		edit.Runcmd("P")
	}
	if dowrite || printflag {
		pedit.Exits("")
	}
	fmt.Fprintf(os.Stderr, "cylinder = %d bytes\n", sec2cyl*int64(edit.Disk.Secsize))
	edit.Runcmd("p")
	for {
		fmt.Fprintf(os.Stderr, ">>> ")
		edit.Runcmd(edit.Getline())
	}
}

func tag2part(p *pedit.Part) *dospart {
	for _, d := range dosparts {
		if d.p.Tag == p.Tag {
			return d
		}
	}
	panic("tag2part: cannot happen")
}

func typestr0(t int) string {
	if ty, ok := types[t]; ok {
		return ty.desc
	}
	return fmt.Sprintf("type %d", t)
}

func gettable(d *disks.Disk, addr int64, mbr bool) []byte {
	table := make([]byte, tableSize+2)
	diskread(d, table, addr, disks.Toffset)
	if mbr && (table[omagic] != disks.Magic0 || table[omagic+1] != disks.Magic1) {
		sysfatal("did not find master boot record")
	}
	return table
}

func diskread(d *disks.Disk, data []byte, sec int64, off int) {
	a := sec*int64(d.Secsize) + int64(off)
	if o, err := d.Fd.Seek(a, io.SeekStart); err != nil || o != a {
		sysfatal(fmt.Sprintf("diskread seek %d.%d: %s", sec, off, sys.Errstr()))
	}
	if n, _ := io.ReadFull(d.Fd, data); n != len(data) {
		sysfatal(fmt.Sprintf("diskread %d at %d.%d: %s", len(data), sec, off, sys.Errstr()))
	}
}

func puttable(d *disks.Disk, table []byte, sec int64) bool {
	return diskwrite(d, table, sec, disks.Toffset)
}

func diskwrite(d *disks.Disk, data []byte, sec int64, off int) bool {
	written = true
	a := sec*int64(d.Secsize) + int64(off)
	if d.Wfd == nil {
		fmt.Fprintf(os.Stderr, "write %d bytes at %d.%d failed: %s\n", len(data), sec, off, "read only")
		return false
	}
	o, err := d.Wfd.Seek(a, io.SeekStart)
	if err == nil && o == a {
		var n int
		if n, err = d.Wfd.Write(data); err == nil && n == len(data) {
			return true
		}
	}
	fmt.Fprintf(os.Stderr, "write %d bytes at %d.%d failed: %s\n", len(data), sec, off, sys.Errstr())
	return false
}

var partgen, parttag int

func mkpart(name string, primary bool, lba, size int64, pc *disks.PCpart) *dospart {
	if name == "" {
		c := 's'
		if primary {
			c = 'p'
		}
		partgen++
		name = fmt.Sprintf("%c%d", c, partgen)
	}
	if pc == nil {
		pc = &disks.PCpart{}
	}
	parttag++
	p := &dospart{pc: pc, primary: primary, lba: lba, size: size}
	p.p = &pedit.Part{Name: name, Start: lba / sec2cyl, End: (lba + size) / sec2cyl,
		Ctlstart: lba, Ctlend: lba + size, Tag: parttag}
	dosparts = append(dosparts, p)
	return p
}

var rtabs []recov

func addrecover(t []byte, lba int64) {
	rtabs = append([]recov{{append([]byte(nil), t[:tableSize+2]...), lba}}, rtabs...)
}

// recover1 puts back the tables as they were read and exits.
func recover1(e *pedit.Edit) {
	bad := false
	for _, r := range rtabs {
		if !puttable(e.Disk, r.table, r.lba) {
			bad = true
		}
	}
	if bad {
		fmt.Fprintf(os.Stderr, "warning: some writes failed during restoration of old partition tables\n")
		pedit.Exits("inconsistent")
	}
	fmt.Fprintf(os.Stderr, "restored old partition tables\n")
	if ctl := e.Disk.Ctlfd; ctl != nil {
		offset := e.Disk.Offset
		for _, p := range e.Part {
			if p.Ctlname != "" {
				if _, err := fmt.Fprintf(ctl, "delpart %s", p.Ctlname); err != nil {
					fmt.Fprintf(os.Stderr, "delpart failed: %s: %s", p.Ctlname, sys.Errstr())
				}
			}
		}
		for _, p := range e.Ctlpart {
			if _, err := fmt.Fprintf(ctl, "delpart %s", p.Name); err != nil {
				fmt.Fprintf(os.Stderr, "delpart failed: %s: %s", p.Name, sys.Errstr())
			}
		}
		for _, p := range e.Ctlpart {
			if _, err := fmt.Fprintf(ctl, "part %s %d %d", p.Name, p.Start+offset, p.End+offset); err != nil {
				fmt.Fprintf(os.Stderr, "restored disk partition table but not kernel; reboot\n")
				pedit.Exits("inconsistent")
			}
		}
	}
	pedit.Exits("restored")
}

// rdpart reads the partition table at lba, and the extended tables it
// leads to, into the editor.
func rdpart(e *pedit.Edit, lba, xbase int64) {
	if xbase == 0 {
		xbase = lba
	}
	table := gettable(e.Disk, mbroffset+lba, lba == 0)
	addrecover(table, mbroffset+lba)
	for tp := 0; tp < tableSize; tp += disks.TentrySize {
		dp := disks.Extract(table[tp:])
		switch dp.Ptype {
		case typeEmpty:
		case typeEXTENDED, typeEXTHUGE, typeLINUXEXT:
			rdpart(e, xbase+dp.Offset, xbase)
		default:
			pc := dp
			p := mkpart("", lba == 0, lba+dp.Offset, dp.Size, &pc)
			if err := e.Addpart(p.p); err != "" {
				fmt.Fprintf(os.Stderr, "error adding partition: %s\n", err)
			}
		}
	}
}

func findmbr(e *pedit.Edit) {
	table := gettable(e.Disk, 0, true)
	for tp := 0; tp < tableSize; tp += disks.TentrySize {
		if disks.Extract(table[tp:]).Ptype == typeDMDDO {
			mbroffset = int64(e.Disk.S)
		}
	}
}

func haveroom(e *pedit.Edit, primary bool, start int64) bool {
	if primary {
		// Primary slots are taken by primary partitions and runs of
		// secondary ones.
		n := 0
		lastsec := false
		for _, ep := range e.Part {
			p := tag2part(ep)
			if p.primary {
				n++
				lastsec = false
			} else if !lastsec {
				n++
				lastsec = true
			}
		}
		return n < 4
	}
	// A secondary partition can go next to another secondary one, or
	// between primaries if a primary slot is free.
	n := 0
	for i, ep := range e.Part {
		p := tag2part(ep)
		if p.primary {
			n++
		}
		pend := p.p.End
		var q *dospart
		qstart := e.End
		if i+1 < len(e.Part) {
			q = tag2part(e.Part[i+1])
			qstart = q.p.Start
		}
		if start < pend || start >= qstart {
			continue
		}
		if !p.primary || q != nil && !q.primary {
			return true
		}
	}
	return n < 4
}

func autopart(e *pedit.Edit) {
	for _, ep := range e.Part {
		if tag2part(ep).pc.Ptype == type9 {
			return
		}
	}
	var start, bigsize, bigstart int64
	for _, ep := range e.Part {
		p := tag2part(ep)
		if p.p.Start > start && p.p.Start-start > bigsize && haveroom(e, true, start) {
			bigsize = p.p.Start - start
			bigstart = start
		}
		start = p.p.End
	}
	if e.End-start > bigsize && haveroom(e, true, start) {
		bigsize = e.End - start
		bigstart = start
	}
	if bigsize < 1 {
		fmt.Fprintf(os.Stderr, "couldn't find space or partition slot for plan 9 partition\n")
		return
	}
	act := active
	for _, ep := range e.Part {
		p := tag2part(ep)
		if p.primary && p.pc.Active&active != 0 {
			act = 0
		}
	}
	bigsize *= sec2cyl
	bigstart *= sec2cyl
	if bigstart == 0 {
		bigstart += int64(e.Disk.S)
		bigsize -= int64(e.Disk.S)
	}
	p := mkpart("", true, bigstart, bigsize, nil)
	p.p.Changed = true
	p.pc.Active = act
	p.pc.Ptype = type9
	e.Changed = true
	if err := e.Addpart(p.p); err != "" {
		fmt.Fprintf(os.Stderr, "error adding plan9 partition: %s\n", err)
	}
}

var namelist []string

// plan9print names the partition for sd(3) after its type, as plan9,
// dos, plan9.1, ...
func plan9print(part *dospart) {
	vname := types[part.pc.Ptype].name
	if vname == "" {
		part.p.Ctlname = ""
		return
	}
	sep := ""
	if c := vname[len(vname)-1]; c >= '0' && c <= '9' {
		sep = "."
	}
	name := vname
	for i := 0; ; {
		ok := true
		for _, n := range namelist {
			if name == n {
				i++
				name = fmt.Sprintf("%s%s%d", vname, sep, i)
				ok = false
			}
		}
		if ok {
			break
		}
	}
	namelist = append([]string{name}, namelist...)
	part.p.Ctlname = name
}

func cmdprintctl(e *pedit.Edit, w io.Writer) {
	namelist = nil
	for _, p := range e.Part {
		plan9print(tag2part(p))
	}
	e.Ctldiff(w)
}

func cmdokname(_ *pedit.Edit, name string) string {
	if name[0] != 'p' && name[0] != 's' || len(name) < 2 {
		return "name must be pN or sN"
	}
	for i := 1; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return "name must be pN or sN"
		}
	}
	return ""
}

const (
	kb = 1024
	mb = kb * kb
	gb = kb * mb
)

func cmdsum(e *pedit.Edit, vp *pedit.Part, a, b int64) {
	var p *dospart
	if vp != nil {
		p = tag2part(vp)
	}
	qual := " "
	if p != nil && p.p.Changed {
		qual = "'"
	}
	if p != nil && p.pc.Active&active != 0 {
		qual += "*"
	} else {
		qual += " "
	}
	name, ty := "empty", ""
	if p != nil {
		name = p.p.Name
		ty = " " + typestr0(p.pc.Ptype)
	}
	sz := (b - a) * int64(e.Disk.Secsize) * sec2cyl
	suf := "B"
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
		fmt.Printf("%s %-12s %*d %-*d (%d cylinders, %d %s)%s\n", qual, name, w, a, w, b, b-a, sz, suf, ty)
	} else {
		fmt.Printf("%s %-12s %*d %-*d (%d cylinders, %d.%.2d %s)%s\n", qual, name, w, a, w, b, b-a,
			sz/div, (sz%div)*100/div, suf, ty)
	}
}

func cmdadd(e *pedit.Edit, name string, start, end int64) string {
	if !haveroom(e, name[0] == 'p', start) {
		return "no room for partition"
	}
	start *= sec2cyl
	end *= sec2cyl
	if start == 0 || name[0] != 'p' {
		start += int64(e.Disk.S)
	}
	p := mkpart(name, name[0] == 'p', start, end-start, nil)
	p.p.Changed = true
	p.pc.Ptype = type9
	return e.Addpart(p.p)
}

const help = "A name - set partition active\n" +
	"P - print table in ctl format\n" +
	"R - restore disk back to initial configuration and exit\n" +
	"e - show empty dos partitions\n" +
	"t name [type] - set partition type\n"

func cmdhelp(*pedit.Edit) string {
	fmt.Printf("%s\n", help)
	return ""
}

func cmdactive(e *pedit.Edit, f []string) string {
	if len(f) != 2 {
		return "args"
	}
	if f[1][0] != 'p' {
		return "cannot set secondary partition active"
	}
	ep := e.Findpart(f[1])
	if ep == nil {
		return "unknown partition"
	}
	p := tag2part(ep)
	for _, x := range e.Part {
		ip := tag2part(x)
		if ip.pc.Active&active != 0 {
			ip.pc.Active &^= active
			ip.p.Changed = true
			e.Changed = true
		}
	}
	if p.pc.Active&active == 0 {
		p.pc.Active |= active
		p.p.Changed = true
		e.Changed = true
	}
	return ""
}

func dumplist() {
	n := 0
	for i := 0; i < partNameTypes; i++ {
		if t, ok := types[i]; ok {
			fmt.Printf("%-16s", t.desc)
			if n%4 == 3 {
				fmt.Printf("\n")
			}
			n++
		}
	}
	if n%4 != 0 {
		fmt.Printf("\n")
	}
}

func cmdtype(e *pedit.Edit, f []string) string {
	if len(f) < 2 {
		return "args"
	}
	ep := e.Findpart(f[1])
	if ep == nil {
		return "unknown partition"
	}
	p := tag2part(ep)
	var q string
	if len(f) == 2 {
		for {
			fmt.Fprintf(os.Stderr, "new partition type [? for list]: ")
			q = e.Getline()
			if strings.HasPrefix(q, "?") {
				dumplist()
			} else {
				break
			}
		}
	} else {
		q = f[2]
	}
	q = strings.TrimRight(strings.ToUpper(q), "\n")
	for i := 0; i < partNameTypes; i++ {
		if t, ok := types[i]; ok && t.desc == q {
			if p.pc.Ptype != i {
				p.pc.Ptype = i
				p.p.Changed = true
				e.Changed = true
			}
			break
		}
	}
	return ""
}

func cmdext(e *pedit.Edit, f []string) string {
	switch f[0][0] {
	case 'A':
		return cmdactive(e, f)
	case 't':
		return cmdtype(e, f)
	case 'R':
		recover1(e)
		return ""
	}
	return "unknown command"
}

func wrextend(e *pedit.Edit, i int, xbase, startlba int64) (int, int64) {
	d := e.Disk
	if i == len(e.Part) {
		endlba := d.Secs
		if startlba < endlba {
			wrzerotab(d, mbroffset+startlba)
		}
		return i, endlba
	}
	p := tag2part(e.Part[i])
	if p.primary {
		endlba := p.p.Start * sec2cyl
		if startlba < endlba {
			wrzerotab(d, mbroffset+startlba)
		}
		return i, endlba
	}
	table := gettable(d, mbroffset+startlba, false)
	ni, endlba := wrextend(e, i+1, xbase, p.p.End*sec2cyl)
	tp := wrtentry(d, table[0:], p.pc.Active, p.pc.Ptype, startlba, startlba+int64(d.S), p.p.End*sec2cyl)
	if p.p.End*sec2cyl != endlba {
		tp += wrtentry(d, table[tp:], 0, typeEXTENDED, xbase, p.p.End*sec2cyl, endlba)
	}
	for ; tp < tableSize; tp++ {
		table[tp] = 0
	}
	table[omagic] = disks.Magic0
	table[omagic+1] = disks.Magic1
	if !puttable(e.Disk, table, mbroffset+startlba) {
		recover1(e)
	}
	return ni, endlba
}

func wrzerotab(d *disks.Disk, addr int64) {
	table := make([]byte, tableSize+2)
	table[omagic] = disks.Magic0
	table[omagic+1] = disks.Magic1
	if !puttable(d, table, addr) {
		recover1(edit)
	}
}

func wrpart(e *pedit.Edit) {
	d := e.Disk
	table := gettable(d, mbroffset, false)
	tp := 0
	i := 0
	for i < len(e.Part) && tp < tableSize {
		p := tag2part(e.Part[i])
		s := p.p.Start * sec2cyl
		if p.p.Start == 0 {
			s = int64(d.S)
		}
		if p.primary {
			tp += wrtentry(d, table[tp:], p.pc.Active, p.pc.Ptype, 0, s, p.p.End*sec2cyl)
			i++
		} else {
			ni, endlba := wrextend(e, i, p.p.Start*sec2cyl, p.p.Start*sec2cyl)
			t := typeEXTENDED
			if endlba >= 1024*sec2cyl {
				t = typeEXTHUGE
			}
			tp += wrtentry(d, table[tp:], 0, t, 0, s, endlba)
			i = ni
		}
	}
	for ; tp < tableSize; tp++ {
		table[tp] = 0
	}
	if i != len(e.Part) {
		panic("wrpart: cannot happen #1")
	}
	if !puttable(d, table, mbroffset) {
		recover1(e)
	}
	namelist = nil
	for _, p := range e.Part {
		plan9print(tag2part(p))
	}
	if d.Ctlfd != nil && !e.Ctldiff(d.Ctlfd) {
		fmt.Fprintf(os.Stderr, "?warning: partitions could not be updated in devsd\n")
	}
}

func sysfatal(s string) {
	fmt.Fprintf(os.Stderr, "fdisk: %s\n", s)
	sys.Exits("fail:error")
}

func wrtentry(d *disks.Disk, entry []byte, act, ptype int, xbase, lba, end int64) int {
	pc := disks.PCpart{Active: act, Ptype: ptype, Base: xbase, Offset: lba - xbase, Size: end - lba}
	copy(entry, pc.Bytes(d))
	return disks.TentrySize
}
