package core

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/styx"
)

type testPlat struct {
	mu  sync.Mutex
	in  *bytes.Reader
	out bytes.Buffer
}

func (*testPlat) Now() int64       { return 1e18 }
func (*testPlat) Entropy(b []byte) {}
func (t *testPlat) Console() platform.Console {
	return tcons{t}
}
func (*testPlat) User() string    { return "inferno" }
func (*testPlat) Sysname() string { return "test" }

type tcons struct{ t *testPlat }

func (c tcons) Read(b []byte) (int, error) {
	c.t.mu.Lock()
	defer c.t.mu.Unlock()
	if c.t.in == nil {
		return 0, nil
	}
	return c.t.in.Read(b)
}
func (c tcons) Write(b []byte) (int, error) {
	c.t.mu.Lock()
	defer c.t.mu.Unlock()
	return c.t.out.Write(b)
}
func (tcons) SetRaw(bool) error { return nil }

// newTestProg boots a kernel whose root is a static tree with a few
// directories and files.
func newTestProg(t *testing.T) *Prog {
	t.Helper()
	k := &Kernel{Platform: &testPlat{}, Progs: NewProgs(), Owner: "inferno"}
	root := NewRoot("inferno")
	root.AddDir("a/x")
	root.AddDir("b/y")
	root.AddFile("a/f", []byte("in a"))
	root.AddFile("b/g", []byte("in b"))
	k.DevTab = NewDevTab(root, NewDevCons(k), NewDevEnv(k), NewDevPipe(k), NewDevDup(k), NewDevProg(k))
	rc, err := k.DevTab.Attach(context.Background(), '/', "")
	if err != nil {
		t.Fatal(err)
	}
	return NewProg(k, NewPgrp(k.DevTab, rc), "inferno")
}

func names(t *testing.T, p *Prog, path string) string {
	t.Helper()
	fd, err := p.Open(path, abi.OREAD)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer p.Close(fd)
	ds, err := p.Dirents(fd)
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, d := range ds {
		s = append(s, d.Name)
	}
	return strings.Join(s, " ")
}

func TestBindUnion(t *testing.T) {
	p := newTestProg(t)
	if err := p.Bind("/b", "/a", abi.MBEFORE); err != nil {
		t.Fatal(err)
	}
	if got := names(t, p, "/a"); got != "y g x f" {
		t.Errorf("union before: %q", got)
	}
	// A name in either member resolves.
	if _, err := p.Stat("/a/g"); err != nil {
		t.Errorf("stat /a/g: %v", err)
	}
	if _, err := p.Stat("/a/f"); err != nil {
		t.Errorf("stat /a/f: %v", err)
	}
	if err := p.Unmount("/b", "/a"); err != nil {
		t.Fatal(err)
	}
	if got := names(t, p, "/a"); got != "x f" {
		t.Errorf("after unmount: %q", got)
	}
	if err := p.Unmount("", "/a"); core_errstr(err) != abi.Eunmount {
		t.Errorf("unmount again: %v", err)
	}
}

func core_errstr(err error) string {
	if err == nil {
		return ""
	}
	return Errstr(err)
}

func TestBindReplaceAndNs(t *testing.T) {
	p := newTestProg(t)
	if err := p.Bind("/b", "/a", abi.MREPL); err != nil {
		t.Fatal(err)
	}
	if got := names(t, p, "/a"); got != "y g" {
		t.Errorf("replace: %q", got)
	}
	if err := p.Bind("#c", "/dev", abi.MAFTER); err != nil {
		t.Fatal(err)
	}
	ns := p.Pgrp().Namespace()
	want := "bind  /b /a\nbind -a #c /dev\ncd /\n"
	if ns != want {
		t.Errorf("ns:\n%s\nwant:\n%s", ns, want)
	}
	if err := p.Bind("/b/g", "/b/y", abi.MREPL); core_errstr(err) != abi.Eismount {
		t.Errorf("file on dir: %v", err)
	}
}

func TestChdirAndDotdot(t *testing.T) {
	p := newTestProg(t)
	if err := p.Chdir("/a/x"); err != nil {
		t.Fatal(err)
	}
	if p.Dot() != "/a/x" {
		t.Errorf("dot %q", p.Dot())
	}
	if _, err := p.Stat("../f"); err != nil {
		t.Errorf("../f: %v", err)
	}
	if err := p.Chdir("/a/f"); core_errstr(err) != abi.Enotdir {
		t.Errorf("chdir to file: %v", err)
	}
	if _, err := p.Stat("/nope"); core_errstr(err) != abi.Enonexist {
		t.Errorf("stat missing: %v", err)
	}
	if _, err := p.Stat("/a/f/z"); core_errstr(err) != abi.Enotdir {
		t.Errorf("walk below file: %v", err)
	}
}

func TestPipe(t *testing.T) {
	p := newTestProg(t)
	fds, err := p.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(fds[0], []byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	n, err := p.Read(fds[1], buf)
	if err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("read %q %v", buf[:n], err)
	}
	p.Close(fds[0])
	if n, err := p.Read(fds[1], buf); n != 0 || err != nil {
		t.Errorf("after close: %d %v", n, err)
	}
	if _, err := p.Write(fds[1], []byte("x")); core_errstr(err) != abi.Ehungup {
		t.Errorf("write to closed pipe: %v", err)
	}
	if _, err := p.Seek(fds[1], 0, 0); core_errstr(err) != abi.Eisstream {
		t.Errorf("seek pipe: %v", err)
	}
}

func TestKillUnblocksRead(t *testing.T) {
	p := newTestProg(t)
	fds, _ := p.Pipe()
	done := make(chan error)
	go func() {
		_, err := p.Read(fds[0], make([]byte, 1))
		done <- err
	}()
	p.Kill()
	if err := <-done; core_errstr(err) != abi.Eintr {
		t.Errorf("killed read: %v", err)
	}
}

func TestEnv(t *testing.T) {
	p := newTestProg(t)
	if err := p.Bind("#e", "/env", abi.MREPL|abi.MCREATE); err != nil {
		t.Fatal(err)
	}
	fd, err := p.Create("/env/x", abi.OWRITE, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	p.Write(fd, []byte("val"))
	p.Close(fd)
	if v, _ := p.Egrp().Get("x"); string(v) != "val" {
		t.Errorf("egrp x = %q", v)
	}
	if got := names(t, p, "/env"); got != "x" {
		t.Errorf("env dir %q", got)
	}
	q, _ := p.Spawn(abi.FORKENV, nil)
	q.Egrp().Set("x", []byte("child"))
	if v, _ := p.Egrp().Get("x"); string(v) != "val" {
		t.Errorf("FORKENV leaked: %q", v)
	}
	if err := p.Remove("/env/x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Egrp().Get("x"); ok {
		t.Error("x not removed")
	}
}

func TestDupAndFd(t *testing.T) {
	p := newTestProg(t)
	p.Bind("#d", "/fd", abi.MREPL)
	fd, err := p.Open("/a/f", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	fd2, err := p.Open("/fd/"+itoa(fd), abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	n, _ := p.Read(fd2, buf)
	if string(buf[:n]) != "in a" {
		t.Errorf("read via #d: %q", buf[:n])
	}
	c, _ := p.Chan(fd)
	if c.Offset() != 4 {
		t.Errorf("#d shares the offset: %d", c.Offset())
	}
	if _, err := p.Open("/fd/"+itoa(fd), abi.OWRITE); core_errstr(err) != abi.Ebadusefd {
		t.Errorf("write open of read fd: %v", err)
	}
}

func itoa(i int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+i))) }

func TestProgWait(t *testing.T) {
	p := newTestProg(t)
	p.Bind("#p", "/prog", abi.MREPL)
	wfd, err := p.Open("/prog/"+itoa(p.Pid)+"/wait", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := p.Spawn(0, nil)
	c.SetName("child")
	st, err := p.Stat("/prog/" + itoa(c.Pid) + "/status")
	if err != nil || st.Qid.Type&styx.QTDIR != 0 {
		t.Fatalf("status stat: %v", err)
	}
	c.Exit("fail:x")
	buf := make([]byte, 100)
	n, err := p.Read(wfd, buf)
	want := itoa(c.Pid) + ` "child":fail:x`
	if err != nil || string(buf[:n]) != want {
		t.Errorf("wait %q %v, want %q", buf[:n], err, want)
	}
}
