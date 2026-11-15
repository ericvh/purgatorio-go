// Command ether checks #l and ipifc's snoop on the kernel: it connects
// to ARP frames on /net/ether0, asks who has the gateway (10.0.2.2,
// QEMU user networking's) and reads the reply; then it sends a UDP
// datagram to the broadcast address and finds it in /net/ipifc/0/snoop.
package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	local   = netip.MustParseAddr("10.0.2.15")
	gateway = netip.MustParseAddr("10.0.2.2")
)

func fail(f string, a ...any) {
	fmt.Printf("ether: "+f+"\n", a...)
	sys.Exits("fail:ether")
}

func open(name string, mode int) *os.File {
	fd, err := sys.Open(name, mode)
	if err != nil {
		fail("open %s: %s", name, sys.Errstr())
	}
	return os.NewFile(uintptr(fd), name)
}

func main() {
	a, err := os.ReadFile("/net/ether0/addr")
	if err != nil {
		fail("addr: %v", err)
	}
	ea, err := hex.DecodeString(string(a))
	if err != nil || len(ea) != 6 {
		fail("addr %q", a)
	}
	ctl := open("/net/ether0/clone", sys.ORDWR)
	b := make([]byte, 2048)
	n, _ := ctl.Read(b)
	dir := "/net/ether0/" + strings.TrimSpace(string(b[:n]))
	if _, err := ctl.Write([]byte("connect 2054")); err != nil {
		fail("connect: %s", sys.Errstr())
	}
	data := open(dir+"/data", sys.ORDWR)
	t, _ := os.ReadFile(dir + "/type")
	fmt.Printf("%s type %s\n", dir, strings.TrimSpace(string(t)))

	// An ARP request, padded to the minimum frame.
	req := make([]byte, 60)
	copy(req[0:], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(req[6:], ea)
	req[12], req[13] = 0x08, 0x06
	arp := req[14:]
	copy(arp, []byte{0, 1, 8, 0, 6, 4, 0, 1})
	copy(arp[8:], ea)
	l4, g4 := local.As4(), gateway.As4()
	copy(arp[14:], l4[:])
	copy(arp[24:], g4[:])
	if _, err := data.Write(req); err != nil {
		fail("write request: %s", sys.Errstr())
	}
	for i := 0; ; i++ {
		if i == 50 {
			fail("no ARP reply")
		}
		n, err := data.Read(b)
		if err != nil || n < 42 {
			fail("read: %v %d", err, n)
		}
		f := b[:n]
		if f[12] != 0x08 || f[13] != 0x06 {
			fail("frame of type %02x%02x", f[12], f[13])
		}
		p := f[14:]
		// The reply comes back from the gateway; our own request comes
		// back too, as a broadcast.
		if p[7] == 2 && bytes.Equal(p[14:18], g4[:]) && bytes.Equal(f[0:6], ea) {
			fmt.Printf("arp reply %s is at %x\n", gateway, p[8:14])
			break
		}
	}
	data.Close()
	ctl.Close()

	snoop := open("/net/ipifc/0/snoop", sys.OREAD)
	c, err := dial.Dial("udp!10.0.2.255!9", "")
	if err != nil {
		fail("dial: %v", err)
	}
	msg := []byte("snooped datagram")
	if _, err := c.Dfd.Write(msg); err != nil {
		fail("udp write: %s", sys.Errstr())
	}
	for i := 0; ; i++ {
		if i == 50 {
			fail("datagram not snooped")
		}
		n, err := snoop.Read(b)
		if err != nil || n == 0 {
			fail("snoop read: %v", err)
		}
		p := b[:n]
		if n >= 28 && p[0]>>4 == 4 && p[9] == 17 && bytes.HasSuffix(p, msg) {
			src, _ := netip.AddrFromSlice(p[12:16])
			dst, _ := netip.AddrFromSlice(p[16:20])
			fmt.Printf("snoop udp %s -> %s %q\n", src, dst, p[28:])
			break
		}
	}
}
