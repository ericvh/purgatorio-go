// Command rm removes files and, with -r, directory trees (Inferno rm(1)).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	recurse bool
	quiet   bool
	force   bool
	errs    int
)

func usage() {
	os.Stderr.WriteString("Usage: rm [-fr] file ...\n")
	sys.Exits("fail: usage")
}

func main() {
	a := arg.New(os.Args)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'r':
			recurse = true
		case 'f':
			quiet = true
		case 'F':
			force = true
		default:
			usage()
		}
	}
	_, _ = sys.Pctl(sys.FORKNS, nil)
	for _, name := range a.Argv() {
		if err := sys.Remove(name); err != nil {
			e := sys.Errstr()
			if d, serr := sys.Stat(name); recurse && serr == nil && d.Mode&sys.DMDIR != 0 {
				rmdir(name)
			} else {
				warn(name, e)
			}
		}
	}
	if errs > 0 {
		sys.Exits("fail:errors")
	}
}

func rmdir(name string) {
	if force {
		d := sys.NullDir()
		d.Mode = 0o777 | sys.DMDIR
		_ = sys.Wstat(name, &d)
	}
	for _, d := range readdir(name) {
		path := name + "/" + d.Name
		if d.Mode&sys.DMDIR != 0 {
			rmdir(path)
		} else {
			remove(path)
		}
	}
	remove(name)
}

// readdir returns a directory's entries, each name once, unsorted.
func readdir(name string) []sys.Dir {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return nil
	}
	defer os.NewFile(uintptr(fd), name).Close()
	var all []sys.Dir
	seen := map[string]bool{}
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			return nil
		}
		if len(ds) == 0 {
			return all
		}
		for _, d := range ds {
			if !seen[d.Name] {
				seen[d.Name] = true
				all = append(all, d)
			}
		}
	}
}

func remove(name string) {
	if err := sys.Remove(name); err != nil {
		warn(name, sys.Errstr())
	}
}

func warn(name, e string) {
	if !quiet {
		os.Stderr.WriteString("rm: " + name + ": " + e + "\n")
		errs++
	}
}
