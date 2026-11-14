package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/styx"
)

// #C qid types, as devcmd.c: type in the low 4 bits, conversation above.
const (
	cmdQtopdir = iota
	cmdQcmd
	cmdQclone
	cmdQconvdir
	cmdQdata
	cmdQstderr
	cmdQctl
	cmdQstatus
	cmdQwait
)

func cmdqid(c, t int) uint64  { return uint64(c)<<4 | uint64(t) }
func cmdtype(q styx.Qid) int  { return int(q.Path & 0xf) }
func cmdconvn(q styx.Qid) int { return int(q.Path>>4) & 0xfff }

type cmdconv struct {
	mu          sync.Mutex
	x           int
	inuse       int
	stdin       *os.File
	stdout      *os.File
	stderr      *os.File
	count       [3]int
	perm        uint32
	owner       string
	state       string
	args        []string
	dir         string
	waitq       *core.Queue
	child       *exec.Cmd
	nice        int
	killonclose bool
	killed      bool
}

// DevCmd is #C, which runs host commands (Inferno's devcmd.c): clone a
// conversation, write "exec cmd args" to its ctl, and talk to the command
// through data and stderr; wait gives its exit status.
type DevCmd struct {
	core.DevBase
	k     *core.Kernel
	root  string
	mu    sync.Mutex
	convs []*cmdconv
}

// NewDevCmd runs commands in root by default (Inferno's rootdir).
func NewDevCmd(k *core.Kernel, root string) *DevCmd { return &DevCmd{k: k, root: root} }

func (d *DevCmd) DC() rune     { return 'C' }
func (d *DevCmd) Name() string { return "cmd" }

func (d *DevCmd) Attach(context.Context, string) (core.Walked, error) {
	return core.Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: cmdqid(0, cmdQtopdir)}}, nil
}

func (d *DevCmd) conv(q styx.Qid) *cmdconv {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := cmdconvn(q); n < len(d.convs) {
		return d.convs[n]
	}
	return nil
}

func (d *DevCmd) gen(q styx.Qid) []styx.Dir {
	mk := func(path uint64, dir bool, name, owner string, perm uint32) styx.Dir {
		qt := uint8(0)
		if dir {
			qt, perm = styx.QTDIR, perm|styx.DMDIR
		}
		return styx.Dir{Type: 'C', Qid: styx.Qid{Type: qt, Path: path}, Name: name, UID: owner, GID: owner, Mode: perm}
	}
	var out []styx.Dir
	switch cmdtype(q) {
	case cmdQtopdir:
		out = append(out, mk(cmdqid(0, cmdQcmd), true, "cmd", "cmd", 0o555))
	case cmdQcmd:
		d.mu.Lock()
		for _, c := range d.convs {
			c.mu.Lock()
			out = append(out, mk(cmdqid(c.x, cmdQconvdir), true, strconv.Itoa(c.x), c.owner, 0o555))
			c.mu.Unlock()
		}
		d.mu.Unlock()
		out = append(out, mk(cmdqid(0, cmdQclone), false, "clone", "cmd", 0o666))
	case cmdQconvdir:
		c := d.conv(q)
		if c == nil {
			return nil
		}
		c.mu.Lock()
		owner, perm := c.owner, c.perm
		c.mu.Unlock()
		x := cmdconvn(q)
		out = append(out, mk(cmdqid(x, cmdQdata), false, "data", owner, perm),
			mk(cmdqid(x, cmdQstderr), false, "stderr", owner, 0o444),
			mk(cmdqid(x, cmdQctl), false, "ctl", owner, perm),
			mk(cmdqid(x, cmdQstatus), false, "status", owner, 0o444),
			mk(cmdqid(x, cmdQwait), false, "wait", owner, 0o444))
	}
	return out
}

func (d *DevCmd) Walk(_ context.Context, c *core.Chan, name string) (core.Walked, error) {
	if !c.IsDir() {
		return core.Walked{}, core.Error(abi.Enotdir)
	}
	for _, e := range d.gen(c.Qid) {
		if e.Name == name {
			return core.Walked{Qid: e.Qid}, nil
		}
	}
	return core.Walked{}, core.Error(abi.Enonexist)
}

func (d *DevCmd) Stat(_ context.Context, c *core.Chan) (styx.Dir, error) {
	var parent styx.Qid
	switch cmdtype(c.Qid) {
	case cmdQtopdir:
		return styx.Dir{Type: 'C', Qid: c.Qid, Name: "#C", UID: d.k.Owner, GID: d.k.Owner, Mode: styx.DMDIR | 0o555}, nil
	case cmdQcmd:
		parent = styx.Qid{Type: styx.QTDIR, Path: cmdqid(0, cmdQtopdir)}
	case cmdQclone, cmdQconvdir:
		parent = styx.Qid{Type: styx.QTDIR, Path: cmdqid(0, cmdQcmd)}
	default:
		parent = styx.Qid{Type: styx.QTDIR, Path: cmdqid(cmdconvn(c.Qid), cmdQconvdir)}
	}
	for _, e := range d.gen(parent) {
		if e.Qid.Path == c.Qid.Path {
			return e, nil
		}
	}
	return styx.Dir{}, core.Error(abi.Enonexist)
}

func (d *DevCmd) DirRead(_ context.Context, c *core.Chan) ([]styx.Dir, error) {
	return d.gen(c.Qid), nil
}

func progUser(ctx context.Context, k *core.Kernel) string {
	if p := core.ProgFrom(ctx); p != nil {
		return p.User()
	}
	return k.Owner
}

func (d *DevCmd) clone(u string) *cmdconv {
	d.mu.Lock()
	defer d.mu.Unlock()
	var c *cmdconv
	for _, x := range d.convs {
		if x.mu.TryLock() {
			if x.inuse == 0 && x.child == nil {
				c = x
				break
			}
			x.mu.Unlock()
		}
	}
	if c == nil {
		if len(d.convs) >= 0xfff {
			return nil
		}
		c = &cmdconv{x: len(d.convs)}
		c.mu.Lock()
		d.convs = append(d.convs, c)
	}
	c.inuse, c.owner, c.dir, c.perm, c.state = 1, u, d.root, 0o660, "Closed"
	c.stdin, c.stdout, c.stderr = nil, nil, nil
	c.count = [3]int{}
	c.mu.Unlock()
	return c
}

func (d *DevCmd) Open(ctx context.Context, c *core.Chan, mode int) (*core.Chan, error) {
	perm := [4]uint32{4, 2, 6, 4}[mode&3]
	mode &= 3
	switch cmdtype(c.Qid) {
	case cmdQtopdir, cmdQcmd, cmdQconvdir, cmdQstatus:
		if mode != abi.OREAD {
			return nil, core.Error(abi.Eperm)
		}
	case cmdQclone:
		cv := d.clone(progUser(ctx, d.k))
		if cv == nil {
			return nil, core.Error(abi.Enodev)
		}
		c.Qid = styx.Qid{Path: cmdqid(cv.x, cmdQctl)}
	case cmdQdata, cmdQstderr, cmdQctl, cmdQwait:
		cv := d.conv(c.Qid)
		if cv == nil {
			return nil, core.Error(abi.Enonexist)
		}
		cv.mu.Lock()
		defer cv.mu.Unlock()
		u := progUser(ctx, d.k)
		if perm&(cv.perm>>6) != perm && (u != cv.owner || perm&cv.perm != perm) {
			return nil, core.Error(abi.Eperm)
		}
		switch cmdtype(c.Qid) {
		case cmdQdata:
			if mode == abi.OWRITE || mode == abi.ORDWR {
				cv.count[0]++
			}
			if mode == abi.OREAD || mode == abi.ORDWR {
				cv.count[1]++
			}
		case cmdQstderr:
			if mode != abi.OREAD {
				return nil, core.Error(abi.Eperm)
			}
			cv.count[2]++
		case cmdQwait:
			if cv.waitq == nil {
				cv.waitq = core.NewMsgQueue(1024)
			}
		}
		cv.inuse++
		if cv.inuse == 1 {
			cv.state, cv.owner, cv.perm, cv.nice = "Open", u, 0o660, 0
		}
	}
	return c, nil
}

func (cv *cmdconv) fdclose(i int) {
	cv.count[i]--
	if cv.count[i] > 0 {
		return
	}
	f := []**os.File{&cv.stdin, &cv.stdout, &cv.stderr}[i]
	if *f != nil {
		(*f).Close()
		*f = nil
	}
}

func (cv *cmdconv) reset(root string) {
	for _, f := range []**os.File{&cv.stdin, &cv.stdout, &cv.stderr} {
		if *f != nil {
			(*f).Close()
			*f = nil
		}
	}
	cv.owner, cv.dir, cv.perm, cv.state = "cmd", root, 0o666, "Closed"
	cv.killonclose, cv.killed, cv.nice = false, false, 0
	cv.args = nil
	if cv.waitq != nil {
		cv.waitq.Hangup()
		cv.waitq = nil
	}
}

func (d *DevCmd) Close(c *core.Chan) {
	t := cmdtype(c.Qid)
	if t != cmdQctl && t != cmdQdata && t != cmdQstderr && t != cmdQwait {
		return
	}
	cv := d.conv(c.Qid)
	if cv == nil {
		return
	}
	cv.mu.Lock()
	defer cv.mu.Unlock()
	switch t {
	case cmdQdata:
		if c.Mode&3 == abi.OWRITE || c.Mode&3 == abi.ORDWR {
			cv.fdclose(0)
		}
		if c.Mode&3 == abi.OREAD || c.Mode&3 == abi.ORDWR {
			cv.fdclose(1)
		}
	case cmdQstderr:
		cv.fdclose(2)
	}
	cv.inuse--
	if cv.child != nil {
		if !cv.killed && (cv.inuse == 0 || (cv.killonclose && t == cmdQctl)) {
			_ = cv.child.Process.Kill()
			cv.killed = true
		}
	} else if cv.inuse == 0 {
		cv.reset(d.root)
	}
}

// pipeRead reads a host pipe, ending early on kill or interrupt.
func pipeRead(ctx context.Context, f *os.File, buf []byte) (int, error) {
	n := core.NoteFrom(ctx)
	if n != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-n.Dead():
				_ = f.SetReadDeadline(time.Now())
			case <-done:
			}
		}()
	}
	k, err := f.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			_ = f.SetReadDeadline(time.Time{})
			return 0, core.Error(abi.Eintr)
		}
		return 0, core.Error(err.Error())
	}
	return k, nil
}

func (d *DevCmd) Read(ctx context.Context, ch *core.Chan, buf []byte, off int64) (int, error) {
	switch cmdtype(ch.Qid) {
	case cmdQctl:
		return core.ReadStr([]byte(strconv.Itoa(cmdconvn(ch.Qid))), buf, off), nil
	case cmdQstatus:
		c := d.conv(ch.Qid)
		c.mu.Lock()
		cmds := ""
		if len(c.args) > 0 {
			cmds = c.args[0]
		}
		s := fmt.Sprintf("cmd/%d %d %s %s %s\n", c.x, c.inuse, c.state, core.Quote(c.dir), core.Quote(cmds))
		c.mu.Unlock()
		return core.ReadStr([]byte(s), buf, off), nil
	case cmdQdata, cmdQstderr:
		c := d.conv(ch.Qid)
		c.mu.Lock()
		f := c.stdout
		if cmdtype(ch.Qid) == cmdQstderr {
			f = c.stderr
		}
		c.mu.Unlock()
		if f == nil {
			return 0, nil
		}
		return pipeRead(ctx, f, buf)
	case cmdQwait:
		c := d.conv(ch.Qid)
		c.mu.Lock()
		q := c.waitq
		c.mu.Unlock()
		return q.Read(ctx, buf)
	}
	return 0, core.Error(abi.Eperm)
}

func (d *DevCmd) Write(ctx context.Context, ch *core.Chan, buf []byte, _ int64) (int, error) {
	switch cmdtype(ch.Qid) {
	case cmdQctl:
		c := d.conv(ch.Qid)
		f := tokenize(strings.TrimRight(string(buf), "\n"))
		if len(f) == 0 {
			return 0, core.Error(abi.Ecmdargs)
		}
		switch f[0] {
		case "dir":
			if len(f) != 2 {
				return 0, core.Error(abi.Ecmdargs)
			}
			c.mu.Lock()
			c.dir = f[1]
			c.mu.Unlock()
		case "exec":
			if err := d.exec(c, f[1:]); err != nil {
				return 0, err
			}
		case "kill":
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.child == nil {
				return 0, core.Error("not started")
			}
			if err := c.child.Process.Kill(); err != nil {
				return 0, core.Error(err.Error())
			}
		case "nice":
			c.mu.Lock()
			c.nice = 1
			if len(f) > 1 {
				c.nice, _ = strconv.Atoi(f[1])
			}
			c.mu.Unlock()
		case "killonclose":
			c.mu.Lock()
			c.killonclose = true
			c.mu.Unlock()
		default:
			return 0, core.Error(abi.Enoctl)
		}
		return len(buf), nil
	case cmdQdata:
		c := d.conv(ch.Qid)
		c.mu.Lock()
		f := c.stdin
		c.mu.Unlock()
		if f == nil {
			return 0, core.Error(abi.Ehungup)
		}
		n, err := f.Write(buf)
		if n == 0 && err != nil {
			return 0, core.Error(abi.Ehungup)
		}
		return n, nil
	}
	return 0, core.Error(abi.Eperm)
}

// exec starts the command, as cmdproc and oscmd do: no shell, in the
// conversation's directory, with three pipes.
func (d *DevCmd) exec(c *cmdconv, args []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.child != nil || c.args != nil || c.stdin != nil || c.stdout != nil || c.stderr != nil {
		return core.Error(abi.Einuse)
	}
	if len(args) < 1 {
		return core.Error(abi.Etoosmall)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = c.dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	inR, inW, err1 := os.Pipe()
	outR, outW, err2 := os.Pipe()
	errR, errW, err3 := os.Pipe()
	if err := errors.Join(err1, err2, err3); err != nil {
		return core.Error(err.Error())
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	c.state = "Execute"
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			f.Close()
		}
		c.state = "Done"
		var pe *exec.Error
		if errors.As(err, &pe) {
			return core.Error(abi.Enonexist)
		}
		return core.Error(err.Error())
	}
	inR.Close()
	outW.Close()
	errW.Close()
	c.stdin, c.stdout, c.stderr = inW, outR, errR
	c.args, c.child = args, cmd
	go d.waitfor(c, cmd)
	return nil
}

func (d *DevCmd) waitfor(c *cmdconv, cmd *exec.Cmd) {
	err := cmd.Wait()
	pid := cmd.Process.Pid
	var status string
	ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
	switch {
	case err == nil:
		status = fmt.Sprintf("%d 0 0 0 ''", pid)
	case ws.Exited():
		status = fmt.Sprintf("%d 0 0 0 'exit: %d'", pid, ws.ExitStatus())
	case ws.Signaled() && ws.Signal() == syscall.SIGKILL:
		status = fmt.Sprintf("%d 0 0 0 killed", pid)
	case ws.Signaled():
		status = fmt.Sprintf("%d 0 0 0 'signal: %d'", pid, int(ws.Signal()))
	default:
		status = fmt.Sprintf("%d 0 0 0 'odd status: 0x%x'", pid, int(ws))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.child = nil
	if c.inuse > 0 {
		c.state = "Done"
		if c.waitq != nil {
			c.waitq.Produce([]byte(status))
		}
	} else {
		c.reset(d.root)
	}
}

// tokenize splits a ctl message as Inferno's parsecmd does, honoring
// single quotes.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	in, have := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'':
			if in && i+1 < len(s) && s[i+1] == '\'' {
				cur.WriteByte('\'')
				i++
			} else {
				in = !in
			}
			have = true
		case !in && (ch == ' ' || ch == '\t' || ch == '\n'):
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(ch)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}
