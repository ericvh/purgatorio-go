// Command fcp copies files, like cp, but with many reads and writes in
// flight (Inferno fcp(1)). A guest has one thread, so here the blocks are
// copied one after another; -R and -W are accepted and checked, and with
// either above 1 the copy goes by the source's length with positioned
// reads and writes, as Inferno's does.
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
	errors  = 0
	multi   = false
	visited = map[[3]uint64]bool{}
)

func main() {
	recursive := false
	nreaders, nwriters := 8, 8
	a := arg.New(os.Args)
	a.SetUsage("\tfcp [-r] [-R nproc] [-W nproc] src target\n\tfcp [-r] [-R nproc] [-W nproc] src ... directory")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'R':
			nreaders = atoi(a.EArg())
		case 'W':
			nwriters = atoi(a.EArg())
		case 'r':
			recursive = true
		default:
			a.Usage()
		}
	}
	if nreaders < 1 || nwriters < 1 {
		a.Usage()
	}
	multi = nreaders > 1 || nwriters > 1
	argv := a.Argv()
	if len(argv) < 2 {
		a.Usage()
	}
	dst := argv[len(argv)-1]
	d, err := sys.Stat(dst)
	todir := err == nil && d.Mode&sys.DMDIR != 0
	if len(argv) > 2 && !todir {
		os.Stderr.WriteString("fcp: " + dst + "  not a directory\n")
		sys.Exits("fail:error")
	}
	if recursive {
		cpdir(argv, dst)
	} else {
		for _, src := range argv[:len(argv)-1] {
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

func basename(s string) string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == '/' })
	if len(f) == 0 {
		return s
	}
	return f[len(f)-1]
}

func warning(e string) {
	os.Stderr.WriteString("fcp: " + e + "\n")
	errors++
}

func same(a, b *sys.Dir) bool {
	return a.Qid.Path == b.Qid.Path && a.Dev == b.Dev && a.Type == b.Type
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
	if dd, err := sys.Stat(dst); err == nil && same(ds, dd) {
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
	dfd, err := sys.Create(dst, sys.OWRITE, ds.Mode)
	if err != nil {
		warning("cannot create " + dst + ": " + sys.Errstr())
		return
	}
	df := os.NewFile(uintptr(dfd), dst)
	defer df.Close()
	if multi {
		mcopy(sf, df)
	} else {
		copyfile(sf, df, src, dst)
	}
}

func copyfile(sf, df *os.File, src, dst string) {
	buf := make([]byte, atomicio)
	for {
		n, err := sf.Read(buf)
		if n == 0 && err == io.EOF {
			return
		}
		if err != nil && err != io.EOF {
			warning("error reading " + src + ": " + sys.Errstr())
			return
		}
		if w, err := df.Write(buf[:n]); err != nil || w != n {
			warning("error writing " + dst + ": " + sys.Errstr())
			return
		}
	}
}

// mcopy copies the source's length in blocks at their offsets; as in
// Inferno, errors here are not reported.
func mcopy(sf, df *os.File) {
	d, err := sys.Fstat(int(sf.Fd()))
	if err != nil {
		return
	}
	buf := make([]byte, atomicio)
	for o := int64(0); o < int64(d.Length); o += atomicio {
		nb := min(int64(d.Length)-o, atomicio)
		n, _ := sf.ReadAt(buf[:nb], o)
		if n > 0 {
			df.WriteAt(buf[:n], o)
		}
	}
}

func cpdir(argv []string, dst string) {
	dexists := false
	dd, err := sys.Stat(dst)
	if err == nil {
		if dd.Mode&sys.DMDIR == 0 {
			warning(dst + ": destination not a directory")
			return
		}
		dexists = true
	}
	for _, src := range argv[:len(argv)-1] {
		ds, err := sys.Stat(src)
		if err != nil {
			warning("can't stat " + src + ": " + sys.Errstr())
			continue
		}
		switch {
		case ds.Mode&sys.DMDIR == 0:
			cp(src, dst, basename(src))
		case dexists:
			if same(ds, dd) {
				warning("cannot copy " + src + " into itself")
				continue
			}
			copydir(src, dst+"/"+basename(src), ds.Mode)
		default:
			copydir(src, dst, ds.Mode)
		}
	}
}

func copydir(src, dst string, srcmode uint32) {
	if _, err := sys.Stat(dst); err == nil {
		warning("cannot copy " + src + " onto another directory")
		return
	}
	fd, err := sys.Create(dst, sys.OREAD, sys.DMDIR|srcmode|0o777)
	if err != nil {
		warning("cannot make directory " + dst + ": " + sys.Errstr())
		return
	}
	os.NewFile(uintptr(fd), dst).Close()
	for _, e := range readdir(src) {
		path := src + "/" + e.Name
		if e.Mode&sys.DMDIR == 0 {
			cp(path, dst, e.Name)
		} else if seen(&e) {
			warning(path + ": directory loop found")
		} else {
			copydir(path, dst+"/"+e.Name, e.Mode)
		}
	}
	chmod(dst, srcmode)
}

// readdir reads a whole directory, dropping repeated names as union
// directories can give.
func readdir(name string) []sys.Dir {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return nil
	}
	defer os.NewFile(uintptr(fd), name).Close()
	var all []sys.Dir
	names := map[string]bool{}
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			return all
		}
		for _, d := range ds {
			if !names[d.Name] {
				names[d.Name] = true
				all = append(all, d)
			}
		}
	}
}

// seen reports whether a directory was copied already, to stop loops in
// tangled name spaces.
func seen(d *sys.Dir) bool {
	k := [3]uint64{d.Qid.Path, uint64(d.Type), uint64(d.Dev)}
	if visited[k] {
		return true
	}
	visited[k] = true
	return false
}

func chmod(s string, mode uint32) {
	d, err := sys.Stat(s)
	if err != nil || d.Mode == mode {
		return
	}
	nd := sys.NullDir()
	nd.Mode = mode
	if err := sys.Wstat(s, &nd); err != nil {
		warning("cannot wstat " + s + ": " + sys.Errstr())
	}
}

// atoi converts a leading decimal number, as Limbo's int of a string.
func atoi(s string) int {
	i, neg := 0, false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	n := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		n = -n
	}
	return n
}
