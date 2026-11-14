// Command cp copies files, and with -r directory trees (Inferno cp(1)).
package main

import (
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const atomicio = 8192

var (
	errors int
	gflag  bool
	uflag  bool
	xflag  bool
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("\tcp [-gux] src target\n\tcp [-r] [-gux] src ... directory")
	recursive := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'r':
			recursive = true
		case 'g':
			gflag = true
		case 'u':
			uflag, gflag = true, true
		case 'x':
			xflag = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) < 2 {
		a.Usage()
	}
	dst := args[len(args)-1]
	dir, err := sys.Stat(dst)
	todir := err == nil && dir.Mode&sys.DMDIR != 0
	if len(args) > 2 && !todir {
		os.Stderr.WriteString("cp: " + dst + "  not a directory\n")
		sys.Exits("fail:error")
	}
	if recursive {
		cpdir(args, dst)
	} else {
		for _, src := range args[:len(args)-1] {
			if todir {
				cp(src, dst, basename(src))
			} else {
				cp(src, dst, "")
			}
		}
	}
	if errors > 0 {
		sys.Exits("fail:error")
	}
}

// basename is the last non-empty element of s.
func basename(s string) string {
	for _, e := range strings.Split(s, "/") {
		if e != "" {
			s = e
		}
	}
	return s
}

func cp(src, dst, newname string) {
	if newname != "" {
		dst += "/" + newname
	}
	ds, err := sys.Stat(src)
	if err != nil {
		warning(src + ": " + sys.Errstr())
		return
	}
	if ds.Mode&sys.DMDIR != 0 {
		warning(src + " is a directory")
		return
	}
	if dd, err := sys.Stat(dst); err == nil && samefile(ds, dd) {
		warning(src + " and " + dst + " are the same file")
		return
	}
	sfd, err := sys.Open(src, sys.OREAD)
	if err != nil {
		warning("cannot open " + src + ": " + sys.Errstr())
		return
	}
	sf := os.NewFile(uintptr(sfd), src)
	defer sf.Close()
	dfd, err := sys.Create(dst, sys.OWRITE, ds.Mode&0o777)
	if err != nil {
		warning("cannot create " + dst + ": " + sys.Errstr())
		return
	}
	df := os.NewFile(uintptr(dfd), dst)
	defer df.Close()
	if copyfile(sf, df, src, dst) != 0 {
		return
	}
	if wstat(dfd, ds, false) != nil {
		warning("can't wstat " + src + ": " + sys.Errstr())
	}
}

func copyfile(sf, df *os.File, src, dst string) int {
	buf := make([]byte, atomicio)
	for {
		r, err := sf.Read(buf)
		if r > 0 {
			if w, _ := df.Write(buf[:r]); w != r {
				warning("error writing " + dst + ": " + sys.Errstr())
				return -1
			}
		}
		if err == io.EOF {
			return 0
		}
		if err != nil {
			warning("error reading " + src + ": " + sys.Errstr())
			return -1
		}
	}
}

func cpdir(args []string, dst string) {
	dexists := false
	dd, err := sys.Stat(dst)
	if err == nil {
		if dd.Mode&sys.DMDIR == 0 {
			warning(dst + ": destination not a directory")
			return
		}
		dexists = true
	}
	for _, src := range args[:len(args)-1] {
		ds, err := sys.Stat(src)
		if err != nil {
			warning("can't stat " + src + ": " + sys.Errstr())
			continue
		}
		switch {
		case ds.Mode&sys.DMDIR == 0:
			cp(src, dst, basename(src))
		case dexists:
			if samefile(ds, dd) {
				warning("cannot copy " + src + " into itself")
				continue
			}
			copydir(src, dst+"/"+basename(src), ds)
		default:
			copydir(src, dst, ds)
		}
	}
}

func copydir(src, dst string, srcd *sys.Dir) {
	if _, err := sys.Stat(dst); err == nil {
		warning("cannot copy " + src + " onto another directory")
		return
	}
	dfd, err := sys.Create(dst, sys.OREAD, sys.DMDIR|srcd.Mode|0o777)
	if err != nil {
		warning("cannot make directory " + dst + ": " + sys.Errstr())
		return
	}
	df := os.NewFile(uintptr(dfd), dst)
	defer df.Close()
	for _, e := range readdir(src) {
		path := src + "/" + e.Name
		if e.Mode&sys.DMDIR == 0 {
			cp(path, dst, e.Name)
		} else if seen(e) {
			warning(path + ": directory loop found")
		} else {
			copydir(path, dst+"/"+e.Name, e)
		}
	}
	if wstat(dfd, srcd, true) != nil {
		warning("can't wstat " + dst + ": " + sys.Errstr())
	}
}

// readdir returns the entries of a directory, each name once, in the
// order read.
func readdir(name string) []*sys.Dir {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return nil
	}
	defer os.NewFile(uintptr(fd), name).Close()
	var all []*sys.Dir
	names := map[string]bool{}
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			return nil
		}
		if len(ds) == 0 {
			return all
		}
		for i := range ds {
			if !names[ds[i].Name] {
				names[ds[i].Name] = true
				all = append(all, &ds[i])
			}
		}
	}
}

func wstat(fd int, ds *sys.Dir, mflag bool) error {
	if !xflag && !gflag && !uflag && !mflag {
		return nil
	}
	d := sys.NullDir()
	if xflag {
		d.Mtime = ds.Mtime
	}
	if xflag || mflag {
		d.Mode = ds.Mode
	}
	if uflag {
		d.UID = ds.UID
	}
	if gflag {
		d.GID = ds.GID
	}
	return sys.Fwstat(fd, &d)
}

func samefile(a, b *sys.Dir) bool {
	return a.Type == b.Type && a.Dev == b.Dev && a.Qid == b.Qid
}

// seen guards against loops in tangled name spaces.
var cache = map[uint64][]*sys.Dir{}

func seen(d *sys.Dir) bool {
	for _, c := range cache[d.Qid.Path] {
		if samefile(d, c) {
			return true
		}
	}
	cache[d.Qid.Path] = append(cache[d.Qid.Path], d)
	return false
}

func warning(e string) {
	os.Stderr.WriteString("cp: " + e + "\n")
	errors++
}
