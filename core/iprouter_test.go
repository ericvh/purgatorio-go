package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
)

// fakeRouter is a fakeIfcs that takes iproute's table, forwarding,
// multicast groups and gives counters, as the kernel's netstack does.
type fakeRouter struct {
	*fakeIfcs
	routes  []platform.Route
	sets    int
	forward bool
	socks   []*fakeSock
}

func (f *fakeRouter) BootRoutes() []platform.Route {
	r := platform.Route{Addr: v4addr(0, 0, 0, 0), Gate: v4addr(10, 0, 2, 2), Tag: "none"}
	r.Mask, _ = parseipmask("0.0.0.0")
	return []platform.Route{r}
}
func (f *fakeRouter) SetRoutes(r []platform.Route) { f.routes, f.sets = r, f.sets+1 }
func (f *fakeRouter) SetForwarding(on bool) error  { f.forward = on; return nil }
func (f *fakeRouter) AddMulti(n int, g [16]byte) error {
	f.ifcs[n].Multi = append(f.ifcs[n].Multi, g)
	return nil
}
func (f *fakeRouter) RemMulti(n int, g [16]byte) error {
	f.ifcs[n].Multi = nil
	return nil
}
func (f *fakeRouter) ProtoStats(kind int) ([]uint64, bool) {
	v := make([]uint64, 400)
	for i := range v {
		v[i] = uint64(i + 1)
	}
	return v, true
}
func (f *fakeRouter) Socket(kind int) (platform.Socket, error) {
	s := &fakeSock{}
	f.socks = append(f.socks, s)
	return s, nil
}

// fakeSock is a socket that only records its multicast groups.
type fakeSock struct{ multi [][2][16]byte }

func (*fakeSock) Bind([16]byte, uint16, bool) error { return nil }
func (*fakeSock) Listen() error                     { return nil }
func (*fakeSock) Accept(<-chan struct{}) (platform.Socket, [16]byte, uint16, error) {
	return nil, [16]byte{}, 0, Error("no")
}
func (*fakeSock) Connect(<-chan struct{}, [16]byte, uint16) error { return nil }
func (*fakeSock) Recv(<-chan struct{}, []byte, []byte) (int, error) {
	return 0, nil
}
func (*fakeSock) Send(_ <-chan struct{}, b, _ []byte) (int, error) { return len(b), nil }
func (*fakeSock) LocalAddr() ([16]byte, uint16)                    { return [16]byte{}, 0 }
func (*fakeSock) Hangup() error                                    { return nil }
func (*fakeSock) Close() error                                     { return nil }
func (*fakeSock) Keepalive(int) error                              { return nil }
func (s *fakeSock) AddMulti(g, ia [16]byte) error {
	s.multi = append(s.multi, [2][16]byte{g, ia})
	return nil
}
func (s *fakeSock) RemMulti(g, ia [16]byte) error {
	s.multi = nil
	return nil
}

func v4addr(a, b, c, d byte) [16]byte {
	return [16]byte{10: 0xff, 11: 0xff, 12: a, 13: b, 14: c, 15: d}
}

func writeFile(t *testing.T, p *Prog, fd int, s string) string {
	t.Helper()
	_, err := p.Write(fd, []byte(s))
	return core_errstr(err)
}

// TestIproute checks that iproute starts with a router's routes and
// gives it the table after each change.
func TestIproute(t *testing.T) {
	st := &fakeRouter{fakeIfcs: &fakeIfcs{etherAt: -1}}
	p := ipifcProg(t, st)
	if st.sets != 1 || len(st.routes) != 1 {
		t.Fatalf("seeded: %d sets, %v", st.sets, st.routes)
	}
	if got := readFile(t, p, "#I/iproute"); got != "0.0.0.0         /96  10.0.2.2        4    none   -\n" {
		t.Errorf("iproute: %q", got)
	}
	fd, err := p.Open("#I/iproute", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"tag rip", "add 192.168.5.0 255.255.255.0 10.0.2.9", "add 2001:db8:: /32 fec0::2"} {
		if e := writeFile(t, p, fd, c); e != "" {
			t.Fatalf("%s: %s", c, e)
		}
	}
	if st.sets != 3 || len(st.routes) != 3 {
		t.Errorf("after adds: %d sets, %v", st.sets, st.routes)
	}
	l := readFile(t, p, "#I/iproute")
	if !strings.Contains(l, "\n192.168.5.0     /120 10.0.2.9        4     rip   -\n") ||
		!strings.Contains(l, "2001:db8::      /32  fec0::2         6     rip   -\n") {
		t.Errorf("iproute:\n%s", l)
	}
	var tags []string
	for _, r := range st.routes {
		tags = append(tags, FormatIP(r.Addr)+" "+r.Tag)
	}
	if strings.Join(tags, ",") != "0.0.0.0 none,192.168.5.0 rip,2001:db8:: rip" {
		t.Errorf("routes given: %v", tags)
	}
	writeFile(t, p, fd, "remove 192.168.5.0 255.255.255.0")
	if len(st.routes) != 2 {
		t.Errorf("after remove: %v", st.routes)
	}
	writeFile(t, p, fd, "flush rip")
	if len(st.routes) != 1 || FormatIP(st.routes[0].Gate) != "10.0.2.2" {
		t.Errorf("after flush rip: %v", st.routes)
	}
	writeFile(t, p, fd, "flush")
	if len(st.routes) != 0 || readFile(t, p, "#I/iproute") != "" {
		t.Errorf("after flush: %v", st.routes)
	}
	p.Close(fd)
}

// TestIpifcRouting checks iprouting, addmulti and remmulti on an
// interface and on a conversation, and the protocols' stats files.
func TestIpifcRouting(t *testing.T) {
	st := &fakeRouter{fakeIfcs: &fakeIfcs{etherAt: -1}}
	p := ipifcProg(t, st)
	ctl, err := p.Open("#I/ipifc/clone", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ msg, err string }{
		{"addmulti 224.0.0.9", ""},
		{"bind ether /net/ether0", ""},
		{"add 10.0.2.15 255.255.255.0", ""},
		{"add fec0::15 /64", ""},
		{"iprouting", ""},
		{"addmulti 224.0.0.9", ""},
		{"addmulti 10.0.0.1", abi.Ebadarg},
		{"addmulti ff02::9", ""},
		{"addmulti", abi.Ebadarg},
	} {
		if got := writeFile(t, p, ctl, c.msg); got != c.err {
			t.Errorf("%q: got %q, want %q", c.msg, got, c.err)
		}
	}
	if !st.forward {
		t.Error("iprouting did not turn forwarding on")
	}
	writeFile(t, p, ctl, "iprouting 0")
	if st.forward {
		t.Error("iprouting 0 did not turn forwarding off")
	}
	local := readFile(t, p, "#I/ipifc/0/local")
	lines := strings.Split(local, "\n")
	if f := strings.Fields(lines[0]); len(f) < 3 || f[0] != "10.0.2.15" || f[2] != "224.0.0.9" {
		t.Errorf("local: %q", local)
	}
	if f := strings.Fields(lines[1]); len(f) < 3 || f[0] != "fec0::15" || f[2] != "ff02::9" {
		t.Errorf("local: %q", local)
	}
	status := readFile(t, p, "#I/ipifc/0/status")
	if !strings.Contains(status, fmt.Sprintf("\n\t%-40s %-10s %-40s ", "fec0::15", "/64", "fec0::")) {
		t.Errorf("status: %q", status)
	}
	p.Close(ctl)

	// A conversation's addmulti.
	cfd, err := p.Open("#I/udp/clone", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ msg, err string }{
		{"addmulti", "addmulti needs interface address"},
		{"addmulti 10.0.2.15", "addmulti for a non multicast address"},
		{"addmulti 10.0.2.15 10.0.0.1", "addmulti for a non multicast address"},
		{"addmulti 10.0.2.15 224.0.0.9", ""},
		{"addmulti 10.0.2.15 224.0.0.9", ""},
		{"remmulti 10.0.2.15", "remmulti for a non multicast address"},
	} {
		if got := writeFile(t, p, cfd, c.msg); got != c.err {
			t.Errorf("%q: got %q, want %q", c.msg, got, c.err)
		}
	}
	s := st.socks[len(st.socks)-1]
	if len(s.multi) != 1 || FormatIP(s.multi[0][0]) != "224.0.0.9" || FormatIP(s.multi[0][1]) != "10.0.2.15" {
		t.Errorf("socket groups %v", s.multi)
	}
	p.Close(cfd)

	tcp := readFile(t, p, "#I/tcp/stats")
	if !strings.HasPrefix(tcp, "MaxConn: 4096\nActiveOpens: 2\nPassiveOpens: 3\n") || !strings.HasSuffix(tcp, "\nOutOfOrder: 15\n") {
		t.Errorf("tcp stats:\n%s", tcp)
	}
	if got := readFile(t, p, "#I/udp/stats"); got != "InDatagrams: 1\nNoPorts: 2\nInErrors: 3\nOutDatagrams: 4\n" {
		t.Errorf("udp stats:\n%s", got)
	}
	icmp := strings.Split(readFile(t, p, "#I/icmp/stats"), "\n")
	if len(icmp) != 6+19+1 || icmp[0] != "InMsgs: 1" || icmp[6] != "EchoReply: 7 8" || icmp[7] != "1: 9 10" ||
		icmp[24] != "AddrMaskReply  : 43 44" {
		t.Errorf("icmp stats:\n%s", strings.Join(icmp, "\n"))
	}
	icmp6 := readFile(t, p, "#I/icmpv6/stats")
	if !strings.HasPrefix(icmp6, "InMsgs: 1\n") || !strings.Contains(icmp6, "\nRouterAddrErrs: 12\nEchoReply: 13 14\nUnreachableV6: 15 16\n") ||
		!strings.HasSuffix(icmp6, "\nRedirectV6: 287 288\n") {
		t.Errorf("icmpv6 stats:\n%s", icmp6)
	}
	if got := names(t, p, "#I"); got != "udp tcp icmp icmpv6 ipifc arp ndb iproute" {
		t.Errorf("#I: %q", got)
	}
}

// TestFormatIP checks Inferno's %I: IPv4 dotted, IPv6 with the longest
// run of zero groups, even one, elided.
func TestFormatIP(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.2.15": "10.0.2.15", "::": "::", "::1": "::1", "fec0::15": "fec0::15",
		"2001:db8:0:1:2:3:4:5": "2001:db8::1:2:3:4:5", "1::": "1::", "1:0:0:1:0:0:0:1": "1:0:0:1::1",
		"fe80::2:0:0:1": "fe80::2:0:0:1",
	} {
		a, _ := ParseIP(in)
		if got := FormatIP(a); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}
