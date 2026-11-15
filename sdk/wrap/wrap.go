// Package wrap reads the /wrap description of installed packages and
// writes it into package archives (Inferno's appl/cmd/install/wrap.b).
//
// A package's tree holds wrap/name/time for each package or update made
// at time: a "package" file marks a full package, an "update" file holds
// the time of the package an update applies to, "desc" describes it, and
// "md5sum" lists each file it holds with its MD5 digest.
package wrap

import (
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arch"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Update kinds.
const (
	FULL = 1
	UPD  = 2
)

// Update is one package or update.
type Update struct {
	Desc  string
	Dir   string
	Time  int
	Utime int
	// MD5 is the md5sum file, a line "name digest" per file, sorted.
	MD5 []string
	Typ int
	md5 map[string]string
}

// Wrapped is a package: the updates that still matter, oldest first.
type Wrapped struct {
	Name  string
	Root  string
	Tfull int
	U     []Update
}

// Nu is the number of updates.
func (w *Wrapped) Nu() int { return len(w.U) }

var (
	gzfile  string
	mounted string
)

// End removes the temporary file and unmounts the archive that opening a
// package made.
func End() {
	if gzfile != "" {
		sys.Remove(gzfile)
		gzfile = ""
	}
	if mounted != "" {
		sys.Unmount("", mounted)
		mounted = ""
	}
}

// mountarch mounts archive f on mtpt with archfs(4), all of it or only
// its /wrap files.
func mountarch(f, mtpt string, all bool) bool {
	if _, err := sys.Stat(mtpt); err != nil {
		mkdirs(mtpt)
	}
	sys.Pctl(sys.FORKNS, nil)
	args := []string{"archfs", "-m", mtpt, f}
	if !all {
		args = []string{"archfs", "-s", "-m", mtpt, f, "/wrap"}
	}
	st, err := Run("/bin/archfs.wasm", args, [][2]int{{0, 0}, {1, 1}, {2, 2}})
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: cannot run archfs\n")
		return false
	}
	if st != "" {
		return false
	}
	mounted = mtpt
	return true
}

func mkdirs(p string) {
	if p == "" || p == "/" {
		return
	}
	if _, err := sys.Stat(p); err == nil {
		return
	}
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		mkdirs(p[:i])
	}
	if fd, err := sys.Create(p, sys.OREAD, sys.DMDIR|0o775); err == nil {
		sys.Close(fd)
	}
}

// Run runs the command at path with args and the given fds (child, parent),
// waits for it, and returns its exit status.
func Run(path string, args []string, fds [][2]int) (string, error) {
	pid, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("#p/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		wfd, err = sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
		if err != nil {
			return "", err
		}
	}
	wf := os.NewFile(uintptr(wfd), "wait")
	defer wf.Close()
	child, err := sys.Spawn(path, args, sys.NEWFD, fds)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 512)
	for {
		n, err := wf.Read(buf)
		if err != nil || n == 0 {
			return "", sys.Error("wait failed")
		}
		id, rest, _ := strings.Cut(string(buf[:n]), " ")
		if id == strconv.Itoa(child) {
			if i := strings.Index(rest, "\":"); i >= 0 {
				return rest[i+2:], nil
			}
			return "", nil
		}
	}
}

func openmount(f, d string) *Wrapped {
	if f == "" {
		f = getfirstdir(d + "/wrap")
		if f == "" {
			return nil
		}
	}
	w := &Wrapped{Name: f, Root: d}
	var ok bool
	w.U, w.Tfull, ok = openupdate(Pathcat(d, Pathcat("wrap", f)))
	if !ok {
		return nil
	}
	return w
}

// OpenHdr opens the package in f, which may be a package archive, or a
// tree as Open takes; an archive is mounted on /mnt/wrap.
func OpenHdr(f, d string, all bool) *Wrapped {
	dir, err := sys.Stat(f)
	if err != nil || dir.Mode&sys.DMDIR != 0 {
		return Open(f, d, all)
	}
	if nf, fd := arch.OpenGz(f); nf != "" {
		gzfile = nf
		f = nf
		fd.Close()
	}
	return Open(f, "/mnt/wrap", all)
}

// Open opens package f installed under root d; f may also be a package
// tree (root/ or root/wrap/name) or an archive.
func Open(f, d string, all bool) *Wrapped {
	if d == "" {
		d = "/"
	}
	if w := openmount(f, d); w != nil {
		return w
	}
	dir, err := sys.Stat(f)
	if err != nil {
		return nil
	}
	if dir.Mode&sys.DMDIR != 0 {
		d = f
		if i := strings.Index(f, "/wrap/"); i >= 0 {
			f = f[i+6:]
			d = d[:i+6]
		} else {
			f = ""
		}
		return openmount(f, d)
	}
	if !mountarch(f, d, all) {
		return nil
	}
	return openmount("", d)
}

func getfirstdir(d string) string {
	fd, err := sys.Open(d, sys.OREAD)
	if err != nil {
		return ""
	}
	defer sys.Close(fd)
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			return ""
		}
		for _, x := range ds {
			if x.Mode&sys.DMDIR != 0 {
				return x.Name
			}
		}
	}
}

func sniffdir(base, elem string) (int, int) {
	t := String2now(elem, false)
	if t == 0 {
		return 0, 0
	}
	rv := 0
	if _, err := sys.Stat(base + "/" + elem + "/package"); err == nil {
		rv |= FULL
	}
	if _, err := sys.Stat(base + "/" + elem + "/update"); err == nil {
		rv |= UPD
	}
	return rv, t
}

func dirnames(d string) ([]string, bool) {
	fd, err := sys.Open(d, sys.OREAD)
	if err != nil {
		return nil, false
	}
	defer sys.Close(fd)
	var names []string
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			return names, true
		}
		for _, x := range ds {
			names = append(names, x.Name)
		}
	}
}

// openupdate finds the most recent full package and the updates since,
// and returns them oldest first.
func openupdate(d string) ([]Update, int, bool) {
	names, ok := dirnames(d)
	if !ok {
		return nil, 0, false
	}
	tbase, tfull, nu := -1, -1, 0
	for _, n := range names {
		k, t := sniffdir(d, n)
		switch k {
		case FULL:
			nu++
			if t > tfull {
				tfull = t
			}
			if t > tbase {
				tbase = t
			}
		case FULL | UPD:
			nu++
			if t > tfull {
				tfull = t
			}
		case UPD:
			nu++
		}
	}
	if nu == 0 {
		return nil, 0, false
	}
	var u []Update
	for _, n := range names {
		k, t := sniffdir(d, n)
		if k == 0 || t < tbase || t < tfull && k == UPD {
			continue
		}
		up := Update{Typ: k}
		if readupdate(&up, d, n) {
			u = append(u, up)
		}
	}
	if len(u) == 0 {
		return nil, 0, false
	}
	sort.SliceStable(u, func(i, j int) bool { return u[i].Time < u[j].Time })
	return u, tfull, true
}

func readupdate(u *Update, base, elem string) bool {
	u.Dir = Pathcat(base, elem)
	u.Desc = readfile(u.Dir + "/desc")
	u.Time = String2now(elem, false)
	if q := readfile(u.Dir + "/update"); q != "" {
		u.Utime, _ = strconv.Atoi(strings.TrimSpace(q))
	}
	b, err := os.ReadFile(u.Dir + "/md5sum")
	if err != nil {
		return false
	}
	u.md5 = map[string]string{}
	for _, l := range strings.SplitAfter(string(b), "\n") {
		if l == "" {
			continue
		}
		u.MD5 = append(u.MD5, l)
		if name, rest, ok := strings.Cut(strings.TrimSuffix(l, "\n"), " "); ok {
			if _, dup := u.md5[name]; !dup {
				u.md5[name] = rest
			}
		}
	}
	return true
}

func readfile(s string) string {
	b, err := os.ReadFile(s)
	if err != nil || len(b) == 0 {
		return ""
	}
	return strings.TrimSuffix(string(b), "\n")
}

func hex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// Getfileinfo looks f up in the package's md5sum files, newest first. It
// fails when f is not there, or when rdigest is given and matches neither
// the recorded digest nor ardigest; otherwise it copies the recorded
// digest to wdigest and returns the time of the update holding f.
func Getfileinfo(w *Wrapped, f string, rdigest, wdigest, ardigest []byte) (bool, int) {
	if w == nil {
		return false, 0
	}
	var digest [md5.Size]byte
	i := len(w.U) - 1
	for ; i >= 0; i-- {
		q, ok := w.U[i].md5[f]
		if !ok || len(q) != 2*md5.Size {
			continue
		}
		j := 0
		for ; j < md5.Size; j++ {
			a, b := hex(q[2*j]), hex(q[2*j+1])
			if a < 0 || b < 0 {
				break
			}
			digest[j] = byte(a<<4 | b)
		}
		if j != md5.Size {
			continue
		}
		if rdigest == nil || string(rdigest) == string(digest[:]) || ardigest != nil && string(ardigest) == string(digest[:]) {
			break
		}
		return false, 0
	}
	if i < 0 {
		return false, 0
	}
	if wdigest != nil {
		copy(wdigest, digest[:])
	}
	return true, w.U[i].Time
}

// Md5file puts the MD5 digest of file in digest; a directory leaves it
// unchanged. It fails if file cannot be read.
func Md5file(file string, digest []byte) bool {
	d, err := sys.Stat(file)
	if err != nil {
		return false
	}
	if d.Mode&sys.DMDIR != 0 {
		return true
	}
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	h := md5.New()
	io.Copy(h, f)
	copy(digest, h.Sum(nil))
	return true
}

// Md5filea is Md5file, but with each CR LF taken as LF, as for a text
// file written on Windows.
func Md5filea(file string, digest []byte) bool {
	d, err := sys.Stat(file)
	if err != nil {
		return false
	}
	if d.Mode&sys.DMDIR != 0 {
		return true
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	s := md5.Sum([]byte(strings.ReplaceAll(string(b), "\r\n", "\n")))
	copy(digest, s[:])
	return true
}

// Md5sum puts the digest of the next n bytes of b in digest.
func Md5sum(b io.Reader, digest []byte, n int64) bool {
	h := md5.New()
	if k, _ := io.CopyN(h, b, n); k != n {
		return false
	}
	copy(digest, h.Sum(nil))
	return true
}

// Md5conv is the digest in hex.
func Md5conv(d []byte) string { return fmt.Sprintf("%x", d[:md5.Size]) }

func newd(time int, uid, gid string) *sys.Dir {
	return &sys.Dir{UID: uid, GID: gid, Mtime: uint32(time)}
}

// Putwrapfile puts file in the archive as /wrap/name/time/elem.
func Putwrapfile(b io.Writer, name string, time int, elem, file, uid, gid string) {
	d := newd(time, uid, gid)
	d.Mode = 0o444
	dir, err := sys.Stat(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot stat %s: %s", file, sys.Errstr())
	} else {
		d.Length = dir.Length
	}
	s := "/wrap/" + name + "/" + Now2string(time, false) + "/" + elem
	arch.Puthdr(b, s, d)
	arch.Putfile(b, file, int64(d.Length))
}

// Putwrap puts the /wrap directories of a package or update made at time
// in the archive.
func Putwrap(b io.Writer, name string, time int, desc string, utime int, pkg bool, uid, gid string) {
	if utime == 0 && !pkg {
		fmt.Fprintf(os.Stderr, "bad precondition in putwrap()")
	}
	d := newd(time, uid, gid)
	d.Mode = sys.DMDIR | 0o775
	s := "/wrap"
	arch.Puthdr(b, s, d)
	s += "/" + name
	arch.Puthdr(b, s, d)
	s += "/" + Now2string(time, false)
	arch.Puthdr(b, s, d)
	d.Mode = 0o444
	dir := s + "/"
	if utime != 0 {
		d.Length = 23
		arch.Puthdr(b, dir+"update", d)
		arch.Putstring(b, fmt.Sprintf("%22d\n", uint32(utime)))
	}
	if pkg {
		d.Length = 0
		arch.Puthdr(b, dir+"package", d)
	}
	if desc != "" {
		d.Length = uint64(len(desc) + 1)
		arch.Puthdr(b, dir+"desc", d)
		arch.Putstring(b, desc+"\n")
	}
}

// Match reports whether s is under one of the prefixes pre, or in /wrap;
// every name matches no prefixes.
func Match(s string, pre []string) bool {
	if pre == nil || s == "/wrap" || strings.HasPrefix(s, "/wrap/") {
		return true
	}
	for _, p := range pre {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// Notmatch reports whether s is under none of the prefixes, or in /wrap.
func Notmatch(s string, pre []string) bool {
	if pre == nil || s == "/wrap" || strings.HasPrefix(s, "/wrap/") {
		return true
	}
	for _, p := range pre {
		if strings.HasPrefix(s, p) {
			return false
		}
	}
	return true
}

// Pathcat joins s and t with one slash.
func Pathcat(s, t string) string {
	if s == "" {
		return t
	}
	if t == "" {
		return s
	}
	slashs := s[len(s)-1] == '/'
	slasht := t[0] == '/'
	switch {
	case slashs && slasht:
		return s + t[1:]
	case !slashs && !slasht:
		return s + "/" + t
	}
	return s + t
}

const ten2eight = 100000000

// Now2string is a package time as its directory name: decimal, or with
// flag the nine-character form whose first character counts hundreds
// of millions of seconds (0-9, then A-L).
func Now2string(n int, flag bool) string {
	if !flag {
		return strconv.FormatUint(uint64(uint32(n)), 10)
	}
	if n < 0 {
		return ""
	}
	q := n / ten2eight
	s := fmt.Sprintf("%09d", n-ten2eight*q)
	switch {
	case q <= 9:
		return string(rune('0'+q)) + s[1:]
	case q <= 21:
		return string(rune('A'+q-10)) + s[1:]
	}
	return ""
}

// String2now is the inverse of Now2string.
func String2now(s string, flag bool) int {
	if s == "" {
		return 0
	}
	if !flag && s[0] != 'A' {
		return atoi(s)
	}
	if len(s) != 9 {
		return 0
	}
	r := atoi(s[1:])
	c := s[0]
	var q int
	switch {
	case c >= '0' && c <= '9':
		q = int(c - '0')
	case c >= 'A' && c <= 'L':
		q = int(c-'A') + 10
	default:
		return 0
	}
	n := ten2eight*q + r
	if n < 0 {
		return 0
	}
	return n
}

// atoi is Limbo's int of a string: the leading decimal digits.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		n = -n
	}
	return int(int32(n))
}
