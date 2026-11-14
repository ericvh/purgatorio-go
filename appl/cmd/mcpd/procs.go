package main

// Programs the session runs: commands for run and mount, which the
// session waits for, file workers, and jobs, which it keeps. Each has
// pipes for its output, read whenever they are ready, and its exit status
// from the session prog's wait file. Also the small helper progs mcpd
// starts from its own binary: the launcher and the stdin feeder.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/mcp"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	// killGrace is how long a killed command gets to die.
	killGrace = 3000 * time.Millisecond
	// drain is how long output already on its way is still collected
	// after a program has exited. Programs it started may hold its pipes
	// open.
	drain = 200 * time.Millisecond
	// execFailed starts the launcher's exit status when it could not
	// start the program: the rest is why.
	execFailed = "mcpd: cannot start: "
	// pipeRoom is what a pipe surely holds: up to this much is written
	// straight into it, with no feeder.
	pipeRoom = 16 * 1024
)

// Stream is one output pipe of a program and what has come out of it.
type Stream struct {
	fd    int // -1 once closed
	data  []byte
	start uint64 // offset of data[0]
	cap   int
	// ring keeps the last cap bytes (a job, a worker) rather than the
	// first (a call).
	ring      bool
	truncated bool
}

func newStream(fd, cap int, ring bool) *Stream {
	return &Stream{fd: fd, cap: cap, ring: ring}
}

// end is the offset after the last byte that has come out.
func (s *Stream) end() uint64 { return s.start + uint64(len(s.data)) }

func (s *Stream) close() {
	closefd(s.fd)
	s.fd = -1
}

// fill reads what is ready. Output past the cap is dropped, at the end
// for a call and at the start for a job, so the program is never held up
// by a full pipe.
func (s *Stream) fill() {
	var buf [8192]byte
	n, err := rd(s.fd, buf[:])
	if err != nil || n == 0 {
		s.close()
		return
	}
	if s.ring {
		// Dropping the front reslices; append copies only what is kept
		// when it grows the array, so a long stream costs linear time.
		s.data = append(s.data, buf[:n]...)
		if over := len(s.data) - s.cap; over > 0 {
			s.data = s.data[over:]
			s.start += uint64(over)
			s.truncated = true
		}
		return
	}
	keep := min(n, s.cap-len(s.data))
	s.data = append(s.data, buf[:keep]...)
	if keep < n {
		s.truncated = true
	}
}

func (s *Stream) text() string { return mcp.Lossy(s.data) }

// read is up to max bytes from offset from as text, and the offsets of
// the first byte returned and the one after the last. While more can
// come, a character cut at the end is left for the next read.
func (s *Stream) read(from uint64, max int) (string, uint64, uint64) {
	from = min(max64(from, s.start), s.end())
	skip := int(from - s.start)
	piece := s.data[skip:min(skip+max, len(s.data))]
	if n, incomplete := mcp.ValidUTF8Prefix(piece); incomplete && (s.fd >= 0 || len(piece) == max) {
		piece = piece[:n]
	}
	return mcp.Lossy(piece), from, from + uint64(len(piece))
}

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// What the session does with a program when it is over.
type kind int

const (
	kindCall kind = iota // answer a run or a mount
	kindFile             // answer a file tool with what its worker wrote
	kindJob              // keep it for the job tools
)

// Job is the state of a job_start.
type Job struct {
	id      int
	command string
	// stdin is the write end of the job's standard input, -1 once closed.
	stdin int
	// queue is data for the input, not yet given to a feeder.
	queue [][]byte
	// feeding: a feeder is writing to the input.
	feeding bool
	// eof: close the input once the queue is written.
	eof bool
	// cursor is where the last job_read ended, for stdout and stderr.
	cursor [2]uint64
	// done: exited, and its output collected.
	done bool
	// deadline is when it is killed, if it was given a timeout_ms.
	deadline time.Time
	timedOut bool
	// note says why it failed, when it was a limit of the session.
	note string
}

// Proc is a program the session started.
type Proc struct {
	pid int
	// ctl is open on /prog/<pid>/ctl, so the group can be killed even if
	// a command takes /prog out of the namespace.
	ctl      int
	streams  [2]*Stream
	status   string
	exited   bool
	exitedAt time.Time
	killedAt time.Time
	kind     kind
	tag      mcp.Value
	deadline time.Time // for a call or a worker
	mount    bool
	job      *Job
}

// launch starts argv (cmd is the program, found as sh finds it) through
// the launcher, mcpd --exec, which changes to cwd (empty for /tmp) and
// starts the program there. The session prog itself then neither changes
// directory nor looks the program up, so a file server that hangs on
// either holds up only this call.
func launch(cwd, cmd string, argv []string, stdin, cap int, p *Proc) error {
	args := append([]string{"mcpd", "--exec", cwd, cmd}, argv...)
	return start(self, args, stdin, cap, p)
}

// start starts argv (cmd is the module) in a new process group, with
// stdin as its input and pipes for its output, capped at cap. It takes
// stdin, which it closes.
func start(cmd string, argv []string, stdin, cap int, p *Proc) error {
	defer closefd(stdin)
	out, err := sys.Pipe()
	if err != nil {
		return fail("pipe", err)
	}
	errp, err := sys.Pipe()
	if err != nil {
		closefd(out[0])
		closefd(out[1])
		return fail("pipe", err)
	}
	// NEWPGRP: the program and what it starts are one group, killed
	// together. The namespace and environment are shared with the
	// session prog, so binds and variables outlive the program.
	pid, err := sys.Spawn(cmd, argv, sys.NEWFD|sys.NEWPGRP,
		[][2]int{{0, stdin}, {1, out[1]}, {2, errp[1]}})
	closefd(out[1])
	closefd(errp[1])
	if err != nil {
		closefd(out[0])
		closefd(errp[0])
		return fail(argv[0], err)
	}
	ring := p.kind != kindCall
	p.pid = pid
	p.ctl, err = sys.Open(fmt.Sprintf("/prog/%d/ctl", pid), sys.OWRITE)
	if err != nil {
		p.ctl = -1
	}
	p.streams = [2]*Stream{newStream(out[0], cap, ring), newStream(errp[0], cap, ring)}
	return nil
}

// limit is when the program is killed, if it has a time limit.
func (p *Proc) limit() time.Time {
	if p.kind == kindJob {
		return p.job.deadline
	}
	return p.deadline
}

// overdue says whether the program has run past its limit.
func (p *Proc) overdue(now time.Time) bool {
	l := p.limit()
	return !p.exited && !l.IsZero() && !now.Before(l)
}

func (p *Proc) kill(now time.Time) {
	if p.killedAt.IsZero() {
		p.killedAt = now
		killGroup(p.pid, p.ctl)
	}
}

func (p *Proc) open() bool { return p.streams[0].fd >= 0 || p.streams[1].fd >= 0 }

// settled says whether the program is over: it has exited and its pipes
// are closed or have had drain to empty, or it was killed killGrace ago.
func (p *Proc) settled(now time.Time) bool {
	return p.exited && !p.open() ||
		!p.exitedAt.IsZero() && !now.Before(p.exitedAt.Add(drain)) ||
		!p.killedAt.IsZero() && !now.Before(p.killedAt.Add(killGrace))
}

// due is when something is next due for this program, or zero.
func (p *Proc) due() time.Time {
	if p.kind == kindJob && p.job.done {
		return time.Time{}
	}
	var d time.Time
	add := func(t time.Time) {
		if !t.IsZero() && (d.IsZero() || t.Before(d)) {
			d = t
		}
	}
	if !p.exitedAt.IsZero() {
		add(p.exitedAt.Add(drain))
	}
	if !p.killedAt.IsZero() {
		add(p.killedAt.Add(killGrace))
	}
	if !p.exited && p.killedAt.IsZero() {
		add(p.limit())
	}
	return d
}

// waitGone waits, a little, until no prog of the group is left, so that a
// caller told the program was killed finds it gone.
func (p *Proc) waitGone() {
	if p.killedAt.IsZero() {
		return
	}
	for time.Since(p.killedAt) < killGrace {
		left := false
		for _, q := range progs("/prog") {
			if q.group == p.pid {
				left = true
			}
		}
		if !left {
			return
		}
		_ = sys.Sleep(20)
	}
}

func (p *Proc) closeAll() {
	for _, s := range p.streams {
		s.close()
	}
	closefd(p.ctl)
	p.ctl = -1
	if p.job != nil {
		closefd(p.job.stdin)
		p.job.stdin = -1
	}
}

// killGroup kills every prog in the process group led by pid.
func killGroup(pid, ctl int) {
	if ctl >= 0 && writeAll(ctl, []byte("killgrp")) == nil {
		return
	}
	// The leader has gone; find the members that are left.
	for _, q := range progs("/prog") {
		if q.group == pid {
			_ = writeFile(fmt.Sprintf("/prog/%d/ctl", q.pid), "kill")
		}
	}
}

type progInfo struct{ pid, group int }

// progs are the progs listed in dir (a #p), from their status files,
// which start with the pid and the process group, as Inferno's do.
func progs(dir string) []progInfo {
	fd, err := sys.Open(dir, sys.OREAD)
	if err != nil {
		return nil
	}
	defer closefd(fd)
	var out []progInfo
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			return out
		}
		for _, d := range ds {
			text, err := readFile(dir + "/" + d.Name + "/status")
			if err != nil {
				continue
			}
			f := strings.Fields(text)
			if len(f) < 2 {
				continue
			}
			pid, err1 := strconv.Atoi(f[0])
			group, err2 := strconv.Atoi(f[1])
			if err1 == nil && err2 == nil {
				out = append(out, progInfo{pid, group})
			}
		}
	}
}

// startFeeder starts a feeder that writes data to fd to and returns its
// pid. A separate prog does the writing because a pipe holds only so
// much: the reader may not read until it has written output the session
// must be free to collect. The feeder reads all of data before it writes
// any of it, so handing it over cannot wait on the reader.
func startFeeder(data []byte, to int) (int, error) {
	p, err := sys.Pipe()
	if err != nil {
		return 0, fail("pipe", err)
	}
	pid, err := sys.Spawn(self, []string{"mcpd", "--feed"}, sys.NEWFD, [][2]int{{0, p[0]}, {1, to}})
	closefd(p[0])
	if err != nil {
		closefd(p[1])
		return 0, fail("stdin feeder", err)
	}
	err = writeAll(p[1], data)
	closefd(p[1])
	if err != nil {
		return 0, fail("stdin", err)
	}
	return pid, nil
}

// feed is a pipe whose read end gives text and then end of file.
func feed(text string) (int, error) {
	p, err := sys.Pipe()
	if err != nil {
		return -1, fail("pipe", err)
	}
	if len(text) <= pipeRoom {
		err = writeAll(p[1], []byte(text))
	} else {
		_, err = startFeeder([]byte(text), p[1])
	}
	closefd(p[1])
	if err != nil {
		closefd(p[0])
		return -1, err
	}
	return p[0], nil
}

// execMain is mcpd --exec cwd cmd argv...: it changes to cwd (/tmp when
// it is empty), starts cmd with argv on this prog's three standard
// files, waits for it and exits with its status. A failure to change
// directory or to start the program is written to standard error and
// given as the status after execFailed. The program shares this prog's
// process group, namespace and environment.
func execMain(args []string) {
	die := func(msg string) {
		stderr(msg + "\n")
		sys.Exits(execFailed + msg)
	}
	if len(args) < 2 {
		die("usage: mcpd --exec cwd cmd argv...")
	}
	cwd, cmd, argv := args[0], args[1], args[2:]
	if cwd == "" {
		home()
	} else if err := sys.Chdir(cwd); err != nil {
		die(cwd + ": " + err.Error())
	}
	name := cmd
	if len(argv) > 0 {
		name = argv[0]
	}
	wait, err := sys.Open(fmt.Sprintf("/prog/%d/wait", mypid()), sys.OREAD)
	if err != nil {
		die("wait: " + err.Error())
	}
	path := commandPath(cmd)
	pid, err := sys.Spawn(path, argv, sys.NEWFD, [][2]int{{0, 0}, {1, 1}, {2, 2}})
	if err != nil {
		die(name + ": " + path + ": " + err.Error())
	}
	// The program has its own copies; the pipes close when it is done.
	for fd := 0; fd < 3; fd++ {
		closefd(fd)
	}
	buf := make([]byte, 4096)
	for {
		n, err := rd(wait, buf)
		if err != nil || n == 0 {
			if err == nil {
				err = sys.Error("wait closed")
			}
			sys.Exits("killed: " + err.Error())
		}
		if p, status, ok := parseWait(string(buf[:n])); ok && p == pid {
			sys.Exits(status)
		}
	}
}

// feederMain is mcpd --feed: copy fd 0 to fd 1, reading all of it first.
func feederMain() {
	data, err := readAll(0)
	closefd(0)
	if err == nil {
		_ = writeAll(1, data)
	}
}
