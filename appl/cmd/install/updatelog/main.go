// Command updatelog adds to a replica log the changes in a file tree
// since the log was written (Inferno install/updatelog; see applylog(8)).
// It replays x.log to rebuild the state of the files it recorded, walks
// the files the proto file names under the replica root, and prints a log
// entry for each file added, changed or removed.
//
// Options: -p proto (/lib/proto/all); -r root (.); -t now gen sets the
// time and first generation of new entries; -c prints only changes to
// files already in the log; -u and -g set the owner and group recorded;
// -s records MD5 digests of the contents; -x leaves out a path. Path
// arguments restrict the walk to those paths.
package main

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/logs"
	"github.com/ericvh/purgatorio-go/sdk/proto"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const seen = 1 << 30

var (
	now         int
	gen         int
	changesonly bool
	uid, gid    string
	debug       bool
	state       *logs.Db
	rootdir     = "."
	scanonly    []string
	exclude     []string
	sums        bool
	bout        = bufio.NewWriter(os.Stdout)
)

func main() {
	protofile := "/lib/proto/all"
	a := arg.New(os.Args)
	a.SetUsage("updatelog [-p proto] [-r root] [-t now gen] [-c] [-x path] x.log [path ...]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'D':
			debug = true
		case 'p':
			protofile = a.EArg()
		case 'r':
			rootdir = a.EArg()
		case 'c':
			changesonly = true
		case 'u':
			uid = a.EArg()
		case 'g':
			gid = a.EArg()
		case 's':
			sums = true
		case 't':
			now, _ = strconv.Atoi(a.EArg())
			gen, _ = strconv.Atoi(a.EArg())
		case 'x':
			exclude = append([]string{trimpath(a.EArg())}, exclude...)
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	logfile := args[0]
	for _, p := range args[1:] {
		scanonly = append([]string{trimpath(p)}, scanonly...)
	}
	checkroot(rootdir, "replica root")
	state = logs.New("server state")

	f, err := os.Open(logfile)
	if err != nil {
		errorf("can't open " + logfile + ": " + sys.Errstr())
	}
	in := bufio.NewReader(f)
	for {
		e, err := logs.Read(in)
		if err != "" {
			errorf(err)
		}
		if e == nil {
			break
		}
		replaylog(state, e)
	}
	f.Close()

	if now == 0 {
		now = daytime.Now()
	}
	doproto(rootdir, protofile)
	if changesonly {
		bout.Flush()
		return
	}
	// Files in the state not seen in the walk were removed: log them,
	// children before parents. Inferno's updatelog gave a deletion the
	// stamp of the file's last change, so a client that had that change
	// took the deletion for one it had already applied; here it gets a
	// stamp of its own, and a file already deleted is not deleted again.
	state.Sort(logs.Byname)
	for i := len(state.State) - 1; i >= 0; i-- {
		e := state.State[i]
		if e.X&seen == 0 && !e.Removed() && considered(e.Path) {
			change('d', e, nextseq(), e.D, e.Path, e.Serverpath, e.Contents)
			if debug {
				fmt.Fprintf(os.Stderr, "remove %s\n", quote.Quote(e.Path))
			}
		}
	}
	bout.Flush()
}

func checkroot(dir, what string) {
	d, err := sys.Stat(dir)
	if err != nil {
		errorf(fmt.Sprintf("can't stat %s %s: %s", what, quote.Quote(dir), sys.Errstr()))
	}
	if d.Mode&sys.DMDIR == 0 {
		errorf(fmt.Sprintf("%s %s: not a directory", what, quote.Quote(dir)))
	}
}

func considered(s string) bool {
	if scanonly != nil && !islisted(s, scanonly) {
		return false
	}
	return exclude == nil || !islisted(s, exclude)
}

func replaylog(db *logs.Db, log *logs.Entry) {
	e := db.Look(log.Path)
	indb := e != nil && !e.Removed()
	switch log.Action {
	case 'a':
		if indb {
			note(quote.Quote(log.Path) + " duplicate create")
			return
		}
	case 'c':
		if !indb {
			note(quote.Quote(log.Path) + " contents but no entry")
			return
		}
	case 'd':
		if !indb {
			note(quote.Quote(log.Path) + " deleted but no entry")
			return
		}
		if e.D.Mtime > log.D.Mtime {
			note(quote.Quote(log.Path) + " deleted but it's newer")
			return
		}
	case 'm':
		if !indb {
			note(quote.Quote(log.Path) + " metadata but no entry")
			return
		}
	default:
		errorf(fmt.Sprintf("bad log entry: %d %d", log.Seq>>32, log.Seq&0xFFFFFFFF))
	}
	if e == nil {
		e = db.Entry(log.Seq, log.Path, log.D)
	}
	e.Update(log)
}

func doproto(tree, protofile string) {
	r := &proto.Reader{Entry: entry, Problem: func(_, _ int, old, msg string) {
		fmt.Fprintf(os.Stderr, "updatelog: warning[old=%s]: %s\n", old, msg)
	}}
	if err := r.ReadFile(protofile, tree); err != nil {
		errorf("can't read " + protofile + ": " + err.Error())
	}
}

func entry(newf, old string, d *sys.Dir) {
	if debug {
		fmt.Fprintf(os.Stderr, "old=%s new=%s length=%d\n", quote.Quote(old), quote.Quote(newf), int64(d.Length))
	}
	newf = strings.TrimLeft(newf, "/")
	if !considered(newf) {
		return
	}
	var digests []string
	if sums && d.Mode&sys.DMDIR == 0 {
		digests = []string{md5sum(old)}
	}
	if uid != "" {
		d.UID = uid
	}
	if gid != "" {
		d.GID = gid
	}
	old = relative(old, rootdir)
	db := state.Look(newf)
	if db == nil {
		if !changesonly {
			db = state.Entry(nextseq(), newf, *d)
			change('a', db, db.Seq, db.D, db.Path, old, digests)
		}
	} else {
		if !samestat(db.D, *d) {
			change('c', db, nextseq(), *d, newf, old, digests)
		}
		if !samemeta(db.D, *d) {
			change('m', db, nextseq(), *d, newf, old, nil)
		}
	}
	if db != nil {
		db.X |= seen
	}
}

func change(action byte, e *logs.Entry, seq int64, d sys.Dir, path, serverpath string, digests []string) {
	log := &logs.Entry{Seq: seq, Action: action, D: d, Path: path, Serverpath: serverpath, Contents: digests}
	e.Update(log)
	bout.WriteString(log.Logtext() + "\n")
}

func samestat(a, b sys.Dir) bool {
	if a.Mode&sys.DMDIR != 0 {
		return b.Mode&sys.DMDIR != 0
	}
	return a.Length == b.Length && a.Mtime == b.Mtime && a.Qid.Type == b.Qid.Type
}

func samemeta(a, b sys.Dir) bool {
	return a.Mode == b.Mode && (uid == "" || a.UID == b.UID) && (gid == "" || a.GID == b.GID) && samestat(a, b)
}

func nextseq() int64 {
	s := int64(now)<<32 | int64(gen)
	gen++
	return s
}

func errorf(s string) {
	bout.Flush()
	fmt.Fprintf(os.Stderr, "updatelog: %s\n", s)
	sys.Exits("fail:error")
}

func note(s string) { fmt.Fprintf(os.Stderr, "updatelog: note: %s\n", s) }

func isprefix(a, b string) bool {
	switch {
	case len(a) > len(b):
		return false
	case len(a) == len(b):
		return a == b
	}
	return strings.HasPrefix(b, a) && b[len(a)] == '/'
}

func trimpath(s string) string {
	for len(s) > 1 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return strings.TrimLeft(s, "/")
}

func relative(name, root string) string {
	if root == "" || name == "" {
		return name
	}
	if isprefix(root, name) {
		name = strings.TrimLeft(name[len(root):], "/")
	}
	return name
}

func islisted(s string, l []string) bool {
	for _, p := range l {
		if isprefix(p, s) {
			return true
		}
	}
	return false
}

func md5sum(file string) string {
	f, err := os.Open(file)
	if err != nil {
		errorf("can't open " + file + ": " + sys.Errstr())
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		errorf("error reading " + file + ": " + sys.Errstr())
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
