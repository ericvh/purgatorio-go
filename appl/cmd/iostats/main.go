// Command iostats runs a command in a copy of the name space served
// through itself and reports the Styx traffic when it ends (Inferno
// iostats(4)).
//
// As in iostats.b, the current name space is exported on one pipe and
// the command mounts another on / ; iostats passes messages between the
// two and counts them. iostats.b uses threads for the command, the
// export and the two directions. Here the export is the host's
// (EXPASYNC), the command runs in a child prog (a second iostats, hidden
// first argument "@cmd", that mounts the pipe and execs the shell), and
// one loop relays both directions, waiting with sys.Wait.
//
// Times are read from /dev/time, which counts microseconds, and are then
// treated as nanoseconds, as iostats.b does, so the times and rates are
// those iostats.b prints.
package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	cmdFlag = "@cmd"
	maxmsg  = 128*1024 + styx.IOHDRSZ
	ns2ms   = 1000000
)

type rpc struct {
	name                string
	count, time, lo, hi int64
	bin, bout           int64
}

type path struct {
	parent *path
	name   string
}

type fid struct {
	nr                           uint32
	seq                          int // creation order
	path                         *path
	qid                          styx.Qid
	nread, nwrite, bread, bwrite int64
}

type frec struct {
	op                           *path
	qid                          styx.Qid
	nread, nwrite, bread, bwrite int64
	opens                        int
}

type tag struct {
	m     *styx.Fcall
	fid   *fid
	stime int64
}

var (
	dbg      bool
	totread  int64
	totwrite int64
	nrpc     int
	nproto   int
	rpcs     = map[uint8]*rpc{}
	fids     = map[uint32]*fid{}
	tags     = map[uint16]*tag{}
	frecs    []*frec // newest first
	timefd   *os.File
	stderr   = os.Stderr
	fidseq   int
)

// rpcorder is the order of the report: tagof Tmsg in styx.m.
var rpcorder = []uint8{styx.Tversion, styx.Tauth, styx.Tattach, styx.Tflush, styx.Twalk, styx.Topen,
	styx.Tcreate, styx.Tread, styx.Twrite, styx.Tclunk, styx.Tstat, styx.Tremove, styx.Twstat}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "iostats: %s: %s\n", s, sys.Errstr())
	sys.Exits("fatal:error")
}

func main() {
	if len(os.Args) == 4 && os.Args[1] == cmdFlag {
		runcmd(os.Args[2], os.Args[3])
		return
	}
	dbfile := "iostats.out"
	a := arg.New(os.Args)
	a.SetUsage("iostats [-d] [-f debugfile] cmds [args ...]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd':
			dbg = true
		case 'f':
			dbfile = a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "/"
	}
	sys.Pctl(sys.FORKFD|sys.FORKNS|sys.NEWPGRP|sys.FORKENV, nil)
	if dbg {
		fd, err := sys.Create(dbfile, sys.OWRITE, 0o666)
		if err != nil {
			fatal("can't create " + quote(dbfile))
		}
		sys.Dup(fd, 2)
		sys.Close(fd)
	}
	if err := sys.Chdir("/"); err != nil {
		fatal("chdir /")
	}
	for _, t := range rpcorder {
		rpcs[t] = &rpc{name: tname[t], lo: 1 << 40}
	}
	mpipe, err := sys.Pipe()
	if err != nil {
		fatal("can't create pipe")
	}
	epipe, err := sys.Pipe()
	if err != nil {
		fatal("can't create pipe")
	}
	if err := sys.Export(epipe[1], "/", sys.EXPASYNC); err != nil {
		fatal("can't export /")
	}
	sys.Close(epipe[1])
	tf, err := os.Open("/dev/time")
	if err != nil {
		fatal("can't open /dev/time")
	}
	timefd = tf
	pid, _ := sys.Pctl(0, nil)
	waitfd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		fatal("can't open wait file")
	}
	if _, err := sys.Spawn("/bin/iostats.wasm", []string{"iostats", cmdFlag, wd, cmdline(args)},
		sys.NEWFD|sys.FORKNS, [][2]int{{0, 0}, {1, 1}, {2, 2}, {5, mpipe[0]}}); err != nil {
		fatal("can't run command")
	}
	sys.Close(mpipe[0])
	relay(mpipe[1], epipe[0], waitfd)
	results()
}

// runcmd is the command's prog: it mounts the pipe on fd 5 on / and
// runs the command line in the shell.
func runcmd(wd, cmd string) {
	if err := sys.Mount(5, -1, "/", sys.MREPL, ""); err != nil {
		fatal("can't mount /")
	}
	sys.Close(5)
	sys.Bind("#e", "/env", sys.MREPL|sys.MCREATE)
	sys.Bind("#d", "/fd", sys.MREPL)
	if err := sys.Chdir(wd); err != nil {
		fatal("can't chdir to " + wd)
	}
	if err := sys.Exec("/bin/sh.wasm", []string{"sh", "-c", cmd}); err != nil {
		fatal("can't run sh")
	}
}

func nsec() int64 {
	b := make([]byte, 100)
	n, _ := timefd.ReadAt(b, 0)
	if n <= 0 {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(string(b[:n])), 10, 64)
	return v
}

// relay passes messages between the mount (Tmsgs in) and the export
// (Rmsgs in) until either side ends, or the command has exited and the
// conversation has been quiet for a moment: the mount can outlive the
// command here, where iostats.b sees the end of the pipe.
func relay(mfd, efd, waitfd int) {
	mount := os.NewFile(uintptr(mfd), "mount")
	export := os.NewFile(uintptr(efd), "export")
	files := [2]*os.File{mount, export}
	ops := [][2]int{{sys.WaitRead, mfd}, {sys.WaitRead, efd}, {sys.WaitRead, waitfd}}
	var bufs [2][]byte
	rbuf := make([]byte, 64*1024)
	timeout := -1
	for {
		i, err := sys.Wait(ops, timeout)
		if err != nil || i < 0 {
			return
		}
		if i == 2 {
			sys.Close(waitfd)
			ops = ops[:2]
			timeout = 200
			continue
		}
		n, _ := files[i].Read(rbuf)
		if n <= 0 {
			return
		}
		bufs[i] = append(bufs[i], rbuf[:n]...)
		for len(bufs[i]) >= 4 {
			sz := styx.Size(bufs[i])
			if sz <= 4 || sz > maxmsg {
				return
			}
			if uint32(len(bufs[i])) < sz {
				break
			}
			a := bufs[i][:sz]
			m, err := styx.Unmarshal(a, sz)
			if err != nil || (m.Type%2 == 0) != (i == 0) {
				return
			}
			if i == 0 {
				tmsg(len(a), m)
				export.Write(a)
			} else {
				rmsg(len(a), m)
				mount.Write(a)
			}
			bufs[i] = bufs[i][sz:]
		}
	}
}

func tmsg(n int, t *styx.Fcall) {
	if dbg {
		fmt.Fprintf(stderr, "->%s\n", tmsgtext(t))
	}
	tg := &tag{m: t, stime: nsec()}
	tags[t.Tag] = tg
	nrpc++
	nproto += n
	r := rpcs[t.Type]
	if r == nil {
		fmt.Fprintf(stderr, "iostats: unexpected T-msg %d\n", t.Type)
		return
	}
	r.count++
	r.bin += int64(n)
	switch t.Type {
	case styx.Tauth:
		tg.fid = newfid(t.Afid)
	case styx.Tattach:
		tg.fid = newfid(t.Fid)
	case styx.Twalk, styx.Topen, styx.Tcreate, styx.Tread, styx.Twrite,
		styx.Tclunk, styx.Tstat, styx.Tremove, styx.Twstat:
		tg.fid = fids[t.Fid]
		t.Data = nil
	}
}

// replymap gives the T-message each R-message answers.
var replymap = map[uint8]uint8{
	styx.Rversion: styx.Tversion, styx.Rauth: styx.Tauth, styx.Rattach: styx.Tattach,
	styx.Rflush: styx.Tflush, styx.Rclunk: styx.Tclunk, styx.Rremove: styx.Tremove,
	styx.Rwstat: styx.Twstat, styx.Rwalk: styx.Twalk, styx.Rcreate: styx.Tcreate,
	styx.Ropen: styx.Topen, styx.Rread: styx.Tread, styx.Rwrite: styx.Twrite, styx.Rstat: styx.Tstat,
}

func rmsg(n int, r *styx.Fcall) {
	if dbg {
		fmt.Fprintf(stderr, "<-%s\n", rmsgtext(r))
	}
	nproto += n
	tg := tags[r.Tag]
	if tg == nil {
		return
	}
	delete(tags, r.Tag)
	if tt, ok := replymap[r.Type]; ok {
		if rp := rpcs[tt]; rp != nil {
			update(rp, nsec()-tg.stime)
			rp.bout += int64(n)
		}
	}
	f := tg.fid
	switch r.Type {
	case styx.Rerror:
		if tg.m.Type == styx.Tauth && f != nil {
			if f.nread != 0 || f.nwrite != 0 {
				fidreport(f)
			}
			delete(fids, f.nr)
		}
	case styx.Rversion:
		tags = map[uint16]*tag{}
		fids = map[uint32]*fid{}
	case styx.Rauth:
		if f != nil {
			f.qid = r.Qid
			f.path = &path{name: "#auth"}
		}
	case styx.Rattach:
		if f != nil {
			f.qid = r.Qid
			f.path = &path{name: "/"}
		}
	case styx.Rwalk:
		m := tg.m
		if m.Type != styx.Twalk || len(r.Wqid) != len(m.Wname) || f == nil {
			break
		}
		if m.Newfid != m.Fid {
			nf := newfid(m.Newfid)
			nf.path = f.path
			f = nf
		}
		for i, name := range m.Wname {
			f.qid = r.Wqid[i]
			if name == ".." {
				if f.path != nil && f.path.parent != nil {
					f.path = f.path.parent
				}
			} else {
				f.path = &path{parent: f.path, name: name}
			}
		}
	case styx.Ropen, styx.Rcreate:
		if f != nil {
			f.qid = r.Qid
		}
	case styx.Rread:
		nr := int64(len(r.Data))
		if f != nil {
			f.nread++
			f.bread += nr
		}
		totread += nr
	case styx.Rwrite:
		if f != nil {
			f.nwrite++
			f.bwrite += int64(r.Count)
		}
		totwrite += int64(r.Count)
	case styx.Rflush:
		if tg.m.Type == styx.Tflush {
			delete(tags, tg.m.Oldtag)
		}
	case styx.Rclunk, styx.Rremove:
		if f != nil {
			if f.nread != 0 || f.nwrite != 0 {
				fidreport(f)
			}
			delete(fids, f.nr)
		}
	}
}

func newfid(nr uint32) *fid {
	if f := fids[nr]; f != nil {
		return f // a faulty client
	}
	fidseq++
	f := &fid{nr: nr, seq: fidseq, qid: styx.Qid{Type: 0xff}}
	fids[nr] = f
	return f
}

func fidreport(f *fid) {
	for _, fr := range frecs {
		if f.qid.Path == fr.qid.Path && f.qid.Type == fr.qid.Type {
			fr.nread += f.nread
			fr.nwrite += f.nwrite
			fr.bread += f.bread
			fr.bwrite += f.bwrite
			fr.opens++
			return
		}
	}
	frecs = append([]*frec{{op: f.path, qid: f.qid, nread: f.nread, nwrite: f.nwrite,
		bread: f.bread, bwrite: f.bwrite, opens: 1}}, frecs...)
}

func update(r *rpc, t int64) {
	if t < 0 {
		t = 0
	}
	r.time += t
	if t < r.lo {
		r.lo = t
	}
	if t > r.hi {
		r.hi = t
	}
}

func makepath(p *path) string {
	var nl []string
	for ; p != nil; p = p.parent {
		if p.name != "/" {
			nl = append([]string{p.name}, nl...)
		}
	}
	return "/" + strings.Join(nl, "/")
}

func results() {
	r := rpcs[styx.Tread]
	brpsec := float64(totread) / (float64(r.time)/1.0e9 + .000001)
	r = rpcs[styx.Twrite]
	bwpsec := float64(totwrite) / (float64(r.time)/1.0e9 + .000001)
	var ttime int64
	for _, t := range rpcorder {
		if r := rpcs[t]; r.count != 0 {
			ttime += r.time
		}
	}
	bppsec := float64(nproto) / (float64(ttime)/1.0e9 + .000001)

	fmt.Fprintf(stderr, "\nread      %d bytes, %s Kb/sec\n", totread, g(brpsec/1024.0))
	fmt.Fprintf(stderr, "write     %d bytes, %s Kb/sec\n", totwrite, g(bwpsec/1024.0))
	fmt.Fprintf(stderr, "protocol  %d bytes, %s Kb/sec\n", nproto, g(bppsec/1024.0))
	fmt.Fprintf(stderr, "rpc       %d count\n\n", nrpc)
	fmt.Fprintf(stderr, "%-10s %5s %5s %5s %5s %5s           T        R\n",
		"Message", "Count", "Low", "High", "Time", "  Avg")
	for _, t := range rpcorder {
		r := rpcs[t]
		if r.count == 0 {
			continue
		}
		fmt.Fprintf(stderr, "%-10s %5d %5d %5d %5d %5d ms %8d %8d bytes\n",
			r.name, r.count, r.lo/ns2ms, r.hi/ns2ms, r.time/ns2ms, r.time/ns2ms/r.count, r.bin, r.bout)
	}
	// Unclunked fids, in the order of iostats.b's hash table: by bucket,
	// newest first within one.
	var left []*fid
	for _, f := range fids {
		left = append(left, f)
	}
	sort.Slice(left, func(i, j int) bool {
		if bi, bj := left[i].nr%16, left[j].nr%16; bi != bj {
			return bi < bj
		}
		return left[i].seq > left[j].seq
	})
	for _, f := range left {
		if f.nread != 0 || f.nwrite != 0 {
			fidreport(f)
		}
	}
	if len(frecs) == 0 {
		return
	}
	fmt.Fprintf(stderr, "\nOpens    Reads  (bytes)   Writes  (bytes) File\n")
	for _, fr := range frecs {
		s := makepath(fr.op)
		switch s {
		case "/fd/0":
			s = "(stdin)"
		case "/fd/1":
			s = "(stdout)"
		case "/fd/2":
			s = "(stderr)"
		case "":
			s = "/."
		}
		fmt.Fprintf(stderr, "%5d %8d %8d %8d %8d %s\n", fr.opens, fr.nread, fr.bread, fr.nwrite, fr.bwrite, s)
	}
}

// g formats as C's %g does.
func g(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }

var tname = map[uint8]string{
	styx.Tversion: "version", styx.Tauth: "auth", styx.Tflush: "flush", styx.Tattach: "attach",
	styx.Twalk: "walk", styx.Topen: "open", styx.Tcreate: "create", styx.Tclunk: "clunk",
	styx.Tread: "read", styx.Twrite: "write", styx.Tremove: "remove", styx.Tstat: "stat",
	styx.Twstat: "wstat",
}

// cmdline makes a shell command of argv: a word in braces is a block
// and goes as it is; other words are quoted where the shell needs it.
func cmdline(argv []string) string {
	w := make([]string, len(argv))
	for i, a := range argv {
		if len(a) >= 2 && a[0] == '{' && a[len(a)-1] == '}' {
			w[i] = a
		} else {
			w[i] = quote(a)
		}
	}
	return strings.Join(w, " ")
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'`{}()<>|&;$^=#*?[]\"\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
