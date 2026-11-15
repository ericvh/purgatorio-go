package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// The files of a /prog/n directory, in Inferno's order.
const (
	qprogdir = iota
	qctl
	qdbgctl
	qheap
	qns
	qnsgrp
	qpgrp
	qstack
	qstatus
	qtext
	qwait
	qfd
	qexception
	qmem
	qmodule
)

var progtab = []Tab{
	{"ctl", qctl, 0o200, 0},
	{"dbgctl", qdbgctl, 0o600, 0},
	{"heap", qheap, 0o600, 0},
	{"ns", qns, 0o400, 0},
	{"nsgrp", qnsgrp, 0o444, 0},
	{"pgrp", qpgrp, 0o444, 0},
	{"stack", qstack, 0o400, 0},
	{"status", qstatus, 0o444, 0},
	{"text", qtext, 0o400, 0}, // 0000 in Inferno, where text is unimplemented
	{"wait", qwait, 0o400, 0},
	{"fd", qfd, 0o400, 0},
	{"exception", qexception, 0o400, 0},
	{"mem", qmem, 0o444, 0},
	{"module", qmodule, 0o444, 0},
}

// qshift puts the file type in the low bits of the qid path; the pid is in
// the qid version, as Inferno's devprog does.
const qshift = 4

// DevProg is #p, the prog device (devprog.c): a directory per prog with
// its status, name space, fds, wait file and ctl file.
//
// #pdomain is a view of the table limited to one prog domain, which
// Inferno does not have (docs/abi.md). Attaching it founds a domain for
// the attaching prog's process group; #pdomain=N is the view of domain N
// without joining it. Pids outside the view do not exist in it, and
// killgrp kills only the group's members. The view is the Chan's Aux.
type DevProg struct {
	DevBase
	k *Kernel
}

func NewDevProg(k *Kernel) *DevProg { return &DevProg{k: k} }

func (d *DevProg) DC() rune     { return 'p' }
func (d *DevProg) Name() string { return "prog" }

func qtype(q styx.Qid) uint64 { return q.Path & (1<<qshift - 1) }
func qpid(q styx.Qid) int     { return int(q.Vers) }

// progView is the domain a #pdomain Chan shows.
type progView struct{ id int }

// Devno keeps each view's mount-table key apart from #p's.
func (v *progView) Devno() uint32 { return uint32(v.id) + 1 }

func (d *DevProg) Attach(ctx context.Context, spec string) (Walked, error) {
	var view *progView
	switch {
	case spec == "":
	case spec == "domain":
		p := ProgFrom(ctx)
		if p == nil {
			return Walked{}, Error(abi.Ebadarg)
		}
		view = &progView{d.k.Progs.FoundDomain(p)}
	case strings.HasPrefix(spec, "domain="):
		id, err := strconv.ParseUint(spec[len("domain="):], 10, 31)
		if err != nil {
			return Walked{}, Error(abi.Ebadarg)
		}
		view = &progView{int(id)}
	default:
		return Walked{}, Error(abi.Ebadarg)
	}
	w := Walked{Qid: styx.Qid{Type: styx.QTDIR}}
	if view != nil {
		w.Aux = view
	}
	return w, nil
}

// get returns prog pid if it is in c's view.
func (d *DevProg) get(c *Chan, pid int) *Prog {
	p := d.k.Progs.Get(pid)
	if p == nil {
		return nil
	}
	if v, ok := c.Aux.(*progView); ok && !p.InDomain(v.id) {
		return nil
	}
	return p
}

func (d *DevProg) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	if !c.IsDir() {
		return Walked{}, Error(abi.Enotdir)
	}
	if c.Qid.Vers == 0 && c.Qid.Path == 0 { // the top directory
		pid, err := strconv.Atoi(name)
		if err != nil || d.get(c, pid) == nil {
			return Walked{}, Error(abi.Enonexist)
		}
		return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: uint64(pid) << qshift, Vers: uint32(pid)}, Aux: c.Aux}, nil
	}
	w, err := TabWalk(progtab, name)
	if err != nil {
		return w, err
	}
	w.Aux = c.Aux
	pid := qpid(c.Qid)
	w.Qid.Path |= uint64(pid) << qshift
	w.Qid.Vers = uint32(pid)
	return w, nil
}

func (d *DevProg) owner(pid int) string {
	if p := d.k.Progs.Get(pid); p != nil {
		return p.User()
	}
	return d.k.Owner
}

func (d *DevProg) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	if c.Qid.Vers == 0 && c.Qid.Path == 0 {
		return styx.Dir{Type: 'p', Qid: c.Qid, Mode: styx.DMDIR | 0o555, Name: "#p",
			UID: d.k.Owner, GID: d.k.Owner}, nil
	}
	pid := qpid(c.Qid)
	if qtype(c.Qid) == qprogdir {
		u := d.owner(pid)
		return styx.Dir{Type: 'p', Qid: c.Qid, Mode: styx.DMDIR | 0o555, Name: strconv.Itoa(pid),
			UID: u, GID: u}, nil
	}
	for _, t := range progtab {
		if t.Path == qtype(c.Qid) {
			dir := TabDir('p', t, d.owner(pid))
			dir.Qid = c.Qid
			return dir, nil
		}
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevProg) DirRead(ctx context.Context, c *Chan) ([]styx.Dir, error) {
	var out []styx.Dir
	if c.Qid.Vers == 0 && c.Qid.Path == 0 {
		for _, pid := range d.k.Progs.Pids() {
			if d.get(c, pid) == nil {
				continue
			}
			u := d.owner(pid)
			out = append(out, styx.Dir{Type: 'p', Mode: styx.DMDIR | 0o555, Name: strconv.Itoa(pid),
				Qid: styx.Qid{Type: styx.QTDIR, Path: uint64(pid) << qshift, Vers: uint32(pid)}, UID: u, GID: u})
		}
		return out, nil
	}
	pid := qpid(c.Qid)
	for _, t := range progtab {
		dir := TabDir('p', t, d.owner(pid))
		dir.Qid.Path |= uint64(pid) << qshift
		dir.Qid.Vers = uint32(pid)
		out = append(out, dir)
	}
	return out, nil
}

// progopen is the per-open state of ns, wait, heap and dbgctl.
type progopen struct {
	lines []string // ns
	wait  *Queue
	prog  *Prog
	heap  *heapqry
	dbg   *DbgCtl
}

func (d *DevProg) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, Error(abi.Eisdir)
		}
		return c, nil
	}
	p := d.get(c, qpid(c.Qid))
	if p == nil {
		return nil, Error(abi.Ethread)
	}
	t := qtype(c.Qid)
	for _, e := range progtab {
		if e.Path == t {
			if err := OpenPerm(e.Perm, mode); err != nil {
				return nil, err
			}
		}
	}
	switch t {
	case qwait:
		c.State = &progopen{wait: p.OpenWait(), prog: p}
	case qns:
		ns := p.Pgrp().Namespace()
		c.State = &progopen{lines: strings.SplitAfter(strings.TrimSuffix(ns, "\n"), "\n")}
	case qheap, qdbgctl:
		if mode&abi.OACCESS != abi.ORDWR || d.k.Progs.Private(p.Group()) {
			return nil, Error(abi.Eperm)
		}
		if t == qheap {
			c.State = &progopen{prog: p, heap: &heapqry{}}
		} else {
			c.State = &progopen{prog: p, dbg: newDbgCtl()}
		}
	}
	return c, nil
}

func (d *DevProg) Close(c *Chan) {
	if o, ok := c.State.(*progopen); ok && o.wait != nil {
		o.prog.CloseWait(o.wait)
		o.wait.Hangup()
	}
	if o, ok := c.State.(*progopen); ok && o.dbg != nil {
		o.prog.dbgclose(o.dbg)
	}
}

// progtime formats a running time as Inferno's progtime does.
func progtime(ms int64) string {
	tenths := ms / 100
	sec := tenths / 10
	return fmt.Sprintf("%4d:%2.2d.%d", sec/60, sec%60, tenths%10)
}

// MemLine is the text of /prog/n/mem: the wasm memory the prog holds and
// its limit, - for none.
func MemLine(p *Prog) string {
	l := "-"
	if m := p.Memlimit(); m != Unlimited {
		l = strconv.FormatUint(m, 10)
	}
	return fmt.Sprintf("%d %s\n", p.Mem(), l)
}

// StatusLine is the text of /prog/n/status.
func StatusLine(p *Prog) string {
	return fmt.Sprintf("%8d %8d %10s %s %10s %5dK %s",
		p.Pid, p.Group(), p.User(), progtime(p.Elapsed().Milliseconds()), p.State(), 0, p.Name())
}

func (d *DevProg) Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	pid := qpid(c.Qid)
	p := d.get(c, pid)
	switch qtype(c.Qid) {
	case qmodule:
		if p == nil {
			return 0, Error(abi.Ethread)
		}
		return ReadStr([]byte(moduleText(p)), buf, off), nil
	case qstatus:
		if p == nil {
			s := fmt.Sprintf("%8d %8d %10s %s %10s %5dK %s", pid, 0, d.k.Owner, progtime(0), "exiting", 0, "[$Sys]")
			return ReadStr([]byte(s), buf, off), nil
		}
		return ReadStr([]byte(StatusLine(p)), buf, off), nil
	case qwait:
		return c.State.(*progopen).wait.Read(ctx, buf)
	case qdbgctl:
		return c.State.(*progopen).dbg.q.Read(ctx, buf)
	case qheap:
		s, err := c.State.(*progopen).heap.read(c.State.(*progopen).prog)
		if err != nil {
			return 0, err
		}
		return ReadStr([]byte(s), buf, off), nil
	case qns:
		o := c.State.(*progopen)
		if len(o.lines) == 0 {
			return 0, nil
		}
		n := copy(buf, o.lines[0])
		o.lines = o.lines[1:]
		return n, nil
	}
	if p == nil {
		return 0, Error(abi.Ethread)
	}
	switch qtype(c.Qid) {
	case qnsgrp:
		return ReadStr([]byte(fmt.Sprintf("%11d ", p.Pid)), buf, off), nil
	case qpgrp:
		return ReadStr([]byte(fmt.Sprintf("%11d ", p.Group())), buf, off), nil
	case qfd:
		var s strings.Builder
		s.WriteString(p.Dot() + "\n")
		f := p.Fgrp()
		for _, fd := range f.Fds() {
			if ch, err := f.Get(fd); err == nil {
				s.WriteString(FdLine(ch, fd))
			}
		}
		return ReadStr([]byte(s.String()), buf, off), nil
	case qexception:
		return ReadStr([]byte(p.Exception()), buf, off), nil
	case qtext:
		return ReadStr([]byte(p.Text()), buf, off), nil
	case qstack:
		if p.State() == "exiting" {
			return 0, Error(abi.Ethread)
		}
		s, ok := p.Stack()
		if !ok {
			return 0, Error(abi.Estopped)
		}
		return ReadStr([]byte(s), buf, off), nil
	case qmem:
		return ReadStr([]byte(MemLine(p)), buf, off), nil
	}
	return 0, Error(abi.Eperm)
}

func (d *DevProg) Write(ctx context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	if c.IsDir() {
		return 0, Error(abi.Eisdir)
	}
	p := d.get(c, qpid(c.Qid))
	if p == nil {
		return 0, Error(abi.Ethread)
	}
	var err error
	switch qtype(c.Qid) {
	case qctl:
		err = d.ctl(ctx, c, p, ParseCmd(buf))
	case qdbgctl:
		err = p.dbgctl(ctx, c.State.(*progopen).dbg, ParseCmd(buf))
	case qheap:
		err = c.State.(*progopen).heap.parse(buf)
	default:
		err = Error(abi.Egreg)
	}
	if err != nil {
		return 0, err
	}
	return len(buf), nil
}

const (
	cmkill = iota
	cmkillgrp
	cmrestricted
	cmexceptions
	cmprivate
	cmmemlimit
	cmdomain
	cmmodules
	cmnodev
)

var progcmd = []Cmdtab{
	{cmkill, "kill", 1},
	{cmkillgrp, "killgrp", 1},
	{cmrestricted, "restricted", 1},
	{cmexceptions, "exceptions", 2},
	{cmprivate, "private", 1},
	{cmmemlimit, "memlimit", 0}, // limits.go checks the arguments
	{cmdomain, "domain", 0},
	{cmmodules, "modules", 0}, // modsig.go checks the arguments
	{cmnodev, "nodev", 2},
}

// ctl carries out a message written to /prog/n/ctl.
func (d *DevProg) ctl(ctx context.Context, c *Chan, p *Prog, f []string) error {
	ct, err := LookupCmd(f, progcmd)
	if err != nil {
		return err
	}
	switch ct.Index {
	case cmkill:
		p.Kill()
	case cmkillgrp:
		// The writer survives, as in Inferno's killgrp; a #pdomain
		// view kills only the group's members in the domain.
		me := ProgFrom(ctx)
		v, inview := c.Aux.(*progView)
		g := p.Group()
		for _, pid := range d.k.Progs.Pids() {
			if q := d.k.Progs.Get(pid); q != nil && q != me && q.Group() == g && (!inview || q.InDomain(v.id)) {
				q.Kill()
			}
		}
	case cmrestricted:
		p.dbg.mu.Lock()
		p.dbg.restrict = true
		p.dbg.mu.Unlock()
	case cmexceptions:
		if p.Group() != p.Pid {
			return Error(abi.Eperm)
		}
		switch f[1] {
		case "propagate":
			p.mu.Lock()
			p.exflags |= exPropagate
			p.mu.Unlock()
		case "notifyleader":
			p.mu.Lock()
			p.exflags |= exNotifyLeader
			p.mu.Unlock()
		default:
			return Error(abi.Ebadctl)
		}
	case cmprivate:
		d.k.Progs.SetPrivate(p.Group())
	case cmmemlimit, cmdomain:
		return d.k.Progs.limitCtl(p, f)
	case cmmodules:
		return moduleCtl(p, f)
	case cmnodev:
		// Not Inferno: forbid this prog's name space, and the copies
		// made of it afterwards, to attach the devices named, as the
		// Rust sibling's nodev does (docs/abi.md).
		if f[1] == "" {
			return Error(abi.Ebadctl)
		}
		p.Pgrp().DenyDevs(f[1])
	}
	return nil
}

func (d *DevProg) Ready(c *Chan, write bool) bool {
	if o, ok := c.State.(*progopen); ok && o.wait != nil && !write {
		return o.wait.Readable()
	}
	return true
}

func (d *DevProg) Watch(c *Chan, _ bool, r *Rendez) {
	if o, ok := c.State.(*progopen); ok && o.wait != nil {
		o.wait.Watch(r)
	}
}

func (d *DevProg) Unwatch(c *Chan, r *Rendez) {
	if o, ok := c.State.(*progopen); ok && o.wait != nil {
		o.wait.Unwatch(r)
	}
}
