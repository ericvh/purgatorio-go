// Command ckproto checks proto files against a file tree (Inferno
// install/ckproto): it prints each file under root (/ by default, -r) that
// no proto names, and each name a proto repeats. -o leaves out generated
// files (.dis, .sbl and object files), -v prints the files and the tree
// the protos describe, and -m reports directories that are not mode 775
// and files not 664 at least.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/proto"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

type dir struct {
	name, proto            string
	parent, child, sibling *dir
}

var (
	root    = "/"
	droot   *dir
	protof  string
	omitgen bool
	verbose bool
	ckmode  bool
)

func main() {
	sys.Pctl(sys.NEWPGRP|sys.FORKNS|sys.FORKFD, nil)
	a := arg.New(os.Args)
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'r':
			root = a.Arg()
			if root == "" {
				fatal("missing argument to -r")
			}
		case 'o':
			omitgen = true
		case 'v':
			verbose = true
		case 'm':
			ckmode = true
		default:
			fatal("usage: install/ckproto [-o] [-v] [-m] [-r root] protofile ....")
		}
	}
	droot = &dir{name: "/"}
	droot.parent = droot
	r := &proto.Reader{Install: true, Entry: protofile, Problem: protoerr}
	for _, p := range a.Argv() {
		protof = p
		if err := r.ReadFile(p, root); err != nil {
			protoerr(proto.Fatal, 0, "", "can't open "+p+": "+err.Error()+": skipping\n")
		}
	}
	if verbose {
		prtree(droot, -1)
	}
	ckdir(root, droot)
}

func protofile(newf, oldf string, _ *sys.Dir) {
	if verbose {
		if oldf == newf {
			fmt.Printf("%s\n", newf)
		} else {
			fmt.Printf("%s %s\n", newf, oldf)
		}
	}
	addfile(droot, oldf)
	if newf != oldf {
		addfile(droot, newf)
	}
}

func protoerr(lev, line int, _, err string) {
	s := fmt.Sprintf("line %d : %s", line, err)
	switch lev {
	case proto.Warn:
		fmt.Printf("%s: %s\n", protof, s)
	case proto.Error:
		fmt.Fprintf(os.Stderr, "%s: %s\n", protof, s)
		os.Exit(0)
	case proto.Fatal:
		fatal(s)
	}
}

func ckdir(d string, dird *dir) {
	ds, _ := proto.ReadDir(d)
	for _, e := range ds {
		dire := lookup(dird, e.Name)
		if omitgen && generated(e.Name) {
			continue
		}
		p := proto.Mkpath(d, e.Name)
		if dire == nil {
			fmt.Printf("%s missing\n", p)
			continue
		}
		if ckmode {
			if e.Mode&sys.DMDIR != 0 {
				if e.Mode&0o775 != 0o775 {
					fmt.Printf("directory %s not 775 at least\n", p)
				}
			} else if e.Mode&0o664 != 0o664 {
				fmt.Printf("file %s not 664 at least\n", p)
			}
		}
		if e.Mode&sys.DMDIR != 0 {
			ckdir(p, dire)
		}
	}
}

func addfile(root *dir, path string) {
	curd := root
	opath := path
	for path != "" {
		var elem string
		elem, path = split(path)
		d := lookup(curd, elem)
		if d == nil {
			d = &dir{name: elem, proto: protof, parent: curd}
			var prev *dir
			this := curd.child
			for ; this != nil; this = this.sibling {
				if elem < this.name {
					d.sibling = this
					if prev == nil {
						curd.child = d
					} else {
						prev.sibling = d
					}
					break
				}
				prev = this
			}
			if this == nil {
				if prev == nil {
					curd.child = d
				} else {
					prev.sibling = d
				}
			}
		} else if path == "" && d.proto == protof {
			fmt.Printf("%s repeated in proto %s\n", opath, protof)
		}
		curd = d
	}
}

func lookup(p *dir, f string) *dir {
	switch f {
	case ".":
		return p
	case "..":
		return p.parent
	}
	for d := p.child; d != nil; d = d.sibling {
		if d.name == f {
			return d
		}
		if d.name > f {
			return nil
		}
	}
	return nil
}

func prtree(root *dir, indent int) {
	if indent >= 0 {
		fmt.Printf("%s%s\n", strings.Repeat("\t", indent), root.name)
	}
	for s := root.child; s != nil; s = s.sibling {
		prtree(s, indent+1)
	}
}

func split(p string) (string, string) {
	if p == "" {
		fatal("nil string in split")
	}
	if p[0] != '/' {
		fatal("p0 notg / in split")
	}
	p = strings.TrimLeft(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i:]
	}
	return p, ""
}

var gens = []string{"dis", "sbl", "out", "0", "1", "2", "5", "8", "k", "q", "v", "t"}

func generated(f string) bool {
	i := strings.LastIndexByte(f, '.')
	if i < 0 {
		return false
	}
	for _, g := range gens {
		if f[i+1:] == g {
			return true
		}
	}
	return false
}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "fatal: %s\n", s)
	os.Exit(0)
}
