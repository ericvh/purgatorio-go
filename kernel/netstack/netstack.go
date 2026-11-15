// Package netstack is the kernel's IP stack: gVisor's tcpip, set up
// through go-net's gnet glue, behind the portable core's
// platform.NetStack, which #I serves. Without a network device it has
// only the loopback interface.
package netstack

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	gnet "github.com/usbarmory/go-net"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
)

// Stack is a gVisor stack with a loopback interface and at most one
// Ethernet interface.
type Stack struct {
	g        *gnet.GVisorStack
	resolver *net.Resolver
	dns      netip.Addr

	mu    sync.Mutex // media and ifcs (ipifc.go)
	media []*medium
	ifcs  []*ifc
}

// New makes a stack with only the loopback interface, which is bound to
// /net/ipifc/0 with 127.0.0.1/8.
func New() (*Stack, error) {
	g := gnet.NewGVisorStack(1)
	if err := g.EnableLoopback(); err != nil {
		return nil, err
	}
	s := &Stack{g: g}
	s.resolver = &net.Resolver{PreferGo: true, Dial: s.dialDNS}
	for id, info := range g.Stack.NICInfo() {
		s.addmedium(&medium{name: "loopback", dev: "/dev/null", nic: id, mtu: int(info.MTU)}, 0, netip.MustParsePrefix("127.0.0.1/8"))
	}
	return s, nil
}

// Interface describes the Ethernet interface: its address with prefix
// length, gateway and name server.
type Interface struct {
	MAC     net.HardwareAddr
	Addr    netip.Prefix
	Gateway netip.Addr
	DNS     netip.Addr
}

// Attach adds the Ethernet interface, with its address, a route to its
// network and a default route through the gateway: transmit sends a
// frame; the returned function takes each frame received. It is
// /net/ipifc/0, bound to ether /net/ether0, ahead of the loopback
// interface; it must be attached before any program runs.
func (s *Stack) Attach(ifc Interface, transmit func(frame []byte)) (receive func(frame []byte), err error) {
	if err := s.g.Configure(ifc.MAC, ifc.Addr, ifc.Gateway); err != nil {
		return nil, err
	}
	s.addmedium(&medium{name: "ether", dev: "/net/ether0", nic: s.g.NICID, mtu: gnet.MTU}, 0, ifc.Addr)
	s.dns = ifc.DNS
	ready := make(chan struct{}, 1)
	s.g.SetWriteNotify(func([]byte) {
		select {
		case ready <- struct{}{}:
		default:
		}
	})
	go func() {
		buf := make([]byte, gnet.MTU+gnet.EthernetMaximumSize)
		for range ready {
			for {
				n, err := s.g.WriteOutboundPacket(buf)
				if err != nil || n == 0 {
					break
				}
				if n >= gnet.EthernetMinimumSize {
					transmit(buf[:n])
				}
			}
		}
	}()
	return func(frame []byte) {
		_ = s.g.RecvInboundPacket(bytes.Clone(frame))
	}, nil
}

func (s *Stack) dialDNS(ctx context.Context, network, _ string) (net.Conn, error) {
	if !s.dns.IsValid() {
		return nil, core.Error("no name server")
	}
	fa := tcpip.FullAddress{Addr: tcpip.AddrFrom4(s.dns.As4()), Port: 53}
	if network == "tcp" || network == "tcp4" {
		return gonet.DialContextTCP(ctx, s.g.Stack, fa, ipv4.ProtocolNumber)
	}
	return gonet.DialUDP(s.g.Stack, nil, &fa, ipv4.ProtocolNumber)
}

func (s *Stack) lookupCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (s *Stack) LookupHost(host string) ([]string, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []string{a.String()}, nil
	}
	if host == "localhost" {
		return []string{"127.0.0.1"}, nil
	}
	ctx, cancel := s.lookupCtx()
	defer cancel()
	addrs, err := s.resolver.LookupHost(ctx, host)
	if err != nil {
		return nil, core.Error("host lookup failed: " + host)
	}
	return addrs, nil
}

func (s *Stack) LookupAddr(addr string) ([]string, error) {
	ctx, cancel := s.lookupCtx()
	defer cancel()
	names, err := s.resolver.LookupAddr(ctx, addr)
	if err != nil {
		return nil, core.Error("address lookup failed: " + addr)
	}
	return names, nil
}

func (s *Stack) LookupPort(network, service string) (int, error) {
	if n, err := strconv.Atoi(service); err == nil {
		return n, nil
	}
	ctx, cancel := s.lookupCtx()
	defer cancel()
	p, err := s.resolver.LookupPort(ctx, network, service)
	if err != nil {
		return 0, core.Error("unknown service " + service)
	}
	return p, nil
}

// Socket makes a TCP or UDP endpoint. UDP may broadcast, as the emu's
// host sockets may. ICMP is not done yet.
func (s *Stack) Socket(kind int) (platform.Socket, error) {
	if kind == platform.ICMP {
		return nil, core.Error("protocol not supported")
	}
	proto := tcp.ProtocolNumber
	if kind == platform.UDP {
		proto = udp.ProtocolNumber
	}
	so := &sock{kind: kind, wq: new(waiter.Queue)}
	ep, err := s.g.Stack.NewEndpoint(proto, ipv4.ProtocolNumber, so.wq)
	if err != nil {
		return nil, soerr(err)
	}
	so.ep = ep
	ops := ep.SocketOptions()
	if kind == platform.UDP {
		ops.SetBroadcast(true)
	}
	ops.SetReuseAddress(true)
	return so, nil
}

type sock struct {
	kind int
	wq   *waiter.Queue
	ep   tcpip.Endpoint
	mu   sync.Mutex
}

// Addresses on the core's side are 16-byte IPv6, IPv4 v4-mapped; the
// stack is IPv4 only.
func toFull(a [16]byte, port uint16) (tcpip.FullAddress, error) {
	ip := netip.AddrFrom16(a).Unmap()
	if ip.IsUnspecified() {
		return tcpip.FullAddress{Port: port}, nil
	}
	if !ip.Is4() {
		return tcpip.FullAddress{}, core.Error(abi.Enetunreach)
	}
	return tcpip.FullAddress{Addr: tcpip.AddrFrom4(ip.As4()), Port: port}, nil
}

func fromFull(fa tcpip.FullAddress) ([16]byte, uint16) {
	var a [16]byte
	a[10], a[11] = 0xff, 0xff
	if fa.Addr.Len() == 4 {
		b := fa.Addr.As4()
		copy(a[12:], b[:])
	}
	return a, fa.Port
}

// wait runs op until it stops saying it would block, waiting for events
// in mask between tries, or until stop is closed.
func (s *sock) wait(stop <-chan struct{}, mask waiter.EventMask, op func() tcpip.Error) error {
	err := op()
	if _, ok := err.(*tcpip.ErrWouldBlock); !ok {
		return soerr(err)
	}
	e, ch := waiter.NewChannelEntry(mask)
	s.wq.EventRegister(&e)
	defer s.wq.EventUnregister(&e)
	for {
		err = op()
		if _, ok := err.(*tcpip.ErrWouldBlock); !ok {
			return soerr(err)
		}
		select {
		case <-stop:
			return platform.ErrInterrupted
		case <-ch:
		}
	}
}

func (s *sock) Bind(addr [16]byte, port uint16, restricted bool) error {
	fa, err := toFull(addr, port)
	if err != nil {
		return err
	}
	if restricted {
		for p := uint16(600); p < 1024; p++ {
			fa.Port = p
			if s.ep.Bind(fa) == nil {
				return nil
			}
		}
		fa.Port = port
	}
	return soerr(s.ep.Bind(fa))
}

func (s *sock) Listen() error { return soerr(s.ep.Listen(256)) }

func (s *sock) Accept(stop <-chan struct{}) (platform.Socket, [16]byte, uint16, error) {
	var nep tcpip.Endpoint
	var nwq *waiter.Queue
	var peer tcpip.FullAddress
	err := s.wait(stop, waiter.ReadableEvents, func() tcpip.Error {
		var e tcpip.Error
		nep, nwq, e = s.ep.Accept(&peer)
		return e
	})
	if err != nil {
		return nil, [16]byte{}, 0, err
	}
	ns := &sock{kind: s.kind, ep: nep, wq: nwq}
	a, p := fromFull(peer)
	return ns, a, p, nil
}

func (s *sock) Connect(stop <-chan struct{}, addr [16]byte, port uint16) error {
	fa, err := toFull(addr, port)
	if err != nil {
		return err
	}
	if fa.Addr.Len() == 0 {
		fa.Addr = tcpip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	e, ch := waiter.NewChannelEntry(waiter.WritableEvents)
	s.wq.EventRegister(&e)
	defer s.wq.EventUnregister(&e)
	terr := s.ep.Connect(fa)
	if _, ok := terr.(*tcpip.ErrConnectStarted); ok {
		select {
		case <-stop:
			return platform.ErrInterrupted
		case <-ch:
		}
		terr = s.ep.LastError()
	}
	return soerr(terr)
}

func (s *sock) Recv(stop <-chan struct{}, buf, hdr []byte) (int, error) {
	var res tcpip.ReadResult
	opts := tcpip.ReadOptions{NeedRemoteAddr: hdr != nil}
	err := s.wait(stop, waiter.ReadableEvents, func() tcpip.Error {
		w := tcpip.SliceWriter(buf)
		var e tcpip.Error
		res, e = s.ep.Read(&w, opts)
		return e
	})
	if err == errEOF {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if hdr != nil {
		ra, rp := fromFull(res.RemoteAddr)
		la, lp := s.LocalAddr()
		puthdr(hdr, ra, rp, la, lp)
	}
	return res.Count, nil
}

func (s *sock) Send(stop <-chan struct{}, buf, hdr []byte) (int, error) {
	var opts tcpip.WriteOptions
	if hdr != nil {
		fa, err := toFull(gethdr(hdr))
		if err != nil {
			return 0, err
		}
		opts.To = &fa
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sent := 0
	for sent < len(buf) || (len(buf) == 0 && sent == 0) {
		var r bytes.Reader
		r.Reset(buf[sent:])
		var n int64
		err := s.wait(stop, waiter.WritableEvents, func() tcpip.Error {
			var e tcpip.Error
			n, e = s.ep.Write(&r, opts)
			return e
		})
		sent += int(n)
		if err != nil {
			if sent > 0 {
				return sent, nil
			}
			return 0, err
		}
		if s.kind == platform.UDP || len(buf) == 0 {
			break
		}
	}
	return sent, nil
}

func (s *sock) LocalAddr() ([16]byte, uint16) {
	fa, err := s.ep.GetLocalAddress()
	if err != nil {
		return [16]byte{}, 0
	}
	return fromFull(fa)
}

func (s *sock) Hangup() error {
	s.ep.Abort()
	s.ep.Close()
	return nil
}

func (s *sock) Close() error {
	s.ep.Close()
	return nil
}

func (s *sock) Keepalive(ms int) error {
	s.ep.SocketOptions().SetKeepAlive(true)
	if ms > 0 {
		d := tcpip.KeepaliveIdleOption(time.Duration(ms) * time.Millisecond)
		_ = s.ep.SetSockOpt(&d)
	}
	return nil
}

// errEOF is a read on a connection the other side has closed.
var errEOF = core.Error("eof")

// soerr turns a stack error into Inferno's wording.
func soerr(err tcpip.Error) error {
	switch err.(type) {
	case nil:
		return nil
	case *tcpip.ErrClosedForReceive:
		return errEOF
	case *tcpip.ErrConnectionRefused:
		return core.Error(abi.Econrefused)
	case *tcpip.ErrTimeout:
		return core.Error(abi.Etimedout)
	case *tcpip.ErrNetworkUnreachable, *tcpip.ErrHostUnreachable:
		return core.Error(abi.Enetunreach)
	case *tcpip.ErrPortInUse, *tcpip.ErrDuplicateAddress:
		return core.Error("address in use")
	case *tcpip.ErrConnectionReset, *tcpip.ErrConnectionAborted, *tcpip.ErrClosedForSend, *tcpip.ErrNotConnected:
		return core.Error(abi.Ehungup)
	case *tcpip.ErrNoPortAvailable:
		return core.Error("no free port")
	}
	return core.Error(err.String())
}

// puthdr fills an Inferno udp header: raddr, laddr, [ifcaddr], rport,
// lport, by its length (ip.h).
func puthdr(h []byte, ra [16]byte, rp uint16, la [16]byte, lp uint16) {
	clear(h)
	switch len(h) {
	case 2*4 + 4:
		copy(h[0:], ra[12:])
		copy(h[4:], la[12:])
		h[8], h[9], h[10], h[11] = byte(rp>>8), byte(rp), byte(lp>>8), byte(lp)
	case 2*16 + 4:
		copy(h[0:], ra[:])
		copy(h[16:], la[:])
		h[32], h[33], h[34], h[35] = byte(rp>>8), byte(rp), byte(lp>>8), byte(lp)
	default:
		copy(h[0:], ra[:])
		copy(h[16:], la[:])
		copy(h[32:], la[:])
		h[48], h[49], h[50], h[51] = byte(rp>>8), byte(rp), byte(lp>>8), byte(lp)
	}
}

func gethdr(h []byte) ([16]byte, uint16) {
	var a [16]byte
	switch len(h) {
	case 2*4 + 4:
		a[10], a[11] = 0xff, 0xff
		copy(a[12:], h[0:4])
		return a, uint16(h[8])<<8 | uint16(h[9])
	case 2*16 + 4:
		copy(a[:], h[0:16])
		return a, uint16(h[32])<<8 | uint16(h[33])
	}
	copy(a[:], h[0:16])
	return a, uint16(h[48])<<8 | uint16(h[49])
}
