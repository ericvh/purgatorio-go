package main

// The session prog: mcpd --session filems mem progs [nsfile].
//
// The front end starts one of these per session with a forked namespace
// and environment and a new process group. It builds the sandbox, gives
// up the right to attach devices, and then serves requests. Everything a
// tool touches is named in the sandbox, because this prog has no other
// namespace.
//
// The sandbox, in order:
//
//  1. A fresh memfs becomes the root (pctl(NEWNS) with it as the current
//     directory), holding only empty mount points. The host tree (#U on
//     emu) is not in it.
//  2. /bin and /lib are mounted from read-only exports (export -r) of the
//     directories the server itself sees. The export refuses every
//     request that changes a file, so nothing the session does can modify
//     them, and the only way to reach those trees is through that mount.
//  3. /dev holds null, random, time, user, sysname, memory and
//     drivers from #c, and a cons that is #c/null: the real console is the
//     MCP connection. /env is #e, /prog is #pdomain (only the progs of
//     this session) and /fd is #d.
//  4. /tmp is a second memfs, private to the session.
//  5. The namespace file, if one was given, is applied by nsbuild. This is
//     where a policy grants more: bind -c '#U/work' /n/work,
//     bind '#I' /net. Missing mount points are made first.
//  6. The limits (-M, -P) are set on the session's domain, which holds
//     this prog, its file servers and everything it starts.
//  7. nodev ¤ on this prog's ctl, unless the namespace file binds #¤: the
//     session cannot reach the factotum's keys.
//  8. pctl(NODEVS): from here on neither this prog nor anything it starts
//     can name a device with #, except the ones Inferno always allows
//     (#e, #s, #|, #M) and #¤ when the policy left it.
//
// The protocol with the front end, on fds 0 and 1, is one JSON object per
// line. A request is {"tag", "name", "arguments"}; the session answers
// {"tag", "result"} or {"tag", "error"}. It sends {"start": tag} when it
// begins a request, and {"ack": n} for every n bytes it reads, which is
// how the front end knows the request pipe has room and never blocks
// writing to it.
//
// File tools run in a worker prog per call (mcpd --file), which shares
// this namespace and environment but none of this prog's descriptors: a
// tool that names /fd/1 reaches the worker's output, not the front end,
// and only result or error is taken from what the worker writes. A worker
// that takes longer than the file tool limit is killed and its call
// fails, and the session goes on.
//
// Requests are begun in order, one at a time: a run, a mount or a file
// tool holds up the ones behind it until it ends. A job_read that waits
// does not; it is answered when output comes, the job exits or the wait
// is over. Meanwhile the session keeps reading every job's output.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/mcp"
	"github.com/ericvh/purgatorio-go/sdk/newns"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	sh = "/bin/sh.wasm"
	// fileReply is the most a file worker's reply is read: a 1 MiB read
	// as base64, and then some.
	fileReply = 4 << 20
	// homeDir is where relative names start and where commands start by
	// default.
	homeDir = "/tmp"
	// readChunk is the most a request line from the front end is read at
	// a time; the front end's window is no larger.
	readChunk = 16 * 1024
)

// dirs are the mount points in the new root. net, chan, n and mnt are
// empty unless the namespace file fills them.
var dirs = []string{"bin", "lib", "tmp", "dev", "env", "prog", "fd", "net", "chan", "n", "mnt"}

// devs are the #c files a session may use. The console, its control
// file, kprint and sysctl are left out.
var devs = []string{"null", "random", "time", "user", "sysname", "memory", "drivers"}

// Limits are what a session may use, set on its domain (mcpd -M, -P).
type Limits struct {
	mem   uint64 // bytes of wasm memory all its progs may hold between them
	progs uint64 // how many progs it may have at once
}

// note says which limit a program's diagnostics show it ran into, if
// any: the Go runtime reports a refused memory.grow as out of memory
// (a Rust program as a failed allocation), and a spawn past the prog
// limit fails with no free procs. Below 4 GiB the domain's limit is the
// only reason an allocation fails.
func (l Limits) note(stderr string) string {
	switch {
	case strings.Contains(stderr, "fatal error: out of memory") ||
		strings.Contains(stderr, "memory allocation of ") && strings.Contains(stderr, " bytes failed"):
		return l.memNote()
	case strings.Contains(stderr, "no free procs"):
		return l.procsNote()
	}
	return ""
}

func (l Limits) memNote() string {
	return "out of memory: the session's programs may hold " + sizeText(l.mem) +
		" of memory between them (mcpd -M), and a program asked for more; it was stopped"
}

func (l Limits) procsNote() string {
	return fmt.Sprintf("no free procs: the session may run at most %d programs at once (mcpd -P), "+
		"counting its own servers; end some jobs or background programs", l.progs)
}

// explain is a spawn error, with the limit named when it was one.
func (l Limits) explain(err error) error {
	if strings.HasSuffix(err.Error(), "no free procs") {
		return sys.Error(err.Error() + " (" + l.procsNote() + ")")
	}
	return err
}

// sizeText is n bytes as 256 MiB, 64 KiB or 100 bytes.
func sizeText(n uint64) string {
	for _, u := range []struct {
		shift uint
		name  string
	}{{30, "GiB"}, {20, "MiB"}, {10, "KiB"}} {
		if n >= 1<<u.shift && n%(1<<u.shift) == 0 {
			return fmt.Sprintf("%d %s", n>>u.shift, u.name)
		}
	}
	return fmt.Sprintf("%d bytes", n)
}

func null(mode int) (int, error) {
	fd, err := sys.Open("/dev/null", mode)
	if err != nil {
		return -1, fail("/dev/null", err)
	}
	return fd, nil
}

type request struct {
	tag  mcp.Value
	name string
	args mcp.Value
}

// parked is a job_read waiting for output.
type parked struct {
	tag      mcp.Value
	job      int
	from     [2]int64 // -1: the cursor
	max      int
	deadline time.Time
}

// feeder is a feeder writing to a job's input.
type feeder struct{ pid, job int }

// Session is the session prog's state.
type Session struct {
	wait     int // open on this prog's /prog/n/wait
	out      int
	queue    []request
	busy     bool // a run, mount or file tool is in progress
	procs    []*Proc
	nextJob  int
	feeders  []feeder
	parked   []parked
	fileMS   int64
	limits   Limits
	waitbuf  []byte
	building bool
}

// reap reads exit messages until the one for pid and returns its status.
// Only used while building, before there is anything else to wait for.
func (s *Session) reap(pid int) (string, error) {
	buf := make([]byte, 4096)
	for {
		n, err := rd(s.wait, buf)
		if err != nil || n == 0 {
			if err == nil {
				err = sys.Error("wait closed")
			}
			return "", fail("wait", err)
		}
		if p, status, ok := parseWait(string(buf[:n])); ok && p == pid {
			return status, nil
		}
	}
}

// helper runs a helper command to completion with stdin as its input,
// which it takes. Its diagnostics are the error.
func (s *Session) helper(cmd string, argv []string, stdin int) error {
	ep, err := sys.Pipe()
	if err != nil {
		closefd(stdin)
		return fail("pipe", err)
	}
	pid, err := sys.Spawn(cmd, argv, sys.NEWFD, [][2]int{{0, stdin}, {1, ep[1]}, {2, ep[1]}})
	closefd(stdin)
	closefd(ep[1])
	if err != nil {
		closefd(ep[0])
		return fail(strings.Join(argv, " "), err)
	}
	// A file server the helper starts keeps the pipe; read what is there
	// when the helper itself is gone.
	status, err := s.reap(pid)
	if err != nil {
		closefd(ep[0])
		return err
	}
	var text []byte
	buf := make([]byte, 4096)
	for {
		if r, _ := waitReadable([]int{ep[0]}, 0); r != 0 {
			break
		}
		n, err := rd(ep[0], buf)
		if err != nil || n == 0 {
			break
		}
		text = append(text, buf[:n]...)
	}
	closefd(ep[0])
	if status == "" {
		return nil
	}
	if t := strings.TrimSpace(mcp.Lossy(text)); t != "" {
		return sys.Error(t)
	}
	return sys.Error(strings.Join(argv, " ") + ": " + status)
}

// readonlyExport starts export -r dir on a pipe and returns the client
// end. The export keeps the namespace it was started in, so it must start
// before the root changes.
func readonlyExport(dir string) (int, error) {
	p, err := sys.Pipe()
	if err != nil {
		return -1, fail("pipe", err)
	}
	_, err = sys.Spawn("/bin/export.wasm", []string{"export", "-A", "-r", dir}, sys.NEWFD|sys.FORKNS,
		[][2]int{{0, p[0]}, {2, 2}})
	closefd(p[0])
	if err != nil {
		closefd(p[1])
		return -1, fail("export", err)
	}
	return p[1], nil
}

// buildSession builds the sandbox described at the top of this file.
// policy is the text of the namespace file, or nil.
func buildSession(policy *string, fileMS int64, limits Limits) (*Session, error) {
	me := mypid()
	wait, err := sys.Open(fmt.Sprintf("#p/%d/wait", me), sys.OREAD)
	if err != nil {
		return nil, fail("wait file", err)
	}
	s := &Session{wait: wait, out: -1, nextJob: 1, fileMS: fileMS, limits: limits}
	// Any directory will do to hold the new root while it is built; the
	// namespace it is in is about to be thrown away.
	stage := ""
	for _, d := range []string{"/tmp", "/mnt", "/n", "/chan"} {
		if st, err := sys.Stat(d); err == nil && st.Mode&sys.DMDIR != 0 {
			stage = d
			break
		}
	}
	if stage == "" {
		return nil, sys.Error("no directory to build the session root on")
	}
	nul, err := null(sys.OREAD)
	if err != nil {
		return nil, err
	}
	if err := s.helper("/bin/memfs.wasm", []string{"memfs", stage}, nul); err != nil {
		return nil, err
	}
	for _, d := range dirs {
		path := stage + "/" + d
		fd, err := sys.Create(path, sys.OREAD, sys.DMDIR|0o777)
		if err != nil {
			return nil, perr(path, err)
		}
		closefd(fd)
	}
	for _, f := range append(append([]string(nil), devs...), "cons") {
		path := stage + "/dev/" + f
		fd, err := sys.Create(path, sys.OWRITE, 0o666)
		if err != nil {
			return nil, perr(path, err)
		}
		closefd(fd)
	}
	bin, err := readonlyExport("/bin")
	if err != nil {
		return nil, err
	}
	lib, err := readonlyExport("/lib")
	if err != nil {
		return nil, err
	}
	if err := sys.Chdir(stage); err != nil {
		return nil, perr(stage, err)
	}
	if _, err := sys.Pctl(sys.NEWNS, nil); err != nil {
		return nil, fail("pctl NEWNS", err)
	}
	for _, m := range []struct {
		fd int
		at string
	}{{bin, "/bin"}, {lib, "/lib"}} {
		_, err := sys.Mount(m.fd, -1, m.at, sys.MREPL, "")
		closefd(m.fd)
		if err != nil {
			return nil, fail("mount "+m.at, err)
		}
	}
	for _, f := range devs {
		if _, err := sys.Bind("#c/"+f, "/dev/"+f, sys.MREPL); err != nil {
			return nil, fail("bind /dev/"+f, err)
		}
	}
	for _, b := range []struct {
		new, old string
		flags    int
	}{
		{"#c/null", "/dev/cons", sys.MREPL},
		{"#e", "/env", sys.MREPL | sys.MCREATE},
		{"#pdomain", "/prog", sys.MREPL},
		{"#d", "/fd", sys.MREPL},
	} {
		if _, err := sys.Bind(b.new, b.old, b.flags); err != nil {
			return nil, fail("bind "+b.old, err)
		}
	}
	// Binding #pdomain founded the domain; it holds this prog, its file
	// servers and all it starts.
	ctl := fmt.Sprintf("/prog/%d/ctl", me)
	for _, l := range []struct {
		what string
		n    uint64
	}{{"memlimit", limits.mem}, {"proglimit", limits.progs}} {
		if err := writeFile(ctl, fmt.Sprintf("domain %s %d", l.what, l.n)); err != nil {
			return nil, fail("domain "+l.what, err)
		}
	}
	if nul, err = null(sys.OREAD); err != nil {
		return nil, err
	}
	if err := s.helper("/bin/memfs.wasm", []string{"memfs", homeDir}, nul); err != nil {
		return nil, limits.explain(err)
	}
	if err := sys.Chdir("/"); err != nil {
		return nil, perr("/", err)
	}
	// What sh's profile would set. The environment is a copy, so the
	// server's own variables are not changed.
	for _, v := range []struct{ name, file string }{{"user", "/dev/user"}, {"sysname", "/dev/sysname"}} {
		if t, err := readFile(v.file); err == nil {
			setenv(v.name, strings.TrimSpace(t))
		}
	}
	setenv("home", homeDir)
	if policy != nil {
		makeMountPoints(*policy)
		// nsbuild reads the file as its standard input; the file itself
		// is in the server's namespace, not this one.
		in, err := feed(*policy)
		if err != nil {
			return nil, sys.Error("namespace file: " + err.Error())
		}
		if err := s.helper("/bin/nsbuild.wasm", []string{"nsbuild", "/fd/0"}, in); err != nil {
			return nil, sys.Error("namespace file: " + err.Error())
		}
	}
	if policy == nil || !grantsFactotum(*policy) {
		if err := writeFile(ctl, "nodev ¤"); err != nil {
			return nil, fail("nodev", err)
		}
	}
	if _, err := sys.Pctl(sys.NODEVS, nil); err != nil {
		return nil, fail("pctl NODEVS", err)
	}
	_ = sys.Chdir(homeDir)
	return s, nil
}

// setenv sets a variable in the session's #e.
func setenv(name, value string) {
	fd, err := sys.Create("/env/"+name, sys.OWRITE, 0o666)
	if err != nil {
		return
	}
	_ = writeAll(fd, []byte(value))
	closefd(fd)
}

func (s *Session) send(v mcp.Value) {
	if s.out >= 0 {
		// A front end that has gone shows as the end of the input.
		_ = writeAll(s.out, []byte(mcp.Encode(v)+"\n"))
	}
}

func (s *Session) reply(tag mcp.Value, v mcp.Value, err error) {
	if err != nil {
		s.send(mcp.O("tag", tag, "error", err.Error()))
		return
	}
	s.send(mcp.O("tag", tag, "result", v))
}

// serve serves requests from the front end until it closes the pipe.
func (s *Session) serve() {
	s.out = 1
	s.send(mcp.O("ready", true))
	var acc []byte
	buf := make([]byte, readChunk)
	for {
		fds := []int{0, s.wait}
		type slot struct{ p, k int }
		var slots []slot
		for i, p := range s.procs {
			for k, st := range p.streams {
				if st.fd >= 0 {
					fds = append(fds, st.fd)
					slots = append(slots, slot{i, k})
				}
			}
		}
		var due time.Time
		at := func(t time.Time) {
			if !t.IsZero() && (due.IsZero() || t.Before(due)) {
				due = t
			}
		}
		for _, p := range s.procs {
			at(p.due())
		}
		for _, w := range s.parked {
			at(w.deadline)
		}
		r, err := waitReadable(fds, msUntil(due))
		switch {
		case err != nil:
			// Only a kill of this prog interrupts the wait. Leave at
			// once, so the front end sees the session end.
			sys.Exits("killed: " + err.Error())
		case r == 0:
			n, err := rd(0, buf)
			if err != nil || n == 0 {
				return
			}
			s.send(mcp.O("ack", n))
			acc = append(acc, buf[:n]...)
			for {
				i := indexByte(acc, '\n')
				if i < 0 {
					break
				}
				s.enqueue(mcp.Lossy(acc[:i+1]))
				acc = acc[i+1:]
			}
		case r == 1:
			s.reaped()
		case r >= 2:
			sl := slots[r-2]
			s.procs[sl.p].streams[sl.k].fill()
		}
		s.tick()
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func (s *Session) enqueue(line string) {
	req, err := mcp.Parse(strings.TrimSpace(line))
	if err != nil {
		return
	}
	tag, _ := mcp.Get(req, "tag")
	name, _ := mcp.Str(req, "name")
	args, ok := mcp.Get(req, "arguments")
	if !ok {
		args = mcp.Obj{}
	}
	s.queue = append(s.queue, request{tag, name, args})
}

// reaped reads one exit message.
func (s *Session) reaped() {
	if s.waitbuf == nil {
		s.waitbuf = make([]byte, 4096)
	}
	n, err := rd(s.wait, s.waitbuf)
	if err != nil || n == 0 {
		if err == nil {
			err = sys.Error("wait closed")
		}
		sys.Exits("killed: " + err.Error())
	}
	pid, status, ok := parseWait(string(s.waitbuf[:n]))
	if !ok {
		return
	}
	for _, p := range s.procs {
		if p.pid != pid {
			continue
		}
		if !p.exited {
			// A job that could not start shows as one that failed;
			// finishCall makes it the error of a run.
			if why, ok := strings.CutPrefix(status, execFailed); ok && p.kind == kindJob {
				status = "fail:" + why
			}
			p.status, p.exited, p.exitedAt = status, true, time.Now()
		}
		return
	}
	for i, f := range s.feeders {
		if f.pid == pid {
			s.feeders = append(s.feeders[:i], s.feeders[i+1:]...)
			if j := s.jobByID(f.job); j != nil {
				j.job.feeding = false
			}
			s.pump(f.job)
			return
		}
	}
}

// tick does what is due: kills commands past their timeout, answers the
// ones that are over and the job reads that can be, and begins queued
// requests.
func (s *Session) tick() {
	now := time.Now()
	for _, p := range s.procs {
		if p.overdue(now) {
			if p.kind == kindJob {
				p.job.timedOut = true
			}
			p.kill(now)
		}
	}
	for i := 0; i < len(s.procs); {
		p := s.procs[i]
		if !p.settled(now) {
			i++
			continue
		}
		switch p.kind {
		case kindJob:
			if j := p.job; !j.done {
				j.done = true
				for _, st := range p.streams {
					st.close()
				}
				closefd(p.ctl)
				p.ctl = -1
				if !p.exited {
					p.status, p.exited = "killed", true
				}
				if j.timedOut {
					j.note = "timed out: killed at its timeout_ms"
				} else {
					j.note = s.limits.note(p.streams[1].text())
				}
			}
			i++
		case kindCall:
			s.procs = append(s.procs[:i], s.procs[i+1:]...)
			s.finishCall(p)
		case kindFile:
			s.procs = append(s.procs[:i], s.procs[i+1:]...)
			s.finishFile(p)
		}
	}
	for k := 0; k < len(s.parked); {
		w := s.parked[k]
		ready := true
		if p := s.jobByID(w.job); p != nil {
			ready = p.job.done || p.streams[0].end() > fromOf(p, 0, w.from[0]) ||
				p.streams[1].end() > fromOf(p, 1, w.from[1])
		}
		if ready || !now.Before(w.deadline) {
			s.parked = append(s.parked[:k], s.parked[k+1:]...)
			v, err := s.readJob(w.job, w.from, w.max)
			s.reply(w.tag, v, err)
		} else {
			k++
		}
	}
	// Keep the newest exited jobs.
	var done []int
	for i, p := range s.procs {
		if p.kind == kindJob && p.job.done {
			done = append(done, i)
		}
	}
	if len(done) > maxJobs {
		drop := map[int]bool{}
		for _, i := range done[:len(done)-maxJobs] {
			drop[i] = true
			s.procs[i].closeAll()
		}
		var keep []*Proc
		for i, p := range s.procs {
			if !drop[i] {
				keep = append(keep, p)
			}
		}
		s.procs = keep
	}
	for !s.busy && len(s.queue) > 0 {
		r := s.queue[0]
		s.queue = s.queue[1:]
		s.begin(r)
	}
}

func (s *Session) finishCall(p *Proc) {
	p.waitGone()
	defer p.closeAll()
	s.busy = false
	killed := !p.killedAt.IsZero()
	status := p.status
	if !p.exited {
		status = "killed"
	}
	if why, ok := strings.CutPrefix(status, execFailed); ok {
		s.reply(p.tag, nil, s.limits.explain(sys.Error(why)))
		return
	}
	out, errs := p.streams[0], p.streams[1]
	if p.mount {
		switch {
		case status == "" && !killed:
			s.reply(p.tag, mcp.O("ok", true), nil)
		case killed:
			s.reply(p.tag, nil, sys.Error("mount: timed out"))
		default:
			if t := strings.TrimSpace(errs.text()); t != "" {
				s.reply(p.tag, nil, sys.Error(t))
			} else {
				s.reply(p.tag, nil, sys.Error("mount: "+status))
			}
		}
		return
	}
	stderr := errs.text()
	v := mcp.O(
		"stdout", out.text(),
		"stderr", stderr,
		"ok", status == "" && !killed,
		"status", status,
		"timed_out", killed,
		"stdout_truncated", out.truncated,
		"stderr_truncated", errs.truncated)
	if n := s.limits.note(stderr); n != "" {
		v = append(v, mcp.Member{K: "note", V: n})
	}
	s.reply(p.tag, v, nil)
}

// finishFile answers a file tool with what its worker wrote. Only result
// or error is taken from it: the worker runs the tool, and the tool can
// write to the worker's standard output, so the reply carries nothing
// the session or the front end acts on.
func (s *Session) finishFile(p *Proc) {
	p.waitGone()
	defer p.closeAll()
	s.busy = false
	if !p.killedAt.IsZero() {
		s.reply(p.tag, nil, sys.Error(fmt.Sprintf("timed out after %d ms (a file that blocks when read or written?); "+
			"the tool was abandoned and the session goes on", s.fileMS)))
		return
	}
	if body, ok := workerReply(p.streams[0].text()); ok {
		if v, err := mcp.Parse(body); err == nil {
			if r, ok := mcp.Get(v, "result"); ok {
				s.reply(p.tag, r, nil)
				return
			}
			if e, ok := mcp.Str(v, "error"); ok {
				s.reply(p.tag, nil, sys.Error(e))
				return
			}
			s.reply(p.tag, nil, sys.Error("the file tool sent a bad reply"))
			return
		}
	}
	status := p.status
	if !p.exited {
		status = "killed"
	}
	s.reply(p.tag, nil, sys.Error("the file tool failed: "+status))
}

// startFile starts a file worker for request r: mcpd --file, in this
// namespace and environment, reading the request on its standard input.
// It has none of this prog's descriptors, so a tool that names /fd/1
// reaches the worker's reply, not the front end.
func (s *Session) startFile(r request) error {
	req := mcp.Encode(mcp.O("name", r.name, "arguments", r.args)) + "\n"
	in, err := feed(req)
	if err != nil {
		return err
	}
	p := &Proc{kind: kindFile, tag: r.tag,
		deadline: time.Now().Add(time.Duration(max(s.fileMS, 1)) * time.Millisecond)}
	if err := start(self, []string{"mcpd", "--file"}, in, fileReply, p); err != nil {
		return s.limits.explain(err)
	}
	s.procs = append(s.procs, p)
	return nil
}

// begin begins a request.
func (s *Session) begin(r request) {
	s.send(mcp.O("start", r.tag))
	args := r.args
	var v mcp.Value
	var err error
	switch r.name {
	case "run", "mount":
		if err = s.startCall(r); err == nil {
			s.busy = true
			return
		}
	case "job_start":
		v, err = s.jobStart(args)
	case "job_read":
		id := int(intArg(args, "id"))
		from := [2]int64{optInt(args, "offset"), optInt(args, "stderr_offset")}
		mx := optInt(args, "max_bytes")
		if mx < 0 {
			mx = defaultOutput
		}
		wait := max(optInt(args, "wait_ms"), 0)
		if p := s.jobByID(id); p != nil && wait > 0 && !p.job.done &&
			p.streams[0].end() <= fromOf(p, 0, from[0]) && p.streams[1].end() <= fromOf(p, 1, from[1]) {
			s.parked = append(s.parked, parked{r.tag, id, from, int(mx),
				time.Now().Add(time.Duration(wait) * time.Millisecond)})
			return
		}
		v, err = s.readJob(id, from, int(mx))
	case "job_write":
		v, err = s.jobWrite(args)
	case "job_kill":
		v, err = s.jobKill(args)
	case "job_list":
		v = s.jobList()
	default:
		if err = s.startFile(r); err == nil {
			s.busy = true
			return
		}
	}
	s.reply(r.tag, v, err)
}

// startCall starts the program of a run or a mount.
func (s *Session) startCall(r request) error {
	args := r.args
	str := func(k string) string { v, _ := mcp.Str(args, k); return v }
	var argv []string
	var cmd string
	var stdin int
	var timeout int64
	var cap int
	var err error
	if r.name == "mount" {
		argv = []string{"mount"}
		flags := "-"
		switch str("order") {
		case "before":
			flags += "b"
		case "after":
			flags += "a"
		}
		if b, _ := mcp.Bool(args, "create"); b {
			flags += "c"
		}
		if b, ok := mcp.Bool(args, "authenticate"); ok && !b {
			flags += "A"
		}
		if flags != "-" {
			argv = append(argv, flags)
		}
		argv = append(argv, str("source"), str("to"))
		if spec, ok := mcp.Str(args, "spec"); ok {
			argv = append(argv, spec)
		}
		timeout = optInt(args, "timeout_ms")
		if timeout < 0 {
			timeout = mountTimeoutMS
		}
		cmd, cap = "/bin/mount.wasm", 64*1024
		if stdin, err = null(sys.OREAD); err != nil {
			return err
		}
	} else {
		argv = []string{"sh", "-n", "-c", str("command")}
		if text := str("stdin"); text != "" {
			stdin, err = feed(text)
		} else {
			stdin, err = null(sys.OREAD)
		}
		if err != nil {
			return err
		}
		timeout = optInt(args, "timeout_ms")
		if timeout < 0 {
			timeout = defaultTimeoutMS
		}
		c := optInt(args, "max_output_bytes")
		if c < 0 {
			c = defaultOutput
		}
		cmd, cap = sh, int(c)
	}
	cwd := ""
	if r.name == "run" {
		cwd = str("cwd")
	}
	p := &Proc{kind: kindCall, tag: r.tag, mount: r.name == "mount",
		deadline: time.Now().Add(time.Duration(max(timeout, 1)) * time.Millisecond)}
	if err := launch(cwd, cmd, argv, stdin, cap, p); err != nil {
		return s.limits.explain(err)
	}
	s.procs = append(s.procs, p)
	return nil
}

func (s *Session) jobByID(id int) *Proc {
	for _, p := range s.procs {
		if p.kind == kindJob && p.job.id == id {
			return p
		}
	}
	return nil
}

func (s *Session) jobStart(args mcp.Value) (mcp.Value, error) {
	running := 0
	for _, p := range s.procs {
		if p.kind == kindJob && !p.job.done {
			running++
		}
	}
	if running >= maxJobs {
		return nil, sys.Error(fmt.Sprintf("%d jobs are running; kill one with job_kill first", maxJobs))
	}
	var cmd string
	var argv []string
	command, isCmd := mcp.Str(args, "command")
	if isCmd {
		cmd, argv = sh, []string{"sh", "-n", "-c", command}
	} else if a, _ := mcp.Get(args, "argv"); a != nil {
		l, _ := a.([]mcp.Value)
		for _, x := range l {
			w, _ := x.(string)
			argv = append(argv, w)
		}
		if len(argv) == 0 {
			return nil, sys.Error("arguments.command or arguments.argv is required")
		}
		cmd = argv[0]
		command = strings.Join(argv, " ")
	} else {
		return nil, sys.Error("arguments.command or arguments.argv is required")
	}
	cwd, _ := mcp.Str(args, "cwd")
	in, err := sys.Pipe()
	if err != nil {
		return nil, fail("pipe", err)
	}
	id := s.nextJob
	j := &Job{id: id, command: command, stdin: in[1]}
	if ms := optInt(args, "timeout_ms"); ms >= 0 {
		j.deadline = time.Now().Add(time.Duration(ms) * time.Millisecond)
	}
	p := &Proc{kind: kindJob, job: j}
	if err := launch(cwd, cmd, argv, in[0], jobBuffer, p); err != nil {
		closefd(in[1])
		return nil, s.limits.explain(err)
	}
	s.nextJob++
	s.procs = append(s.procs, p)
	if text, _ := mcp.Str(args, "stdin"); text != "" {
		j.queue = append(j.queue, []byte(text))
		s.pump(id)
	}
	return mcp.O("id", id, "pid", p.pid), nil
}

// pump hands a job's queued input to a feeder, one at a time so the order
// holds, and closes the input once all is written and EOF was asked.
func (s *Session) pump(id int) {
	p := s.jobByID(id)
	if p == nil {
		return
	}
	j := p.job
	if j.feeding {
		return
	}
	if j.stdin < 0 {
		j.queue = nil
		return
	}
	if len(j.queue) == 0 {
		if j.eof {
			closefd(j.stdin)
			j.stdin = -1
		}
		return
	}
	data := j.queue[0]
	j.queue = j.queue[1:]
	pid, err := startFeeder(data, j.stdin)
	if err != nil {
		// The job's input is gone; what is queued cannot be written.
		j.queue = nil
		closefd(j.stdin)
		j.stdin = -1
		return
	}
	j.feeding = true
	s.feeders = append(s.feeders, feeder{pid, id})
}

func (s *Session) readJob(id int, from [2]int64, mx int) (mcp.Value, error) {
	p := s.jobByID(id)
	if p == nil {
		return nil, sys.Error(fmt.Sprintf("job %d: no such job", id))
	}
	starts := [2]uint64{fromOf(p, 0, from[0]), fromOf(p, 1, from[1])}
	var lost [2]uint64
	for k := range 2 {
		if p.streams[k].start > starts[k] {
			lost[k] = p.streams[k].start - starts[k]
		}
	}
	out, o0, o1 := p.streams[0].read(starts[0], mx)
	errs, e0, e1 := p.streams[1].read(starts[1], mx)
	done := p.job.done
	status := ""
	if done {
		status = p.status
	}
	p.job.cursor = [2]uint64{o1, e1}
	v := mcp.O(
		"stdout", out,
		"stderr", errs,
		"offset", o0,
		"next_offset", o1,
		"stderr_offset", e0,
		"next_stderr_offset", e1,
		"lost", lost[0],
		"stderr_lost", lost[1],
		"running", !done,
		"ok", done && status == "",
		"status", status)
	if p.job.note != "" {
		v = append(v, mcp.Member{K: "note", V: p.job.note})
	}
	return v, nil
}

func (s *Session) jobWrite(args mcp.Value) (mcp.Value, error) {
	id := int(intArg(args, "id"))
	text, _ := mcp.Str(args, "data")
	data := []byte(text)
	if enc, _ := mcp.Str(args, "encoding"); enc == "base64" {
		var err error
		if data, err = mcp.Unbase64(text); err != nil {
			return nil, sys.Error("arguments.data: " + err.Error())
		}
	}
	p := s.jobByID(id)
	if p == nil {
		return nil, sys.Error(fmt.Sprintf("job %d: no such job", id))
	}
	j := p.job
	if p.exited {
		return nil, sys.Error(fmt.Sprintf("job %d has exited", id))
	}
	if j.eof || j.stdin < 0 {
		return nil, sys.Error(fmt.Sprintf("job %d: its input is closed", id))
	}
	if len(data) > 0 {
		j.queue = append(j.queue, data)
	}
	if b, _ := mcp.Bool(args, "eof"); b {
		j.eof = true
	}
	eof := j.eof
	s.pump(id)
	return mcp.O("queued", len(data), "eof", eof), nil
}

func (s *Session) jobKill(args mcp.Value) (mcp.Value, error) {
	id := int(intArg(args, "id"))
	p := s.jobByID(id)
	if p == nil {
		return nil, sys.Error(fmt.Sprintf("job %d: no such job", id))
	}
	killed := !p.exited && p.killedAt.IsZero()
	if killed {
		p.kill(time.Now())
	}
	return mcp.O("killed", killed), nil
}

func (s *Session) jobList() mcp.Value {
	jobs := []mcp.Value{}
	for _, p := range s.procs {
		if p.kind != kindJob {
			continue
		}
		j := p.job
		status := ""
		if j.done {
			status = p.status
		}
		jobs = append(jobs, mcp.O(
			"id", j.id,
			"pid", p.pid,
			"command", j.command,
			"running", !j.done,
			"status", status,
			"stdout_bytes", p.streams[0].end(),
			"stderr_bytes", p.streams[1].end()))
	}
	return mcp.O("jobs", jobs)
}

// home makes /tmp the current directory: tools name files from /tmp,
// whatever a command did to the working directory.
func home() {
	if sys.Chdir(homeDir) != nil {
		_ = sys.Chdir("/")
	}
}

// fileWorker is mcpd --file: it runs the one file tool request on
// standard input and writes {"result"} or {"error"} on standard output.
// The session starts one per call, so a call that blocks holds up only
// its worker, which the session kills at the limit.
func fileWorker() {
	text, err := readAll(0)
	var v mcp.Value
	if err != nil {
		err = fail("request", err)
	} else if req, perr := mcp.Parse(strings.TrimRight(mcp.Lossy(text), " \t\r\n")); perr != nil {
		err = sys.Error("bad request: " + perr.Error())
	} else {
		home()
		name, _ := mcp.Str(req, "name")
		args, ok := mcp.Get(req, "arguments")
		if !ok {
			args = mcp.Obj{}
		}
		v, err = call(name, args)
	}
	closefd(0)
	reply := mcp.O("result", v)
	if err != nil {
		reply = mcp.O("error", err.Error())
	}
	// The reply goes last, followed by its length, so that whatever the
	// tool itself wrote to this prog's standard output is passed over.
	body := mcp.Encode(reply)
	_ = writeAll(1, []byte("\n"+body+"\n"+strconv.Itoa(len(body))+"\n"))
}

// workerReply is the reply at the end of what a file worker wrote: the
// last len bytes before a last line holding len.
func workerReply(out string) (string, bool) {
	out, ok := strings.CutSuffix(out, "\n")
	if !ok {
		return "", false
	}
	i := strings.LastIndexByte(out, '\n')
	if i < 0 {
		return "", false
	}
	body, lens := out[:i], out[i+1:]
	n, err := strconv.Atoi(lens)
	if err != nil || n < 0 || n > len(body) || strings.HasPrefix(lens, "+") {
		return "", false
	}
	return body[len(body)-n:], true
}

// fromOf is where a read of stream k starts: the offset asked for, or
// the cursor.
func fromOf(p *Proc, k int, asked int64) uint64 {
	if asked >= 0 {
		return uint64(asked)
	}
	if p.kind == kindJob {
		return p.job.cursor[k]
	}
	return 0
}

func intArg(args mcp.Value, k string) int64 {
	n, _ := mcp.Int(args, k)
	return n
}

// optInt is a non-negative integer argument, or -1 when there is none.
func optInt(args mcp.Value, k string) int64 {
	n, ok := mcp.Int(args, k)
	if !ok {
		return -1
	}
	return max(n, 0)
}

// call runs a file tool or a resource request.
func call(name string, args mcp.Value) (mcp.Value, error) {
	str := func(k string) string { v, _ := mcp.Str(args, k); return v }
	path := func() (string, error) {
		if p := str("path"); p != "" {
			return p, nil
		}
		return "", sys.Error("arguments.path must not be empty")
	}
	switch name {
	case "read_file", "write_file", "list_dir", "stat", "remove", "resources/read":
		p, err := path()
		if err != nil {
			return nil, err
		}
		switch name {
		case "read_file":
			return readFileTool(p, args)
		case "write_file":
			return writeFileTool(p, args)
		case "list_dir":
			return listDir(p)
		case "stat":
			return statTool(p)
		case "resources/read":
			return readResource(p)
		}
		recursive, _ := mcp.Bool(args, "recursive")
		var removed uint64
		if err := removeTool(p, recursive, &removed); err != nil {
			return nil, err
		}
		return mcp.O("removed", removed), nil
	case "ns":
		ns, err := readFile(fmt.Sprintf("/prog/%d/ns", mypid()))
		if err != nil {
			return nil, fail("ns", err)
		}
		return mcp.O("ns", tidyNS(ns)), nil
	case "bind":
		from, to := str("from"), str("to")
		if _, err := sys.Bind(from, to, order(str("order"))|create(args)); err != nil {
			return nil, sys.Error(fmt.Sprintf("bind: cannot bind %s on %s: %s", from, to, err))
		}
		return mcp.O("ok", true), nil
	case "unmount":
		to := str("to")
		if err := sys.Unmount(str("from"), to); err != nil {
			return nil, sys.Error(fmt.Sprintf("unmount: %s: %s", to, err))
		}
		return mcp.O("ok", true), nil
	case "resources/list":
		return listResources(homeDir)
	}
	return nil, sys.Error("unknown tool: " + name)
}

func order(o string) int {
	switch o {
	case "before":
		return sys.MBEFORE
	case "after":
		return sys.MAFTER
	}
	return sys.MREPL
}

func create(args mcp.Value) int {
	if b, _ := mcp.Bool(args, "create"); b {
		return sys.MCREATE
	}
	return 0
}

type policyLine struct {
	verb string
	args []string
}

// policyLines are the lines of a namespace file as a verb and its
// arguments, flags (and the values of mount -k and -C) left out.
func policyLines(text string) []policyLine {
	var out []policyLine
	for _, line := range strings.Split(text, "\n") {
		words := newns.Unquoted(strings.TrimRight(line, "\r"))
		if len(words) == 0 {
			continue
		}
		verb := words[0]
		var args []string
		rest := words[1:]
		for i := 0; i < len(rest); i++ {
			w := rest[i]
			if len(args) == 0 && strings.HasPrefix(w, "-") && w != "-" {
				if w == "--" {
					args = append(args, rest[i+1:]...)
					break
				}
				if verb == "mount" && (strings.Contains(w, "k") || strings.Contains(w, "C")) {
					i++
				}
				continue
			}
			args = append(args, w)
		}
		out = append(out, policyLine{verb, args})
	}
	return out
}

// grantsFactotum says whether a namespace file grants the factotum: it
// binds #¤ (or a name in it) somewhere. Without that the session's
// namespace may not attach #¤ at all.
func grantsFactotum(text string) bool {
	for _, l := range policyLines(text) {
		if l.verb == "bind" && len(l.args) > 0 && strings.HasPrefix(l.args[0], "#¤") {
			return true
		}
	}
	return false
}

// makeMountPoints makes the mount points a namespace file names that the
// new root lacks, since namespace(6) has no way to make one and the root
// starts with only a few. Lines that name variables are left to nsbuild.
func makeMountPoints(text string) {
	for _, l := range policyLines(text) {
		var target string
		switch {
		case (l.verb == "bind" || l.verb == "mount") && len(l.args) > 1:
			target = l.args[1]
		case l.verb == "import" && len(l.args) >= 2:
			target = l.args[len(l.args)-1]
		default:
			continue
		}
		if !strings.HasPrefix(target, "/") || strings.Contains(target, "$") {
			continue
		}
		if _, err := sys.Stat(target); err == nil {
			continue
		}
		at := ""
		for _, part := range strings.Split(target, "/") {
			if part == "" {
				continue
			}
			at += "/" + part
			if _, err := sys.Stat(at); err != nil {
				if fd, err := sys.Create(at, sys.OREAD, sys.DMDIR|0o777); err == nil {
					closefd(fd)
				}
			}
		}
	}
}

// tidyNS turns /prog/n/ns into what the ns command prints.
func tidyNS(raw string) string {
	var b strings.Builder
	for _, l := range strings.SplitAfter(raw, "\n") {
		nsLine(&b, l)
	}
	return b.String()
}

func nsLine(b *strings.Builder, l string) {
	f := strings.FieldsFunc(l, func(r rune) bool { return r == ' ' || r == '\n' })
	if len(f) < 2 {
		return
	}
	cmd, f := f[0], f[1:]
	if cmd == "cd" {
		fmt.Fprintf(b, "%s %s\n", cmd, nsQuoted(f[0]))
		return
	}
	flag := ""
	if f[0][0] == '-' {
		flag, f = f[0]+" ", f[1:]
	}
	if len(f) < 2 {
		return
	}
	src, dest := f[0], f[1]
	// #/ and #U are the root anyway.
	if len(src) >= 3 && (src[:2] == "#/" || src[:2] == "#U") {
		src = src[2:]
	}
	if dest == "#M" {
		dest = "/"
	}
	if cmd == "mount" {
		src = netaddr(src)
	}
	fmt.Fprintf(b, "%s %s%s %s\n", cmd, flag, nsQuoted(src), nsQuoted(dest))
}

// netaddr turns /net/proto/n/data into proto!remote-address.
func netaddr(f string) string {
	if f == "" || f[0] != '/' {
		return f
	}
	fl := strings.FieldsFunc(f, func(r rune) bool { return r == '/' })
	if len(fl) < 4 {
		return f
	}
	netdir, proto, d := fl[0], fl[1], fl[2]
	if netdir != "net" && netdir != "net.alt" || fl[3] != "data" {
		return f
	}
	t, err := readFile("/" + netdir + "/" + proto + "/" + d + "/remote")
	if err != nil || t == "" {
		return f
	}
	t = strings.TrimSuffix(t, "\n")
	if netdir != "net" {
		proto = "/" + netdir + "/" + proto
	}
	return proto + "!" + t
}

// nsQuoted quotes s for the shell if it has any of its metacharacters.
func nsQuoted(s string) string {
	if !strings.ContainsAny(s, "' \t\\$#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
