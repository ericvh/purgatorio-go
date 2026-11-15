// Command proto2list prints a version list of the files that a series of
// proto files name (Inferno install/proto2list): for each file, sorted
// and once only, its name, the name it is installed as ("-" if the
// same), its length, mode, modification time and CRC-32, and 0.
package main

import (
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"

	"github.com/ericvh/purgatorio-go/sdk/proto"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

type element struct{ old, new string }

var (
	protof string
	flist  []element
)

func main() {
	r := &proto.Reader{Install: true, Entry: protofile, Problem: protoerr}
	for _, a := range os.Args[1:] {
		protof = a
		if err := r.ReadFile(a, "/"); err != nil {
			protoerr(proto.Fatal, 0, "", "can't open "+a+": "+err.Error()+": skipping\n")
		}
	}
	sort.SliceStable(flist, func(i, j int) bool { return flist[i].old < flist[j].old })
	out(uniq(flist))
}

func protofile(newf, oldf string, _ *sys.Dir) {
	if newf == oldf {
		newf = "-"
	}
	flist = append(flist, element{oldf, newf})
}

func out(fs []element) {
	for _, e := range fs {
		d, err := sys.Stat(e.old)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot open %s\n", e.old)
			continue
		}
		if d.Mode&sys.DMDIR != 0 {
			d.Length = 0
		}
		fmt.Printf("%s\t%s\t%d\t%d\t%d\t%d\t%d\n", e.old, e.new, int32(d.Length), int32(d.Mode), int32(d.Mtime), crc(e.old, d), 0)
	}
}

func protoerr(lev, line int, _, err string) {
	s := fmt.Sprintf("line %d : %s", line, err)
	switch lev {
	case proto.Warn:
		warn(s)
	case proto.Error:
		fmt.Fprintf(os.Stderr, "%s: %s\n", protof, s)
		os.Exit(0)
	case proto.Fatal:
		fmt.Fprintf(os.Stderr, "fatal: %s\n", s)
		os.Exit(0)
	}
}

func warn(s string) { fmt.Fprintf(os.Stderr, "%s: %s\n", protof, s) }

// crc is the CRC-32 of a file, as Inferno's Crc module computes it.
func crc(f string, d *sys.Dir) int32 {
	if d.Mode&sys.DMDIR != 0 {
		return 0
	}
	fd, err := os.Open(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open %s\n", f)
		return 0
	}
	defer fd.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, fd); err != nil {
		fmt.Fprintf(os.Stderr, "bad read on %s : %s\n", f, sys.Errstr())
		return 0
	}
	return int32(h.Sum32())
}

func uniq(a []element) []element {
	var out []element
	for i := 0; i < len(a); i++ {
		if len(out) > 0 && out[len(out)-1].old == a[i].old {
			if p := out[len(out)-1]; p.new != a[i].new {
				warn(fmt.Sprintf("duplicate %s(%s %s)", p.old, p.new, a[i].new))
			}
			continue
		}
		out = append(out, a[i])
	}
	return out
}
