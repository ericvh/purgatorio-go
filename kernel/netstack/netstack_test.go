package netstack

import (
	"testing"

	"github.com/ericvh/purgatorio-go/platform"
)

func v4(a, b, c, d byte) [16]byte {
	return [16]byte{10: 0xff, 11: 0xff, 12: a, 13: b, 14: c, 15: d}
}

func TestLoopbackTCP(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	l, _ := s.Socket(platform.TCP)
	if err := l.Bind(v4(127, 0, 0, 1), 0, false); err != nil {
		t.Fatal(err)
	}
	if err := l.Listen(); err != nil {
		t.Fatal(err)
	}
	_, port := l.LocalAddr()
	if port == 0 {
		t.Fatal("no port")
	}
	done := make(chan error)
	go func() {
		c, _ := s.Socket(platform.TCP)
		if err := c.Connect(stop, v4(127, 0, 0, 1), port); err != nil {
			done <- err
			return
		}
		c.Send(stop, []byte("ping"), nil)
		buf := make([]byte, 16)
		n, err := c.Recv(stop, buf, nil)
		if err != nil || string(buf[:n]) != "pong" {
			t.Errorf("client got %q %v", buf[:n], err)
		}
		c.Close()
		done <- nil
	}()
	a, ra, _, err := l.Accept(stop)
	if err != nil {
		t.Fatal(err)
	}
	if ra != v4(127, 0, 0, 1) {
		t.Errorf("remote %v", ra)
	}
	buf := make([]byte, 16)
	n, err := a.Recv(stop, buf, nil)
	if err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("server got %q %v", buf[:n], err)
	}
	a.Send(stop, []byte("pong"), nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	n, err = a.Recv(stop, buf, nil)
	if n != 0 || err != nil {
		t.Errorf("eof: %d %v", n, err)
	}
	// A blocked call ends when stop is closed.
	go close(stop)
	if _, _, _, err := l.Accept(stop); err != platform.ErrInterrupted {
		t.Errorf("accept after stop: %v", err)
	}
}

func TestLoopbackUDP(t *testing.T) {
	s, _ := New()
	stop := make(chan struct{})
	a, _ := s.Socket(platform.UDP)
	if err := a.Bind([16]byte{}, 0, false); err != nil {
		t.Fatal(err)
	}
	_, port := a.LocalAddr()
	b, _ := s.Socket(platform.UDP)
	if err := b.Connect(stop, v4(127, 0, 0, 1), port); err != nil {
		t.Fatal(err)
	}
	b.Send(stop, []byte("hello"), nil)
	buf := make([]byte, 16)
	hdr := make([]byte, 52)
	n, err := a.Recv(stop, buf, hdr)
	if err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("got %q %v", buf[:n], err)
	}
	ra, _ := gethdr(hdr)
	if ra != v4(127, 0, 0, 1) {
		t.Errorf("header raddr %v", ra)
	}
	if _, err := a.Send(stop, []byte("back"), hdr); err != nil {
		t.Fatal(err)
	}
	n, _ = b.Recv(stop, buf, nil)
	if string(buf[:n]) != "back" {
		t.Errorf("reply %q", buf[:n])
	}
}

func TestRefused(t *testing.T) {
	s, _ := New()
	c, _ := s.Socket(platform.TCP)
	err := c.Connect(make(chan struct{}), v4(127, 0, 0, 1), 9)
	if err == nil || err.Error() != "connection refused" {
		t.Errorf("got %v", err)
	}
}
