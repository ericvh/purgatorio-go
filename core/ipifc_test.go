package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
)

// fakeIfcs is a NetStack with configurable interfaces and one medium,
// ether, as the kernel's netstack has.
type fakeIfcs struct {
	ifcs    []platform.Ifc
	etherAt int // the interface ether is bound to, or -1
}

func (*fakeIfcs) Socket(int) (platform.Socket, error)    { return nil, Error("no sockets") }
func (*fakeIfcs) LookupHost(string) ([]string, error)    { return nil, Error("no") }
func (*fakeIfcs) LookupAddr(string) ([]string, error)    { return nil, Error("no") }
func (*fakeIfcs) LookupPort(string, string) (int, error) { return 0, Error("no") }
func (f *fakeIfcs) Interfaces() []platform.Ifc           { return slices.Clone(f.ifcs) }
func (f *fakeIfcs) NewIfc() (int, error) {
	f.ifcs = append(f.ifcs, platform.Ifc{})
	return len(f.ifcs) - 1, nil
}
func (f *fakeIfcs) AddAddr(n int, a platform.IfcAddr, _ int) error {
	if f.ifcs[n].Dev == "" {
		return Error("ipifc not yet bound to device")
	}
	if !slices.Contains(f.ifcs[n].Addrs, a) {
		f.ifcs[n].Addrs = append(f.ifcs[n].Addrs, a)
	}
	return nil
}
func (f *fakeIfcs) RemoveAddr(n int, a platform.IfcAddr) error {
	i := slices.Index(f.ifcs[n].Addrs, a)
	if i < 0 {
		return Error("address not on this interface")
	}
	f.ifcs[n].Addrs = slices.Delete(f.ifcs[n].Addrs, i, i+1)
	return nil
}
func (f *fakeIfcs) BindIfc(n int, medium string, args []string) error {
	if f.ifcs[n].Dev != "" {
		return Error("interface already bound")
	}
	if medium != "ether" {
		return Error("unknown interface type")
	}
	if f.etherAt >= 0 {
		return Error(abi.Einuse)
	}
	f.etherAt = n
	f.ifcs[n].Dev, f.ifcs[n].MTU = "/net/ether0", 1500
	return nil
}
func (f *fakeIfcs) UnbindIfc(n int) error {
	if f.etherAt == n {
		f.etherAt = -1
	}
	f.ifcs[n] = platform.Ifc{}
	return nil
}

// listOnlyStack lists interfaces but cannot change them, as the emu's
// host.
type listOnlyStack struct {
	platform.NetStack
	l *fakeIfcs
}

func (s listOnlyStack) Interfaces() []platform.Ifc { return s.l.Interfaces() }

func ipifcProg(t *testing.T, net platform.NetStack) *Prog {
	t.Helper()
	p := newTestProg(t)
	k := p.Kernel
	k.DevTab = NewDevTab(append(k.DevTab.Devs(), NewDevIP(k, net))...)
	p.SetPgrp(NewPgrp(k.DevTab, mustAttach(t, k)))
	return p
}

func readFile(t *testing.T, p *Prog, path string) string {
	t.Helper()
	fd, err := p.Open(path, abi.OREAD)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer p.Close(fd)
	var out []byte
	b := make([]byte, 4096)
	for {
		n, err := p.Read(fd, b)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if n == 0 {
			return string(out)
		}
		out = append(out, b[:n]...)
	}
}

const unboundStatus = "device  maxtu 0 sendra 0 recvra 0 mflag 0 oflag 0 maxraint 0 minraint 0 linkmtu 0 reachtime 0 rxmitra 0 ttl 0 routerlt 0 pktin 0 pktout 0 errin 0 errout 0\n\n"

func TestIpifcConfig(t *testing.T) {
	st := &fakeIfcs{etherAt: -1}
	p := ipifcProg(t, st)
	if got := names(t, p, "#I/ipifc"); got != "clone stats" {
		t.Errorf("empty ipifc: %q", got)
	}
	ctl, err := p.Open("#I/ipifc/clone", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 16)
	n, _ := p.Read(ctl, b)
	if string(b[:n]) != "0" {
		t.Errorf("clone gave %q", b[:n])
	}
	if got := readFile(t, p, "#I/ipifc/0/status"); got != unboundStatus {
		t.Errorf("unbound status: %q", got)
	}
	write := func(s string) string {
		_, err := p.Write(ctl, []byte(s))
		return core_errstr(err)
	}
	for _, c := range []struct{ msg, err string }{
		{"add 10.0.2.15 255.255.255.0", "ipifc not yet bound to device"},
		{"bind loopback", "unknown interface type"},
		{"bind ether /net/ether0", ""},
		{"bind ether /net/ether0", "interface already bound"},
		{"add 10.0.2.15 255.255.255.0", ""},
		{"add 10.0.2.15 255.255.255.0", ""},
		{"add 192.168.1.1", ""},
		{"add :: ::", ""},
		{"add nonsense", abi.Ebadarg},
		{"remove 10.0.2.15", abi.Ebadarg},
		{"remove 10.0.2.99 255.255.255.0", "address not on this interface"},
		{"remove :: ::", ""},
		{"iprouting 1", "unsupported ctl"},
		{"", "short control request"},
	} {
		if got := write(c.msg); got != c.err {
			t.Errorf("%q: got %q, want %q", c.msg, got, c.err)
		}
	}
	line := func(ip, mask, rem string) string {
		return fmt.Sprintf("\t%-40s %-10s %-40s %-12s %-12s\n", ip, mask, rem, "4294967295", "4294967295")
	}
	want := "device /net/ether0 maxtu 1500 sendra 0 recvra 0 mflag 0 oflag 0 maxraint 600000 minraint 200000 linkmtu 0 reachtime 0 rxmitra 0 ttl 255 routerlt 1800000 pktin 0 pktout 0 errin 0 errout 0\n" +
		line("10.0.2.15", "/120", "10.0.2.0") + line("192.168.1.1", "/120", "192.168.1.0")
	if got := readFile(t, p, "#I/ipifc/0/status"); got != want {
		t.Errorf("status:\n%q\nwant\n%q", got, want)
	}
	local := readFile(t, p, "#I/ipifc/0/local")
	f := strings.Fields(strings.SplitN(local, "\n", 2)[0])
	if strings.Join(f, " ") != "10.0.2.15 -> 255.255.255.255 10.0.0.0 10.255.255.255 10.0.2.0 10.0.2.255 10.0.2.15" {
		t.Errorf("local: %q", local)
	}
	// connect replaces the addresses.
	if got := write("connect 10.1.0.5 255.255.0.0"); got != "" {
		t.Errorf("connect: %s", got)
	}
	if got := st.ifcs[0].Addrs; len(got) != 1 || FormatIP(got[0].IP) != "10.1.0.5" || FormatIP(got[0].Remote) != "10.1.0.0" {
		t.Errorf("after connect: %v", got)
	}
	// A bound interface is kept when closed; clone makes another.
	p.Close(ctl)
	ctl, err = p.Open("#I/ipifc/clone", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	n, _ = p.Read(ctl, b)
	if string(b[:n]) != "1" {
		t.Errorf("second clone gave %q", b[:n])
	}
	if got := write("bind ether /net/ether0"); got != abi.Einuse {
		t.Errorf("ether bound twice: %q", got)
	}
	p.Close(ctl)
	// An unbound interface no one has open is reused.
	ctl, _ = p.Open("#I/ipifc/0/ctl", abi.OWRITE)
	if got := write("unbind"); got != "" {
		t.Errorf("unbind: %s", got)
	}
	p.Close(ctl)
	ctl, _ = p.Open("#I/ipifc/clone", abi.OREAD)
	n, _ = p.Read(ctl, b)
	if string(b[:n]) != "0" {
		t.Errorf("clone after unbind gave %q", b[:n])
	}
	p.Close(ctl)
	if got := names(t, p, "#I/ipifc/1"); got != "ctl local snoop status" {
		t.Errorf("ipifc/1: %q", got)
	}
	if _, err := p.Open("#I/ipifc/2/status", abi.OREAD); core_errstr(err) != abi.Enonexist {
		t.Errorf("ipifc/2: %v", err)
	}
	if _, err := p.Open("#I/ipifc/stats", abi.OREAD); err != nil {
		t.Error(err)
	} else if _, err := p.Pread(mustOpen(t, p, "#I/ipifc/stats"), b, 0); core_errstr(err) != "stats not implemented" {
		t.Errorf("stats: %v", err)
	}
}

func mustOpen(t *testing.T, p *Prog, path string) int {
	t.Helper()
	fd, err := p.Open(path, abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

// TestIpifcListOnly checks a stack that only lists its interfaces: they
// can be read, but clone and writes to ctl are refused.
func TestIpifcListOnly(t *testing.T) {
	lo := platform.IfcAddr{IP: [16]byte{10: 0xff, 11: 0xff, 12: 127, 15: 1}}
	lo.Mask, _ = parseipmask("255.0.0.0")
	lo.Remote = mask16(lo.IP, lo.Mask)
	l := &fakeIfcs{ifcs: []platform.Ifc{{Dev: "lo", MTU: 65536, Addrs: []platform.IfcAddr{lo}}}}
	p := ipifcProg(t, listOnlyStack{l: l})
	if got := names(t, p, "#I/ipifc"); got != "0 clone stats" {
		t.Errorf("ipifc: %q", got)
	}
	st := readFile(t, p, "#I/ipifc/0/status")
	if !strings.HasPrefix(st, "device lo maxtu 65536 ") || !strings.Contains(st, "\n\t127.0.0.1 ") || !strings.Contains(st, " /104 ") {
		t.Errorf("status: %q", st)
	}
	if got := readFile(t, p, "#I/ipifc/0/ctl"); got != "0" {
		t.Errorf("ctl: %q", got)
	}
	if _, err := p.Open("#I/ipifc/0/ctl", abi.OWRITE); core_errstr(err) != abi.Eperm {
		t.Errorf("open ctl for writing: %v", err)
	}
	if _, err := p.Open("#I/ipifc/clone", abi.ORDWR); core_errstr(err) != abi.Eperm {
		t.Errorf("clone: %v", err)
	}
	if _, err := p.Open("#I/ipifc/0/status", abi.OWRITE); core_errstr(err) != abi.Eperm {
		t.Errorf("open status for writing: %v", err)
	}
}
