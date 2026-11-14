// Command mcpd [-l logfile] [-n nsfile] [-t filetimeout] [-M memory]
// [-P progs] is a Model Context Protocol server on standard input and
// output, for driving a sandboxed Purgatorio session from an LLM client.
// It is a port of the Rust sibling's mcpd; docs/mcp.md is the manual.
//
// The transport is MCP's stdio transport: JSON-RPC 2.0, one message per
// line. Only protocol messages are written to standard output. On emu,
// fd 2 is the console too, which is that same output, so the audit log
// goes to logfile (appended) or nowhere. See rpc.go for the protocol
// revision and session.go for the sandbox.
//
// Three progs make a server. This one, the front end, speaks the
// protocol, writes the log and never touches a file a tool names. A relay
// prog (mcpd --relay) copies the client's input to a pipe, which can be
// waited on together with the session whatever the input is: the
// console can be waited on, but a console mounted from another machine
// (rcmd host mcpd) cannot. The session prog (mcpd --session) builds the
// sandbox namespace, confines itself to it and answers the calls, file
// tools through a worker prog per call (mcpd --file, limited by -t).
// Calls finish out of order, so the front end matches answers to requests
// by tag. reset, and a session that dies, are handled by killing the
// session's progs and starting another.
//
// A session is a prog domain (#pdomain): the session prog and every prog
// started from it, whatever their process groups. -M and -P limit the
// wasm memory the domain's progs may hold between them and how many there
// may be. The front end ends a session by killing every member of its
// domain, which it sees through #pdomain=<session pid>.
//
// -n nsfile names a namespace(6) file that nsbuild applies in the new
// namespace before devices are locked out; it is how a policy grants a
// workspace or the network.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/mcp"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	self  = "/bin/mcpd.wasm"
	usage = "mcpd [-l logfile] [-n nsfile] [-t filetimeout] [-M memory] [-P progs]"
	// maxLine is the longest request line; it bounds the memory a client
	// can make the server hold.
	maxLine = 16 << 20
	// fileToolMS is how long a file tool may take by default (-t).
	fileToolMS = 30_000
	// sessionSlackMS is what the front end allows the session beyond a
	// call's own limit before it gives up on the session.
	sessionSlackMS = 15_000
	// runSlackMS is what a command may take beyond its own timeout:
	// killing it and collecting its output.
	runSlackMS = 15_000
	// reapMS is how long the front end waits for a killed session.
	reapMS = 3000
	// window is the bytes written to the session and not yet read by it.
	// The session reads as much at a time, and a pipe holds more, so a
	// write never blocks.
	window = 16 * 1024
	// sessionMem is the wasm memory a session's progs may hold between
	// them by default (-M).
	sessionMem = 256 << 20
	// sessionProgs is how many progs a session may have by default (-P).
	sessionProgs = 128

	sessionEnded = "the session ended while the tool was running (its server prog was killed); " +
		"a new session has been started, with an empty /tmp and a fresh namespace"
)

// worker is a running session prog and the pipes to it.
type worker struct {
	pid      int
	to, from int
	acc      []byte // what has come from the session, not yet a line
	inflight int    // bytes written to to that the session has not acknowledged
}

// ticket is when a call came, and its place among the replies: seq is
// -1 for a job_read that waits, whose reply goes out when it is ready.
type ticket struct {
	received time.Time
	seq      int64
}

// pending is a call the session has not answered.
type pending struct {
	call   *Call
	ticket ticket
	// deadline is the session's limit once it begins the call.
	deadline time.Time
}

type config struct {
	nsfile string
	hasNS  bool
	fileMS int64
	limits Limits
}

type heldCall struct {
	call *Call
	t    ticket
}

type slot struct {
	seq   int64
	reply string
	ready bool
}

type front struct {
	config config
	// worker is the session, or nil with why it could not be built. A
	// failure is reported by every tool call rather than by exiting, so
	// the client is told.
	worker  *worker
	why     string
	wait    int // open on this prog's /prog/n/wait
	pending map[int64]*pending
	nextTag int64
	outq    []byte
	// held are calls held back behind a reset, which waits for the calls
	// before it.
	held []heldCall
	// order holds replies in the order of the requests, each sent once
	// it and all before it are ready.
	order   []slot
	nextSeq int64
	log     bool
}

func nowSecs() int64 { return time.Now().Unix() }

// shorten cuts a long string argument for the log.
func shorten(v mcp.Value) mcp.Value {
	switch x := v.(type) {
	case string:
		if r := []rune(x); len(r) > 64 {
			return fmt.Sprintf("%s...(%d bytes)", string(r[:48]), len(x))
		}
	case []mcp.Value:
		a := make([]mcp.Value, len(x))
		for i, e := range x {
			a[i] = shorten(e)
		}
		return a
	case mcp.Obj:
		o := make(mcp.Obj, len(x))
		for i, m := range x {
			o[i] = mcp.Member{K: m.K, V: shorten(m.V)}
		}
		return o
	}
	return v
}

// summary is the arguments of a call as JSON, each long string cut and
// the whole cut at 400 characters.
func summary(args mcp.Value) string {
	text := mcp.Encode(shorten(args))
	if r := []rune(text); len(r) > 400 {
		return string(r[:400]) + "..."
	}
	return text
}

// logf appends a line to the log: seconds since 1970, what happened.
func logf(format string, a ...any) {
	stderr(fmt.Sprintf("%d %s\n", nowSecs(), fmt.Sprintf(format, a...)))
}

func logCall(c *Call, status string, received time.Time) {
	logf("%s %s %s %dms %s", c.Kind(), c.Name, status, time.Since(received).Milliseconds(), summary(c.Args))
}

// limit is how long the session may take over a call once it has begun
// it.
func limit(c *Call, fileMS int64) int64 {
	arg := func(k string, def int64) int64 {
		if n, ok := mcp.Int(c.Args, k); ok {
			return n
		}
		return def
	}
	switch c.Name {
	case "run":
		return arg("timeout_ms", defaultTimeoutMS) + runSlackMS
	case "mount":
		return arg("timeout_ms", mountTimeoutMS) + runSlackMS
	case "job_read":
		return arg("wait_ms", 0) + sessionSlackMS
	}
	return fileMS + sessionSlackMS
}

// waits says whether a call is a job_read that waits: it does not hold up
// a reset.
func waits(c *Call) bool {
	n, _ := mcp.Int(c.Args, "wait_ms")
	return c.Name == "job_read" && n > 0
}

func startSession(cfg *config) (*worker, string) {
	req, err := sys.Pipe()
	if err != nil {
		return nil, "pipe: " + err.Error()
	}
	resp, err := sys.Pipe()
	if err != nil {
		return nil, "pipe: " + err.Error()
	}
	nul, err := sys.Open("/dev/null", sys.OWRITE)
	if err != nil {
		return nil, "/dev/null: " + err.Error()
	}
	argv := []string{"mcpd", "--session", strconv.FormatInt(cfg.fileMS, 10),
		strconv.FormatUint(cfg.limits.mem, 10), strconv.FormatUint(cfg.limits.progs, 10)}
	if cfg.hasNS {
		argv = append(argv, cfg.nsfile)
	}
	// The session gets copies of the namespace and the environment and
	// a process group of its own. Its fd 2 is not the log, which it could
	// otherwise write through /fd/2.
	pid, err := sys.Spawn(self, argv, sys.NEWFD|sys.FORKNS|sys.FORKENV|sys.NEWPGRP,
		[][2]int{{0, req[0]}, {1, resp[1]}, {2, nul}})
	closefd(req[0])
	closefd(resp[1])
	closefd(nul)
	if err != nil {
		closefd(req[1])
		closefd(resp[0])
		return nil, "cannot start the session: " + err.Error()
	}
	w := &worker{pid: pid, to: req[1], from: resp[0]}
	buf := make([]byte, 4096)
	for indexByte(w.acc, '\n') < 0 {
		n, err := rd(w.from, buf)
		if err != nil || n == 0 {
			w.close()
			return nil, "the session prog exited while starting"
		}
		w.acc = append(w.acc, buf[:n]...)
	}
	i := indexByte(w.acc, '\n')
	line := w.acc[:i+1]
	w.acc = append([]byte(nil), w.acc[i+1:]...)
	v, err := mcp.Parse(strings.TrimSpace(mcp.Lossy(line)))
	if err != nil {
		w.close()
		return nil, "the session sent a bad reply"
	}
	if mcp.Has(v, "ready") {
		return w, ""
	}
	why, ok := mcp.Str(v, "fatal")
	if !ok {
		why = "unknown error"
	}
	w.close()
	return nil, "cannot build the session namespace: " + why
}

func (w *worker) close() {
	closefd(w.to)
	closefd(w.from)
	w.to, w.from = -1, -1
}

// end ends the session: it kills every prog in its domain (the session
// prog, its file servers, the commands it ran and everything they
// started, whatever their groups and whether or not their parents live),
// and collects the session prog's exit.
func (f *front) end() {
	f.outq = nil
	w := f.worker
	f.worker, f.why = nil, "no session"
	if w == nil {
		return
	}
	// Closing the pipes lets a healthy session prog exit by itself.
	w.close()
	killDomain(w.pid)
	if f.wait < 0 {
		return
	}
	buf := make([]byte, 4096)
	for {
		r, _ := waitReadable([]int{f.wait}, reapMS)
		if r != 0 {
			return
		}
		n, err := rd(f.wait, buf)
		if err != nil || n == 0 {
			return
		}
		if p, _, ok := parseWait(string(buf[:n])); ok && p == w.pid {
			return
		}
	}
}

// restart replaces the session. Calls in progress fail with why.
func (f *front) restart(why string) {
	f.end()
	f.failPending(why)
	f.worker, f.why = startSession(&f.config)
	f.logStart()
}

func (f *front) logStart() {
	if f.worker != nil {
		logf("session start %d", f.worker.pid)
	} else {
		logf("session failed %s", f.why)
	}
}

func (f *front) failPending(why string) {
	ps := f.pending
	f.pending = map[int64]*pending{}
	// In tag order, as the Rust mcpd's BTreeMap gives them.
	var tags []int64
	for t := range ps {
		tags = append(tags, t)
	}
	sortInt64(tags)
	for _, t := range tags {
		p := ps[t]
		f.answer(p.call, nil, why, true, p.ticket)
	}
}

func sortInt64(a []int64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func (f *front) writeOut(line string) {
	_ = writeAll(1, []byte(line+"\n"))
}

// ticket gives a request that has come its place among the replies.
func (f *front) ticket(c *Call) ticket {
	t := ticket{received: time.Now(), seq: -1}
	if c == nil || !waits(c) {
		t.seq = f.nextSeq
		f.nextSeq++
		f.order = append(f.order, slot{seq: t.seq})
	}
	return t
}

// deliver sends a reply when the replies before it have gone.
func (f *front) deliver(seq int64, reply string) {
	if seq < 0 {
		f.writeOut(reply)
		return
	}
	for i := range f.order {
		if f.order[i].seq == seq {
			f.order[i].reply, f.order[i].ready = reply, true
		}
	}
	for len(f.order) > 0 && f.order[0].ready {
		f.writeOut(f.order[0].reply)
		f.order = f.order[1:]
	}
}

// replyNow replies to a request that needs no call.
func (f *front) replyNow(reply string) {
	t := f.ticket(nil)
	f.deliver(t.seq, reply)
}

func (f *front) answer(c *Call, v mcp.Value, e string, failed bool, t ticket) {
	st := "ok"
	if failed {
		st = "error"
	}
	logCall(c, st, t.received)
	f.deliver(t.seq, c.Finish(v, e, failed))
}

func (f *front) busy() bool {
	for _, p := range f.pending {
		if !waits(p.call) {
			return true
		}
	}
	return false
}

// submit takes a call from the client: answers it, or sends it to the
// session. held says the call was held behind a reset and is now free to
// go.
func (f *front) submit(c *Call, t ticket, held bool) {
	if c.Rejected != "" {
		logCall(c, "invalid", t.received)
		f.deliver(t.seq, c.Finish(nil, c.Rejected, true))
		return
	}
	reset := c.What == Tool && c.Name == "reset"
	if !held && len(f.held) > 0 || reset && f.busy() {
		f.held = append(f.held, heldCall{c, t})
		return
	}
	if reset {
		logf("session reset")
		f.restart("the session was reset before the call finished")
		if f.worker != nil {
			f.answer(c, mcp.O("reset", true), "", false, t)
		} else {
			f.answer(c, nil, f.why, true, t)
		}
		return
	}
	if f.worker == nil {
		f.answer(c, nil, f.why, true, t)
		return
	}
	tag := f.nextTag
	f.nextTag++
	line := mcp.Encode(mcp.O("tag", tag, "name", c.Name, "arguments", c.Args)) + "\n"
	f.outq = append(f.outq, line...)
	f.pending[tag] = &pending{call: c, ticket: t}
}

// take handles one line from the client.
func (f *front) take(s *Server, line []byte) {
	text := "\x01" // a line that is not UTF-8 gets the parse error
	if validUTF8(line) {
		text = string(line)
	}
	step := s.Begin(text)
	switch {
	case step.Call != nil:
		t := f.ticket(step.Call)
		f.submit(step.Call, t, false)
	case step.Reply != "":
		f.replyNow(step.Reply)
	}
}

// release submits the calls held behind a reset once it can go.
func (f *front) release() {
	for len(f.held) > 0 {
		h := f.held[0]
		if h.call.Name == "reset" && f.busy() {
			return
		}
		f.held = f.held[1:]
		f.submit(h.call, h.t, true)
	}
}

// flush writes what the window allows.
func (f *front) flush() {
	w := f.worker
	if w == nil {
		return
	}
	for w.inflight < window && len(f.outq) > 0 {
		n := min(window-w.inflight, len(f.outq))
		chunk := f.outq[:n]
		if writeAll(w.to, chunk) != nil {
			f.restart(sessionEnded)
			return
		}
		f.outq = f.outq[n:]
		w.inflight += n
	}
}

// readSession reads what the session has sent.
func (f *front) readSession() {
	w := f.worker
	buf := make([]byte, 65536)
	n, err := rd(w.from, buf)
	if err != nil || n == 0 {
		f.restart(sessionEnded)
		return
	}
	w.acc = append(w.acc, buf[:n]...)
	var lines [][]byte
	for {
		i := indexByte(w.acc, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, w.acc[:i+1])
		w.acc = w.acc[i+1:]
	}
	for _, line := range lines {
		v, err := mcp.Parse(strings.TrimSpace(mcp.Lossy(line)))
		if err != nil {
			continue
		}
		if n, ok := mcp.Int(v, "ack"); ok {
			if f.worker != nil {
				f.worker.inflight = max(f.worker.inflight-int(max(n, 0)), 0)
			}
			continue
		}
		if tag, ok := mcp.Int(v, "start"); ok {
			if p := f.pending[tag]; p != nil {
				ms := max(limit(p.call, f.config.fileMS), 0)
				p.deadline = time.Now().Add(time.Duration(ms) * time.Millisecond)
			}
			continue
		}
		tag, ok := mcp.Int(v, "tag")
		if !ok {
			continue
		}
		p := f.pending[tag]
		if p == nil {
			continue
		}
		delete(f.pending, tag)
		if r, ok := mcp.Get(v, "result"); ok {
			f.answer(p.call, r, "", false, p.ticket)
		} else if e, ok := mcp.Get(v, "error"); ok {
			es, isStr := e.(string)
			if !isStr {
				es = "tool failed"
			}
			f.answer(p.call, nil, es, true, p.ticket)
		} else {
			f.answer(p.call, nil, "the session sent a bad reply", true, p.ticket)
		}
	}
}

func (f *front) nextDeadline() time.Time {
	var d time.Time
	for _, p := range f.pending {
		if !p.deadline.IsZero() && (d.IsZero() || p.deadline.Before(d)) {
			d = p.deadline
		}
	}
	return d
}

// checkDeadlines gives up on a session that has not answered in time.
func (f *front) checkDeadlines() {
	if d := f.nextDeadline(); !d.IsZero() && !time.Now().Before(d) {
		f.restart("the session did not answer in time; " +
			"a new session has been started, with an empty /tmp and a fresh namespace")
	}
}

// killDomain kills every prog in the domain founded by the session prog
// pid, until none is left or reapMS has passed: a prog may start another
// while it is being killed.
func killDomain(pid int) {
	view := fmt.Sprintf("#pdomain=%d", pid)
	if _, err := sys.Stat(view); err != nil {
		// No view of the domain (this prog has given up devices): kill
		// the session's group and what descends from it.
		killDescendants(pid)
		return
	}
	start := time.Now()
	for {
		members := progs(view)
		if len(members) == 0 || time.Since(start) > reapMS*time.Millisecond {
			return
		}
		for _, p := range members {
			_ = writeFile(fmt.Sprintf("%s/%d/ctl", view, p.pid), "kill")
		}
		_ = sys.Sleep(10)
	}
}

// killDescendants kills the progs of group pid, as far as /prog shows
// them. (Inferno's status files do not give a prog's parent, so this
// cannot follow progs that left the group, as killDomain does.)
func killDescendants(pid int) {
	for _, p := range progs("/prog") {
		if p.group == pid {
			_ = writeFile(fmt.Sprintf("/prog/%d/ctl", p.pid), "kill")
		}
	}
}

// redirectStderr points fd 2 at the log file, or at /dev/null.
func redirectStderr(log string, hasLog bool) error {
	var fd int
	var err error
	if hasLog {
		if fd, err = sys.Open(log, sys.OWRITE); err != nil {
			if fd, err = sys.Create(log, sys.OWRITE, 0o666); err != nil {
				return sys.Error(log + ": " + err.Error())
			}
		}
		if _, err := sys.Seek(fd, 0, 2); err != nil {
			return err
		}
	} else if fd, err = sys.Open("/dev/null", sys.OWRITE); err != nil {
		return err
	}
	if _, err := sys.Dup(fd, 2); err != nil {
		return err
	}
	closefd(fd)
	return nil
}

// relay starts the relay and returns the pipe it writes the client's
// input to.
func relay() (int, error) {
	p, err := sys.Pipe()
	if err != nil {
		return -1, err
	}
	_, err = sys.Spawn(self, []string{"mcpd", "--relay"}, sys.NEWFD, [][2]int{{0, 0}, {1, p[1]}})
	closefd(p[1])
	if err != nil {
		closefd(p[0])
		return -1, err
	}
	return p[0], nil
}

// relayMain is mcpd --relay: copy fd 0 to fd 1 as it comes.
func relayMain() {
	buf := make([]byte, 65536)
	for {
		n, err := rd(0, buf)
		if err != nil || n == 0 || writeAll(1, buf[:n]) != nil {
			return
		}
	}
}

func serve(cfg config) error {
	wait, err := sys.Open(fmt.Sprintf("#p/%d/wait", mypid()), sys.OREAD)
	if err != nil {
		wait = -1
	}
	input, err := relay()
	if err != nil {
		return err
	}
	f := &front{config: cfg, wait: wait, pending: map[int64]*pending{}, nextTag: 1}
	f.worker, f.why = startSession(&f.config)
	f.logStart()
	var s Server
	var acc []byte
	skipping, eof := false, false
	buf := make([]byte, 65536)
	for {
		if eof && len(f.pending) == 0 && len(f.held) == 0 {
			break
		}
		var fds []int
		if !eof {
			fds = append(fds, input)
		}
		if f.worker != nil {
			fds = append(fds, f.worker.from)
		}
		if len(fds) == 0 {
			break
		}
		r, err := waitReadable(fds, msUntil(f.nextDeadline()))
		if err != nil {
			break
		}
		switch {
		case r == 0 && !eof:
			n, err := rd(input, buf)
			if err != nil || n == 0 {
				eof = true
				n = 0
			}
			acc = append(acc, buf[:n]...)
			for {
				i := indexByte(acc, '\n')
				if i < 0 {
					if len(acc) > maxLine {
						// Drop the rest of the line without keeping it.
						acc = nil
						if !skipping {
							skipping = true
							f.replyNow(tooLong())
						}
					}
					break
				}
				line := acc[:i+1]
				acc = acc[i+1:]
				if skipping {
					skipping = false
					continue
				}
				if len(line) > maxLine+1 {
					f.replyNow(tooLong())
					continue
				}
				f.take(&s, line)
			}
			// A last line without a newline.
			if eof && len(acc) > 0 && !skipping {
				line := acc
				acc = nil
				f.take(&s, line)
			}
		case r >= 0:
			if f.worker != nil {
				f.readSession()
			}
		}
		f.checkDeadlines()
		f.release()
		f.flush()
	}
	logf("session end")
	f.end()
	return nil
}

func main() {
	args := os.Args
	if len(args) > 1 {
		switch args[1] {
		case "--feed":
			feederMain()
			return
		case "--relay":
			relayMain()
			return
		case "--file":
			fileWorker()
			return
		case "--exec":
			execMain(args[2:])
			return
		case "--session":
			sessionMain(args)
			return
		}
	}
	a := arg.New(args)
	a.SetUsage(usage)
	cfg := config{fileMS: fileToolMS, limits: Limits{mem: sessionMem, progs: sessionProgs}}
	log, hasLog := "", false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'l':
			log, hasLog = a.EArg(), true
		case 'n':
			cfg.nsfile, cfg.hasNS = a.EArg(), true
		case 't':
			n, err := strconv.ParseInt(a.EArg(), 10, 64)
			if err != nil || n <= 0 {
				a.Usage()
			}
			cfg.fileMS = n
		case 'M':
			n, ok := parseSize(a.EArg())
			if !ok || n == 0 {
				a.Usage()
			}
			cfg.limits.mem = n
		case 'P':
			n, err := strconv.ParseUint(a.EArg(), 10, 64)
			if err != nil || n == 0 {
				a.Usage()
			}
			cfg.limits.progs = n
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	if err := redirectStderr(log, hasLog); err != nil {
		stderr("mcpd: " + err.Error() + "\n")
		sys.Exits("fail:" + err.Error())
	}
	if err := serve(cfg); err != nil {
		stderr("mcpd: " + err.Error() + "\n")
		sys.Exits("fail:" + err.Error())
	}
}

// sessionMain is mcpd --session filems mem progs [nsfile].
func sessionMain(args []string) {
	num := func(i int, def uint64) uint64 {
		if i < len(args) {
			if n, err := strconv.ParseUint(args[i], 10, 64); err == nil {
				return n
			}
		}
		return def
	}
	fileMS := int64(num(2, fileToolMS))
	limits := Limits{mem: num(3, sessionMem), progs: num(4, sessionProgs)}
	var policy *string
	var err error
	if len(args) > 5 {
		var text string
		if text, err = readFile(args[5]); err != nil {
			err = sys.Error(args[5] + ": " + err.Error())
		} else {
			policy = &text
		}
	}
	var s *Session
	if err == nil {
		s, err = buildSession(policy, fileMS, limits)
	}
	if err != nil {
		_ = writeAll(1, []byte(mcp.Encode(mcp.O("fatal", err.Error()))+"\n"))
		sys.Exits("fail:" + err.Error())
	}
	s.serve()
}

// parseSize is a byte count: digits, then optionally k, m or g for 2^10,
// 2^20 or 2^30 times as many, as /prog/n/ctl takes them.
func parseSize(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	shift := 0
	switch s[len(s)-1] {
	case 'k', 'K':
		shift = 10
	case 'm', 'M':
		shift = 20
	case 'g', 'G':
		shift = 30
	}
	if shift > 0 {
		s = s[:len(s)-1]
	}
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > (1<<64-1)>>shift {
		return 0, false
	}
	return n << shift, true
}
