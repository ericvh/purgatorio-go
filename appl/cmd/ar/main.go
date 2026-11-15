// Command ar maintains archives of files in the portable ASCII format of
// Plan 9 and Unix (Inferno ar(10.1)): it replaces (r), deletes (d),
// extracts (x), prints (p), moves (m), lists (t) or appends (q) members.
//
// Inferno's ar spills the archive to temporary files under /tmp when it
// holds more than a megabyte; this one keeps the archive in memory.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	armag   = "!<arch>\n"
	sarmag  = len(armag)
	arfmag0 = '`'
	arfmag1 = '\n'
	sarname = 16 // ancient limit

	// The printable archive header: name[16] date[12] uid[6] gid[6]
	// mode[8] size[10] fmag[2].
	oname    = 0
	lname    = sarname
	odate    = oname + lname
	ldate    = 12
	ouid     = odate + ldate
	luid     = 6
	ogid     = ouid + luid
	lgid     = 6
	omode    = ogid + lgid
	lmode    = 8
	osize    = omode + lmode
	lsize    = 10
	ofmag    = osize + lsize
	sarhdr   = ofmag + 2 // 60
	atomicio = 8192
)

const (
	man = "mrxtdpq"
	opt = "uvnbailo"
)

// member is one archive member. Its contents are in contents (with the
// padding byte, if read from an archive) or else in the file named by
// file.
type member struct {
	name                       string
	date, uid, gid, mode, size int
	contents                   []byte
	file                       *os.File
}

// arfile is a sequence of members to be written back.
type arfile struct{ mems []*member }

type file struct {
	name, trimmed string
	found         bool
}

// archive is an archive read into memory, with a read offset.
type archive struct {
	data []byte
	off  int
}

var (
	aflag, bflag, cflag, oflag, uflag, vflag bool

	pivotname string
	bout      = bufio.NewWriter(os.Stdout)
	stderr    = os.Stderr
	comfun    func(string, []*file)
	state     int // of bamatch
)

func fail(status string) {
	bout.Flush()
	sys.Exits(status)
}

func main() {
	args := os.Args
	if len(args) < 3 {
		usage()
	}
	args = args[1:]
	s := args[0]
	args = args[1:]
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'a':
			aflag = true
		case 'b', 'i':
			bflag = true
		case 'c':
			cflag = true
		case 'd':
			setcom(dcmd)
		case 'l': // ignored
		case 'm':
			setcom(mcmd)
		case 'o':
			oflag = true
		case 'p':
			setcom(pcmd)
		case 'q':
			setcom(qcmd)
		case 'r':
			setcom(rcmd)
		case 't':
			setcom(tcmd)
		case 'u':
			uflag = true
		case 'v':
			vflag = true
		case 'x':
			setcom(xcmd)
		default:
			fmt.Fprintf(stderr, "ar: bad option `%c'\n", s[i])
			usage()
		}
	}
	if aflag && bflag {
		fmt.Fprintf(stderr, "ar: only one of 'a' and 'b' can be specified\n")
		usage()
	}
	if aflag || bflag {
		pivotname = trim(args[0])
		args = args[1:]
		if len(args) < 2 {
			usage()
		}
	}
	if comfun == nil {
		if !uflag {
			fmt.Fprintf(stderr, "ar: one of [%s] must be specified\n", man)
			usage()
		}
		setcom(rcmd)
	}
	cp := args[0]
	files := make([]*file, 0, len(args)-1)
	for _, a := range args[1:] {
		files = append(files, &file{name: a, trimmed: trim(a)})
	}
	comfun(cp, files)
	allfound := true
	for _, f := range files {
		if !f.found {
			fmt.Fprintf(stderr, "ar: %s not found\n", f.name)
			allfound = false
		}
	}
	bout.Flush()
	if !allfound {
		fail("fail: file not found")
	}
}

func setcom(fun func(string, []*file)) {
	if comfun != nil {
		fmt.Fprintf(stderr, "ar: only one of [%s] allowed\n", man)
		usage()
	}
	comfun = fun
}

// rcmd does the r and u commands.
func rcmd(arname string, files []*file) {
	bar := openar(arname, sys.ORDWR, true)
	parts := []*arfile{{}, nil}
	ap := parts[0]
	if bar != nil {
		for mem := bar.rdhdr(); mem != nil; mem = bar.rdhdr() {
			if bamatch(mem.name, pivotname) {
				parts[1] = &arfile{}
				ap = parts[1]
			}
			f := match(files, mem.name)
			if f == nil {
				ap.copy(bar, mem)
				continue
			}
			f.found = true
			dfd, err := os.Open(f.name)
			if err != nil {
				if len(files) > 0 {
					fmt.Fprintf(stderr, "ar: cannot open %s: %s\n", f.name, sys.Errstr())
				}
				ap.copy(bar, mem)
				continue
			}
			if uflag {
				d, err := sys.Fstat(int(dfd.Fd()))
				if err != nil || int(d.Mtime) <= mem.date {
					if err != nil {
						fmt.Fprintf(stderr, "ar: cannot stat %s: %s\n", f.name, sys.Errstr())
					}
					dfd.Close()
					ap.copy(bar, mem)
					continue
				}
			}
			bar.skip(mem)
			mesg('r', f.name)
			mem.replace(f.name, dfd)
			ap.insert(mem)
		}
	}
	// Copy in the remaining files named on the command line.
	for _, f := range files {
		if f.found {
			continue
		}
		f.found = true
		dfd, err := os.Open(f.name)
		if err != nil {
			fmt.Fprintf(stderr, "ar: cannot open %s: %s\n", f.name, sys.Errstr())
			continue
		}
		mesg('a', f.name)
		mem := &member{}
		mem.replace(f.trimmed, dfd)
		parts[0].insert(mem)
	}
	install(arname, parts, bar == nil && !cflag)
}

func dcmd(arname string, files []*file) {
	if len(files) == 0 {
		return
	}
	changed := false
	parts := []*arfile{{}}
	bar := openar(arname, sys.ORDWR, false)
	for mem := bar.rdhdr(); mem != nil; mem = bar.rdhdr() {
		if match(files, mem.name) != nil {
			mesg('d', mem.name)
			bar.skip(mem)
			changed = true
		} else {
			parts[0].copy(bar, mem)
		}
	}
	if changed {
		install(arname, parts, false)
	}
}

func xcmd(arname string, files []*file) {
	bar := openar(arname, sys.OREAD, false)
	i := 0
	for mem := bar.rdhdr(); mem != nil; mem = bar.rdhdr() {
		f := match(files, mem.name)
		if f == nil {
			bar.skip(mem)
			continue
		}
		f.found = true
		fd, err := sys.Create(f.name, sys.OWRITE, uint32(mem.mode&0o777))
		if err != nil {
			fmt.Fprintf(stderr, "ar: cannot create %s: %s\n", f.name, sys.Errstr())
			bar.skip(mem)
		} else {
			mesg('x', f.name)
			out := os.NewFile(uintptr(fd), f.name)
			bar.copyout(mem, out)
			if oflag {
				d := sys.NullDir()
				d.Atime = uint32(mem.date)
				d.Mtime = uint32(mem.date)
				if sys.Fwstat(fd, &d) != nil {
					fmt.Fprintf(stderr, "ar: can't set times on %s: %s\n", f.name, sys.Errstr())
				}
			}
			out.Close()
		}
		if i++; len(files) > 0 && i >= len(files) {
			break
		}
	}
}

func pcmd(arname string, files []*file) {
	bar := openar(arname, sys.OREAD, false)
	i := 0
	for mem := bar.rdhdr(); mem != nil; mem = bar.rdhdr() {
		f := match(files, mem.name)
		if f == nil {
			bar.skip(mem)
			continue
		}
		bout.Flush()
		if vflag {
			fmt.Printf("\n<%s>\n\n", f.name)
		}
		bar.copyout(mem, os.Stdout)
		if i++; len(files) > 0 && i >= len(files) {
			break
		}
	}
}

func mcmd(arname string, files []*file) {
	if len(files) == 0 {
		return
	}
	parts := []*arfile{{}, {}, nil}
	bar := openar(arname, sys.ORDWR, false)
	ap := parts[0]
	for mem := bar.rdhdr(); mem != nil; mem = bar.rdhdr() {
		if bamatch(mem.name, pivotname) {
			parts[2] = &arfile{}
			ap = parts[2]
		}
		if f := match(files, mem.name); f != nil {
			mesg('m', f.name)
			parts[1].copy(bar, mem)
		} else {
			ap.copy(bar, mem)
		}
	}
	if pivotname != "" && parts[2] == nil {
		fmt.Fprintf(stderr, "ar: %s not found - files moved to end\n", pivotname)
	}
	install(arname, parts, false)
}

func tcmd(arname string, files []*file) {
	bar := openar(arname, sys.OREAD, false)
	for mem := bar.rdhdr(); mem != nil; mem = bar.rdhdr() {
		if f := match(files, mem.name); f != nil {
			longls := ""
			if vflag {
				longls = longtext(mem) + " "
			}
			bout.WriteString(longls + f.trimmed + "\n")
		}
		bar.skip(mem)
	}
}

func qcmd(arname string, files []*file) {
	if aflag || bflag {
		fmt.Fprintf(stderr, "ar: abi not allowed with q\n")
		fail("fail:usage")
	}
	fd := openrawar(arname, sys.ORDWR, true)
	if fd == nil {
		if !cflag {
			fmt.Fprintf(stderr, "ar: creating %s\n", arname)
		}
		fd = arcreate(arname)
	}
	fd.Seek(0, io.SeekEnd)
	for _, f := range files {
		f.found = true
		dfd, err := os.Open(f.name)
		if err != nil {
			fmt.Fprintf(stderr, "ar: cannot open %s: %s\n", f.name, sys.Errstr())
			continue
		}
		mesg('q', f.name)
		mem := &member{}
		mem.replace(f.trimmed, dfd)
		mem.write(fd)
	}
	fd.Close()
}

// openrawar opens an archive and checks its magic.
func openrawar(arname string, mode int, errok bool) *os.File {
	fd, err := sys.Open(arname, mode)
	if err != nil {
		if !errok {
			fmt.Fprintf(stderr, "ar: cannot open %s: %s\n", arname, sys.Errstr())
			fail("fail:error")
		}
		return nil
	}
	f := os.NewFile(uintptr(fd), arname)
	mbuf := make([]byte, sarmag)
	if n, _ := io.ReadFull(f, mbuf); n != sarmag || string(mbuf) != armag {
		fmt.Fprintf(stderr, "ar: %s not in archive format\n", arname)
		fail("fail:error")
	}
	return f
}

// openar reads an archive into memory, positioned after the magic.
func openar(arname string, mode int, errok bool) *archive {
	f := openrawar(arname, mode, errok)
	if f == nil {
		return nil
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		rderr()
	}
	return &archive{data: data}
}

func arcreate(arname string) *os.File {
	fd, err := sys.Create(arname, sys.OWRITE, 0o666)
	if err != nil {
		fmt.Fprintf(stderr, "ar: cannot create %s: %s\n", arname, sys.Errstr())
		fail("fail:create")
	}
	f := os.NewFile(uintptr(fd), arname)
	mustwrite(f, []byte(armag))
	return f
}

func wrerr() {
	fmt.Fprintf(stderr, "ar: write error: %s\n", sys.Errstr())
	fail("fail:write error")
}

func rderr() {
	fmt.Fprintf(stderr, "ar: read error: %s\n", sys.Errstr())
	fail("fail:read error")
}

func phaseerr(offset int) {
	fmt.Fprintf(stderr, "ar: phase error at offset %d\n", offset)
	fail("fail:phase error")
}

func usage() {
	fmt.Fprintf(stderr, "usage: ar [%s][%s] archive files ...\n", opt, man)
	fail("fail:usage")
}

// install writes the sequences of members out as the archive.
func install(arname string, seqs []*arfile, createflag bool) {
	if createflag {
		fmt.Fprintf(stderr, "ar: creating %s\n", arname)
	}
	fd := arcreate(arname)
	for _, ap := range seqs {
		if ap != nil {
			for _, mem := range ap.mems {
				mem.write(fd)
			}
		}
	}
	fd.Close()
}

// match returns the command line file matching name; an empty list
// matches everything.
func match(files []*file, name string) *file {
	if len(files) == 0 {
		return &file{name: name, trimmed: name}
	}
	for _, f := range files {
		if !f.found && f.trimmed == name {
			f.found = true
			return f
		}
	}
	return nil
}

// bamatch reports whether name is the pivot and the archive is at the
// right place for the a or b option.
func bamatch(name, pivot string) bool {
	switch state {
	case 0: // looking for the position file
		if aflag {
			if name == pivot {
				state = 1
			}
		} else if bflag && name == pivot {
			state = 2
			return true
		}
	case 1: // found: after the previous file
		state = 2
		return true
	}
	return false
}

func mesg(c byte, name string) {
	if vflag {
		fmt.Fprintf(bout, "%c - %s\n", c, name)
	}
}

// trim returns just the file name.
func trim(s string) string {
	s = strings.TrimRight(s, "/")
	return s[strings.LastIndexByte(s, '/')+1:]
}

var mtab = []string{"---", "--x", "-w-", "-wx", "r--", "r-x", "rw-", "rwx"}

func modes(mode int) string {
	return mtab[(mode>>6)&7] + mtab[(mode>>3)&7] + mtab[mode&7]
}

func longtext(mem *member) string {
	s := modes(mem.mode)
	s += fmt.Sprintf(" %3d/%1d", mem.uid, mem.gid)
	s += fmt.Sprintf(" %7d", uint32(mem.size))
	t := daytime.Text(daytime.Local(mem.date))
	return s + fmt.Sprintf(" %-12.12s %-4.4s ", t[4:], t[min(24, len(t)):])
}

// rdhdr reads the next member's header, or returns nil at the end.
func (b *archive) rdhdr() *member {
	if b.off == len(b.data) {
		return nil
	}
	if len(b.data)-b.off < sarhdr {
		sys.Werrstr("unexpected end-of-file")
		rderr()
	}
	buf := b.data[b.off : b.off+sarhdr]
	b.off += sarhdr
	i := oname + lname
	for ; i > oname; i-- {
		if buf[i-1] != '/' && buf[i-1] != ' ' {
			break
		}
	}
	mem := &member{name: string(buf[oname:i])}
	mem.date = intof(buf[odate:odate+ldate], 10)
	mem.uid = intof(buf[ouid:ouid+luid], 10)
	mem.gid = intof(buf[ogid:ogid+lgid], 10)
	mem.mode = intof(buf[omode:omode+lmode], 8)
	mem.size = intof(buf[osize:osize+lsize], 10)
	if buf[ofmag] != arfmag0 || buf[ofmag+1] != arfmag1 {
		phaseerr(sarmag + b.off - sarhdr)
	}
	return mem
}

// intof is String->toint of a header field without its trailing spaces;
// anything left over is an error.
func intof(a []byte, base int) int {
	for i := len(a); i > 0; i-- {
		if a[i-1] != ' ' {
			a = a[:i]
			break
		}
	}
	n, rest := toint(string(a), base)
	if rest != "" {
		fmt.Fprintf(stderr, "ar: invalid integer in archive member's header: %s\n", quote.Quote(string(a)))
		fail("fail:error")
	}
	return n
}

// toint is String->toint: white space, a sign and digits in base; with
// no digits it returns 0 and all of s.
func toint(s string, base int) (int, string) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	n, ok := 0, false
	for ; i < len(s); i++ {
		v := base
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'a' && c <= 'z':
			v = int(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			v = int(c-'A') + 10
		}
		if v >= base {
			break
		}
		ok = true
		n = n*base + v
	}
	if !ok {
		return 0, s
	}
	if neg {
		n = -n
	}
	return int(int32(n)), s[i:]
}

func (mem *member) wrhdr(fd *os.File) {
	b := []byte(strings.Repeat(" ", sarhdr))
	nm := []byte(mem.name)
	if len(nm) > lname {
		nm = nm[:lname]
	}
	copy(b[oname:], nm)
	copy(b[odate:], fmt.Sprintf("%-12d", uint32(mem.date)))
	copy(b[ouid:], fmt.Sprintf("%-6d", 0))
	copy(b[ogid:], fmt.Sprintf("%-6d", 0))
	copy(b[omode:], fmt.Sprintf("%-8o", uint32(mem.mode)))
	copy(b[osize:], fmt.Sprintf("%-10d", uint32(mem.size)))
	b[ofmag] = arfmag0
	b[ofmag+1] = arfmag1
	mustwrite(fd, b[:sarhdr])
}

// replace gives mem the contents of the file open on fd.
func (mem *member) replace(name string, fd *os.File) {
	d, err := sys.Fstat(int(fd.Fd()))
	if err != nil {
		fmt.Fprintf(stderr, "ar: cannot stat %s: %s\n", name, sys.Errstr())
		fail("fail:no stat")
	}
	mem.name = trim(name)
	mem.date = int(d.Mtime)
	mem.uid, mem.gid = 0, 0
	mem.mode = int(d.Mode & 0o777)
	mem.size = int(d.Length)
	if d.Length > 0x7fffffff {
		fmt.Fprintf(stderr, "ar: file %s too big\n", name)
		fail("fail:error")
	}
	mem.file = fd
	mem.contents = nil // copied from the file when needed
}

func mustwrite(fd *os.File, buf []byte) {
	if n, _ := fd.Write(buf); n != len(buf) {
		wrerr()
	}
}

// write writes mem, header and contents, to ofd.
func (mem *member) write(ofd *os.File) {
	mem.wrhdr(ofd)
	if mem.contents != nil {
		mustwrite(ofd, mem.contents)
		return
	}
	buf := make([]byte, atomicio)
	for nr := mem.size; nr > 0; {
		n, err := mem.file.Read(buf[:min(nr, len(buf))])
		if n <= 0 {
			if err == io.EOF || err == nil {
				sys.Werrstr("unexpected end-of-file")
			}
			rderr()
		}
		mustwrite(ofd, buf[:n])
		nr -= n
	}
	if mem.size&1 != 0 {
		mustwrite(ofd, []byte{'\n'})
	}
}

// read takes mem's contents, with any padding byte, from the archive.
func (b *archive) read(mem *member) {
	if mem.contents != nil {
		return
	}
	n := mem.size + mem.size&1
	if len(b.data)-b.off < n {
		sys.Werrstr("unexpected end-of-file")
		rderr()
	}
	mem.contents = b.data[b.off : b.off+n]
	b.off += n
}

func (b *archive) skip(mem *member) {
	b.off = min(b.off+mem.size+mem.size&1, len(b.data))
}

// copyout copies mem's contents from the archive to ofd.
func (b *archive) copyout(mem *member, ofd *os.File) {
	if len(b.data)-b.off < mem.size {
		mustwrite(ofd, b.data[b.off:])
		sys.Werrstr("unexpected end-of-file")
		rderr()
	}
	mustwrite(ofd, b.data[b.off:b.off+mem.size])
	b.skip(mem)
}

func (ap *arfile) copy(b *archive, mem *member) {
	b.read(mem)
	ap.insert(mem)
}

func (ap *arfile) insert(mem *member) { ap.mems = append(ap.mems, mem) }
