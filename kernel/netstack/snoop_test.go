package netstack

import (
	"bytes"
	"net/netip"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/platform"
)

// TestSnoop sends a UDP datagram out of the Ethernet interface and
// finds its IP packet, and a received one's, in the interface's snoop.
func TestSnoop(t *testing.T) {
	s, _ := New()
	recv, err := s.Attach(Interface{MAC: []byte{2, 0, 0, 0, 0, 1}, Addr: netip.MustParsePrefix("10.0.2.15/24"),
		Gateway: netip.MustParseAddr("10.0.2.2")}, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte, 16)
	stop, err := s.SnoopIfc(0, func(pkt []byte) { got <- bytes.Clone(pkt) })
	if err != nil {
		t.Fatal(err)
	}
	// A frame from the wire: an IPv4 header, enough for the snoop.
	in := make([]byte, 14+20)
	copy(in, []byte{2, 0, 0, 0, 0, 1, 2, 0, 0, 0, 0, 2, 0x08, 0x00, 0x45})
	recv(in)
	select {
	case p := <-got:
		if !bytes.Equal(p, in[14:]) {
			t.Errorf("snooped %x, want %x", p, in[14:])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("received packet not snooped")
	}
	// An ARP frame is not the interface's.
	arp := make([]byte, 60)
	arp[12], arp[13] = 0x08, 0x06
	recv(arp)

	so, err := s.Socket(platform.UDP)
	if err != nil {
		t.Fatal(err)
	}
	defer so.Close()
	// The packet waits for ARP to resolve 10.0.2.9, which gets no
	// answer, so send to the broadcast address.
	hdr := make([]byte, 12)
	copy(hdr, []byte{10, 0, 2, 255})
	hdr[8], hdr[9] = 0x30, 0x39
	if _, err := so.Send(nil, []byte("hello"), hdr); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if len(p) < 28 || p[0]>>4 != 4 || p[9] != 17 || !bytes.HasSuffix(p, []byte("hello")) {
			t.Errorf("snooped %x, want the UDP packet", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sent packet not snooped")
	}
	stop()
	if n := s.snooping.Load(); n != 0 {
		t.Errorf("%d snoopers after stop", n)
	}
}
