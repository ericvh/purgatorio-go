package netstack

import (
	"fmt"
	"net/netip"
	"strings"
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

func mask(bits int) [16]byte {
	m := [16]byte{0: 0xff, 1: 0xff, 2: 0xff, 3: 0xff, 4: 0xff, 5: 0xff, 6: 0xff, 7: 0xff, 8: 0xff, 9: 0xff, 10: 0xff, 11: 0xff}
	for i := 12; bits > 0; i++ {
		m[i] = byte(0xff << max(0, 8-bits))
		bits -= 8
	}
	return m
}

// TestIfc checks the interfaces /net/ipifc shows and their addresses
// and routes in gVisor.
func TestIfc(t *testing.T) {
	s, _ := New()
	_, err := s.Attach(Interface{MAC: []byte{2, 0, 0, 0, 0, 1}, Addr: netip.MustParsePrefix("10.0.2.15/24"),
		Gateway: netip.MustParseAddr("10.0.2.2")}, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	l := s.Interfaces()
	if len(l) != 2 || l[0].Dev != "/net/ether0" || l[1].Dev != "/dev/null" {
		t.Fatalf("interfaces %+v", l)
	}
	boot := platform.IfcAddr{IP: v4(10, 0, 2, 15), Mask: mask(24), Remote: v4(10, 0, 2, 0)}
	if len(l[0].Addrs) != 1 || l[0].Addrs[0] != boot {
		t.Errorf("ether addresses %v", l[0].Addrs)
	}
	routes := func() string {
		var b []string
		for _, r := range s.g.Stack.GetRouteTable() {
			b = append(b, fmt.Sprintf("%s nic%d", r.Destination, r.NIC))
		}
		return strings.Join(b, ", ")
	}
	if err := s.RemoveAddr(0, boot); err != nil {
		t.Fatal(err)
	}
	if got := routes(); got != "127.0.0.0/8 nic2, 0.0.0.0/0 nic1" {
		t.Errorf("routes after remove: %s", got)
	}
	if err := s.RemoveAddr(0, boot); err == nil || err.Error() != "address not on this interface" {
		t.Errorf("remove again: %v", err)
	}
	null := platform.IfcAddr{}
	if err := s.AddAddr(0, null, 0); err != nil {
		t.Fatal(err)
	}
	a := platform.IfcAddr{IP: v4(192, 168, 7, 9), Mask: mask(16), Remote: v4(192, 168, 0, 0)}
	if err := s.AddAddr(0, a, 0); err != nil {
		t.Fatal(err)
	}
	if got := routes(); got != "192.168.0.0/16 nic1, 127.0.0.0/8 nic2, 0.0.0.0/0 nic1" {
		t.Errorf("routes after add: %s", got)
	}
	if err := s.RemoveAddr(0, null); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, pa := range s.g.Stack.AllAddresses()[1] {
		if x := pa.AddressWithPrefix.String(); x != "255.255.255.255/32" {
			got = append(got, x)
		}
	}
	if strings.Join(got, " ") != "192.168.7.9/16" {
		t.Errorf("gVisor addresses %v", got)
	}
	bad := platform.IfcAddr{IP: v4(10, 0, 0, 1), Mask: v4(255, 0, 255, 0), Remote: v4(10, 0, 0, 0)}
	if err := s.AddAddr(0, bad, 0); err == nil {
		t.Error("non-contiguous mask accepted")
	}
	if err := s.BindIfc(0, "ether", nil); err == nil || err.Error() != "interface already bound" {
		t.Errorf("bind bound: %v", err)
	}
	if err := s.UnbindIfc(0); err != nil {
		t.Fatal(err)
	}
	if got := routes(); got != "127.0.0.0/8 nic2, 0.0.0.0/0 nic1" {
		t.Errorf("routes after unbind: %s", got)
	}
	n, _ := s.NewIfc()
	if err := s.BindIfc(n, "ether", []string{"/net/ether1"}); err == nil || err.Error() != "file does not exist" {
		t.Errorf("bind ether1: %v", err)
	}
	if err := s.BindIfc(n, "ppp", nil); err == nil || err.Error() != "unknown interface type" {
		t.Errorf("bind ppp: %v", err)
	}
	if err := s.BindIfc(n, "loopback", nil); err == nil || err.Error() != "device or object already in use" {
		t.Errorf("bind loopback: %v", err)
	}
	if err := s.AddAddr(n, a, 0); err == nil || err.Error() != "ipifc not yet bound to device" {
		t.Errorf("add unbound: %v", err)
	}
	if err := s.BindIfc(n, "ether", []string{"/net/ether0"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAddr(n, boot, 0); err != nil {
		t.Fatal(err)
	}
	l = s.Interfaces()
	if l[0].Dev != "" || l[n].Dev != "/net/ether0" || len(l[n].Addrs) != 1 {
		t.Errorf("after rebinding: %+v", l)
	}
}
