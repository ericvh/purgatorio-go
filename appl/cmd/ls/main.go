// Command ls lists files (Inferno ls(1)).
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const dmtmp = 0x04000000

// Sort keys, as Inferno's Readdir.
const (
	byName = iota
	byAtime
	byMtime
	bySize
	byNone
	compact    = 1 << 4
	descending = 1 << 5
)

var (
	dopt, eopt, lopt, mopt, nopt, popt, qopt, sopt, topt, uopt, Fopt, Topt bool

	now    int
	sortby int
	out    = bufio.NewWriter(os.Stdout)
	// Files named on the command line wait here to be sorted together.
	delayed []*entry
)

// entry is a Dir with how to print its name: a name given with a
// directory part prints as given.
type entry struct {
	sys.Dir
	prefixed bool
}

func main() {
	a := arg.New(os.Args)
	rev, comp := 0, 0
	sortby = byName
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'l':
			lopt = true
			now = daytime.Now()
		case 'p':
			popt = true
		case 'q':
			qopt = true
		case 'd':
			dopt = true
		case 'e':
			eopt = true
		case 'm':
			mopt = true
		case 'n':
			nopt = true
		case 'k':
			sopt = true
		case 't':
			topt = true
		case 'u':
			uopt = true
		case 's':
			sortby = bySize
		case 'c':
			comp = compact
		case 'r':
			rev = descending
		case 'T':
			Topt = true
		case 'F':
			Fopt = true
		default:
			os.Stderr.WriteString("usage: ls [-delmnpqrstucFT] [files]\n")
			sys.Exits("fail:usage")
		}
	}
	args := a.Argv()
	if !nopt {
		if topt {
			sortby = byMtime
			if uopt {
				sortby = byAtime
			}
		}
	} else {
		sortby = byNone
	}
	sortby |= rev | comp
	if len(args) == 0 {
		args = []string{"."}
		popt = true
	}
	errs := false
	for _, f := range args {
		if !ls(f) {
			errs = true
		}
	}
	delaywrite()
	out.Flush()
	if errs {
		sys.Exits("fail:errors")
	}
}

func ls(file string) bool {
	d, err := sys.Stat(file)
	if err != nil {
		out.Flush()
		os.Stderr.WriteString("ls: " + file + ": " + sys.Errstr() + "\n")
		return false
	}
	if dopt || d.Mode&sys.DMDIR == 0 {
		e := &entry{Dir: *d}
		if i := strings.LastIndexByte(file, '/'); i >= 0 {
			e.Name = file
			e.prefixed = true
		}
		delayed = append(delayed, e)
		return true
	}
	delaywrite()
	ents, err := readdir(file, sortby)
	if err != nil {
		out.Flush()
		os.Stderr.WriteString("ls: " + file + ": " + sys.Errstr() + "\n")
		return false
	}
	lsprint(file, ents)
	return true
}

func delaywrite() {
	if len(delayed) == 0 {
		return
	}
	sortdir(delayed, sortby)
	lsprint("", delayed)
	delayed = nil
}

// readdir reads a directory as Readdir->init does: with compact, each
// name once; then sorted unless the key is none.
func readdir(name string, key int) ([]*entry, error) {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return nil, err
	}
	defer os.NewFile(uintptr(fd), name).Close()
	var all []*entry
	seen := map[string]bool{}
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			return nil, err
		}
		if len(ds) == 0 {
			break
		}
		for _, d := range ds {
			if key&compact != 0 {
				if seen[d.Name] {
					continue
				}
				seen[d.Name] = true
			}
			all = append(all, &entry{Dir: d})
		}
	}
	key &^= compact
	if key&^descending != byNone {
		sortdir(all, key)
	}
	return all, nil
}

// sortdir sorts stably by key. As in Inferno, a key that still carries
// the compact bit, or is none, leaves the order alone.
func sortdir(a []*entry, key int) {
	var less func(x, y *entry) bool
	switch key {
	case byName:
		less = func(x, y *entry) bool { return x.Name < y.Name }
	case byAtime:
		less = func(x, y *entry) bool { return x.Atime > y.Atime }
	case byMtime:
		less = func(x, y *entry) bool { return x.Mtime > y.Mtime }
	case bySize:
		less = func(x, y *entry) bool { return x.Length < y.Length }
	case byName | descending:
		less = func(x, y *entry) bool { return x.Name > y.Name }
	case byAtime | descending:
		less = func(x, y *entry) bool { return x.Atime < y.Atime }
	case byMtime | descending:
		less = func(x, y *entry) bool { return x.Mtime < y.Mtime }
	case bySize | descending:
		less = func(x, y *entry) bool { return x.Length > y.Length }
	default:
		return
	}
	sort.SliceStable(a, func(i, j int) bool { return less(a[i], a[j]) })
}

type widths struct{ vers, dev, uid, gid, muid, length, size int }

func dowidths(ents []*entry) widths {
	var w widths
	for _, d := range ents {
		if sopt {
			w.size = max(w.size, len(strconv.FormatUint((d.Length+1023)/1024, 10)))
		}
		if mopt {
			w.muid = max(w.muid, len(d.MUID)+2)
		}
		if qopt {
			w.vers = max(w.vers, len(strconv.FormatUint(uint64(d.Qid.Vers), 10)))
		}
		if lopt {
			w.dev = max(w.dev, len(strconv.FormatUint(uint64(d.Dev), 10)))
			w.uid = max(w.uid, len(d.UID))
			w.gid = max(w.gid, len(d.GID))
			w.length = max(w.length, len(strconv.FormatUint(d.Length, 10)))
		}
	}
	return w
}

func lsprint(dirname string, ents []*entry) {
	w := dowidths(ents)
	for _, d := range ents {
		lsline(dirname, d, &w)
	}
}

func lsline(dirname string, d *entry, w *widths) {
	if sopt {
		fmt.Fprintf(out, "%*d ", w.size, (d.Length+1023)/1024)
	}
	if mopt {
		fmt.Fprintf(out, "[%s] ", d.MUID)
		for i := len(d.MUID) + 2; i < w.muid; i++ {
			out.WriteByte(' ')
		}
	}
	if qopt {
		fmt.Fprintf(out, "(%.16x %*d %.2x) ", d.Qid.Path, w.vers, d.Qid.Vers, d.Qid.Type)
	}
	if Topt {
		if d.Mode&dmtmp != 0 {
			out.WriteString("t ")
		} else {
			out.WriteString("- ")
		}
	}
	file := d.Name
	switch {
	case popt:
		if d.prefixed {
			file = file[strings.LastIndexByte(file, '/')+1:]
		}
	case dirname != "":
		if strings.HasSuffix(dirname, "/") {
			file = dirname + file
		} else {
			file = dirname + "/" + file
		}
	}
	if Fopt {
		file += fileflag(d)
	}
	if !lopt {
		out.WriteString(file + "\n")
		return
	}
	t := d.Mtime
	if uopt {
		t = d.Atime
	}
	var ts string
	if eopt {
		ts = strconv.FormatUint(uint64(t), 10)
	} else {
		ts = daytime.Filet(now, int(t))
	}
	fmt.Fprintf(out, "%s %c %*d %-*s %-*s %*d %s %s\n",
		modes(d.Mode), rune(d.Type), w.dev, d.Dev, w.uid, d.UID, w.gid, d.GID,
		w.length, d.Length, ts, file)
}

func fileflag(d *entry) string {
	if d.Qid.Type&sys.QTDIR != 0 {
		return "/"
	}
	if d.Mode&0o111 != 0 {
		return "*"
	}
	return ""
}

var mtab = []string{"---", "--x", "-w-", "-wx", "r--", "r-x", "rw-", "rwx"}

func modes(mode uint32) string {
	var s string
	switch {
	case mode&sys.DMDIR != 0:
		s = "d"
	case mode&sys.DMAPPEND != 0:
		s = "a"
	case mode&sys.DMAUTH != 0:
		s = "A"
	default:
		s = "-"
	}
	if mode&sys.DMEXCL != 0 {
		s += "l"
	} else {
		s += "-"
	}
	return s + mtab[mode>>6&7] + mtab[mode>>3&7] + mtab[mode&7]
}
