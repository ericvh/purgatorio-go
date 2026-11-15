// Command inst installs a package archive (Inferno install/inst). A file
// is installed unless the copy already there was changed locally or
// comes from a newer package; directories are always made. With file
// arguments only the files under them are installed. After the files,
// those the package's remove list names are removed, unless they were
// changed locally.
//
// Options: -r the root to install under (/); -h prints the headers of
// what would be installed instead; -t sets modification times and -u
// times, owners and groups; -v reports directories and removals; -f
// does not replace files that cannot be created; -F installs everything
// whatever the state of the installed files; -c carries on after an
// error.
package main

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arch"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wrap"
)

const argv0 = "inst"

var (
	tflag, uflag, hflag, vflag bool
	fflag                      = true
	bout                       *bufio.Writer
	oldw, w                    *wrap.Wrapped
	root                       = "/"
	force                      bool
	stoponerr                  = true
)

func main() {
	a := arg.New(os.Args)
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'f':
			fflag = false
		case 'h':
			hflag = true
			bout = bufio.NewWriter(os.Stdout)
		case 't':
			tflag = true
		case 'u':
			uflag = true
		case 'v':
			vflag = true
		case 'r':
			root = a.Arg()
			if root == "" {
				fatal("root missing")
			}
		case 'F':
			force = true
		case 'c':
			stoponerr = false
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		usage()
	}
	ar := arch.Open(args[0])
	if ar == nil {
		errorf("can't access " + args[0] + ": " + sys.Errstr())
	}
	w = wrap.OpenHdr(args[0], root, false)
	if w == nil {
		fatal("no such package found")
	}
	if len(w.U) != 1 {
		fatal("strange package: more than one piece")
	}
	if !force {
		oldw = wrap.Open(w.Name, root, false)
	}
	if !force && w.U[0].Utime != 0 && (oldw == nil || oldw.Tfull < w.U[0].Utime) {
		tfull := -1
		if oldw != nil {
			tfull = oldw.Tfull
		}
		fatal(fmt.Sprintf("need %s version of %s already installed (pkg %d)", wrap.Now2string(w.U[0].Utime, false), w.Name, tfull))
	}
	args = args[1:]
	digest := make([]byte, md5.Size)
	digest0 := make([]byte, md5.Size)
	digest1 := make([]byte, md5.Size)
	for {
		h := ar.Gethdr()
		if h == nil {
			break
		}
		why := ""
		docopy := false
		switch {
		case force:
			docopy = true
		case h.D.Mode&sys.DMDIR != 0:
			docopy = true
		case !wrap.Md5file(root+h.Name, digest):
			docopy = true
		default:
			wrap.Md5filea(root+h.Name, digest1)
			if ok, t := wrap.Getfileinfo(oldw, h.Name, digest, nil, digest1); ok {
				if t > w.U[0].Time {
					why = "version from newer package exists"
				} else {
					docopy = true
				}
			} else if ok, _ := wrap.Getfileinfo(oldw, h.Name, nil, nil, nil); ok {
				why = "locally modified"
			} else {
				why = "locally created"
			}
		}
		if !docopy {
			wrap.Md5sum(ar, digest0, int64(h.D.Length))
			if string(digest) != string(digest0) {
				fmt.Fprintf(os.Stderr, "skipping %s: %s\n", h.Name, why)
			}
			continue
		}
		if len(args) > 0 {
			if !selected(h.Name, args) {
				ar.Drain(int64(h.D.Length))
				continue
			}
			if !hflag {
				mkdirs(root, h.Name)
			}
		}
		name := wrap.Pathcat(root, h.Name)
		if hflag {
			fmt.Fprintf(bout, "%s %o %s %s %d %d\n", name, h.D.Mode, h.D.UID, h.D.GID, h.D.Mtime, int32(h.D.Length))
			ar.Drain(int64(h.D.Length))
			continue
		}
		if h.D.Mode&sys.DMDIR != 0 {
			mkdir(name, &h.D)
		} else {
			extract(ar, name, &h.D)
		}
	}
	ar.Close()
	if ar.Err == "" {
		quit("")
	}
	fmt.Fprintf(os.Stderr, "%s\n", ar.Err)
	quit("eof")
}

func doremove(s string) {
	p := wrap.Pathcat(root, s)
	digest := make([]byte, md5.Size)
	digest1 := make([]byte, md5.Size)
	if !wrap.Md5file(p, digest) {
		return
	}
	wrap.Md5filea(p, digest1)
	if ok, _ := wrap.Getfileinfo(oldw, s, digest, nil, digest1); !force && !ok {
		fmt.Fprintf(os.Stderr, "not removing %s: %s\n", p, "locally modified")
		return
	}
	if vflag {
		fmt.Printf("rm %s\n", p)
	}
	remove(p)
}

func quit(s string) {
	if s == "" {
		if b, err := os.ReadFile(w.U[0].Dir + "/remove"); err == nil {
			for _, t := range strings.SplitAfter(string(b), "\n") {
				if t != "" {
					doremove(strings.TrimSuffix(t, "\n"))
				}
			}
		}
	}
	if bout != nil {
		bout.Flush()
	}
	wrap.End()
	if s != "" {
		sys.Exits("fail: " + s)
	}
	fmt.Fprintf(os.Stderr, "done\n")
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
		if fd, err := create(basedir, sys.OREAD, 0o775|sys.DMDIR); err == nil {
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

// setmodes sets the mode, and with -t or -u the time and owner, of a
// file just installed.
func setmodes(name string, dir *sys.Dir) {
	d := sys.NullDir()
	d.Name = elem(name)
	d.Mode = dir.Mode
	if tflag || uflag {
		d.Mtime = dir.Mtime
	}
	if uflag {
		d.UID = dir.UID
		d.GID = dir.GID
	}
	if err := sys.Wstat(name, &d); err != nil {
		e := sys.Errstr()
		if !wstat(name, &d) {
			warn("can't set modes for " + name + ": " + e)
		}
	}
	if uflag {
		nd, err := sys.Stat(name)
		if err != nil {
			warn("can't reread modes for " + name + ": " + sys.Errstr())
			return
		}
		if dir.UID != nd.UID {
			warn(fmt.Sprintf("%s: uid mismatch %s %s", name, dir.UID, nd.UID))
		}
		if dir.GID != nd.GID {
			warn(fmt.Sprintf("%s: gid mismatch %s %s", name, dir.GID, nd.GID))
		}
	}
}

func mkdir(name string, dir *sys.Dir) {
	if vflag {
		const mtpt = "/n/remote"
		fmt.Printf("installing directory %s\n", strings.TrimPrefix(name, mtpt))
	}
	fd, err := create(name, sys.OREAD, dir.Mode)
	if err != nil {
		e := sys.Errstr()
		d, err := sys.Stat(name)
		if err != nil || d.Mode&sys.DMDIR == 0 {
			werr("can't make directory " + name + ": " + e)
			return
		}
	} else {
		sys.Close(fd)
	}
	setmodes(name, dir)
}

func extract(ar *arch.Archive, name string, dir *sys.Dir) {
	sfd, err := create(name, sys.OWRITE, dir.Mode)
	if err != nil {
		if !fflag || !remove(name) {
			werr("can't make file " + name + ": " + sys.Errstr())
			ar.Drain(int64(dir.Length))
			return
		}
		if sfd, err = create(name, sys.OWRITE, dir.Mode); err != nil {
			werr("can't make file " + name + ": " + sys.Errstr())
			ar.Drain(int64(dir.Length))
			return
		}
	}
	f := os.NewFile(uintptr(sfd), name)
	b := bufio.NewWriter(f)
	if e := ar.Getfile(b, int64(dir.Length)); e != "" {
		if strings.HasPrefix(e, "premature") {
			fatal(e)
		}
		warn(e)
	}
	if err := b.Flush(); err != nil {
		werr("error writing " + name + ": " + sys.Errstr())
	}
	f.Close()
	setmodes(name, dir)
}

func errorf(s string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", argv0, s)
	quit("error")
}

func werr(s string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", argv0, s)
	if stoponerr {
		quit("werr")
	}
}

func warn(s string) { fmt.Fprintf(os.Stderr, "%s: %s\n", argv0, s) }

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: inst [-h] [-u] [-v] [-f] [-c] [-F] [-r dest-root] [file ...]\n")
	sys.Exits("fail: usage")
}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "inst: %s\n", s)
	wrap.End()
	os.Exit(0)
}

func parent(name string) string {
	if i := strings.LastIndexByte(name, '/'); i > 0 {
		return name[:i]
	}
	return "/"
}

// create makes name, making its parent writable for the moment if need
// be.
func create(name string, rw int, mode uint32) (int, error) {
	fd, err := sys.Create(name, rw, mode)
	if err == nil {
		return fd, nil
	}
	p := parent(name)
	d, err2 := sys.Stat(p)
	if err2 != nil {
		return -1, err
	}
	omode := d.Mode
	nd := sys.NullDir()
	nd.Mode = omode | 0o222
	sys.Wstat(p, &nd)
	fd, err = sys.Create(name, rw, mode)
	nd.Mode = omode
	sys.Wstat(p, &nd)
	return fd, err
}

func remove(name string) bool {
	if sys.Remove(name) == nil {
		return true
	}
	d, err := sys.Stat(name)
	if err != nil {
		return false
	}
	omode := d.Mode
	nd := sys.NullDir()
	nd.Mode = omode | 0o222
	sys.Wstat(name, &nd)
	if sys.Remove(name) == nil {
		return true
	}
	nd.Mode = omode
	sys.Wstat(name, &nd)
	return false
}

func wstat(name string, d *sys.Dir) bool {
	dir, err := sys.Stat(name)
	if err != nil {
		return false
	}
	omode := dir.Mode
	nd := sys.NullDir()
	nd.Mode = omode | 0o222
	sys.Wstat(name, &nd)
	if sys.Wstat(name, d) == nil {
		return true
	}
	nd.Mode = omode
	sys.Wstat(name, &nd)
	return false
}
