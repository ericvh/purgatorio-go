// Command wfind finds the packages that hold files (Inferno
// install/wfind). It looks in the package archives in /install/package
// and the packages installed in /wrap, for the packages named with -p or
// else every directory in /install. A file argument matches each name in
// a package's md5sum that ends with it. For each name found it prints the
// name, a line for each package holding it (the digest there, the package
// and its time and kind, and where it was found), and the digest of the
// file as it is now.
package main

import (
	"crypto/md5"
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/proto"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wrap"
)

const (
	install = iota
	wrapped
)

type occ struct {
	w     *wrap.Wrapped
	i     int
	md5   string
	where int
	pkg   string
}

type stat struct {
	name string
	occs []occ
	md5  string
}

var stats []*stat

func fatal(err string) {
	fmt.Fprintf(os.Stderr, "%s\n", err)
	wrap.End()
	os.Exit(0)
}

func main() {
	var pkgs []string
	indir := "/install"
	a := arg.New(os.Args)
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'p':
			p := a.Arg()
			if p == "" {
				fatal("missing package name")
			}
			pkgs = append(pkgs, p)
		default:
			fatal(fmt.Sprintf("bad argument -%c", c))
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		fatal("usage: install/wfind [-p package ... ] file ...")
	}
	if pkgs != nil {
		for _, p := range pkgs {
			scanpkg(p, indir+"/"+p, args)
		}
	} else {
		ds, _ := proto.ReadDir(indir)
		for _, d := range ds {
			if d.Mode&sys.DMDIR != 0 {
				scanpkg(d.Name, indir+"/"+d.Name, args)
			}
		}
	}
	for i := len(stats) - 1; i >= 0; i-- {
		st := stats[i]
		fmt.Printf("%s\n", st.name)
		for _, o := range st.occs {
			fmt.Printf("\t%s %s/%s(%s)\t%s\n", o.md5, o.w.Name, wrap.Now2string(o.w.U[o.i].Time, false),
				ptype(o.w.U[o.i].Typ), wtype(o.where)+"/"+o.pkg)
		}
		fmt.Printf("\t%s %s\n", st.md5, st.name)
	}
	wrap.End()
}

func scanpkg(pkg, d string, files []string) {
	ds, _ := proto.ReadDir(d)
	for _, e := range ds {
		scanfile(pkg, d+"/"+e.Name, files)
	}
	w := wrap.Open(pkg, "/", false)
	if w == nil {
		return
	}
	for i := range w.U {
		scanw(w, i, files, wrapped, pkg)
	}
}

func scanfile(pkg, d string, files []string) {
	w := wrap.OpenHdr(d, "/", false)
	if w == nil {
		return
	}
	if len(w.U) != 1 {
		fatal("strange package: more than one piece")
	}
	scanw(w, 0, files, install, pkg)
	wrap.End()
}

func scanw(w *wrap.Wrapped, i int, files []string, where int, pkg string) {
	for _, p := range w.U[i].MD5 {
		l := strings.Fields(p)
		if len(l) != 2 {
			fatal(fmt.Sprintf("bad md5 file in %s\n", wtype(where)+"/"+w.Name+"/"+wrap.Now2string(w.U[i].Time, false)))
		}
		for _, f := range files {
			if strings.HasSuffix(l[0], f) {
				addfile(l[0], w, i, l[1], where, pkg)
			}
		}
	}
}

func addfile(file string, w *wrap.Wrapped, i int, md5s string, where int, pkg string) {
	for _, st := range stats {
		if st.name == file {
			st.occs = append(st.occs, occ{w, i, md5s, where, pkg})
			return
		}
	}
	digest := make([]byte, md5.Size)
	s := "non-existent" + strings.Repeat(" ", 32-12)
	if wrap.Md5file(file, digest) {
		s = wrap.Md5conv(digest)
	}
	stats = append(stats, &stat{name: file, occs: []occ{{w, i, md5s, where, pkg}}, md5: s})
}

func ptype(p int) string {
	return []string{"???", "package ", "update  ", "full upd"}[p]
}

func wtype(w int) string { return []string{"/install", "/wrap"}[w] }
