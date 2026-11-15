// Command wdiff compares the files of a package with those installed
// under root (Inferno install/wdiff): for each file whose installed copy
// differs from the package's, it runs diff(1) (diff -b with -b) on the
// two, or with -l lists it as modified; a file that is gone is listed as
// removed. -p compares with the full package alone, ignoring updates;
// -r gives the root (/).
package main

import (
	"crypto/md5"
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wrap"
)

var (
	root    = "/"
	bflag   bool
	listing bool
	pkg     bool
)

func diff(w *wrap.Wrapped, name string) {
	wrapped := w.Root + "/" + name
	local := root + "/" + name
	if _, err := sys.Stat(local); err != nil {
		fmt.Printf("cannot stat %s\n", local)
		return
	}
	if _, err := sys.Stat(wrapped); err != nil {
		fmt.Printf("cannot stat %s\n", wrapped)
		return
	}
	args := []string{"diff", wrapped, local}
	if bflag {
		args = []string{"diff", "-b", wrapped, local}
	}
	wrap.Run("/bin/diff.wasm", args, [][2]int{{0, 0}, {1, 1}, {2, 2}})
}

func fatal(err string) {
	fmt.Fprintf(os.Stderr, "%s\n", err)
	wrap.End()
	os.Exit(0)
}

func main() {
	a := arg.New(os.Args)
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'b':
			bflag = true
		case 'l':
			listing = true
		case 'p':
			pkg = true
		case 'r':
			root = a.Arg()
			if root == "" {
				fatal("missing root name")
			}
		default:
			fatal(fmt.Sprintf("bad argument -%c", c))
		}
	}
	args := a.Argv()
	if len(args) != 1 {
		fatal("usage: install/wdiff [-blp] [-r root] package")
	}
	if _, err := sys.Stat(args[0]); err != nil {
		fatal("no such file " + args[0])
	}
	w := wrap.OpenHdr(args[0], root, !listing)
	if w == nil {
		fatal("no such package found")
	}
	if pkg {
		for len(w.U) > 0 && w.U[len(w.U)-1].Typ == wrap.UPD {
			w.U = w.U[:len(w.U)-1]
		}
	}
	digest := make([]byte, md5.Size)
	digest0 := make([]byte, md5.Size)
	for i := range w.U {
		for _, p := range w.U[i].MD5 {
			f := strings.Fields(p)
			if len(f) != 2 {
				fatal("error in md5sum file")
			}
			p = f[0]
			q := root + "/" + p
			if d, err := sys.Stat(q); err == nil && d.Mode&sys.DMDIR != 0 {
				continue
			}
			ok, t := wrap.Getfileinfo(w, p, nil, digest0, nil)
			if !ok {
				fmt.Printf("cannot happen\n")
				continue
			}
			if t != w.U[i].Time {
				continue
			}
			if !wrap.Md5file(q, digest) {
				fmt.Printf("%s removed\n", p)
				continue
			}
			if wrap.Md5conv(digest) == wrap.Md5conv(digest0) {
				continue
			}
			if listing {
				fmt.Printf("%s modified\n", p)
			} else {
				diff(w, p)
			}
		}
	}
	wrap.End()
}
