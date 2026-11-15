package netstack

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"

	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
)

var etherMAC = []byte{2, 0, 0, 0, 0, 1}

// attached is a stack with the Ethernet interface, as QEMU's user
// networking sets it up; frames sent go to tx.
func attached(t *testing.T) (*Stack, func([]byte), chan []byte) {
	t.Helper()
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	tx := make(chan []byte, 64)
	recv, err := s.Attach(Interface{MAC: etherMAC, Addr: netip.MustParsePrefix("10.0.2.15/24"),
		Gateway: netip.MustParseAddr("10.0.2.2")}, func(b []byte) {
		select {
		case tx <- append([]byte(nil), b...):
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, recv, tx
}

func ip16(s string) [16]byte { return netip.MustParseAddr(s).As16() }

func v6mask(bits int) [16]byte {
	var m [16]byte
	for i := 0; bits > 0; i++ {
		m[i] = byte(0xff << max(0, 8-bits))
		bits -= 8
	}
	return m
}

// TestRoutes checks that iproute's table goes into gVisor's: routes
// whose gateway an interface reaches, through that interface, longest
// prefix first.
func TestRoutes(t *testing.T) {
	s, _, _ := attached(t)
	boot := s.BootRoutes()
	if len(boot) != 1 || core.FormatIP(boot[0].Gate) != "10.0.2.2" || boot[0].Addr != v4(0, 0, 0, 0) || boot[0].Mask != mask(0) {
		t.Fatalf("boot routes %+v", boot)
	}
	if got := routestr(s); got != "10.0.2.0/24 nic1, 127.0.0.0/8 nic2, 0.0.0.0/0 via 10.0.2.2 nic1" {
		t.Errorf("boot table: %s", got)
	}
	s.SetRoutes([]platform.Route{
		boot[0],
		{Addr: v4(192, 168, 5, 0), Mask: mask(24), Gate: v4(10, 0, 2, 9)},
		{Addr: v4(172, 16, 0, 0), Mask: mask(16), Gate: v4(192, 168, 1, 1)},      // unreachable gateway
		{Addr: v4(10, 9, 0, 0), Mask: v4(255, 0, 255, 0), Gate: v4(10, 0, 2, 9)}, // not a prefix
		{Addr: v4(10, 0, 3, 0), Mask: mask(24), Gate: v4(0, 0, 0, 0)},            // not on a network we have
		{Addr: v4(10, 0, 2, 128), Mask: mask(25), Gate: v4(10, 0, 2, 15)},        // through our own address
	})
	want := "10.0.2.128/25 nic1, 10.0.2.0/24 nic1, 192.168.5.0/24 via 10.0.2.9 nic1, 127.0.0.0/8 nic2, 0.0.0.0/0 via 10.0.2.2 nic1"
	if got := routestr(s); got != want {
		t.Errorf("table:\n got %s\nwant %s", got, want)
	}
	// A new network makes the waiting route usable.
	if err := s.AddAddr(0, platform.IfcAddr{IP: v4(192, 168, 1, 7), Mask: mask(24), Remote: v4(192, 168, 1, 0)}, 0); err != nil {
		t.Fatal(err)
	}
	if got := routestr(s); !strings.Contains(got, "172.16.0.0/16 via 192.168.1.1 nic1") {
		t.Errorf("after adding 192.168.1.7: %s", got)
	}
	s.SetRoutes(nil)
	if got := routestr(s); got != "10.0.2.0/24 nic1, 192.168.1.0/24 nic1, 127.0.0.0/8 nic2" {
		t.Errorf("flushed: %s", got)
	}
	// An IPv6 network and default route.
	a6 := platform.IfcAddr{IP: ip16("fec0::15"), Mask: v6mask(64), Remote: ip16("fec0::")}
	if err := s.AddAddr(0, a6, 0); err != nil {
		t.Fatal(err)
	}
	s.SetRoutes([]platform.Route{{Addr: ip16("::"), Mask: v6mask(0), Gate: ip16("fec0::2")}})
	if got := routestr(s); !strings.HasPrefix(got, "fec0::/64 nic1, ") || !strings.HasSuffix(got, ", ::/0 via fec0::2 nic1") {
		t.Errorf("v6 table: %s", got)
	}
	r, err := s.g.Stack.FindRoute(0, tcpip.Address{}, tcpip.AddrFrom16(ip16("2001:db8::1")), ipv6.ProtocolNumber, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.NextHop() != tcpip.AddrFrom16(ip16("fec0::2")) || r.LocalAddress() != tcpip.AddrFrom16(ip16("fec0::15")) {
		t.Errorf("route to 2001:db8::1: next hop %s from %s", r.NextHop(), r.LocalAddress())
	}
	r.Release()
	if err := s.RemoveAddr(0, a6); err != nil {
		t.Fatal(err)
	}
	if got := routestr(s); strings.Contains(got, "fec0") {
		t.Errorf("v6 routes after removing the address: %s", got)
	}
}

// TestForwarding checks iprouting.
func TestForwarding(t *testing.T) {
	s, _, _ := attached(t)
	if s.IPStats()[0] != 2 {
		t.Error("forwarding at start")
	}
	if err := s.SetForwarding(true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []tcpip.NetworkProtocolNumber{ipv4.ProtocolNumber, ipv6.ProtocolNumber} {
		if on, err := s.g.Stack.NICForwarding(1, p); err != nil || !on {
			t.Errorf("protocol %d: forwarding %v %v", p, on, err)
		}
	}
	if s.IPStats()[0] != 1 {
		t.Error("Forwarding is not 1")
	}
	if err := s.SetForwarding(false); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.g.Stack.NICForwarding(1, ipv4.ProtocolNumber); on || s.IPStats()[0] != 2 {
		t.Error("forwarding still on")
	}
}

// TestMulticast checks addmulti on an interface and on a conversation,
// IGMP reports, and that a UDP conversation receives datagrams sent to
// a group it joined.
func TestMulticast(t *testing.T) {
	s, _, tx := attached(t)
	g := v4(224, 0, 0, 9)
	ga := tcpip.AddrFrom4([4]byte{224, 0, 0, 9})
	if err := s.AddMulti(0, g); err != nil {
		t.Fatal(err)
	}
	if in, _ := s.g.Stack.IsInGroup(1, ga); !in {
		t.Error("not in 224.0.0.9")
	}
	if l := s.Interfaces(); len(l[0].Multi) != 1 || l[0].Multi[0] != g {
		t.Errorf("Multi %v", l[0].Multi)
	}
	// An IGMP membership report for the group goes out.
	if !waitFrame(tx, func(b []byte) bool { return len(b) > 34 && b[12] == 8 && b[13] == 0 && b[23] == 2 }) {
		t.Error("no IGMP report")
	}
	if err := s.RemMulti(0, g); err != nil {
		t.Fatal(err)
	}
	if err := s.RemMulti(0, g); err == nil {
		t.Error("removed twice")
	}
	if in, _ := s.g.Stack.IsInGroup(1, ga); in {
		t.Error("still in 224.0.0.9")
	}

	stop := make(chan struct{})
	rx, _ := s.Socket(platform.UDP)
	if err := rx.Bind([16]byte{}, 520, false); err != nil {
		t.Fatal(err)
	}
	if err := rx.(platform.MulticastSocket).AddMulti(g, v4(10, 0, 2, 15)); err != nil {
		t.Fatal(err)
	}
	if in, _ := s.g.Stack.IsInGroup(1, ga); !in {
		t.Error("conversation did not join")
	}
	if l := s.Interfaces(); len(l[0].Multi) != 1 || l[0].Multi[0] != g {
		t.Errorf("Multi with a conversation's group: %v", l[0].Multi)
	}
	sx, _ := s.Socket(platform.UDP)
	if err := sx.Bind(v4(10, 0, 2, 15), 0, false); err != nil {
		t.Fatal(err)
	}
	hdr := make([]byte, 52)
	puthdr(hdr, g, 520, [16]byte{}, [16]byte{}, 0)
	if _, err := sx.Send(stop, []byte("rip"), hdr); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	got := make(chan string, 1)
	go func() {
		n, err := rx.Recv(stop, buf, hdr)
		if err != nil {
			got <- err.Error()
			return
		}
		got <- string(buf[:n])
	}()
	select {
	case m := <-got:
		if m != "rip" {
			t.Errorf("received %q", m)
		}
		if ra, _ := gethdr(hdr); ra != v4(10, 0, 2, 15) || [16]byte(hdr[16:32]) != g {
			t.Errorf("header % x", hdr)
		}
	case <-time.After(5 * time.Second):
		close(stop)
		t.Fatal("no multicast datagram")
	}
	rx.Close()
	if in, _ := s.g.Stack.IsInGroup(1, ga); in {
		t.Error("still in the group after close")
	}
	sx.Close()
}

func waitFrame(tx chan []byte, ok func([]byte) bool) bool {
	deadline := time.After(5 * time.Second)
	for {
		select {
		case b := <-tx:
			if ok(b) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// TestIPv6Loopback checks TCP, UDP and ICMPv6 over ::1, added to the
// loopback interface as ipifc's add does.
func TestIPv6Loopback(t *testing.T) {
	s, _ := New()
	lo := platform.IfcAddr{IP: ip16("::1"), Mask: v6mask(128), Remote: ip16("::1")}
	if err := s.AddAddr(0, lo, 0); err != nil {
		t.Fatal(err)
	}
	if l := s.Interfaces(); len(l[0].Addrs) != 2 || l[0].Addrs[1] != lo {
		t.Fatalf("loopback addresses %v", l[0].Addrs)
	}
	stop := make(chan struct{})
	l, _ := s.Socket(platform.TCP)
	if err := l.Bind([16]byte{}, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := l.Listen(); err != nil {
		t.Fatal(err)
	}
	_, port := l.LocalAddr()
	done := make(chan error, 1)
	go func() {
		c, _ := s.Socket(platform.TCP)
		err := c.Connect(stop, ip16("::1"), port)
		if err == nil {
			_, err = c.Send(stop, []byte("six"), nil)
		}
		if la, _ := c.LocalAddr(); la != ip16("::1") {
			t.Errorf("client local %s", core.FormatIP(la))
		}
		c.Close()
		done <- err
	}()
	a, ra, _, err := l.Accept(stop)
	if err != nil {
		t.Fatal(err)
	}
	if ra != ip16("::1") {
		t.Errorf("remote %s", core.FormatIP(ra))
	}
	buf := make([]byte, 64)
	if n, err := a.Recv(stop, buf, nil); err != nil || string(buf[:n]) != "six" {
		t.Errorf("got %q %v", buf[:n], err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// IPv4 still reaches the same listener.
	c4, _ := s.Socket(platform.TCP)
	if err := c4.Connect(stop, v4(127, 0, 0, 1), port); err != nil {
		t.Fatalf("v4 to a listener on *: %v", err)
	}
	if _, ra, _, err := l.Accept(stop); err != nil || ra != v4(127, 0, 0, 1) {
		t.Errorf("v4 accept %s %v", core.FormatIP(ra), err)
	}
	c4.Close()

	u, _ := s.Socket(platform.UDP)
	if err := u.Bind(ip16("::1"), 0, false); err != nil {
		t.Fatal(err)
	}
	_, up := u.LocalAddr()
	w, _ := s.Socket(platform.UDP)
	if err := w.Connect(stop, ip16("::1"), up); err != nil {
		t.Fatal(err)
	}
	w.Send(stop, []byte("datagram"), nil)
	hdr := make([]byte, 52)
	if n, err := u.Recv(stop, buf, hdr); err != nil || string(buf[:n]) != "datagram" {
		t.Fatalf("udp got %q %v", buf[:n], err)
	}
	if ra, _ := gethdr(hdr); ra != ip16("::1") || [16]byte(hdr[16:32]) != ip16("::1") {
		t.Errorf("udp header % x", hdr)
	}

	ic, err := s.Socket(platform.ICMPv6)
	if err != nil {
		t.Fatal(err)
	}
	if err := ic.Connect(stop, ip16("::1"), 1); err != nil {
		t.Fatal(err)
	}
	la, ident := ic.LocalAddr()
	if la != ip16("::1") || ident < 5000 {
		t.Errorf("icmpv6 local %s!%d", core.FormatIP(la), ident)
	}
	msg := make([]byte, 40+8+8)
	msg[40] = 128 // echo request
	msg[46], msg[47] = 0, 7
	copy(msg[48:], "pingpong")
	if _, err := ic.Send(stop, msg, nil); err != nil {
		t.Fatal(err)
	}
	n, err := ic.Recv(stop, buf, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(msg) || buf[40] != 129 || binary.BigEndian.Uint16(buf[44:]) != ident || string(buf[48:n]) != "pingpong" {
		t.Errorf("reply % x", buf[:n])
	}
	if buf[0] != 0x60 || buf[6] != 58 || buf[7] == 0 || [16]byte(buf[8:24]) != ip16("::1") || [16]byte(buf[24:40]) != ip16("::1") {
		t.Errorf("header % x", buf[:40])
	}
	if c, err := s.Socket(platform.ICMPv6); err == nil {
		if err := c.Connect(stop, v4(127, 0, 0, 1), 1); err == nil {
			t.Error("icmpv6 to an IPv4 address")
		}
	}
	v, _ := s.ProtoStats(platform.ICMPv6)
	if v[12+2*128] == 0 || v[12+2*129+1] == 0 || v[12+2*128+1] == 0 {
		t.Errorf("icmpv6 echo counts: request %d %d reply %d %d", v[12+2*128], v[12+2*128+1], v[12+2*129], v[12+2*129+1])
	}
	tv, _ := s.ProtoStats(platform.TCP)
	if tv[1] < 2 || tv[2] < 2 || tv[6] == 0 {
		t.Errorf("tcp stats %v", tv)
	}
	uv, _ := s.ProtoStats(platform.UDP)
	if uv[0] == 0 || uv[3] == 0 {
		t.Errorf("udp stats %v", uv)
	}
}

// TestICMPMessages checks /net/icmp beyond echo, over the Ethernet
// interface: a reply of another type with the conversation's ident is
// read, one with another ident is not, and a destination unreachable
// about one of its messages hangs it up with Inferno's text.
func TestICMPMessages(t *testing.T) {
	s, recv, _ := attached(t)
	stop := make(chan struct{})
	c, _ := s.Socket(platform.ICMP)
	if err := c.Connect(stop, v4(10, 0, 2, 2), 1); err != nil {
		t.Fatal(err)
	}
	la, ident := c.LocalAddr()
	if la != v4(10, 0, 2, 15) || ident < 5000 {
		t.Errorf("local %s!%d", core.FormatIP(la), ident)
	}
	// A timestamp reply with another ident, then one with ours.
	recv(icmpFrame(14, 0, ident+1, nil))
	recv(icmpFrame(14, 0, ident, []byte("timestamps..")))
	buf := make([]byte, 128)
	n, err := c.Recv(stop, buf, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 20+8+12 || buf[20] != 14 || binary.BigEndian.Uint16(buf[24:]) != ident || string(buf[28:n]) != "timestamps.." {
		t.Errorf("message % x", buf[:n])
	}
	if buf[0] != 0x45 || buf[8] != 64 || [4]byte(buf[12:16]) != [4]byte{10, 0, 2, 2} || [4]byte(buf[16:20]) != [4]byte{10, 0, 2, 15} {
		t.Errorf("header % x", buf[:20])
	}
	// Host unreachable about our echo request.
	inner := ipv4hdr([4]byte{10, 0, 2, 15}, [4]byte{10, 0, 2, 2}, 8)
	echo := []byte{8, 0, 0, 0, byte(ident >> 8), byte(ident), 0, 1}
	recv(icmpFrame(3, 1, 0, append(inner, echo...)))
	if _, err := c.Recv(stop, buf, nil); err == nil || err.Error() != "host unreachable" {
		t.Errorf("after unreachable: %v", err)
	}
	if _, err := c.Send(stop, make([]byte, 28), nil); err == nil || err.Error() != "host unreachable" {
		t.Errorf("send after unreachable: %v", err)
	}
	c.Close()
	v, _ := s.ProtoStats(platform.ICMP)
	if v[0] < 3 || v[6+2*14] != 2 || v[6+2*3] != 1 {
		t.Errorf("icmp stats %v", v)
	}
}

// ipv4hdr is an IPv4 header carrying n bytes of protocol 1.
func ipv4hdr(src, dst [4]byte, n int) []byte {
	h := make([]byte, 20)
	h[0] = 0x45
	binary.BigEndian.PutUint16(h[2:], uint16(20+n))
	h[8], h[9] = 64, 1
	copy(h[12:], src[:])
	copy(h[16:], dst[:])
	binary.BigEndian.PutUint16(h[10:], cksum(h))
	return h
}

// icmpFrame is an Ethernet frame from the gateway with an ICMP message
// of type typ, code, ident (or the unused word) and data.
func icmpFrame(typ, code byte, ident uint16, data []byte) []byte {
	msg := append([]byte{typ, code, 0, 0, byte(ident >> 8), byte(ident), 0, 1}, data...)
	binary.BigEndian.PutUint16(msg[2:], cksum(msg))
	f := append([]byte{}, etherMAC...)
	f = append(f, 2, 0, 0, 0, 0, 2, 8, 0)
	f = append(f, ipv4hdr([4]byte{10, 0, 2, 2}, [4]byte{10, 0, 2, 15}, len(msg))...)
	return append(f, msg...)
}
