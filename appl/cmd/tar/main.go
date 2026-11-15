// Command tar creates, lists or extracts ustar archives. Inferno has no
// tar(1), so this follows the Rust sibling's, whose key is Plan 9's: one
// function letter, c (create), t (list) or x (extract), and modifiers:
//
//	f	the next argument names the archive instead of standard
//		input or output
//	v	print names as they are added or extracted, on standard
//		error; with t, list modes, sizes and dates too
//	z	gzip the archive on c; t and x take a gzipped archive
//		with or without z
//	k	with x, keep existing files
//	m	with x, do not set modification times from the archive
//	T	with x, set modification times (the default)
//	R	with x, keep a leading / or # in names rather than
//		extracting relative to the current directory
//	p	write POSIX ustar (the only format written)
//
// x sets modes from the archive. Names given to t and x select those
// members and anything under them. The archive code is sdk/ustar,
// shared with gettar, puttar and lstar.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/ustar"
)

const usage = "usage: tar {ctx}[fkmpRTvz] [archive] [file ...]"

type opts struct {
	fn                byte
	file              string
	havefile          bool
	verbose, gz, keep bool
	nomtime, rooted   bool
}

var failed bool

func warn(msg string) {
	os.Stdout.Sync()
	os.Stderr.WriteString("tar: " + msg + "\n")
}

func fail(msg string) {
	warn(msg)
	failed = true
}

func fatal(msg string) {
	warn(msg)
	sys.Exits("fail:" + msg)
}

func main() {
	if len(os.Args) < 2 {
		fatalUsage()
	}
	var o opts
	rest := os.Args[2:]
	for _, c := range []byte(strings.TrimLeft(os.Args[1], "-")) {
		switch c {
		case 'c', 't', 'x':
			if o.fn != 0 {
				fatalUsage()
			}
			o.fn = c
		case 'f':
			if len(rest) == 0 {
				fatalUsage()
			}
			o.file, o.havefile, rest = rest[0], true, rest[1:]
		case 'v':
			o.verbose = true
		case 'z':
			o.gz = true
		case 'k':
			o.keep = true
		case 'm':
			o.nomtime = true
		case 'T':
			o.nomtime = false
		case 'R':
			o.rooted = true
		case 'p':
		default:
			fatalUsage()
		}
	}
	switch o.fn {
	case 'c':
		create(&o, rest)
	case 't':
		list(&o, rest)
	case 'x':
		extract(&o, rest)
	default:
		fatalUsage()
	}
	if failed {
		sys.Exits("fail:errors")
	}
}

func fatalUsage() {
	os.Stderr.WriteString(usage + "\n")
	sys.Exits("fail:usage")
}

// readArchive reads the whole archive, ungzipping it if need be.
func readArchive(o *opts) []byte {
	var data []byte
	var err error
	if o.havefile && o.file != "-" {
		if data, err = os.ReadFile(o.file); err != nil {
			fatal(o.file + ": " + sys.Errstr())
		}
	} else if data, err = io.ReadAll(os.Stdin); err != nil {
		fatal("read error: " + sys.Errstr())
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err == nil {
			data, err = io.ReadAll(zr)
		}
		if err != nil {
			fatal("gzip: " + err.Error())
		}
	}
	return data
}

// selected says whether name is one of pats or inside one; no patterns
// select everything.
func selected(name string, pats []string) bool {
	if len(pats) == 0 {
		return true
	}
	name = strings.TrimSuffix(name, "/")
	for _, p := range pats {
		p = strings.TrimRight(p, "/")
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}

func create(o *opts, files []string) {
	if len(files) == 0 {
		fatalUsage()
	}
	var out io.Writer = os.Stdout
	if o.havefile && o.file != "-" {
		fd, err := sys.Create(o.file, sys.OWRITE, 0o666)
		if err != nil {
			fatal(o.file + ": " + sys.Errstr())
		}
		f := os.NewFile(uintptr(fd), o.file)
		defer f.Close()
		out = f
	}
	bout := bufio.NewWriter(out)
	var zw *gzip.Writer
	w := io.Writer(bout)
	if o.gz {
		zw, _ = gzip.NewWriterLevel(bout, 6)
		zw.OS = 3 // as Inferno's deflate, and gzip here
		w = zw
	}
	tw := ustar.NewWriter(w)
	for _, f := range files {
		add(o, tw, f)
	}
	err := tw.Close()
	if zw != nil && err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = bout.Flush()
	}
	if err != nil {
		fatal("write error: " + sys.Errstr())
	}
}

func add(o *opts, tw *ustar.Writer, path string) {
	d, err := sys.Stat(path)
	if err != nil {
		fail(path + ": " + sys.Errstr())
		return
	}
	name := path
	if len(name) > 1 {
		name = strings.TrimRight(name, "/")
	}
	h := &ustar.Header{Name: name, Mode: int(d.Mode & 0o777), Mtime: int64(d.Mtime),
		Type: '0', Uname: d.UID, Gname: d.GID}
	if d.Mode&sys.DMDIR != 0 {
		h.Type = '5'
		var names []string
		fd, err := sys.Open(name, sys.OREAD)
		if err != nil {
			fail(name + ": " + sys.Errstr())
			return
		}
		for {
			ds, err := sys.Dirread(fd)
			if err != nil || len(ds) == 0 {
				break
			}
			for _, e := range ds {
				names = append(names, e.Name)
			}
		}
		sys.Close(fd)
		sort.Strings(names)
		if !putHeader(o, tw, h) {
			return
		}
		for _, e := range names {
			add(o, tw, strings.TrimRight(name, "/")+"/"+e)
		}
		return
	}
	f, err := os.Open(name)
	if err != nil {
		fail(name + ": " + sys.Errstr())
		return
	}
	defer f.Close()
	h.Size = int64(d.Length)
	if !putHeader(o, tw, h) {
		return
	}
	buf := make([]byte, ustar.NBlock*ustar.BlockSize)
	left := h.Size
	for left > 0 {
		n, err := io.ReadFull(f, buf[:min(int64(len(buf)), left)])
		if n == 0 && err != nil {
			fail(name + ": " + sys.Errstr())
			break
		}
		for b := buf[:n]; len(b) > 0; b = b[min(len(b), ustar.BlockSize):] {
			if tw.Block(b[:min(len(b), ustar.BlockSize)]) != nil {
				fatal("write error: " + sys.Errstr())
			}
		}
		left -= int64(n)
	}
	// A file that shrank is padded to the size in its header.
	for ; left > 0; left -= ustar.BlockSize {
		if tw.Block(nil) != nil {
			fatal("write error: " + sys.Errstr())
		}
	}
}

func putHeader(o *opts, tw *ustar.Writer, h *ustar.Header) bool {
	b, err := h.Pack()
	if err != nil {
		fail(h.Name + ": name too long")
		return false
	}
	if tw.Block(b) != nil {
		fatal("write error: " + sys.Errstr())
	}
	if o.verbose {
		os.Stderr.WriteString(h.Name + "\n")
	}
	return true
}

func archive(o *opts) *ustar.Archive {
	return ustar.NewArchive(ustar.NewFullReader(bytes.NewReader(readArchive(o))))
}

func next(a *ustar.Archive) *ustar.Header {
	h, err := a.Next()
	if err != nil {
		fatal(err.Error())
	}
	return h
}

func list(o *opts, names []string) {
	a := archive(o)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	now := daytime.Now()
	for h := next(a); h != nil; h = next(a) {
		if !selected(h.Name, names) {
			continue
		}
		if h.Type == '5' && len(h.Name) > 1 {
			h.Name = strings.TrimSuffix(h.Name, "/")
		}
		if !o.verbose {
			fmt.Fprintf(out, "%s\n", h.Name)
			continue
		}
		m := uint32(h.Mode)
		if h.Type == '5' {
			m |= sys.DMDIR
		}
		name := h.Name
		if h.Type == '1' || h.Type == '2' {
			name += " -> " + h.Link
		}
		fmt.Fprintf(out, "%s %8d %s %s\n", modestring(m), h.Size, daytime.Filet(now, int(h.Mtime)), name)
	}
}

// modestring is the mode as ls -l prints it.
func modestring(m uint32) string {
	s := []byte("--")
	if m&sys.DMDIR != 0 {
		s[0] = 'd'
	}
	for _, shift := range []uint{6, 3, 0} {
		b := m >> shift
		for i, c := range "rwx" {
			if b&(4>>i) != 0 {
				s = append(s, byte(c))
			} else {
				s = append(s, '-')
			}
		}
	}
	return string(s)
}

type dirmode struct {
	path        string
	mode, mtime uint32
}

func extract(o *opts, names []string) {
	a := archive(o)
	var dirs []dirmode
	defer func() {
		// Directories get their modes and times once their contents are in.
		for i := len(dirs) - 1; i >= 0; i-- {
			d := sys.NullDir()
			d.Mode = sys.DMDIR | dirs[i].mode
			if !o.nomtime {
				d.Mtime = dirs[i].mtime
			}
			if sys.Wstat(dirs[i].path, &d) != nil {
				fail(dirs[i].path + ": " + sys.Errstr())
			}
		}
	}()
	for h := next(a); h != nil; h = next(a) {
		if !selected(h.Name, names) {
			continue
		}
		switch h.Type {
		case '0', '7', 0, '5':
		case '1':
			warn("skipping link " + h.Name + " -> " + h.Link)
			continue
		case '2', 's':
			warn("skipping symlink " + h.Name)
			continue
		default:
			warn("skipping special file " + h.Name)
			continue
		}
		path := local(o, h.Name)
		if path == "" {
			fail(h.Name + ": bad file name")
			continue
		}
		if !parents(path) {
			continue
		}
		if o.verbose {
			os.Stderr.WriteString(path + "\n")
		}
		st, serr := sys.Stat(path)
		if h.Type == '5' {
			if serr == nil && st.Mode&sys.DMDIR == 0 {
				fail(path + ": not a directory")
				continue
			}
			if serr != nil {
				// Writable until its contents are in.
				fd, err := sys.Create(path, sys.OREAD, sys.DMDIR|0o700|uint32(h.Mode))
				if err != nil {
					fail(path + ": " + sys.Errstr())
					continue
				}
				sys.Close(fd)
			}
			dirs = append(dirs, dirmode{path, uint32(h.Mode), uint32(h.Mtime)})
			continue
		}
		if o.keep && serr == nil {
			continue
		}
		fd, err := sys.Create(path, sys.OWRITE, 0o666)
		if err != nil {
			fail(path + ": " + sys.Errstr())
			continue
		}
		f := os.NewFile(uintptr(fd), path)
		_, err = io.Copy(f, a)
		if err != nil {
			f.Close()
			fatal(err.Error())
		}
		d := sys.NullDir()
		d.Mode = uint32(h.Mode)
		if !o.nomtime {
			d.Mtime = uint32(h.Mtime)
		}
		if sys.Fwstat(fd, &d) != nil {
			fail(path + ": " + sys.Errstr())
		}
		f.Close()
	}
}

// local is the name to create for an archive name, or "" to refuse it.
func local(o *opts, name string) string {
	if !o.rooted {
		name = strings.TrimLeft(name, "/#")
		for _, c := range strings.Split(name, "/") {
			if c == ".." {
				return ""
			}
		}
	}
	return strings.TrimRight(name, "/")
}

// parents makes the missing directories leading to path.
func parents(path string) bool {
	for i := 1; i < len(path); i++ {
		if path[i] != '/' {
			continue
		}
		p := path[:i]
		if _, err := sys.Stat(p); err == nil {
			continue
		}
		fd, err := sys.Create(p, sys.OREAD, sys.DMDIR|0o777)
		if err != nil {
			fail(p + ": " + sys.Errstr())
			return false
		}
		sys.Close(fd)
	}
	return true
}
