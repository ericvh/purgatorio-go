package netstack

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"sync"
	"sync/atomic"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
)

// /net/icmp and /net/icmpv6 over gVisor's raw ICMP endpoints, as
// Inferno's icmp.c and icmp6.c. A conversation sends any ICMP message
// behind an IP header (20 bytes for IPv4, 40 for IPv6), which is
// ignored: the message goes to the remote address with the
// conversation's local port as its ident, and the stack fills in the
// header. A read gives one message received behind a header that gives
// its ttl or hop limit, source and destination. The stack answers echo
// requests and neighbour discovery itself and gives each raw endpoint a
// copy of every ICMP message; a conversation keeps those whose ident is
// its local port and whose source is its remote address (router
// solicitations and advertisements: whose type is its local port and
// whose destination is its remote address). A destination unreachable
// or ttl exceeded message about one of its own messages hangs it up
// with Inferno's text, as icmpadvise does.

// IP header lengths in front of /net/icmp's and /net/icmpv6's messages.
const (
	icmpIPHdr  = 20
	icmp6IPHdr = 40
)

// icmpState holds the idents in use and the messages sent through raw
// endpoints, which gVisor does not count.
type icmpState struct {
	mu     sync.Mutex
	idents map[uint16]bool
	next   uint16
	out    [2][256]atomic.Uint64 // IPv4, IPv6 messages sent, by type
}

// icmpConv is an ICMP conversation's addresses and ident.
type icmpConv struct {
	v6           bool
	ident        uint16
	laddr, raddr tcpip.Address
	err          error // the advice that hung it up
	buf          []byte
}

func (s *Stack) icmpSocket(kind int) (platform.Socket, error) {
	so := &sock{st: s, kind: kind, wq: new(waiter.Queue), icmp: &icmpConv{v6: kind == platform.ICMPv6}}
	np, tp := tcpip.NetworkProtocolNumber(ipv4.ProtocolNumber), tcpip.TransportProtocolNumber(icmp.ProtocolNumber4)
	if so.icmp.v6 {
		np, tp = ipv6.ProtocolNumber, icmp.ProtocolNumber6
	}
	ep, err := s.g.Stack.NewRawEndpoint(tp, np, so.wq, true)
	if err != nil {
		return nil, soerr(err)
	}
	so.ep = ep
	ops := ep.SocketOptions()
	if so.icmp.v6 {
		ops.SetReceiveHopLimit(true)
		ops.SetIPv6ReceivePacketInfo(true)
	}
	return so, nil
}

// ident takes a free ident, at random from 5000 to 32767 at first, as
// setlport takes a port, or the one asked for.
func (s *Stack) ident(want uint16) (uint16, error) {
	st := &s.icmp
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.idents == nil {
		st.idents = map[uint16]bool{}
	}
	if want != 0 {
		if st.idents[want] {
			return 0, core.Error("address in use")
		}
		st.idents[want] = true
		return want, nil
	}
	for {
		for st.next < 5000 {
			st.next = uint16(rand.IntN(1 << 15))
		}
		if !st.idents[st.next] {
			break
		}
		st.next++
	}
	id := st.next
	st.next++
	st.idents[id] = true
	return id, nil
}

func (s *Stack) icmpRelease(c *icmpConv) {
	st := &s.icmp
	st.mu.Lock()
	defer st.mu.Unlock()
	if c.ident != 0 {
		delete(st.idents, c.ident)
		c.ident = 0
	}
}

// family checks that an address is of the conversation's protocol.
func (c *icmpConv) family(a tcpip.Address) error {
	if a.Len() != 0 && (a.Len() == 16) != c.v6 {
		return core.Error(abi.Enetunreach)
	}
	return nil
}

func (s *sock) icmpBind(addr [16]byte, port uint16) error {
	c := s.icmp
	a := toAddr(addr)
	if err := c.family(a); err != nil {
		return err
	}
	if c.ident == 0 || port != 0 && port != c.ident {
		s.st.icmpRelease(c)
		id, err := s.st.ident(port)
		if err != nil {
			return err
		}
		c.ident = id
	}
	if a.Len() != 0 {
		if err := s.ep.Bind(tcpip.FullAddress{Addr: a}); err != nil {
			return soerr(err)
		}
		c.laddr = a
	}
	return nil
}

func (s *sock) icmpConnect(addr [16]byte) error {
	c := s.icmp
	a := toAddr(addr)
	if err := c.family(a); err != nil {
		return err
	}
	if a.Len() == 0 {
		a = tcpip.AddrFrom4([4]byte{127, 0, 0, 1})
		if c.v6 {
			a = tcpip.AddrFrom16([16]byte{15: 1})
		}
	}
	if c.ident == 0 {
		id, err := s.st.ident(0)
		if err != nil {
			return err
		}
		c.ident = id
	}
	np := netproto(a)
	if c.laddr.Len() == 0 {
		r, err := s.st.g.Stack.FindRoute(0, tcpip.Address{}, a, np, false)
		switch {
		case err == nil:
			c.laddr = r.LocalAddress()
			r.Release()
		case !header.IsV4MulticastAddress(a) && !header.IsV6MulticastAddress(a):
			return soerr(err)
		}
	}
	c.raddr = a
	return nil
}

func (s *sock) icmpLocal() ([16]byte, uint16) {
	return fromAddr(s.icmp.laddr), s.icmp.ident
}

// icmpSend sends the message behind buf's IP header, with the
// conversation's ident, as icmpkick and icmpkick6.
func (s *sock) icmpSend(stop <-chan struct{}, buf []byte) (int, error) {
	c := s.icmp
	hl := icmpIPHdr
	if c.v6 {
		hl = icmp6IPHdr
	}
	if len(buf) < hl+8 {
		return 0, core.Error(abi.Eshort)
	}
	if c.err != nil {
		return 0, c.err
	}
	if c.raddr.Len() == 0 {
		return 0, core.Error("not connected")
	}
	msg := bytes.Clone(buf[hl:])
	binary.BigEndian.PutUint16(msg[4:], c.ident)
	msg[2], msg[3] = 0, 0
	if !c.v6 {
		// The stack fills in the ICMPv6 checksum itself.
		binary.BigEndian.PutUint16(msg[2:], cksum(msg))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	to := tcpip.FullAddress{Addr: c.raddr}
	err := s.wait(stop, waiter.WritableEvents, func() tcpip.Error {
		_, e := s.ep.Write(bytes.NewReader(msg), tcpip.WriteOptions{To: &to})
		return e
	})
	if err != nil {
		return 0, err
	}
	v := 0
	if c.v6 {
		v = 1
	}
	s.st.icmp.out[v][msg[0]].Add(1)
	return len(buf), nil
}

// cksum is the Internet checksum of b.
func cksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum > 0xffff {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// unreachcode and unreachcode6 are the texts of destination unreachable
// codes, as icmp.c's and icmp6.c's.
var (
	unreachcode = []string{"net unreachable", "host unreachable", "protocol unreachable", "port unreachable",
		"fragmentation needed and DF set", "source route failed"}
	unreachcode6 = []string{"no route to destination", "comm with destination administratively prohibited",
		"icmp unreachable: unassigned error code (2)", "address unreachable", "port unreachable",
		"icmp unreachable: unknown code"}
)

// icmpRecv reads the next message for the conversation into buf behind
// an IP header.
func (s *sock) icmpRecv(stop <-chan struct{}, buf []byte) (int, error) {
	c := s.icmp
	hl := icmpIPHdr
	if c.v6 {
		hl = icmp6IPHdr
	}
	if len(buf) < hl {
		return 0, core.Error(abi.Eshort)
	}
	if c.buf == nil {
		c.buf = make([]byte, 1<<16)
	}
	for {
		if c.err != nil {
			return 0, c.err
		}
		var res tcpip.ReadResult
		err := s.wait(stop, waiter.ReadableEvents, func() tcpip.Error {
			w := tcpip.SliceWriter(c.buf)
			var e tcpip.Error
			res, e = s.ep.Read(&w, tcpip.ReadOptions{NeedRemoteAddr: true})
			return e
		})
		if err != nil {
			return 0, err
		}
		pkt := c.buf[:res.Count]
		var n int
		if c.v6 {
			n = c.input6(buf, pkt, res)
		} else {
			n = c.input4(buf, pkt)
		}
		if n > 0 {
			return n, nil
		}
	}
}

// input4 takes an IPv4 packet: it copies a message for the
// conversation into buf, behind its IP header cut to 20 bytes, and
// returns its length, or 0.
func (c *icmpConv) input4(buf, pkt []byte) int {
	if len(pkt) < icmpIPHdr {
		return 0
	}
	ihl := int(pkt[0]&0xf) * 4
	if ihl < icmpIPHdr || len(pkt) < ihl+8 {
		return 0
	}
	src := tcpip.AddrFrom4([4]byte(pkt[12:16]))
	msg := pkt[ihl:]
	switch msg[0] {
	case 8: // echo request: the stack answers
		return 0
	case 3, 11: // unreachable, ttl exceeded
		if msg[0] == 3 || msg[1] == 0 {
			// Advice about a message of ours (inner IP header and
			// the start of its ICMP message)?
			in := msg[8:]
			if len(in) >= icmpIPHdr && in[9] == 1 {
				iihl := int(in[0]&0xf) * 4
				if len(in) >= iihl+8 && binary.BigEndian.Uint16(in[iihl+4:]) == c.ident &&
					tcpip.AddrFrom4([4]byte(in[16:20])) == c.raddr {
					if code := int(msg[1]); msg[0] == 3 && code > 5 {
						c.err = core.Error(unreachcode[1])
					} else if msg[0] == 3 {
						c.err = core.Error(unreachcode[code])
					} else {
						c.err = core.Error("ttl exceeded at " + src.String())
					}
				}
			}
			return 0
		}
	}
	if binary.BigEndian.Uint16(msg[4:]) != c.ident || src != c.raddr {
		return 0
	}
	n := copy(buf[icmpIPHdr:], msg)
	copy(buf, pkt[:icmpIPHdr])
	buf[0] = 0x45
	binary.BigEndian.PutUint16(buf[2:], uint16(icmpIPHdr+n))
	return icmpIPHdr + n
}

// input6 is input4 for an ICMPv6 message, which gVisor gives without
// its IP header: the header is made from the hop limit and addresses.
func (c *icmpConv) input6(buf, msg []byte, res tcpip.ReadResult) int {
	if len(msg) < 8 {
		return 0
	}
	src := res.RemoteAddr.Addr
	dst := c.laddr
	if res.ControlMessages.HasIPv6PacketInfo {
		dst = res.ControlMessages.IPv6PacketInfo.Addr
	}
	key, from := binary.BigEndian.Uint16(msg[4:]), src
	switch msg[0] {
	case 128, 135, 136: // echo request, neighbour discovery: the stack's
		return 0
	case 133, 134: // router solicitation, advertisement
		key, from = uint16(msg[0]), dst
	case 1, 3: // unreachable, time exceeded
		if msg[0] == 1 || msg[1] == 0 {
			in := msg[8:]
			if len(in) >= icmp6IPHdr+8 && in[6] == 58 && binary.BigEndian.Uint16(in[icmp6IPHdr+4:]) == c.ident &&
				tcpip.AddrFrom16([16]byte(in[24:40])) == c.raddr {
				if msg[0] == 1 {
					c.err = core.Error(unreachcode6[min(int(msg[1]), len(unreachcode6)-1)])
				} else {
					c.err = core.Error("ttl exceeded at " + core.FormatIP(src.As16()))
				}
			}
			return 0
		}
	}
	if key != c.ident || from != c.raddr {
		return 0
	}
	n := copy(buf[icmp6IPHdr:], msg)
	h := buf[:icmp6IPHdr]
	clear(h)
	h[0] = 0x60
	binary.BigEndian.PutUint16(h[4:], uint16(n))
	h[6] = 58
	if res.ControlMessages.HasHopLimit {
		h[7] = res.ControlMessages.HopLimit
	}
	if src.Len() == 16 {
		b := src.As16()
		copy(h[8:], b[:])
	}
	if dst.Len() == 16 {
		b := dst.As16()
		copy(h[24:], b[:])
	}
	return icmp6IPHdr + n
}
