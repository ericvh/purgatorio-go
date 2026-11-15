// Command wrap2list prints a version list of the files that the
// packages installed in /wrap hold (Inferno install/wrap2list), less those
// their updates remove, and of the files named in the md5sum-style files
// given as arguments: for each, its name, "-", its length, mode,
// modification time (0 with -i) and CRC-32, and 0.
package main

import (
	"bufio"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wrap"
)

var flist = map[string]bool{}

func main() {
	argv := os.Args[1:]
	initf := false
	if len(argv) > 0 && argv[0] == "-i" {
		initf = true
		argv = argv[1:]
	}
	if fd, err := sys.Open("/wrap", sys.OREAD); err == nil {
		for {
			ds, err := sys.Dirread(fd)
			if err != nil || len(ds) == 0 {
				break
			}
			for _, d := range ds {
				if d.Mode&sys.DMDIR == 0 {
					continue
				}
				w := wrap.Open(d.Name, "/", true)
				if w == nil {
					continue
				}
				for _, u := range w.U {
					for _, l := range u.MD5 {
						add(l)
					}
					if b, err := os.ReadFile(u.Dir + "/remove"); err == nil {
						for _, l := range strings.SplitAfter(string(b), "\n") {
							if f := strings.Fields(l); len(f) > 0 {
								delete(flist, f[0])
							}
						}
					}
				}
			}
		}
		sys.Close(fd)
	}
	for _, a := range argv {
		f, err := os.Open(a)
		if err != nil {
			continue
		}
		b := bufio.NewReader(f)
		for {
			l, err := b.ReadString('\n')
			add(l)
			if err != nil {
				break
			}
		}
		f.Close()
	}
	var fs []string
	for f := range flist {
		fs = append(fs, f)
	}
	sort.Strings(fs)
	out(fs, initf)
	wrap.End()
}

func add(l string) {
	if f := strings.Fields(l); len(f) > 0 {
		flist[f[0]] = true
	}
}

func out(fs []string, initf bool) {
	for _, f := range fs {
		outl(f, "", initf)
		if strings.HasSuffix(f, "emu.new") {
			g := f
			f = f[:len(f)-3] + "exe"
			outl(f, g, initf)
			outl(f[:len(f)-4], g, initf)
		}
	}
}

func outl(f, g string, initf bool) {
	d, err := sys.Stat(f)
	if err != nil {
		return
	}
	if g == "" {
		g = "-"
	}
	if d.Mode&sys.DMDIR != 0 {
		d.Length = 0
	}
	mtime := int32(d.Mtime)
	if initf {
		mtime = 0
	}
	fmt.Printf("%s\t%s\t%d\t%d\t%d\t%d\t%d\n", f, g, int32(d.Length), int32(d.Mode), mtime, crc(f, d), 0)
}

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
