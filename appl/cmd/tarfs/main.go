// Command tarfs mounts a tar archive read-only (Inferno tarfs(4)).
//
// A guest is single-threaded and mount waits for the server's answers, so
// tarfs cannot serve and mount at once as tarfs.b does with a spawned
// thread. It checks the archive, then starts a second prog of itself
// (hidden first argument "@serve") holding the archive and one end of a
// pipe, and mounts the other end.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const serveFlag = "@serve"

type file struct {
	x        int
	name     string
	mode     uint32
	uid, gid string
	mtime    uint32
	length   int64
	offset   int64
	parent   *file
	children []*file // newest first, as tarfs.b conses them
}

var (
	pflag bool
	root  *file
	files []*file
	tarf  *os.File
)

func fatal(s string) {
	os.Stderr.WriteString("tarfs: " + s + "\n")
	sys.Exits("fail:error")
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == serveFlag {
		server(os.Args[2:])
		return
	}
	a := arg.New(os.Args)
	a.SetUsage("tarfs [-a|-b|-ac|-bc] [-D] file mountpoint")
	flags := sys.MREPL
	var opts []string
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			flags = sys.MAFTER
		case 'b':
			flags = sys.MBEFORE
		case 'D':
			opts = append(opts, "-D")
		case 'p':
			opts = append(opts, "-p")
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) != 2 {
		a.Usage()
	}
	name, mountpt := args[0], args[1]
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		fatal("can't open " + name + ": " + sys.Errstr())
	}
	tarf = os.NewFile(uintptr(fd), name)
	load(name)
	p, err := sys.Pipe()
	if err != nil {
		fatal("can't create pipe: " + sys.Errstr())
	}
	argv := append([]string{"tarfs", serveFlag}, opts...)
	argv = append(argv, name)
	if _, err := sys.Spawn("/bin/tarfs.wasm", argv, sys.NEWFD|sys.FORKNS,
		[][2]int{{0, p[1]}, {2, 2}, {5, fd}}); err != nil {
		fatal("can't start server: " + sys.Errstr())
	}
	sys.Close(p[1])
	if err := sys.Mount(p[0], -1, mountpt, flags, ""); err != nil {
		fatal("can't mount tarfs: " + sys.Errstr())
	}
}

func load(name string) {
	root = &file{name: "/", mode: styx.DMDIR | 0o555, uid: "0", gid: "0"}
	files = []*file{root}
	if err := readtar(tarf); err != nil {
		fatal("error reading " + name + ": " + err.Error())
	}
}

// server is the serving prog: fd 0 is the conversation, fd 5 the archive (3 and 4 are the preopens).
func server(args []string) {
	trace := false
	var name string
	for _, s := range args {
		switch s {
		case "-D":
			trace = true
		case "-p":
			pflag = true
		default:
			name = s
		}
	}
	tarf = os.NewFile(5, name)
	load(name)
	srv := styxservers.New(os.NewFile(0, "styx"), nav{}, 0)
	if trace {
		srv.Trace = os.Stderr
	}
	for {
		t, err := srv.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				os.Stderr.WriteString("tarfs: mount read error: " + err.Error() + "\n")
			}
			return
		}
		root.mtime = uint32(time.Now().Unix())
		if t.Type != styx.Tread {
			srv.Default(t)
			continue
		}
		c, err := srv.CanRead(t)
		if c == nil {
			srv.Error(t, err.Error())
			continue
		}
		if c.Qtype&styx.QTDIR != 0 {
			srv.Default(t)
			continue
		}
		f := files[c.Path]
		n := int64(t.Count)
		if int64(t.Offset)+n > f.length {
			n = f.length - int64(t.Offset)
		}
		if n <= 0 {
			srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag})
			continue
		}
		b := make([]byte, n)
		k, rerr := tarf.ReadAt(b, f.offset+int64(t.Offset))
		if k == 0 && rerr != nil {
			srv.Error(t, sys.Errstr())
			continue
		}
		srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: b[:k]})
	}
}

func (dir *file) enter(f *file) {
	f.x = len(files)
	f.parent = dir
	dir.children = append([]*file{f}, dir.children...)
	files = append(files, f)
}

func (dir *file) find(name string) *file {
	for _, g := range dir.children {
		if g.name == name {
			return g
		}
	}
	return nil
}

func (f *file) stat() *styx.Dir {
	mode := f.mode
	if pflag {
		mode = mode&(0xff<<24) | 0o444
		if f.mode&styx.DMDIR != 0 {
			mode |= 0o111
		}
	}
	return &styx.Dir{Mode: mode, Qid: styx.Qid{Path: uint64(f.x), Type: uint8(f.mode >> 24)},
		Name: f.name, UID: f.uid, GID: f.gid, MUID: f.uid, Length: uint64(f.length),
		Mtime: f.mtime, Atime: root.mtime}
}

type nav struct{}

func (nav) Stat(path uint64) (*styx.Dir, error) {
	if path >= uint64(len(files)) {
		return nil, styxservers.Enotfound
	}
	return files[path].stat(), nil
}

func (nav) Walk(path uint64, name string) (*styx.Dir, error) {
	f := files[path]
	if f.mode&styx.DMDIR == 0 {
		return nil, styxservers.Enotdir
	}
	if name == ".." {
		if f.parent != nil {
			f = f.parent
		}
		return f.stat(), nil
	}
	if g := f.find(name); g != nil {
		return g.stat(), nil
	}
	return nil, styxservers.Enotfound
}

func (nav) Readdir(path uint64, off, n int) []*styx.Dir {
	f := files[path]
	if f.mode&styx.DMDIR == 0 || off >= len(f.children) {
		return nil
	}
	var ds []*styx.Dir
	for _, g := range f.children[off:min(off+n, len(f.children))] {
		ds = append(ds, g.stat())
	}
	return ds
}

// split returns the first element of s and the rest after its slashes.
func split(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			j := i + 1
			for j < len(s) && s[j] == '/' {
				j++
			}
			return s[:i], s[j:]
		}
	}
	return "", s
}

func putfile(f *file) {
	orign, n := f.name, f.name
	df := root
	for {
		d, rest := split(n)
		if d == "." {
			n = rest
			continue
		}
		if d == ".." {
			os.Stderr.WriteString("ignoring " + quote(orign) + "\n")
			return
		}
		if d == "" || rest == "" {
			f.name = n
			break
		}
		g := df.find(d)
		if g == nil {
			c := *f
			g = &c
			g.children = nil
			g.name = d
			g.mode |= styx.DMDIR
			df.enter(g)
		}
		n = rest
		df = g
	}
	if f.name != "." && f.name != ".." {
		df.enter(f)
	}
}

func quote(s string) string {
	for _, c := range s {
		if c <= ' ' || c == '\'' {
			return "'" + replaceQuotes(s) + "'"
		}
	}
	if s == "" {
		return "''"
	}
	return s
}

func replaceQuotes(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b = append(b, '\'')
		}
		b = append(b, s[i])
	}
	return string(b)
}

// The ustar header layout.
const (
	blocksize = 512
	namelen   = 100
	userlen   = 32

	oname     = 0
	omode     = namelen
	ouid      = omode + 8
	ogid      = ouid + 8
	osize     = ogid + 8
	omtime    = osize + 12
	ochksum   = omtime + 12
	olinkflag = ochksum + 8
	olinkname = olinkflag + 1
	omagic    = olinkname + namelen
	ouname    = omagic + 8
	ogname    = ouname + userlen
	omajor    = ogname + userlen
)

func readtar(r *os.File) error {
	buf := make([]byte, blocksize)
	var offset int64
	for {
		n, err := r.ReadAt(buf, offset)
		if n == 0 {
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("%s", sys.Errstr())
			}
			break
		}
		if n < len(buf) {
			return fmt.Errorf("short read: expected %d, got %d", len(buf), n)
		}
		if buf[0] == 0 {
			break
		}
		offset += blocksize
		mode := uint32(octal(buf[omode:ouid]))
		linkflag := buf[olinkflag]
		if mode&0o170000 == 0o40000 {
			linkflag = '5'
		}
		mode &= 0o777
		switch linkflag {
		case '1', '2', 's':
			continue
		case '5':
			mode |= styx.DMDIR
		}
		f := &file{name: ascii(buf[oname:omode])}
		for len(f.name) > 0 && f.name[0] == '/' {
			f.name = f.name[1:]
		}
		for len(f.name) > 0 && f.name[len(f.name)-1] == '/' {
			mode |= styx.DMDIR
			f.name = f.name[:len(f.name)-1]
		}
		f.mode = mode
		f.uid = strconv.FormatInt(octal(buf[ouid:ogid]), 10)
		f.gid = strconv.FormatInt(octal(buf[ogid:osize]), 10)
		f.length = octal(buf[osize:omtime])
		if f.length < 0 {
			fatal("tar file size is negative: " + f.name)
		}
		if mode&styx.DMDIR != 0 {
			f.length = 0
		}
		f.mtime = uint32(octal(buf[omtime:ochksum]))
		if int(octal(buf[ochksum:olinkflag])) != checksum(buf) {
			fatal("checksum error on " + f.name)
		}
		f.offset = offset
		offset += f.length
		if v := f.length % blocksize; v != 0 {
			offset += blocksize - v
		}
		if ascii(buf[omagic:ouname]) == "ustar" && string(buf[omagic+6:omagic+8]) == "00" {
			f.uid = ascii(buf[ouname:ogname])
			f.gid = ascii(buf[ogname:omajor])
		}
		putfile(f)
	}
	return nil
}

func ascii(b []byte) string {
	i := 0
	for i < len(b) && b[i] != 0 {
		i++
	}
	return string(b[:i])
}

func octal(b []byte) int64 {
	var v int64
	i := 0
	for i < len(b) && b[i] == ' ' {
		i++
	}
	for ; i < len(b) && b[i] != 0 && b[i] != ' '; i++ {
		c := b[i]
		if c < '0' || c > '7' {
			fatal(fmt.Sprintf("bad octal value in tar header: %s (%c)", string(b), c))
		}
		v = v<<3 | int64(c-'0')
	}
	return v
}

func checksum(b []byte) int {
	c := 0
	i := 0
	for ; i < ochksum; i++ {
		c += int(b[i])
	}
	for ; i < olinkflag; i++ {
		c += ' '
	}
	for ; i < len(b); i++ {
		c += int(b[i])
	}
	return c
}
