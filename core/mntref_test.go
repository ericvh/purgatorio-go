package core

import (
	"context"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// A mount keeps its conversation's Chan: the fd it was made from can be
// closed (the mounting prog may exit) and the mount still works.
func TestMountOutlivesFd(t *testing.T) {
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()
	fds, err := p.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.Export(fds[0], "/a", false) }()
	if err := p.Mount(fds[1], -1, "/b", abi.MREPL, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if err := p.Close(fds[1]); err != nil {
		t.Fatal(err)
	}
	if got := names(t, p, "/b"); got != "x f" {
		t.Errorf("mounted dir after closing the fd: %q", got)
	}
}

// exportOn starts an export of /a in a child of p on one end of a pipe
// and returns the other end's fd in p and the channel the export's
// result comes on.
func exportOn(t *testing.T, p *Prog) (int, chan error) {
	t.Helper()
	fds, err := p.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	q, err := p.Spawn(abi.NEWFD, [][2]int{{0, fds[0]}})
	if err != nil {
		t.Fatal(err)
	}
	p.Close(fds[0])
	done := make(chan error, 1)
	go func() { done <- q.Export(0, "/a", false) }()
	return fds[1], done
}

func waitExport(t *testing.T, done chan error, why string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("export: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("export still running " + why)
	}
}

// A conversation ends when the last reference to it goes, as in
// Inferno: here at unmount, once the current directory in it has moved
// away. The server sees its client hang up and returns, without waiting
// for the garbage collector.
func TestMountEndsAtUnmount(t *testing.T) {
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()
	fd, done := exportOn(t, p)
	if err := p.Mount(fd, -1, "/b", abi.MREPL, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	p.Close(fd)
	if err := p.Chdir("/b/x"); err != nil {
		t.Fatal(err)
	}
	if err := p.Unmount("", "/b"); err != nil {
		t.Fatal(err)
	}
	// The current directory still holds the conversation.
	select {
	case err := <-done:
		t.Fatalf("export ended while dot held its conversation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := p.Stat("."); err != nil {
		t.Fatalf("stat dot: %v", err)
	}
	if err := p.Chdir("/"); err != nil {
		t.Fatal(err)
	}
	waitExport(t, done, "after the last reference went")
}

// An open file holds the conversation after the unmount, until it is
// closed.
func TestMountHeldByOpenFile(t *testing.T) {
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()
	fd, done := exportOn(t, p)
	if err := p.Mount(fd, -1, "/b", abi.MREPL, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	p.Close(fd)
	f, err := p.Open("/b/f", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Unmount("", "/b"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if n, err := p.Read(f, buf); err != nil || string(buf[:n]) != "in a" {
		t.Errorf("read after unmount: %q, %v", buf[:n], err)
	}
	p.Close(f)
	waitExport(t, done, "after the open file was closed")
}

// A prog's mounts go when it exits and no other prog shares its name
// space; a name space shared with a running prog keeps them.
func TestMountEndsAtExit(t *testing.T) {
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()
	fd, done := exportOn(t, p)
	c, err := p.Spawn(abi.FORKNS|abi.NEWFD, [][2]int{{0, fd}})
	if err != nil {
		t.Fatal(err)
	}
	p.Close(fd)
	if err := c.Mount(0, -1, "/b", abi.MREPL, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	c.Close(0)
	d, err := c.Spawn(abi.NEWFD, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Exit("")
	if got := names(t, d, "/b"); got != "x f" {
		t.Errorf("mount after its maker exited: %q", got)
	}
	d.Exit("")
	waitExport(t, done, "after its name space went")
}

// A request still outstanding when the last reference goes (a clunk
// sent without waiting, as the garbage collector's are) is answered
// before the conversation hangs up, as in Inferno, where a conversation
// is closed only with no request outstanding: the server's reply never
// meets a closed pipe.
func TestMountHangupWaitsForReplies(t *testing.T) {
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()
	fds, err := p.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := p.Spawn(abi.NEWFD, [][2]int{{0, fds[0]}})
	if err != nil {
		t.Fatal(err)
	}
	p.Close(fds[0])
	gotClunk := make(chan struct{})
	replied := make(chan error, 1)
	go func() {
		var buf []byte
		tmp := make([]byte, 8192)
		for {
			n := styx.Size(buf)
			if n == 0 || int(n) > len(buf) {
				k, err := srv.Read(0, tmp)
				if err != nil || k == 0 {
					return
				}
				buf = append(buf, tmp[:k]...)
				continue
			}
			f, err := styx.Unmarshal(buf[:n], 8192)
			buf = buf[n:]
			if err != nil {
				return
			}
			r := &styx.Fcall{Type: f.Type + 1, Tag: f.Tag}
			switch f.Type {
			case styx.Tversion:
				r.Msize, r.Version = f.Msize, f.Version
			case styx.Tattach:
				r.Qid = styx.Qid{Type: styx.QTDIR}
			case styx.Tclunk:
				if f.Fid == 99 {
					close(gotClunk)
					time.Sleep(200 * time.Millisecond)
				}
			}
			b, _ := styx.Marshal(nil, r)
			_, err = srv.Write(0, b)
			if f.Type == styx.Tclunk && f.Fid == 99 {
				replied <- err
			}
		}
	}()
	if err := p.Mount(fds[1], -1, "/b", abi.MREPL, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	p.Close(fds[1])
	var m *Mnt
	p.Kernel.Mnt.mu.Lock()
	for _, x := range p.Kernel.Mnt.conns {
		m = x
	}
	p.Kernel.Mnt.mu.Unlock()
	go m.clunk(context.Background(), 99)
	<-gotClunk
	if err := p.Unmount("", "/b"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-replied:
		if err != nil {
			t.Errorf("server's reply to the outstanding clunk: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no reply to the outstanding clunk")
	}
}
