package core

import (
	"strconv"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
)

func ctlWrite(p *Prog, pid int, msg string) error {
	fd, err := p.Open("#p/"+strconv.Itoa(pid)+"/ctl", abi.OWRITE)
	if err != nil {
		return err
	}
	defer p.Close(fd)
	_, err = p.Write(fd, []byte(msg))
	return err
}

func wantErr(t *testing.T, what string, err error, want string) {
	t.Helper()
	if want == "" && err != nil || want != "" && (err == nil || Errstr(err) != want) {
		t.Fatalf("%s: got %v, want %q", what, err, want)
	}
}

func TestParseSize(t *testing.T) {
	for s, want := range map[string]uint64{"0": 0, "4096": 4096, "4k": 4096, "2M": 2 << 20, "1g": 1 << 30} {
		if n, ok := ParseSize(s); !ok || n != want {
			t.Errorf("%q: got %d %v", s, n, ok)
		}
	}
	for _, s := range []string{"", "k", "4q", "-1", "1.5m", "99999999999999999999", "17179869184g"} {
		if _, ok := ParseSize(s); ok {
			t.Errorf("%q parsed", s)
		}
	}
}

func TestMemlimit(t *testing.T) {
	p := newTestProg(t)
	if got := MemLine(p); got != "0 -\n" {
		t.Fatalf("mem %q", got)
	}
	wantErr(t, "memlimit 4k", ctlWrite(p, p.Pid, "memlimit 4k"), "")
	wantErr(t, "raise", ctlWrite(p, p.Pid, "memlimit 8k"), abi.Eperm)
	for _, bad := range []string{"memlimit", "memlimit x", "memlimit 4q", "memlimit 1 2", "domain memlimit 1k"} {
		wantErr(t, bad, ctlWrite(p, p.Pid, bad), abi.Ebadarg)
	}
	if !p.ChargeMem(4000) || p.ChargeMem(97) || !p.ChargeMem(96) {
		t.Fatal("charges within and past the limit")
	}
	if got := MemLine(p); got != "4096 4096\n" {
		t.Fatalf("mem %q", got)
	}
	p.UnchargeMem(4096)
	c, err := p.Spawn(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Exit("")
	if c.Memlimit() != 4096 {
		t.Fatalf("child limit %d", c.Memlimit())
	}
}

func TestDomains(t *testing.T) {
	p := newTestProg(t)
	k := p.Kernel
	same, _ := p.Spawn(0, nil)            // in p's group: joins
	other, _ := p.Spawn(abi.NEWPGRP, nil) // another group: does not
	defer same.Exit("")
	defer other.Exit("")
	if err := p.Bind("#pdomain", "/a", abi.MREPL); err != nil {
		t.Fatal(err)
	}
	if p.Domain() != p.Pid || same.Domain() != p.Pid || other.Domain() != 0 {
		t.Fatalf("domains %v %v %v", p.Domains(), same.Domains(), other.Domains())
	}
	late, _ := other.Spawn(0, nil) // not a member's child
	defer late.Exit("")
	kid, _ := same.Spawn(abi.NEWPGRP, nil) // a member's child, in any group
	if !kid.InDomain(p.Pid) || late.Domain() != 0 {
		t.Fatal("inheritance")
	}
	want := strconv.Itoa(p.Pid) + " " + strconv.Itoa(same.Pid) + " " + strconv.Itoa(kid.Pid)
	if got := names(t, p, "/a"); got != want {
		t.Fatalf("view lists %q, want %q", got, want)
	}
	if got := names(t, p, "#pdomain="+strconv.Itoa(p.Pid)); got != want {
		t.Fatalf("#pdomain=N lists %q, want %q", got, want)
	}
	if _, err := p.Open("/a/"+strconv.Itoa(other.Pid)+"/status", abi.OREAD); err == nil || Errstr(err) != abi.Enonexist {
		t.Fatalf("outside the view: %v", err)
	}
	if _, err := p.Open("#pbad", abi.OREAD); err == nil || Errstr(err) != abi.Ebadarg {
		t.Fatalf("bad spec: %v", err)
	}

	// A nested domain, founded by the member kid leads.
	kid.SetPgrp(p.Pgrp())
	if _, err := kid.Open("#pdomain", abi.OREAD); err != nil {
		t.Fatal(err)
	}
	if d := kid.Domains(); len(d) != 2 || d[0] != p.Pid || d[1] != kid.Pid {
		t.Fatalf("nested %v", d)
	}

	// Limits apply to the innermost domain, and to every outer one.
	wantErr(t, "proglimit", ctlWrite(p, p.Pid, "domain proglimit 3"), "")
	wantErr(t, "raise", ctlWrite(p, p.Pid, "domain proglimit 4"), abi.Eperm)
	if _, err := kid.Spawn(0, nil); err == nil || Errstr(err) != abi.Enoproc {
		t.Fatalf("spawn past the prog limit: %v", err)
	}
	kid.Exit("")
	c, err := same.Spawn(0, nil)
	if err != nil {
		t.Fatalf("spawn within the prog limit: %v", err)
	}
	defer c.Exit("")
	wantErr(t, "memlimit", ctlWrite(p, p.Pid, "domain memlimit 1k"), "")
	if !p.ChargeMem(600) || same.ChargeMem(500) || !same.ChargeMem(424) {
		t.Fatal("domain memory limit")
	}
	if n, mem := k.Progs.DomainUsage(p.Pid); n != 3 || mem != 1024 {
		t.Fatalf("usage %d %d", n, mem)
	}

	// killgrp through the view kills only the group's members, and
	// spares the writer (prog(3)).
	fd, err := p.Open("/a/"+strconv.Itoa(p.Pid)+"/ctl", abi.OWRITE)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(fd, []byte("killgrp")); err != nil {
		t.Fatal(err)
	}
	if !same.Note.Killed() || p.Note.Killed() || other.Note.Killed() {
		t.Fatal("killgrp")
	}
}

func TestSrvDomains(t *testing.T) {
	p := newTestProg(t)
	k := p.Kernel
	srv := NewDevSrv(k)
	k.DevTab = NewDevTab(append(k.DevTab.Devs(), srv)...)
	in, _ := p.Spawn(abi.NEWPGRP|abi.FORKNS, nil)
	defer in.Exit("")
	if _, err := in.Open("#pdomain", abi.OREAD); err != nil {
		t.Fatal(err)
	}
	attach := func(q *Prog) any {
		w, err := srv.Attach(q.Ctx, "chan")
		if err != nil {
			t.Fatal(err)
		}
		return w.Aux
	}
	outside, inside := attach(p), attach(in)
	if outside == inside || attach(p) != outside || attach(in) != inside {
		t.Fatal("each domain has its own #s specs")
	}
}

// TestNodev: nodev forbids the name space, and copies made of it
// afterwards, to attach the devices named, NODEVS or not.
func TestNodev(t *testing.T) {
	p := newTestProg(t)
	if _, err := p.Stat("#e"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "nodev", ctlWrite(p, p.Pid, "nodev e"), "")
	_, err := p.Stat("#e")
	wantErr(t, "#e", err, abi.Enoattach)
	c, err := p.Spawn(abi.FORKNS, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Stat("#e")
	wantErr(t, "#e in a copy", err, abi.Enoattach)
	if _, err := p.Stat("#c"); err != nil {
		t.Errorf("#c: %v", err)
	}
	if err := ctlWrite(p, p.Pid, "nodev"); err == nil {
		t.Error("nodev with no device")
	}
}
