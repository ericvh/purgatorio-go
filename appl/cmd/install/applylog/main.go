// Command applylog applies a replica log read from standard input to a
// replica tree (Inferno install/applylog, applylog(8)). It rebuilds the
// replica's state from clientlog, then applies each new entry whose file
// is in the state the log expects, copying files from serverroot, and
// appends each entry it applies to clientlog. A file changed locally is a
// conflict: it is reported and left alone unless -s (take the server's)
// or -c (keep the client's) is given.
//
// Options: -n reports what would be done; -v prints each entry; -u and -g
// set owners and groups; -e makes any error fatal; -T timefile applies
// only entries newer than the stamp in timefile and records the last one
// applied; -d prints the states for debugging.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/logs"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	apply = iota
	applydb
	install
	asis
	skip
)

var (
	client, updates *logs.Db
	nerror          int
	nconflict       int
	debug, verbose  bool
	resolve         rune
	setuid, setgid  bool
	nflag           bool
	timefile        string
	clientroot      string
	srvroot         string
	logfd           *os.File
	noerr           bool
	stdout          = os.Stdout
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("applylog [-vuged] [-sc] [-T timefile] clientlog clientroot serverroot [path ... ] <serverlog")
	dump := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'T':
			timefile = a.EArg()
		case 'd':
			dump, debug = true, true
		case 'e':
			noerr = true
		case 'g':
			setgid = true
		case 'n':
			nflag, verbose = true, true
		case 's', 'c':
			resolve = o
		case 'u':
			setuid = true
		case 'v':
			verbose = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) < 3 {
		a.Usage()
	}
	client = logs.New("client log")
	updates = logs.New("update log")
	clientlog := args[0]
	clientroot = args[1]
	srvroot = args[2]
	if len(args) > 3 {
		errorf("restriction by path not yet done")
	}
	checkroot(clientroot, "client root")
	checkroot(srvroot, "server root")

	mode := sys.ORDWR
	if nflag {
		mode = sys.OREAD
	}
	fd, err := sys.Open(clientlog, mode)
	if err != nil {
		errorf(fmt.Sprintf("can't open %s: %s", clientlog, sys.Errstr()))
	}
	logfd = os.NewFile(uintptr(fd), clientlog)
	in := bufio.NewReader(logfd)
	for {
		e := readlog(in)
		if e == nil {
			break
		}
		replaylog(client, e)
	}
	logfd.Seek(0, io.SeekEnd)
	if dump {
		for i, e := range client.State {
			fmt.Fprintf(stdout, "%d\t%s\n", i, e.Text())
		}
	}
	if debug {
		fmt.Fprintf(stdout, "\tCLIENT STATE\n")
		client.Sort(logs.Byname)
		dumpdb(client, false)
	}

	var minseq int64
	if timefile != "" {
		minseq = readseq(timefile)
	}
	in = bufio.NewReader(os.Stdin)
	for {
		e := readlog(in)
		if e == nil {
			break
		}
		if e.Seq > minseq {
			update(updates, updates.Look(e.Path), e)
		}
	}
	updates.Sort(logs.Byseq)
	if debug {
		fmt.Fprintf(stdout, "\tSEQUENCED UPDATES\n")
		dumpdb(updates, true)
	}

	maxseq := minseq
	skipped := false
	for _, e := range updates.State {
		ce := client.Look(e.Path)
		if ce != nil && ce.Seq >= e.Seq {
			if debug {
				fmt.Fprintf(stdout, "replay %c %s\n", e.Action, quote.Quote(e.Path))
			}
			if !nflag && !skipped {
				maxseq = e.Seq
			}
			continue
		}
		if verbose {
			fmt.Fprintf(stdout, "%s\n", e.Sumtext())
		}
		switch chooseaction(e) {
		case install:
			if debug {
				fmt.Fprintf(stdout, "resolve %s to install\n", quote.Quote(e.Path))
			}
			e.Action = 'a'
			if !enact(e) {
				skipped = true
				continue
			}
		case apply:
			if !enact(e) {
				skipped = true
				continue
			}
		case applydb:
			if debug {
				fmt.Fprintf(stdout, "resolve %s to update db\n", quote.Quote(e.Path))
			}
		case asis:
			if debug {
				fmt.Fprintf(stdout, "resolve %s to client\n", quote.Quote(e.Path))
			}
		case skip:
			if debug {
				fmt.Fprintf(stdout, "conflict %s\n", quote.Quote(e.Path))
			}
			skipped = true
			continue
		}
		if ce == nil {
			ce = client.Entry(e.Seq, e.Path, e.D)
		}
		ce.Update(e)
		if !nflag {
			if !skipped {
				maxseq = e.Seq
			}
			if _, err := fmt.Fprintf(logfd, "%s\n", e.Logtext()); err != nil {
				errorf(fmt.Sprintf("error writing to %s: %s", quote.Quote(clientlog), sys.Errstr()))
			}
		}
	}
	fmt.Fprintf(os.Stderr, "maxseq: %d %d\n", uint64(maxseq)>>32, maxseq&0xFFFFFFFF)
	if !nflag && !skipped && timefile != "" {
		writeseq(timefile, maxseq)
	}
	if nconflict > 0 {
		sys.Exits(fmt.Sprintf("fail:%d conflicts", nconflict))
	}
	if nerror > 0 {
		sys.Exits(fmt.Sprintf("fail:%d errors", nerror))
	}
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

func readlog(in *bufio.Reader) *logs.Entry {
	e, err := logs.Read(in)
	if err != "" {
		errorf(err)
	}
	return e
}

func readseq(file string) int64 {
	b, err := os.ReadFile(file)
	if err != nil {
		errorf(fmt.Sprintf("can't open %s: %s", quote.Quote(file), sys.Errstr()))
	}
	if len(b) > 128 {
		b = b[:128]
	}
	if len(b) == 0 {
		errorf("can't read valid seq from " + quote.Quote(file))
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		errorf("illegal sequence number in " + quote.Quote(file))
	}
	return bigof(f[0])<<32 | bigof(f[1])
}

func writeseq(file string, n int64) {
	fd, err := sys.Create(file, sys.OWRITE, 0o666)
	if err != nil {
		errorf(fmt.Sprintf("can't create %s: %s", quote.Quote(file), sys.Errstr()))
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%11d %11d", uint64(n)>>32, n&0xFFFFFFFF); err != nil {
		errorf(fmt.Sprintf("error writing seq to %s: %s", quote.Quote(file), sys.Errstr()))
	}
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
	update(db, e, log)
}

func update(db *logs.Db, e, log *logs.Entry) {
	if e == nil {
		e = db.Entry(log.Seq, log.Path, log.D)
	}
	e.Update(log)
}

func chooseaction(e *logs.Entry) int {
	cf := logs.Mkpath(clientroot, e.Path)
	sf := logs.Mkpath(srvroot, e.Serverpath)
	cd, err := sys.Stat(cf)
	ishere := err == nil
	db := client.Look(e.Path)
	indb := db != nil && !db.Removed()
	unchanged := indb && ishere && (samestat(db.D, *cd) || samecontents(sf, cf)) || !indb && !ishere
	if unchanged && (e.Action != 'm' || samemeta(db.D, *cd)) {
		return apply
	}
	if !ishere && e.Action == 'd' {
		if indb {
			return applydb
		}
		return asis
	}
	switch resolve {
	case 'c':
		return asis
	case 's':
		if !ishere || e.Action == 'm' && !unchanged {
			return install
		}
		return apply
	}
	switch {
	case indb && ishere && e.Action == 'm' && unchanged && !samemeta(db.D, *cd):
		conflict(e.Path, "locally modified metadata", action(e.Action))
	case indb && ishere:
		conflict(e.Path, "locally modified", action(e.Action))
	case indb:
		conflict(e.Path, "locally removed", action(e.Action))
	case db != nil:
		conflict(e.Path, "locally retained or recreated", action(e.Action))
	default:
		conflict(e.Path, "locally created", action(e.Action))
	}
	return skip
}

func enact(e *logs.Entry) bool {
	if nflag {
		return false
	}
	srcfile := logs.Mkpath(srvroot, e.Serverpath)
	dstfile := logs.Mkpath(clientroot, e.Path)
	switch e.Action {
	case 'a', 'c':
		var err string
		if e.Action == 'a' {
			if debug {
				fmt.Fprintf(stdout, "create %s\n", quote.Quote(dstfile))
			}
			if e.D.Mode&sys.DMDIR != 0 {
				err = mkdir(dstfile, e)
			} else {
				err = copyin(srcfile, dstfile, true, e)
			}
		} else {
			err = copyin(srcfile, dstfile, false, e)
		}
		if err != "" {
			if noerr {
				errorf(err)
			}
			warn(err)
			return false
		}
	case 'd':
		if debug {
			fmt.Fprintf(stdout, "remove %s\n", quote.Quote(dstfile))
		}
		if !remove(dstfile) {
			warn(fmt.Sprintf("can't remove %s: %s", quote.Quote(dstfile), sys.Errstr()))
			return false
		}
	case 'm':
		if debug {
			fmt.Fprintf(stdout, "wstat %s\n", quote.Quote(dstfile))
		}
		d := sys.NullDir()
		d.Mode = e.D.Mode
		if sys.Wstat(dstfile, &d) != nil {
			warn(fmt.Sprintf("%s: can't change mode to %o", quote.Quote(dstfile), d.Mode))
		}
		if setgid {
			d = sys.NullDir()
			d.GID = e.D.GID
			if sys.Wstat(dstfile, &d) != nil {
				warn(fmt.Sprintf("%s: can't change gid to %s", quote.Quote(dstfile), quote.Quote(d.GID)))
			}
		}
		if setuid {
			d = sys.NullDir()
			d.UID = e.D.UID
			if sys.Wstat(dstfile, &d) != nil {
				warn(fmt.Sprintf("%s: can't change uid to %s", quote.Quote(dstfile), quote.Quote(d.UID)))
			}
		}
	default:
		errorf(fmt.Sprintf("unexpected log operation: %c %s", e.Action, quote.Quote(e.Path)))
	}
	return true
}

func errorf(s string) {
	fmt.Fprintf(os.Stderr, "applylog: %s\n", s)
	sys.Exits("fail:error")
}

func note(s string) { fmt.Fprintf(os.Stderr, "applylog: note: %s\n", s) }

func warn(s string) {
	fmt.Fprintf(os.Stderr, "applylog: warning: %s\n", s)
	nerror++
}

func conflict(name, why, wont string) {
	fmt.Fprintf(os.Stderr, "%s: %s; will not %s\n", quote.Quote(name), why, wont)
	nconflict++
}

func action(a byte) string {
	switch a {
	case 'a':
		return "create"
	case 'c':
		return "update"
	case 'd':
		return "delete"
	case 'm':
		return "update metadata"
	}
	return fmt.Sprintf("unknown action %c", a)
}

func samecontents(path1, path2 string) bool {
	b1, err1 := os.ReadFile(path1)
	b2, err2 := os.ReadFile(path2)
	return err1 == nil && err2 == nil && string(b1) == string(b2)
}

func samestat(a, b sys.Dir) bool {
	if a.Mode&sys.DMDIR != 0 {
		return b.Mode&sys.DMDIR != 0
	}
	return a.Length == b.Length && a.Mtime == b.Mtime && a.Qid.Type == b.Qid.Type
}

func samemeta(a, b sys.Dir) bool {
	return a.Mode == b.Mode && (!setuid || a.UID == b.UID) && (!setgid || a.GID == b.GID) && samestat(a, b)
}

func bigof(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		errorf("cruft in integer field in log entry: " + s)
	}
	return v
}

func mkdir(dstpath string, e *logs.Entry) string {
	fd, err := create(dstpath, sys.OREAD, e.D.Mode)
	if err != nil {
		return fmt.Sprintf("can't mkdir %s: %s", quote.Quote(dstpath), sys.Errstr())
	}
	defer sys.Close(fd)
	fchmod(fd, e.D.Mode)
	if setgid {
		fchgrp(fd, e.D.GID)
	}
	if setuid {
		fchown(fd, e.D.UID)
	}
	return ""
}

func fdpath(fd int) string {
	p, _ := sys.Fd2path(fd)
	return p
}

func fchmod(fd int, mode uint32) {
	d := sys.NullDir()
	d.Mode = mode
	if sys.Fwstat(fd, &d) != nil {
		warn(fmt.Sprintf("%s: can't set mode %o: %s", quote.Quote(fdpath(fd)), mode, sys.Errstr()))
	}
}

func fchgrp(fd int, gid string) {
	d := sys.NullDir()
	d.GID = gid
	if sys.Fwstat(fd, &d) != nil {
		warn(fmt.Sprintf("%s: can't set group id %s: %s", quote.Quote(fdpath(fd)), gid, sys.Errstr()))
	}
}

func fchown(fd int, uid string) {
	d := sys.NullDir()
	d.UID = uid
	if sys.Fwstat(fd, &d) != nil {
		warn(fmt.Sprintf("%s: can't set user id %s: %s", quote.Quote(fdpath(fd)), uid, sys.Errstr()))
	}
}

func copyin(srcpath, dstpath string, dowstat bool, e *logs.Entry) string {
	if debug {
		fmt.Fprintf(stdout, "copyin %s -> %s\n", quote.Quote(srcpath), quote.Quote(dstpath))
	}
	ffd, err := sys.Open(srcpath, sys.OREAD)
	if err != nil {
		return fmt.Sprintf("can't open %s: %s", quote.Quote(srcpath), sys.Errstr())
	}
	f := os.NewFile(uintptr(ffd), srcpath)
	defer f.Close()
	var tfd int
	if _, err := sys.Stat(dstpath); err != nil {
		tfd, err = create(dstpath, sys.OWRITE, e.D.Mode|0o222)
		if err != nil {
			return fmt.Sprintf("can't create %s: %s", quote.Quote(dstpath), sys.Errstr())
		}
		dowstat = true
	} else {
		tfd, err = sys.Open(dstpath, sys.OWRITE|sys.OTRUNC)
		if err != nil {
			e := sys.Errstr()
			if !strings.Contains(e, "permission") {
				return fmt.Sprintf("can't overwrite %s: %s", quote.Quote(dstpath), e)
			}
		}
	}
	t := os.NewFile(uintptr(tfd), dstpath)
	defer t.Close()
	nw, err := io.Copy(t, f)
	if err != nil {
		return fmt.Sprintf("error writing %s: %s", quote.Quote(dstpath), sys.Errstr())
	}
	if uint64(nw) != e.D.Length {
		warn(fmt.Sprintf("%s: log said %d bytes, copied %d bytes", quote.Quote(dstpath), e.D.Length, nw))
	}
	if dowstat {
		fchmod(tfd, e.D.Mode)
		if setgid {
			fchgrp(tfd, e.D.GID)
		}
		if setuid {
			fchown(tfd, e.D.UID)
		}
	}
	nd := sys.NullDir()
	nd.Mtime = e.D.Mtime
	if sys.Fwstat(tfd, &nd) != nil {
		warn(fmt.Sprintf("%s: can't set mtime: %s", quote.Quote(dstpath), sys.Errstr()))
	}
	return ""
}

func dumpdb(db *logs.Db, tag bool) {
	for _, e := range db.State {
		if tag {
			fmt.Fprintf(stdout, "%s\n", e.Text())
		} else {
			fmt.Fprintf(stdout, "%s\n", e.Dbtext())
		}
	}
}

func parent(name string) string {
	if i := strings.LastIndexByte(name, '/'); i > 0 {
		return name[:i]
	}
	return "/"
}

func writableparent(name string) (int64, string) {
	p := parent(name)
	d, err := sys.Stat(p)
	if err != nil {
		return -1, ""
	}
	nd := sys.NullDir()
	nd.Mode = d.Mode | 0o222
	sys.Wstat(p, &nd)
	return int64(d.Mode), p
}

func create(name string, rw int, mode uint32) (int, error) {
	fd, err := sys.Create(name, rw, mode)
	if err == nil {
		return fd, nil
	}
	e := sys.Errstr()
	if !strings.Contains(e, "permission") {
		sys.Werrstr(e)
		return -1, err
	}
	pm, p := writableparent(name)
	if pm >= 0 {
		fd, err = sys.Create(name, rw, mode)
		d := sys.NullDir()
		d.Mode = uint32(pm)
		sys.Wstat(p, &d)
	}
	sys.Werrstr(e)
	return fd, err
}

func remove(name string) bool {
	if sys.Remove(name) == nil {
		return true
	}
	e := sys.Errstr()
	if strings.Contains(e, "entry not found") || strings.Contains(e, "not exist") {
		return true
	}
	pm, p := writableparent(name)
	err := sys.Remove(name)
	if pm >= 0 {
		d := sys.NullDir()
		d.Mode = uint32(pm)
		sys.Wstat(p, &d)
	}
	sys.Werrstr(e)
	return err == nil
}
