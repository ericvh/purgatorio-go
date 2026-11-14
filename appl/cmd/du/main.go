// Command du summarizes disk usage, or with -t modification times
// (Inferno du(1)).
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	aflag     bool // all files, not just directories
	nflag     bool // names only (but see -t); implies -a
	sflag     bool // summary of top level names
	tflag     bool // modification time, not size
	uflag     bool // access time, not size
	blocksize = int64(1024)
	bout      = bufio.NewWriter(os.Stdout)
)

func kb(b int64) int64 {
	return (b + blocksize - 1) / blocksize * blocksize / 1024
}

func report(name string, mtime, atime uint32, l int64) {
	t := mtime
	if uflag {
		t = atime
	}
	switch {
	case nflag && tflag:
		fmt.Fprintf(bout, "%s %d %d %d\n", quote.Quote(name), t, l, 0)
	case nflag:
		fmt.Fprintf(bout, "%s\n", quote.Quote(name))
	case tflag:
		fmt.Fprintf(bout, "%d %s\n", t, quote.Quote(name))
	default:
		fmt.Fprintf(bout, "%-4d %s\n", kb(l), quote.Quote(name))
	}
}

// seen avoids loops in tangled name spaces.
type devqid struct {
	path uint64
	typ  uint16
	dev  uint32
}

var cache = map[devqid]bool{}

func seen(d *sys.Dir) bool {
	k := devqid{d.Qid.Path, d.Type, d.Dev}
	if cache[k] {
		return true
	}
	cache[k] = true
	return false
}

func readdir(name string) ([]sys.Dir, error) {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return nil, err
	}
	defer os.NewFile(uintptr(fd), name).Close()
	var all []sys.Dir
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			return nil, err
		}
		if len(ds) == 0 {
			break
		}
		all = append(all, ds...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

func dir(dirname string) int64 {
	prefix := dirname + "/"
	if dirname == "." {
		prefix = ""
	}
	var sum int64
	de, err := readdir(dirname)
	if err != nil {
		warn("can't read", dirname)
	}
	for i := range de {
		d := &de[i]
		s := prefix + d.Name
		if d.Mode&sys.DMDIR != 0 {
			if !seen(d) {
				size := dir(s)
				sum += size
				if !sflag && !nflag {
					report(s, d.Mtime, d.Atime, size)
				}
			}
		} else {
			l := int64(d.Length)
			sum += l
			if aflag {
				report(s, d.Mtime, d.Atime, l)
			}
		}
	}
	return sum
}

func du(name string) {
	d, err := sys.Stat(name)
	if err != nil {
		warn("can't stat", name)
		return
	}
	l := int64(d.Length)
	if d.Mode&sys.DMDIR != 0 {
		l = dir(name)
		if nflag && !sflag {
			return
		}
	}
	report(name, d.Mtime, d.Atime, l)
}

func warn(why, f string) {
	bout.Flush()
	os.Stderr.WriteString("du: " + why + " " + quote.Quote(f) + ": " + sys.Errstr() + "\n")
}

func main() {
	_, _ = sys.Pctl(sys.FORKFD, nil)
	a := arg.New(os.Args)
	a.SetUsage("du [-anstu] [-b bsize] [file ...]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			aflag = true
		case 'b':
			s := a.EArg()
			blocksize = bigatoi(s)
			if len(s) > 0 && s[len(s)-1] == 'k' {
				blocksize *= 1024
			}
			if blocksize <= 0 {
				blocksize = 1
			}
		case 'n':
			nflag, aflag = true, true
		case 's':
			sflag = true
		case 't':
			tflag = true
		case 'u':
			uflag, tflag = true, true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		args = []string{"."}
	}
	for _, f := range args {
		du(f)
	}
	bout.Flush()
}

// bigatoi is Limbo's big of a string: the leading decimal number.
func bigatoi(s string) int64 {
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.ParseInt(s[:i], 10, 64)
	return n
}
