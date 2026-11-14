package host

import (
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
)

func eiaProg(t *testing.T, devs []string) *core.Prog {
	t.Helper()
	k := &core.Kernel{Platform: &Host{}, Progs: core.NewProgs(), Owner: "eve"}
	k.Mnt = core.NewDevMnt()
	k.Srv = core.NewDevSrv(k)
	k.DevTab = core.NewDevTab(core.NewRoot(k.Owner), NewDevEia(k, devs))
	rootc, err := k.DevTab.Attach(nil, '/', "")
	if err != nil {
		t.Fatal(err)
	}
	return core.NewProg(k, core.NewPgrp(k.DevTab, rootc), k.Owner)
}

func readAll(t *testing.T, p *core.Prog, path string) string {
	t.Helper()
	fd, err := p.Open(path, abi.OREAD)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer p.Close(fd)
	buf := make([]byte, 256)
	n, err := p.Read(fd, buf)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(buf[:n])
}

func ctl(p *core.Prog, fd int, s string) string {
	if _, err := p.Write(fd, []byte(s)); err != nil {
		return core.Errstr(err)
	}
	return ""
}

// TestEia drives #t over a pseudo-terminal: data both ways, the status
// and ctl files, settings that outlive a close, and an interrupted read.
func TestEia(t *testing.T) {
	if _, err := eiaProg(t, nil).Kernel.DevTab.Attach(nil, 't', ""); core.Errstr(err) != abi.Enodev {
		t.Errorf("attach with no ports: %v", err)
	}
	m, slave, err := openpty()
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	defer m.Close()
	p := eiaProg(t, []string{slave, "/nonexistent/tty"})
	fd, err := p.Open("#t", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := p.Dirents(fd)
	p.Close(fd)
	var names []string
	for _, d := range ds {
		names = append(names, d.Name)
		if d.Mode != 0o660 || d.UID != "eve" {
			t.Errorf("%s: mode %o uid %s", d.Name, d.Mode, d.UID)
		}
	}
	if err != nil || strings.Join(names, " ") != "eia0 eia0ctl eia0status" {
		t.Fatalf("#t: %v %v", names, err)
	}

	data, err := p.Open("#t/eia0", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	if s := readAll(t, p, "#t/eia0status"); s != "opens 1 ferr 0 oerr 0 baud 9600\n" {
		t.Errorf("status %q", s)
	}
	if s := readAll(t, p, "#t/eia0ctl"); s != "          0 " {
		t.Errorf("ctl read %q", s)
	}
	if _, err := p.Write(data, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = m.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := m.Read(buf); err != nil || string(buf[:n]) != "hello" {
		t.Errorf("master read %q, %v", buf[:n], err)
	}
	if _, err := m.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Read(data, buf); err != nil || string(buf[:n]) != "world" {
		t.Errorf("eia0 read %q, %v", buf[:n], err)
	}

	c, err := p.Open("#t/eia0ctl", abi.OWRITE)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct{ cmd, want string }{
		{"b12345", abi.Ebadarg},
		{"l9", abi.Ebadarg},
		{"s3", abi.Ebadarg},
		{"b115200 l7 pe s2 x1 f i1 n1 q8192", ""},
	} {
		if e := ctl(p, c, x.cmd); e != x.want {
			t.Errorf("ctl %q: %q, want %q", x.cmd, e, x.want)
		}
	}
	if s := readAll(t, p, "#t/eia0status"); s != "opens 2 ferr 0 oerr 0 baud 115200\n" {
		t.Errorf("status %q", s)
	}
	check := func(when string) {
		t.Helper()
		f, err := os.OpenFile(slave, os.O_RDWR|unix.O_NOCTTY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		ts, err := tcget(int(f.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		// A Linux pty keeps CS8 and no parity, so those are checked
		// in the saved settings instead.
		if ts.Cflag&unix.CSTOPB == 0 || ts.Iflag&unix.IXON == 0 {
			t.Errorf("%s: termios %+v", when, ts)
		}
		sv := p.Kernel.DevTab.Lookup('t').(*DevEia).ports[0].ts
		if sv.Cflag&unix.CSIZE != unix.CS7 || sv.Cflag&(unix.PARENB|unix.PARODD) != unix.PARENB {
			t.Errorf("%s: saved termios %+v", when, sv)
		}
		if b, _ := getspeed(ts); b != 115200 {
			t.Errorf("%s: baud %d", when, b)
		}
	}
	check("after ctl")
	p.Close(c)
	p.Close(data)

	// The settings outlive the last close.
	data, err = p.Open("#t/eia0", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	check("after reopen")
	if s := readAll(t, p, "#t/eia0status"); s != "opens 1 ferr 0 oerr 0 baud 115200\n" {
		t.Errorf("status %q", s)
	}

	// A kill ends a read that waits for input.
	child, err := p.Spawn(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := child.Read(data, buf)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	child.Kill()
	select {
	case err := <-done:
		if core.Errstr(err) != abi.Eintr {
			t.Errorf("killed read: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("killed read did not return")
	}
	if _, err := m.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Read(data, buf); err != nil || string(buf[:n]) != "again" {
		t.Errorf("read after a killed read %q, %v", buf[:n], err)
	}
	p.Close(data)
}
