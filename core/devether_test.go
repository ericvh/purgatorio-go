package core

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
)

var (
	testEA   = [6]byte{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}
	testPeer = [6]byte{0x52, 0x55, 0x0a, 0x00, 0x02, 0x02}
)

// frame makes a minimum-size frame from src to dst of type typ.
func frame(dst, src [6]byte, typ int, body string) []byte {
	f := make([]byte, etherMinTU)
	copy(f, dst[:])
	copy(f[6:], src[:])
	f[12], f[13] = byte(typ>>8), byte(typ)
	copy(f[14:], body)
	return f
}

type wire struct {
	mu        sync.Mutex
	sent      [][]byte
	stackGot  [][]byte
	snoopSent func([]byte)
}

func (w *wire) transmit(f []byte) {
	w.mu.Lock()
	w.sent = append(w.sent, bytes.Clone(f))
	w.mu.Unlock()
}

func (w *wire) stack(f []byte) {
	w.mu.Lock()
	w.stackGot = append(w.stackGot, bytes.Clone(f))
	w.mu.Unlock()
}

func etherProg(t *testing.T) (*Prog, *Ether, *wire) {
	t.Helper()
	p := newTestProg(t)
	k := p.Kernel
	w := &wire{}
	e := NewEther("ether0", testEA, 1000, w.transmit)
	e.SetStack(w.stack)
	k.DevTab = NewDevTab(append(k.DevTab.Devs(), NewDevEther(k, e))...)
	p.SetPgrp(NewPgrp(k.DevTab, mustAttach(t, k)))
	return p, e, w
}

// conn clones a connection and connects it to typ, giving its ctl and
// data fds and its directory.
func conn(t *testing.T, p *Prog, typ string) (int, int, string) {
	t.Helper()
	ctl, err := p.Open("#l/ether0/clone", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 32)
	n, err := p.Read(ctl, b)
	if err != nil {
		t.Fatal(err)
	}
	dir := "#l/ether0/" + strings.TrimSpace(string(b[:n]))
	if _, err := p.Write(ctl, []byte("connect "+typ)); err != nil {
		t.Fatalf("connect %s: %v", typ, err)
	}
	data, err := p.Open(dir+"/data", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	return ctl, data, dir
}

func TestEtherFiles(t *testing.T) {
	p, _, _ := etherProg(t)
	if got := names(t, p, "#l"); got != "ether0" {
		t.Errorf("#l: %q", got)
	}
	if got := names(t, p, "#l/ether0"); got != "clone addr stats ifstats" {
		t.Errorf("#l/ether0: %q", got)
	}
	if got := readFile(t, p, "#l/ether0/addr"); got != "525400123456" {
		t.Errorf("addr: %q", got)
	}
	_, _, dir := conn(t, p, "2054")
	if dir != "#l/ether0/0" {
		t.Errorf("first connection %s", dir)
	}
	if got := names(t, p, "#l/ether0"); got != "clone addr stats ifstats 0" {
		t.Errorf("#l/ether0 with a connection: %q", got)
	}
	if got := names(t, p, dir); got != "data ctl stats type ifstats" {
		t.Errorf("%s: %q", dir, got)
	}
	if got := readFile(t, p, dir+"/type"); got != "       2054 " {
		t.Errorf("type: %q", got)
	}
	if got := readFile(t, p, dir+"/ctl"); got != "          0 " {
		t.Errorf("ctl: %q", got)
	}
	if got := readFile(t, p, dir+"/ifstats"); got != "" {
		t.Errorf("ifstats: %q", got)
	}
	d, err := p.Stat(dir + "/data")
	if err != nil || d.UID != "inferno" || d.Mode != 0o660 {
		t.Errorf("data: %+v %v", d, err)
	}
	if _, err := p.Open("#l1", abi.OREAD); core_errstr(err) != abi.Enodev {
		t.Errorf("#l1: %v", err)
	}
}

func TestEtherFrames(t *testing.T) {
	p, e, w := etherProg(t)
	ctl, arp, _ := conn(t, p, "2054")
	_, all, _ := conn(t, p, "-1")
	read := func(fd int) []byte {
		t.Helper()
		b := make([]byte, 2000)
		n, err := p.Read(fd, b)
		if err != nil {
			t.Fatal(err)
		}
		return b[:n]
	}
	if _, err := p.Write(ctl, []byte("connect 2054")); core_errstr(err) != abi.Einuse {
		t.Errorf("second connect 2054: %v", err)
	}
	if _, err := p.Write(ctl, []byte("frobnicate")); core_errstr(err) != abi.Ebadctl {
		t.Errorf("bad ctl: %v", err)
	}

	// From the wire: an IP frame for the interface reaches only the
	// connection of every type; ARP to it reaches both; a unicast frame
	// for another station reaches neither. The stack gets them all.
	e.Receive(frame(testEA, testPeer, 0x800, "ip"))
	e.Receive(frame(testEA, testPeer, 0x806, "arp reply"))
	e.Receive(frame(testPeer, testPeer, 0x806, "not for us"))
	if got := read(arp); !bytes.Contains(got, []byte("arp reply")) {
		t.Errorf("arp connection read %q", got)
	}
	if got := read(all); !bytes.Contains(got, []byte("ip")) || etherType(got) != 0x800 {
		t.Errorf("all connection read %q", got)
	}
	if got := read(all); !bytes.Contains(got, []byte("arp reply")) {
		t.Errorf("all connection read %q", got)
	}
	if len(w.stackGot) != 3 {
		t.Errorf("stack got %d frames", len(w.stackGot))
	}

	// Out: the source becomes the interface's address. A broadcast goes
	// on the wire, comes back to the connections and goes to the stack.
	bcast := [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if _, err := p.Write(arp, frame(bcast, [6]byte{}, 0x806, "who has")); err != nil {
		t.Fatal(err)
	}
	if len(w.sent) != 1 || [6]byte(w.sent[0][6:12]) != testEA {
		t.Fatalf("sent %q", w.sent)
	}
	if got := read(arp); !bytes.Contains(got, []byte("who has")) {
		t.Errorf("arp connection read %q", got)
	}
	if len(w.stackGot) != 4 {
		t.Errorf("stack got %d frames", len(w.stackGot))
	}
	// A frame for the interface itself does not go on the wire.
	if _, err := p.Write(arp, frame(testEA, [6]byte{}, 0x806, "to me")); err != nil {
		t.Fatal(err)
	}
	if len(w.sent) != 1 {
		t.Errorf("a frame for the interface went on the wire")
	}
	if got := read(arp); !bytes.Contains(got, []byte("to me")) {
		t.Errorf("arp connection read %q", got)
	}
	if _, err := p.Write(arp, make([]byte, 20)); core_errstr(err) != abi.Etoosmall {
		t.Errorf("short frame: %v", err)
	}
	if _, err := p.Write(arp, make([]byte, 1515)); core_errstr(err) != abi.Etoobig {
		t.Errorf("long frame: %v", err)
	}

	// Frames the stack sends go on the wire; a promiscuous connection
	// hears them and unicast frames for other stations.
	pctl, prom, _ := conn(t, p, "2048")
	if _, err := p.Write(pctl, []byte("promiscuous")); err != nil {
		t.Fatal(err)
	}
	e.Output(frame(testPeer, testEA, 0x800, "from the stack"))
	if len(w.sent) != 2 {
		t.Errorf("stack output not sent")
	}
	if got := read(prom); !bytes.Contains(got, []byte("from the stack")) {
		t.Errorf("promiscuous read %q", got)
	}
	e.Receive(frame(testPeer, testPeer, 0x800, "overheard"))
	if got := read(prom); !bytes.Contains(got, []byte("overheard")) {
		t.Errorf("promiscuous read %q", got)
	}
	stats := readFile(t, p, "#l/ether0/stats")
	if !strings.HasPrefix(stats, "in: 7\nlink: 1\nout: 3\n") || !strings.Contains(stats, "prom: 1\nmbps: 1000\naddr: 525400123456\n") {
		t.Errorf("stats:\n%s", stats)
	}
}

func TestEtherHeadersOnly(t *testing.T) {
	p, e, _ := etherProg(t)
	ctl, data, _ := conn(t, p, "-1")
	if _, err := p.Write(ctl, []byte("headersonly")); err != nil {
		t.Fatal(err)
	}
	f := append(frame(testEA, testPeer, 0x800, "x"), make([]byte, 40)...)
	e.Receive(f)
	b := make([]byte, 2000)
	n, err := p.Read(data, b)
	if err != nil || n != 64 || !bytes.Equal(b[:58], f[:58]) || int(b[58])<<8|int(b[59]) != len(f) {
		t.Errorf("headers only read %d %v: %x", n, err, b[:n])
	}
}

func TestEtherMulticast(t *testing.T) {
	p, e, _ := etherProg(t)
	ctl, data, _ := conn(t, p, "-1")
	group := [6]byte{0x01, 0x00, 0x5e, 0x00, 0x00, 0x01}
	other := [6]byte{0x01, 0x00, 0x5e, 0x00, 0x00, 0x02}
	if _, err := p.Write(ctl, []byte("addmulti 01:00:5e:00:00:01")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(ctl, []byte("addmulti nonsense")); core_errstr(err) != "bad address" {
		t.Errorf("bad multicast address: %v", err)
	}
	e.Receive(frame(other, testPeer, 0x800, "other group"))
	e.Receive(frame(group, testPeer, 0x800, "our group"))
	b := make([]byte, 2000)
	n, _ := p.Read(data, b)
	if !bytes.Contains(b[:n], []byte("our group")) {
		t.Errorf("multicast read %q", b[:n])
	}
}

// snoopStack is a NetStack whose interface 0 copies packets to its
// snoopers.
type snoopStack struct {
	fakeIfcs
	mu      sync.Mutex
	deliver func([]byte)
	stopped bool
}

func (s *snoopStack) SnoopIfc(n int, deliver func([]byte)) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliver = deliver
	return func() {
		s.mu.Lock()
		s.deliver, s.stopped = nil, true
		s.mu.Unlock()
	}, nil
}

var _ platform.IfcSnooper = (*snoopStack)(nil)

func TestIpifcSnoop(t *testing.T) {
	st := &snoopStack{fakeIfcs: fakeIfcs{ifcs: []platform.Ifc{{Dev: "/net/ether0", MTU: 1500}}, etherAt: 0}}
	p := ipifcProg(t, st)
	if got := names(t, p, "#I/ipifc/0"); got != "ctl local snoop status" {
		t.Errorf("ipifc/0: %q", got)
	}
	if _, err := p.Open("#I/ipifc/0/snoop", abi.ORDWR); core_errstr(err) != abi.Eperm {
		t.Errorf("snoop opened for writing: %v", err)
	}
	fd, err := p.Open("#I/ipifc/0/snoop", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.deliver([]byte("packet one"))
	st.deliver([]byte("packet two"))
	st.mu.Unlock()
	b := make([]byte, 100)
	for _, want := range []string{"packet one", "packet two"} {
		n, err := p.Read(fd, b)
		if err != nil || string(b[:n]) != want {
			t.Errorf("snoop read %q %v, want %q", b[:n], err, want)
		}
	}
	p.Close(fd)
	if !st.stopped {
		t.Errorf("snoop not stopped at the last close")
	}
}
