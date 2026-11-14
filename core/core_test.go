package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
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

// TestExportMount serves the name space on one end of a pipe and mounts
// it from the other: the loopback of plan section 11, M3.
func TestExportMount(t *testing.T) {
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()
	fds, err := p.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.Export(fds[0], "/a", 0) }()
	if err := p.Mount(fds[1], -1, "/b", abi.MREPL, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if got := names(t, p, "/b"); got != "x f" {
		t.Errorf("mounted dir: %q", got)
	}
	fd, err := p.Open("/b/f", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 20)
	n, _ := p.Read(fd, buf)
	if string(buf[:n]) != "in a" {
		t.Errorf("read through mount: %q", buf[:n])
	}
	d, err := p.Stat("/b/x")
	if err != nil || d.Type != 'M' || d.Qid.Type&styx.QTDIR == 0 {
		t.Errorf("stat through mount: %+v %v", d, err)
	}
	if _, err := p.Stat("/b/nope"); core_errstr(err) != abi.Enonexist {
		t.Errorf("missing through mount: %v", err)
	}
	if _, err := p.Open("/b/f", abi.OWRITE); core_errstr(err) != abi.Eperm {
		t.Errorf("write open of read-only file: %v", err)
	}
	ns := p.Pgrp().Namespace()
	if !strings.Contains(ns, "mount  #|/data1 /b \n") {
		t.Errorf("ns: %q", ns)
	}
}

func TestIndirSnarfCap(t *testing.T) {
	p := newTestProg(t)
	k := p.Kernel
	snarf, capd := NewDevSnarf(k), NewDevCap(k)
	k.DevTab = NewDevTab(append(k.DevTab.Devs(), snarf, capd, DevIndir{})...)
	p.SetPgrp(NewPgrp(k.DevTab, mustAttach(t, k)))
	// #*name!spec reaches a device by name.
	if got := names(t, p, "#*env"); got != "" {
		t.Errorf("#*env: %q", got)
	}
	if _, err := p.Stat("#*nosuch"); core_errstr(err) != "unknown device: nosuch" {
		t.Errorf("#*nosuch: %v", err)
	}
	// A snarf written and closed reads back.
	fd, err := p.Open("#^/snarf", abi.OWRITE)
	if err != nil {
		t.Fatal(err)
	}
	p.Write(fd, []byte("clip"))
	p.Close(fd)
	fd, _ = p.Open("#^/snarf", abi.OREAD)
	buf := make([]byte, 10)
	n, _ := p.Read(fd, buf)
	if string(buf[:n]) != "clip" {
		t.Errorf("snarf %q", buf[:n])
	}
	// A capability changes the user once.
	key, users := []byte("secret"), []byte("inferno@bob")
	m := hmac.New(sha1.New, key)
	m.Write(users)
	hfd, err := p.Open("#¤/caphash", abi.OWRITE)
	if err != nil {
		t.Fatal(err)
	}
	p.Write(hfd, m.Sum(nil))
	ufd, _ := p.Open("#¤/capuse", abi.OWRITE)
	if _, err := p.Write(ufd, append(append(users, '@'), key...)); err != nil {
		t.Fatalf("capuse: %v", err)
	}
	if p.User() != "bob" {
		t.Errorf("user %q", p.User())
	}
	if _, err := p.Write(ufd, append(append(users, '@'), key...)); core_errstr(err) != "invalid capability" {
		t.Errorf("reuse: %v", err)
	}
}

func mustAttach(t *testing.T, k *Kernel) *Chan {
	c, err := k.DevTab.Attach(context.Background(), '/', "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDS(t *testing.T) {
	p := newTestProg(t)
	k := p.Kernel
	k.DevTab = NewDevTab(append(k.DevTab.Devs(), NewDevDS(k))...)
	p.SetPgrp(NewPgrp(k.DevTab, mustAttach(t, k)))
	p.Egrp().Set("a", []byte("hello "))
	p.Egrp().Set("b", []byte("world"))
	ctl, err := p.Open("#k/ds/ctl", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(ctl, []byte("cat both #e/a #e/b")); err != nil {
		t.Fatalf("config: %v", err)
	}
	if _, err := p.Pwrite(ctl, []byte("part mid #e/a 2 3"), 0); err != nil {
		t.Fatalf("part: %v", err)
	}
	read := func(path string) string {
		fd, err := p.Open(path, abi.OREAD)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer p.Close(fd)
		b := make([]byte, 64)
		n, _ := p.Read(fd, b)
		return string(b[:n])
	}
	if got := read("#k/ds/both"); got != "hello world" {
		t.Errorf("cat: %q", got)
	}
	if got := read("#k/ds/mid"); got != "llo" {
		t.Errorf("part: %q", got)
	}
	if got := read("#k/ds/ctl"); got != "cat both #e/a #e/b\npart mid #e/a 2 3\n" {
		t.Errorf("ctl: %q", got)
	}
}

// TestSharedOffsetWrites checks that writers sharing an fd each get
// their own range of the file, as Inferno's rwrite gives them.
func TestSharedOffsetWrites(t *testing.T) {
	p := newTestProg(t)
	if err := p.Bind("#e", "/env", abi.MREPL|abi.MCREATE); err != nil {
		t.Fatal(err)
	}
	fd, err := p.Create("/env/out", abi.OWRITE, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	const writers, writes = 8, 100
	rec := []byte("0123456789abcdef")
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range writes {
				if _, err := p.Write(fd, rec); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	rfd, err := p.Open("/env/out", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 2*writers*writes*len(rec))
	n, err := p.Read(rfd, b)
	if err != nil {
		t.Fatal(err)
	}
	b = b[:n]
	if want := strings.Repeat(string(rec), writers*writes); string(b) != want {
		t.Errorf("got %d bytes, %d of them NUL; want %d", len(b), bytes.Count(b, []byte{0}), len(want))
	}
}

// TestExportReadonly is export with EXPRDONLY (export -r): walks, stats
// and reads are served, and every request that would change a file is
// refused with exportfs's error.
func TestExportReadonly(t *testing.T) {
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()
	fds, err := p.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.Export(fds[0], "/a", abi.EXPRDONLY) }()
	if err := p.Mount(fds[1], -1, "/b", abi.MREPL|abi.MCREATE, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	fd, err := p.Open("/b/f", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 20)
	if n, _ := p.Read(fd, buf); string(buf[:n]) != "in a" {
		t.Errorf("read: %q", buf[:n])
	}
	const erdonly = "file system read only"
	for _, m := range []int{abi.OWRITE, abi.ORDWR, abi.OREAD | abi.OTRUNC, abi.OREAD | abi.ORCLOSE} {
		if _, err := p.Open("/b/f", m); core_errstr(err) != erdonly {
			t.Errorf("open %#x: %v", m, err)
		}
	}
	if _, err := p.Create("/b/x/new", abi.OWRITE, 0o666); core_errstr(err) != erdonly {
		t.Errorf("create: %v", err)
	}
	if err := p.Remove("/b/f"); core_errstr(err) != erdonly {
		t.Errorf("remove: %v", err)
	}
}
