// Command mkext unpacks an archive made by mkfs -a, read from standard
// input (Inferno disk/mkext, mkfs(8)). With file arguments only the
// files under them are extracted. -d puts the files under destdir; -h
// prints the headers instead; -t (or -T) sets modification times, and -u
// times, owners and groups; -v lists the files; -f replaces files that
// cannot be created.
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

const (
	atomicio = 8192
	nflds    = 6 // name, mode, uid, gid, mtime, bytes
	argv0    = "mkext"
)

var (
	bin                               *bufio.Reader
	uflag, tflag, hflag, vflag, fflag bool
	bout                              *bufio.Writer
)

func main() {
	destdir := ""
	a := arg.New(os.Args)
	a.SetUsage("mkext [-h] [-d destdir] [-T] [-u] [-v] [file ...]")
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'd':
			destdir = a.EArg()
		case 'f':
			fflag = true
		case 'h':
			hflag = true
			bout = bufio.NewWriter(os.Stdout)
		case 'u':
			uflag, tflag = true, true
		case 't', 'T':
			tflag = true
		case 'v':
			vflag = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	bin = bufio.NewReaderSize(os.Stdin, atomicio)
	for {
		p, err := bin.ReadString('\n')
		if p == "" && err != nil {
			break
		}
		if p == "end of archive\n" {
			fmt.Fprintf(os.Stderr, "done\n")
			quit("")
		}
		fields := quote.Unquoted(p)
		if len(fields) != nflds {
			warn("too few fields in file header")
			continue
		}
		name := fields[0]
		mode, _ := strconv.ParseUint(fields[1], 8, 32)
		uid, gid := fields[2], fields[3]
		mtime, _ := strconv.ParseUint(fields[4], 10, 32)
		bytes, _ := strconv.ParseInt(fields[5], 10, 64)
		if len(args) > 0 {
			if !selected(name, args) {
				if bytes != 0 {
					seekpast(bytes)
				}
				continue
			}
			mkdirs(destdir, name)
		}
		name = destdir + name
		if hflag {
			fmt.Fprintf(bout, "%s %o %s %s %d %d\n", quote.Quote(name), mode, uid, gid, mtime, bytes)
			if bytes != 0 {
				seekpast(bytes)
			}
			continue
		}
		if uint32(mode)&sys.DMDIR != 0 {
			mkdir(name, uint32(mode), uint32(mtime), uid, gid)
		} else {
			extract(name, uint32(mode), uint32(mtime), uid, gid, bytes)
		}
	}
	fmt.Fprintf(os.Stderr, "premature end of archive\n")
	quit("eof")
}

func quit(s string) {
	if bout != nil {
		bout.Flush()
	}
	if s != "" {
		sys.Exits("fail: " + s)
	}
	os.Exit(0)
}

func fileprefix(prefix, s string) bool {
	n, m := len(prefix), len(s)
	if n > m || !strings.HasPrefix(s, prefix) {
		return false
	}
	return m == n || s[n] == '/'
}

func selected(s string, args []string) bool {
	for _, a := range args {
		if fileprefix(a, s) {
			return true
		}
	}
	return false
}

func mkdirs(basedir, name string) {
	names := strings.FieldsFunc(name, func(r rune) bool { return r == '/' })
	for i := range names {
		if fd, ok := create(basedir, sys.OREAD, 0o775|sys.DMDIR); ok {
			sys.Close(fd)
		}
		if i == len(names)-1 {
			break
		}
		basedir = basedir + "/" + names[i]
	}
}

func elem(name string) string {
	p := name[strings.LastIndexByte(name, '/')+1:]
	if p == "" {
		p = name
	}
	return p
}

func mkdir(name string, mode, mtime uint32, uid, gid string) {
	fd, ok := create(name, sys.OREAD, mode)
	if !ok {
		d, err := sys.Stat(name)
		if err != nil || d.Mode&sys.DMDIR == 0 {
			warn("can't make directory " + name + ": " + sys.Errstr())
			return
		}
	} else {
		if _, err := sys.Fstat(fd); err != nil {
			warn("can't stat " + name + ": " + sys.Errstr())
		}
		sys.Close(fd)
	}
	d := sys.NullDir()
	d.Name = elem(name)
	if tflag {
		d.Mtime = mtime
	}
	if uflag {
		d.UID, d.GID = uid, gid
	}
	d.Mode = mode
	if err := sys.Wstat(name, &d); err != nil {
		warn("can't set modes for " + name + ": " + sys.Errstr())
	}
	if uflag {
		nd, err := sys.Stat(name)
		if err != nil {
			warn("can't reread modes for " + name + ": " + sys.Errstr())
			return
		}
		checkmodes(name, nd, mtime, uid, gid)
	}
}

func checkmodes(name string, d *sys.Dir, mtime uint32, uid, gid string) {
	if d.Mtime != mtime {
		warn(fmt.Sprintf("%s: time mismatch %d %d\n", name, mtime, d.Mtime))
	}
	if uid != d.UID {
		warn(fmt.Sprintf("%s: uid mismatch %s %s", name, uid, d.UID))
	}
	if gid != d.GID {
		warn(fmt.Sprintf("%s: gid mismatch %s %s", name, gid, d.GID))
	}
}

func extract(name string, mode, mtime uint32, uid, gid string, bytes int64) {
	if vflag {
		fmt.Printf("x %s %d bytes\n", name, bytes)
	}
	sfd, ok := create(name, sys.OWRITE, mode)
	if !ok {
		if !fflag || sys.Remove(name) != nil {
			warn("can't make file " + name + ": " + sys.Errstr())
			seekpast(bytes)
			return
		}
		if sfd, ok = create(name, sys.OWRITE, mode); !ok {
			warn("can't make file " + name + ": " + sys.Errstr())
			seekpast(bytes)
			return
		}
	}
	f := os.NewFile(uintptr(sfd), name)
	defer f.Close()
	b := bufio.NewWriterSize(f, atomicio)
	buf := make([]byte, atomicio)
	for tot := int64(0); tot < bytes; {
		n := int64(len(buf))
		if tot+n > bytes {
			n = bytes - tot
		}
		k, _ := io.ReadFull(bin, buf[:n])
		if k <= 0 {
			errorf("premature eof reading " + name)
		}
		if w, err := b.Write(buf[:k]); w != k || err != nil {
			warn("error writing " + name + ": " + sys.Errstr())
		}
		tot += int64(k)
	}
	if _, err := sys.Fstat(sfd); err != nil {
		warn("can't stat " + name + ": " + sys.Errstr())
	}
	d := sys.NullDir()
	d.Name = elem(name)
	if tflag {
		d.Mtime = mtime
	}
	if uflag {
		d.UID, d.GID = uid, gid
	}
	d.Mode = mode
	if err := b.Flush(); err != nil {
		warn("error writing " + name + ": " + sys.Errstr())
	}
	if err := sys.Fwstat(sfd, &d); err != nil {
		warn("can't set modes for " + name + ": " + sys.Errstr())
	}
	if uflag {
		nd, err := sys.Fstat(sfd)
		if err != nil {
			warn("can't reread modes for " + name + ": " + sys.Errstr())
			return
		}
		checkmodes(name, nd, mtime, uid, gid)
	}
}

func seekpast(bytes int64) {
	if k, _ := io.CopyN(io.Discard, bin, bytes); k != bytes {
		errorf("premature eof")
	}
}

func errorf(s string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", argv0, s)
	quit("error")
}

func warn(s string) { fmt.Fprintf(os.Stderr, "%s: %s\n", argv0, s) }

func parent(name string) string {
	if i := strings.LastIndexByte(name, '/'); i > 0 {
		return name[:i]
	}
	return "/"
}

// create makes name, making its parent writable for the moment if need
// be.
func create(name string, rw int, mode uint32) (int, bool) {
	fd, err := sys.Create(name, rw, mode)
	if err == nil {
		return fd, true
	}
	p := parent(name)
	d, err := sys.Stat(p)
	if err != nil {
		return -1, false
	}
	omode := d.Mode
	nd := sys.NullDir()
	nd.Mode = omode | 0o222
	if err := sys.Wstat(p, &nd); err != nil {
		warn("can't set modes for " + p + ": " + sys.Errstr())
		return -1, false
	}
	fd, err = sys.Create(name, rw, mode)
	nd.Mode = omode
	sys.Wstat(p, &nd)
	return fd, err == nil
}
