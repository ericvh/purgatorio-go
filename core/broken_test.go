package core

import (
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
)

// A prog that trapped is left broken until killed; its parent hears of
// it at once and again at the kill. A prog that did not trap exits.
func TestEndBroken(t *testing.T) {
	p := newTestProg(t)
	q := p.OpenWait()
	c, err := p.Spawn(abi.NEWFD, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.SetName("c")
	c.Trapped(nil, []Frame{{Func: 3, Path: "/c.wasm"}}, nil)
	done := make(chan struct{})
	go func() { c.End("trap"); close(done) }()
	buf := make([]byte, 128)
	n, err := q.Read(p.Ctx, buf)
	if err != nil || string(buf[:n]) != `2 "c":trap` {
		t.Fatalf("wait: %q, %v", buf[:n], err)
	}
	if s := c.State(); s != "broken" {
		t.Errorf("state %q", s)
	}
	if s, ok := c.Stack(); !ok || s != "00000000 00000003 00000000 00000000 1 /c.wasm\n" {
		t.Errorf("stack %q %v", s, ok)
	}
	if p.Kernel.Progs.Get(c.Pid) != c {
		t.Error("broken prog left the table")
	}
	c.Kill()
	if p.Kernel.Progs.Get(c.Pid) != nil {
		t.Error("killed prog still in the table")
	}
	n, err = q.Read(p.Ctx, buf)
	if err != nil || string(buf[:n]) != `2 "c":killed` {
		t.Fatalf("wait after kill: %q, %v", buf[:n], err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("End did not return after the kill")
	}

	c, _ = p.Spawn(abi.NEWFD, nil)
	c.SetName("d")
	c.End("fail:x")
	n, err = q.Read(p.Ctx, buf)
	if err != nil || string(buf[:n]) != `3 "d":fail:x` || p.Kernel.Progs.Get(c.Pid) != nil {
		t.Fatalf("fail: exit: %q, %v", buf[:n], err)
	}
}

// exceptions propagate on the leader: a member's fail: status ends the
// rest of the group, leader included; a group without it is untouched.
func TestPropex(t *testing.T) {
	leader := newTestProg(t)
	a, _ := leader.Spawn(abi.NEWFD, nil)
	leader.mu.Lock()
	leader.exflags = exPropagate
	leader.mu.Unlock()
	b, _ := leader.Spawn(abi.NEWFD, nil)
	c, _ := leader.Spawn(abi.NEWFD, nil)
	other, _ := leader.Spawn(abi.NEWFD|abi.NEWPGRP, nil)
	// a was started before the setting: its end passes nothing on.
	a.End("fail:a")
	if leader.Note.Killed() || b.Note.Killed() {
		t.Fatal("a prog without the setting propagated")
	}
	b.End("fail:b")
	if !c.Note.Killed() || !leader.Note.Killed() {
		t.Error("propagate did not reach the group")
	}
	if other.Note.Killed() {
		t.Error("propagate reached another group")
	}
}
