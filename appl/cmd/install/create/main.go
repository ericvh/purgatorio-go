// Command create makes a package archive from the files a proto file
// names (Inferno install/create). The archive, named by the package time,
// starts with the package's /wrap files: its proto, the sorted md5sum of
// its files and, for an update (-u), the files to remove. -u makes an
// update of the installed package old-package, holding only the files
// that changed, under the given prefixes (or, with -n, outside them).
//
// Options: -o writes the archive on standard output; -x lists the files
// instead; -p proto; -r root of the installed package; -s source tree;
// -N and -G the owner and group in the archive; -d (or -D) description;
// -t package time; -i a notes file; -R a file listing more files to
// remove; -P leaves modes as they are; -v lists the files on standard
// error.
package main

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arch"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/proto"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wrap"
)

var (
	bout      *bufio.Writer
	protof    string
	notesf    string
	oldroot   string
	verb      bool
	xflag     bool
	uid, gid  string
	desc      string
	pass      int
	update    bool
	md5s      []byte // names hashed in each pass
	w         *wrap.Wrapped
	root      = "/"
	prefix    []string
	notprefix []string
	onlist    [][2]string // old, new
	remfile   string
	tmpfiles  []string
	a         *arg.Arg
)

func n2o(n string) string {
	for _, on := range onlist {
		if on[1] == n {
			return on[0]
		}
	}
	return n
}

func main() {
	now := daytime.Now()
	sys.Pctl(sys.NEWPGRP|sys.FORKNS|sys.FORKFD, nil)
	desc = "inferno"
	tostdout, not := false, false
	a = arg.New(os.Args)
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'n':
			not = true
		case 'o':
			tostdout = true
		case 'p':
			protof = reqarg("proto file (-p)")
		case 'r':
			root = reqarg("root directory (-r)")
		case 's':
			oldroot = reqarg("source directory (-d)")
		case 'u':
			update = true
		case 'v':
			verb = true
		case 'x':
			xflag = true
		case 'N':
			uid = reqarg("user name (-U)")
		case 'G':
			gid = reqarg("group name (-G)")
		case 'd', 'D':
			desc = reqarg("product description (-D)")
		case 't':
			now, _ = strconv.Atoi(reqarg("package time (-t)"))
		case 'i':
			notesf = reqarg("file (-i)")
		case 'R':
			remfile = reqarg("remove file (-R)")
		case 'P':
			arch.AddPerms(false)
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		usage()
	}
	if tostdout || xflag {
		bout = bufio.NewWriter(os.Stdout)
	} else {
		ar := wrap.Now2string(now, false)
		fd, err := sys.Create(ar, sys.OWRITE, 0o664)
		if err != nil {
			errorf("can't create " + ar + " for archive: " + sys.Errstr())
		}
		bout = bufio.NewWriter(os.NewFile(uintptr(fd), ar))
		fmt.Printf("archiving package %s to %s\n", args[0], ar)
	}
	name := args[0]
	if update && len(args) > 1 {
		if not {
			notprefix = args[1:]
		} else {
			prefix = args[1:]
		}
	} else if len(args) > 1 {
		fatal("only one name allowed")
	}
	var digest []byte
	if !xflag {
		digest = wrapinit(name, now)
	}
	fmt.Fprintf(os.Stderr, "processing %s\n", protof)
	rdproto()
	if !xflag {
		wrapend(digest)
		fmt.Fprintf(os.Stderr, "file system made\n")
	}
	arch.Putend(bout)
	exits()
}

func rdproto() {
	r := &proto.Reader{Install: true, Entry: protofile, Problem: protoerr}
	if err := r.ReadFile(protof, oldroot); err != nil {
		protoerr(proto.Fatal, 0, "", "can't open "+protof+": "+err.Error()+": skipping\n")
	}
}

func protofile(newf, oldf string, d *sys.Dir) {
	if xflag && bout != nil {
		fmt.Fprintf(bout, "%s\t%d\t%d\n", newf, d.Mtime, int64(d.Length))
		return
	}
	d.UID = uid
	d.GID = gid
	if d.Mode&sys.DMDIR == 0 {
		f, err := os.Open(oldf)
		if err != nil {
			warn("can't open " + oldf + ": " + sys.Errstr())
			return
		}
		f.Close()
	}
	mkarch(newf, oldf, d)
}

func protoerr(lev, line int, _, err string) {
	s := fmt.Sprintf("line %d : %s", line, err)
	switch lev {
	case proto.Warn:
		warn(s)
	case proto.Error:
		errorf(s)
	case proto.Fatal:
		fatal(s)
	}
}

func reqarg(what string) string {
	o := a.Arg()
	if o == "" {
		fmt.Fprintf(os.Stderr, "missing %s\n", what)
		exits()
	}
	return o
}

func errorf(s string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", protof, s)
	if bout != nil {
		bout.Flush()
	}
	exits()
}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "fatal: %s\n", s)
	exits()
}

func warn(s string) { fmt.Fprintf(os.Stderr, "%s: %s\n", protof, s) }

func usage() {
	fmt.Fprintf(os.Stderr, "usage: install/create [-ovx] [-N uid] [-G gid] [-r root] [-d desc] [-s src-fs] [-p proto] name\n")
	fmt.Fprintf(os.Stderr, "or install/create -u [-ovx] [-N uid] [-G gid] [-r root] [-d desc] [-s src-fs] [-p proto] old-package [prefix ...]\n")
	exits()
}

func wrapinit(name string, t int) []byte {
	if uid == "" {
		uid = "inferno"
	}
	if gid == "" {
		gid = "inferno"
	}
	if update {
		w = wrap.OpenHdr(name, root, false)
		if w == nil {
			fatal("no such package found")
		}
		if protof == "" {
			protof = w.U[0].Dir + "/proto"
		}
		name = w.Name
	} else if protof == "" {
		fatal("proto file missing")
	}
	md5file, md5fd := opentemp("wrap.md5", t)
	if md5fd == nil {
		fatal("cannot create " + md5file)
	}
	md5s = nil
	fmt.Fprintf(os.Stderr, "wrap pass %s\n", protof)
	obout := bout
	bout = bufio.NewWriter(md5fd)
	pass = 0
	rdproto()
	bout.Flush()
	md5fd.Close()
	digest := md5.Sum(md5s)
	md5sort, md5sfd := opentemp("wrap.md5s", t)
	if md5sfd == nil {
		fatal("cannot create " + md5sort)
	}
	if !fsort(md5sfd, md5file) {
		fatal("sort failed")
	}
	md5sfd.Close()
	var rmfile string
	if update {
		var rmfd *os.File
		rmfile, rmfd = opentemp("wrap.rm", t)
		if rmfd == nil {
			fatal("cannot create " + rmfile)
		}
		var rmed []string
		for i := len(w.U) - 1; i >= 0; i-- {
			for _, p := range w.U[i].MD5 {
				if prefix != nil && !wrap.Match(p, prefix) {
					continue
				}
				if notprefix != nil && !wrap.Notmatch(p, notprefix) {
					continue
				}
				q, _, _ := strings.Cut(p, " ")
				q = wrap.Pathcat(root, q)
				_, err := sys.Stat(q)
				if err != nil {
					_, err = sys.Stat(n2o(q))
				}
				if strings.HasSuffix(q, "emu.new") {
					continue
				}
				if err != nil && !contains(rmed, q) {
					rmed = append([]string{q}, rmed...)
				}
			}
		}
		for _, r := range rmed {
			fmt.Fprintf(rmfd, "%s\n", r)
		}
		if remfile != "" {
			if b, err := os.ReadFile(remfile); err == nil {
				rmfd.Write(b)
			}
		}
		rmfd.Close()
	}
	bout = obout
	if update {
		wrap.Putwrap(bout, name, t, desc, w.Tfull, prefix == nil && notprefix == nil, uid, gid)
	} else {
		wrap.Putwrap(bout, name, t, desc, 0, true, uid, gid)
	}
	wrap.Putwrapfile(bout, name, t, "proto", protof, uid, gid)
	wrap.Putwrapfile(bout, name, t, "md5sum", md5sort, uid, gid)
	if update {
		wrap.Putwrapfile(bout, name, t, "remove", rmfile, uid, gid)
	}
	if notesf != "" {
		wrap.Putwrapfile(bout, name, t, "notes", notesf, uid, gid)
	}
	md5s = nil
	pass = 1
	return digest[:]
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func wrapend(digest []byte) {
	digest0 := md5.Sum(md5s)
	if string(digest) != string(digest0[:]) {
		warn("files changed underfoot " + wrap.Md5conv(digest) + " " + wrap.Md5conv(digest0[:]))
	}
}

func mkarch(newf, oldf string, d *sys.Dir) {
	if pass == 0 && oldf != newf {
		onlist = append([][2]string{{oldf, newf}}, onlist...)
	}
	if prefix != nil && !wrap.Match(newf, prefix) {
		return
	}
	if notprefix != nil && !wrap.Notmatch(newf, notprefix) {
		return
	}
	digest := make([]byte, md5.Size)
	wrap.Md5file(oldf, digest)
	if ok, _ := wrap.Getfileinfo(w, newf, digest, nil, nil); ok {
		return
	}
	md5s = append(md5s, newf...)
	if pass == 0 {
		fmt.Fprintf(bout, "%s %s\n", newf, wrap.Md5conv(digest))
		return
	}
	if verb {
		fmt.Fprintf(os.Stderr, "%s\n", newf)
	}
	if d.Mode&sys.DMDIR != 0 {
		d.Length = 0
	}
	arch.Puthdr(bout, newf, d)
	if d.Mode&sys.DMDIR == 0 {
		if err := arch.Putfile(bout, oldf, int64(d.Length)); err != "" {
			warn(err)
		}
	}
}

// fsort writes the lines of file to out sorted, as sort(1) would.
func fsort(out *os.File, file string) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	lines := strings.SplitAfter(string(b), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for i, l := range lines {
		if !strings.HasSuffix(l, "\n") {
			lines[i] = l + "\n"
		}
	}
	sort.Strings(lines)
	_, err = out.WriteString(strings.Join(lines, ""))
	return err == nil
}

func opentemp(prefix string, t int) (string, *os.File) {
	pid, _ := sys.Pctl(0, nil)
	name := fmt.Sprintf("/tmp/%s.%d.%d", prefix, uint32(t), pid)
	fd, err := sys.Create(name, sys.ORDWR, 0o666)
	tmpfiles = append([]string{name}, tmpfiles...)
	if err != nil {
		return name, nil
	}
	return name, os.NewFile(uintptr(fd), name)
}

func exits() {
	wrap.End()
	for _, f := range tmpfiles {
		sys.Remove(f)
	}
	os.Exit(0)
}
