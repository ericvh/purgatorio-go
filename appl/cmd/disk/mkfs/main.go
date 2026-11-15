// Command mkfs copies the files that proto files name into a file system
// or an archive (Inferno disk/mkfs, mkfs(8)). By default the files go to
// a kfs mounted on /n/kfs; with -n name mkfs tells that kfs's console
// (kfscmd(8)) to allow any change while it works. -d root copies them
// under root instead, owned by the user running mkfs unless -S; -a
// writes an archive on standard output, which mkext(8) unpacks. Files
// already up to date are not copied again unless -r; -p then sets their
// modes. -x lists the files with their times and lengths instead of
// copying (with -a). -s takes source files from below src-fs, -u copies
// userfile to /adm/users first (for kfs), -U and -G set every file's
// owner and group, -z the kfs block size, and -v lists the files copied.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const prog = "mkfs"

// Kinds of destination.
const (
	kfs = iota
	fs
	archive
)

type file struct {
	new, elem, old string
	uid, gid       string
	mode           uint32
}

var (
	lines    []string // the proto being read
	next     int
	bout     *bufio.Writer // standard output, for an archive
	newfile  string
	oldfile  string
	proto    string
	users    string
	oldroot  string
	newroot  string
	lineno   int
	buflen   = 1024 - 8
	indent   int
	verb     bool
	modes    bool
	ream     bool
	debug    bool
	xflag    bool
	sfd      *os.File
	fskind   int
	user     string
	usrid    string
	grpid    string
	setuid   bool
	buf, zbf []byte
)

func main() {
	sys.Pctl(sys.NEWPGRP|sys.FORKNS|sys.FORKFD, nil)
	user = getuser()
	if user == "" {
		user = "none"
	}
	name := ""
	newroot = "/n/kfs"
	fskind = kfs
	a := arg.New(os.Args)
	a.SetUsage("mkfs [-aprvxS] [-d root] [-n kfscmdname] [-s src-fs] [-u userfile] [-z n] [-G group] [-U user] proto ...")
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'a':
			fskind = archive
			newroot = ""
			bout = bufio.NewWriter(os.Stdout)
		case 'd':
			fskind = fs
			newroot = a.EArg()
		case 'D':
			debug = true
		case 'n':
			name = a.EArg()
		case 'p':
			modes = true
		case 'q':
		case 'r':
			ream = true
		case 's':
			oldroot = a.EArg()
		case 'u':
			users = a.EArg()
		case 'v':
			verb = true
		case 'x':
			xflag = true
		case 'z':
			buflen, _ = strconv.Atoi(a.EArg())
			buflen -= 8 // qid.path and tag at the end of each kfs block
		case 'U':
			usrid = a.EArg()
		case 'G':
			grpid = a.EArg()
		case 'S':
			setuid = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	if buflen <= 0 {
		buflen = 1024 - 8
	}
	buf = make([]byte, buflen)
	zbf = make([]byte, buflen)
	if name != "" {
		openkfscmd(name)
	}
	kfscmd("allow")
	if users != "" {
		proto = "users"
		setusers()
	}
	errs := 0
	for _, p := range args {
		proto = p
		fmt.Fprintf(os.Stderr, "processing %s\n", proto)
		b, err := os.ReadFile(proto)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: can't open %s: %s: skipping\n", prog, quote.Quote(proto), sys.Errstr())
			errs++
			continue
		}
		lines = splitlines(string(b))
		next = 0
		lineno = 0
		indent = 0
		mkfs(&file{}, -1)
	}
	fmt.Fprintf(os.Stderr, "file system made\n")
	kfscmd("disallow")
	kfscmd("sync")
	if errs > 0 {
		quit("skipped protos")
	}
	if fskind == archive {
		bout.WriteString("end of archive\n")
		if err := bout.Flush(); err != nil {
			errorf("write error: " + sys.Errstr())
		}
	}
}

func splitlines(s string) []string {
	var l []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			l = append(l, s)
			break
		}
		l = append(l, s[:i+1])
		s = s[i+1:]
	}
	return l
}

func quit(why string) {
	if bout != nil {
		bout.Flush()
	}
	if why != "" {
		sys.Exits("fail:" + why)
	}
	os.Exit(0)
}

func mkfs(me *file, level int) {
	child, fp := getfile(me)
	if child == nil {
		return
	}
	if child.elem == "+" || child.elem == "*" || child.elem == "%" {
		rec := child.elem == "+"
		filesonly := child.elem == "%"
		child.new = me.new
		setnames(child)
		mktree(child, rec, filesonly)
		child, fp = getfile(me)
	}
	for child != nil && indent > level {
		if mkfile(child) {
			mkfs(child, indent)
		}
		child, fp = getfile(me)
	}
	if child != nil {
		next = fp
		lineno--
	}
}

func mktree(me *file, rec, filesonly bool) {
	fd, err := sys.Open(oldfile, sys.OREAD)
	if err != nil {
		warn("can't open " + quote.Quote(oldfile) + ": " + sys.Errstr())
		return
	}
	defer sys.Close(fd)
	child := *me
	seen := map[string]bool{}
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			break
		}
		for i := range ds {
			d := ds[i]
			if seen[d.Name] {
				continue
			}
			seen[d.Name] = true
			if filesonly && d.Mode&sys.DMDIR != 0 {
				continue
			}
			child.new = mkpath(me.new, d.Name)
			if me.old != "" {
				child.old = mkpath(me.old, d.Name)
			}
			child.elem = d.Name
			setnames(&child)
			if copyfile(&child, &d, true) && rec {
				mktree(&child, rec, filesonly)
			}
		}
	}
}

func mkfile(f *file) bool {
	d, err := sys.Stat(oldfile)
	if err != nil {
		warn("can't stat file " + quote.Quote(oldfile) + ": " + sys.Errstr())
		skipdir()
		return false
	}
	return copyfile(f, d, false)
}

func copyfile(f *file, d *sys.Dir, permonly bool) bool {
	if xflag && bout != nil {
		fmt.Fprintf(bout, "%s\t%d\t%d\n", quote.Quote(f.new), d.Mtime, int64(d.Length))
		return d.Mode&sys.DMDIR != 0
	}
	d.Name = f.elem
	if d.Type != 'M' && d.Type != 'U' {
		d.UID = "inferno"
		d.GID = "inferno"
		mode := (d.Mode >> 6) & 7
		d.Mode |= mode | mode<<3
	}
	if f.uid != "-" {
		d.UID = f.uid
	}
	if f.gid != "-" {
		d.GID = f.gid
	}
	if fskind == fs && !setuid {
		d.UID = user
		d.GID = user
	}
	if usrid != "" {
		d.UID = usrid
	}
	if grpid != "" {
		d.GID = grpid
	}
	if f.mode != ^uint32(0) {
		switch {
		case permonly:
			d.Mode = d.Mode&^0o666 | f.mode&0o666
		case d.Mode&sys.DMDIR != f.mode&sys.DMDIR:
			warn("inconsistent mode for " + f.new)
		default:
			d.Mode = f.mode
		}
	}
	if !uptodate(d, newfile) {
		if d.Mode&sys.DMDIR != 0 {
			mkdir(d)
		} else {
			if verb {
				fmt.Fprintf(os.Stderr, "%s\n", quote.Quote(f.new))
			}
			copy1(d)
		}
	} else if modes {
		nd := sys.NullDir()
		nd.Mode = d.Mode
		nd.Mtime = d.Mtime
		nd.GID = d.GID
		if err := sys.Wstat(newfile, &nd); err != nil {
			warn("can't set modes for " + quote.Quote(f.new) + ": " + sys.Errstr())
		}
		// The owner separately, since file systems differ in what
		// they allow.
		nd = sys.NullDir()
		nd.UID = d.UID
		sys.Wstat(newfile, &nd)
	}
	return d.Mode&sys.DMDIR != 0
}

// uptodate reports whether newf is no older than the file df describes.
func uptodate(df *sys.Dir, newf string) bool {
	if fskind == archive || ream {
		return false
	}
	dt, err := sys.Stat(newf)
	if err != nil {
		return false
	}
	return dt.Mtime >= df.Mtime
}

func iszero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func copy1(d *sys.Dir) {
	f, err := os.Open(oldfile)
	if err != nil {
		warn("can't open " + quote.Quote(oldfile) + ": " + sys.Errstr())
		return
	}
	defer f.Close()
	var t *os.File
	var tfd int
	if fskind == archive {
		arch(d)
	} else {
		i := strings.LastIndexByte(newfile, '/')
		dname := newfile[:i+1]
		if i < 0 || i == len(newfile)-1 {
			errorf(fmt.Sprintf("internal temporary file error (%s)", dname))
		}
		cptmp := dname + "__mkfstmp"
		tfd, err = sys.Create(cptmp, sys.OWRITE, 0o666)
		if err != nil {
			warn("can't create " + quote.Quote(newfile) + ": " + sys.Errstr())
			return
		}
		t = os.NewFile(uintptr(tfd), cptmp)
		defer t.Close()
	}
	var tot, hole int64
	for {
		n, err := io.ReadFull(f, buf)
		if n == 0 {
			if err != nil && err != io.EOF {
				warn("can't read " + quote.Quote(oldfile) + ": " + sys.Errstr())
			}
			break
		}
		switch {
		case fskind == archive:
			if k, err := bout.Write(buf[:n]); k != n || err != nil {
				errorf("write error: " + sys.Errstr())
			}
		case n == buflen && iszero(buf):
			// A block of zeros is left as a hole, which kfs reads
			// back as zeros.
			hole += int64(n)
		default:
			if hole > 0 {
				if _, err := t.Seek(hole, io.SeekCurrent); err != nil {
					errorf("can't write zeros to " + quote.Quote(newfile) + ": " + sys.Errstr())
				}
				hole = 0
			}
			if k, err := t.Write(buf[:n]); k < n || err != nil {
				errorf("can't write " + quote.Quote(newfile) + ": " + sys.Errstr())
			}
		}
		tot += int64(n)
		if n < buflen {
			break
		}
	}
	if hole > 0 {
		// A file cannot end in a hole: write its last byte.
		if _, err := t.Seek(hole-1, io.SeekCurrent); err != nil {
			errorf("can't write zeros to " + quote.Quote(newfile) + ": " + sys.Errstr())
		}
		if _, err := t.Write([]byte{0}); err != nil {
			errorf("can't write " + quote.Quote(newfile) + ": " + sys.Errstr())
		}
	}
	if tot != int64(d.Length) {
		warn(fmt.Sprintf("wrong number bytes written to %s (was %d should be %d)", newfile, tot, int64(d.Length)))
		if fskind == archive {
			warn("seeking to proper position")
			if int64(d.Length) > tot {
				bout.Write(make([]byte, int64(d.Length)-tot))
			}
		}
	}
	if fskind == archive {
		return
	}
	sys.Remove(newfile)
	nd := sys.NullDir()
	nd.Name = d.Name
	nd.Mode = d.Mode
	nd.Mtime = d.Mtime
	if err := sys.Fwstat(tfd, &nd); err != nil {
		errorf("can't move tmp file to " + quote.Quote(newfile) + ": " + sys.Errstr())
	}
	nd = sys.NullDir()
	nd.GID = d.GID
	if err := sys.Fwstat(tfd, &nd); err != nil {
		warn("can't set group id of " + quote.Quote(newfile) + " to " + quote.Quote(d.GID) + ": " + sys.Errstr())
	}
	nd.GID = ""
	nd.UID = d.UID
	sys.Fwstat(tfd, &nd)
}

func mkdir(d *sys.Dir) {
	if fskind == archive {
		arch(d)
		return
	}
	nd := sys.NullDir()
	nd.Mode = d.Mode
	nd.GID = d.GID
	nd.Mtime = d.Mtime
	fd, err := sys.Create(newfile, sys.OREAD, d.Mode)
	if err != nil {
		d1, err := sys.Stat(newfile)
		if err != nil || d1.Mode&sys.DMDIR == 0 {
			errorf("can't create " + quote.Quote(newfile))
		}
		if err := sys.Wstat(newfile, &nd); err != nil {
			warn("can't set modes for " + quote.Quote(newfile) + ": " + sys.Errstr())
		}
		nd = sys.NullDir()
		nd.UID = d.UID
		sys.Wstat(newfile, &nd)
		return
	}
	defer sys.Close(fd)
	if err := sys.Fwstat(fd, &nd); err != nil {
		warn("can't set modes for " + quote.Quote(newfile) + ": " + sys.Errstr())
	}
	nd = sys.NullDir()
	nd.UID = d.UID
	sys.Fwstat(fd, &nd)
}

func arch(d *sys.Dir) {
	fmt.Fprintf(bout, "%s %o %s %s %d %d\n", quote.Quote(newfile), d.Mode, quote.Quote(d.UID), quote.Quote(d.GID), d.Mtime, int64(d.Length))
}

func mkpath(prefix, elem string) string { return prefix + "/" + elem }

func setnames(f *file) {
	newfile = newroot + f.new
	switch {
	case f.old == "":
		oldfile = oldroot + f.new
	case f.old[0] == '/':
		oldfile = oldroot + f.old
	default:
		oldfile = f.old
	}
}

func gets() (string, bool) {
	if next >= len(lines) {
		return "", false
	}
	s := lines[next]
	next++
	return s, true
}

func indentation(p string) (int, string) {
	n := 0
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case ' ':
			n++
		case '\t':
			n += 8
		default:
			return n, p[i:]
		}
	}
	return n, ""
}

// skipdir skips the proto lines that could be in the current directory.
func skipdir() {
	if indent < 0 {
		return
	}
	level := indent
	for {
		fp := next
		p, ok := gets()
		lineno++
		if !ok {
			indent = -1
			return
		}
		indent, _ = indentation(p)
		if indent <= level {
			next = fp
			lineno--
			return
		}
	}
}

func getfile(old *file) (*file, int) {
	if indent < 0 {
		return nil, 0
	}
	fp := next
	var p string
	for {
		s, ok := gets()
		lineno++
		if !ok {
			indent = -1
			return nil, 0
		}
		indent, p = indentation(s)
		if p != "" && p[0] != '\n' && p[0] != '#' {
			break
		}
	}
	f := &file{}
	var elem string
	elem, p = getname(p)
	if debug {
		fmt.Fprintf(os.Stderr, "getfile: %s root %s\n", quote.Quote(elem), quote.Quote(old.new))
	}
	f.new = mkpath(old.new, elem)
	f.elem = f.new[strings.LastIndexByte(f.new, '/')+1:]
	if f.elem == "" {
		errorf("can't find file name component of " + quote.Quote(f.new))
	}
	f.mode, p = getmode(p)
	f.uid, p = getname(p)
	if f.uid == "" {
		f.uid = "-"
	}
	f.gid, p = getname(p)
	if f.gid == "" {
		f.gid = "-"
	}
	f.old = getpath(p)
	if f.old == "-" {
		f.old = ""
	}
	setnames(f)
	if debug {
		if f.old != "" {
			fmt.Fprintf(os.Stderr, "%s from %s %s %s %o\n", quote.Quote(f.new), quote.Quote(f.old), quote.Quote(f.uid), quote.Quote(f.gid), f.mode)
		} else {
			fmt.Fprintf(os.Stderr, "%s %s %s %o\n", quote.Quote(f.new), quote.Quote(f.uid), quote.Quote(f.gid), f.mode)
		}
	}
	return f, fp
}

func getpath(p string) string {
	p = strings.TrimLeft(p, " \t")
	if n := strings.IndexAny(p, "\n \t"); n >= 0 {
		return p[:n]
	}
	return p
}

func getname(p string) (string, string) {
	p = strings.TrimLeft(p, " \t")
	var s strings.Builder
	quoted := false
	i := 0
	for ; i < len(p); i++ {
		c := p[i]
		if c == '\n' || (c == ' ' || c == '\t') && !quoted {
			break
		}
		if c == '\'' {
			if i+1 >= len(p) || p[i+1] != '\'' {
				quoted = !quoted
				continue
			}
			i++
		}
		s.WriteByte(c)
	}
	name := s.String()
	if strings.HasPrefix(name, "$") {
		v := getenv(name[1:])
		if v == "" {
			errorf("can't read environment variable " + quote.Quote(name))
		}
		name = v
	}
	return name, p[i:]
}

func getenv(s string) string {
	if s == "user" {
		return getuser()
	}
	return readfile("/env/" + s)
}

func getuser() string { return readfile("/dev/user") }

func readfile(f string) string {
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	if len(b) > 256 {
		b = b[:256]
	}
	return string(b)
}

func getmode(p string) (uint32, string) {
	var s string
	s, p = getname(p)
	if s == "" || s == "-" {
		return ^uint32(0), p
	}
	orig := s
	var m uint32
	if s[0] == 'd' {
		m |= sys.DMDIR
		s = s[1:]
	}
	if len(s) > 0 && s[0] == 'a' {
		m |= sys.DMAPPEND
		s = s[1:]
	}
	if len(s) > 0 && s[0] == 'l' {
		m |= sys.DMEXCL
		s = s[1:]
	}
	for i := 0; i < len(s) || i < 3; i++ {
		if i >= len(s) || s[i] < '0' || s[i] > '7' {
			warn("bad mode specification " + orig)
			return ^uint32(0), p
		}
	}
	v, _ := strconv.ParseUint(s, 8, 32)
	return m | uint32(v), p
}

// setusers copies the users file into a kfs's /adm/users and has kfs
// read it.
func setusers() {
	if fskind != kfs {
		return
	}
	m := modes
	modes = true
	f := &file{uid: "adm", gid: "adm", mode: sys.DMDIR | 0o775, new: "/adm", elem: "adm"}
	setnames(f)
	mkfile(f)
	f.new, f.old, f.elem, f.mode = "/adm/users", users, "users", 0o664
	setnames(f)
	mkfile(f)
	kfscmd("user")
	mkfile(f)
	f.mode, f.new, f.old, f.elem = sys.DMDIR|0o775, "/adm", "/adm", "adm"
	setnames(f)
	mkfile(f)
	modes = m
}

func openkfscmd(name string) {
	if fskind != kfs {
		return
	}
	kname := fmt.Sprintf("/chan/kfs.%s.cmd", name)
	fd, err := sys.Open(kname, sys.ORDWR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: can't open %s: %s\n", prog, quote.Quote(kname), sys.Errstr())
		quit("open kfscmd")
	}
	sfd = os.NewFile(uintptr(fd), kname)
}

func kfscmd(cmd string) {
	if fskind != kfs || sfd == nil {
		return
	}
	if n, err := sfd.Write([]byte(cmd)); n != len(cmd) || err != nil {
		fmt.Fprintf(os.Stderr, "%s: error writing %s: %s", prog, cmd, sys.Errstr())
		return
	}
	reply := make([]byte, 4*1024)
	for {
		n, err := sfd.Read(reply)
		if n <= 0 || err != nil {
			return
		}
		s := string(reply[:n])
		if s == "done" || s == "success" {
			return
		}
		if s == "unknown command" {
			fmt.Fprintf(os.Stderr, "%s: command %s not recognized\n", prog, cmd)
			return
		}
	}
}

func errorf(s string) {
	fmt.Fprintf(os.Stderr, "%s: %s:%d: %s\n", prog, proto, lineno, s)
	kfscmd("disallow")
	kfscmd("sync")
	quit("error")
}

func warn(s string) { fmt.Fprintf(os.Stderr, "%s: %s:%d: %s\n", prog, proto, lineno, s) }
