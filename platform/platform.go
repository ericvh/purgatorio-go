// Package platform is the seam between the portable core and whatever it
// runs on: the hosted emu on Linux or macOS, or the bare-metal kernel.
//
// In Go, goroutines, sync.Mutex and sync.Cond already give the core its
// kprocs, locks and Rendez on every target that has a Go runtime, so this
// interface is smaller than Inferno's: it covers what the runtime does not.
package platform

import "io"

// Platform is implemented once per target.
type Platform interface {
	// Now returns the time in nanoseconds since the Unix epoch.
	Now() int64
	// Entropy fills b with random bytes (#c/random).
	Entropy(b []byte)
	// Console is the raw byte stream behind #c/cons.
	Console() Console
	// User and Sysname back #c/user and #c/sysname.
	User() string
	Sysname() string
}

// Console is the host terminal or serial line.
type Console interface {
	io.Reader
	io.Writer
	// SetRaw switches raw mode on or off (#c/consctl rawon, rawoff).
	SetRaw(on bool) error
}

// Networker is implemented by platforms with an IP stack, which #I
// serves.
type Networker interface {
	Net() NetStack
}

// Socket kinds, as Inferno's S_TCP and S_UDP, and ICMP. An ICMP socket
// sends and receives ICMP messages behind a 20-byte IPv4 header, as
// Inferno's /net/icmp data file: the header of a message sent is
// ignored, and that of one received gives its ttl, source and
// destination. An ICMPv6 socket does the same behind a 40-byte IPv6
// header, as /net/icmpv6.
const (
	TCP    = 0
	UDP    = 1
	ICMP   = 2
	ICMPv6 = 3
)

// NetStack makes sockets and looks up names (Inferno's ipif and $Srv).
type NetStack interface {
	Socket(kind int) (Socket, error)
	// LookupHost returns the addresses of host, as Srv->iph2a.
	LookupHost(host string) ([]string, error)
	// LookupAddr returns the names of addr, as Srv->ipa2h.
	LookupAddr(addr string) ([]string, error)
	// LookupPort returns the port number of service on network, as
	// Srv->ipn2p.
	LookupPort(network, service string) (int, error)
}

// Ifc is an IP interface as #I's ipifc shows it (Inferno's Ipifc): the
// device it is bound to ("" when unbound), its MTU, its logical
// interfaces and packet counts.
type Ifc struct {
	Dev                    string
	MTU                    int
	Addrs                  []IfcAddr
	In, Out, InErr, OutErr uint64
	// Multi are the multicast groups the interface was added to
	// (ipifc's addmulti).
	Multi [][16]byte
}

// IfcAddr is a logical interface: local address, mask and remote
// address, 16-byte as sockets' (IPv4 v4-mapped, IPv4 masks with the
// first 96 bits set).
type IfcAddr struct{ IP, Mask, Remote [16]byte }

// IfcLister is a NetStack that lists its interfaces, numbered by their
// place in the list, for #I's /net/ipifc.
type IfcLister interface {
	Interfaces() []Ifc
}

// IfcConfig is an IfcLister whose interfaces /net/ipifc may change, as
// ipifc's ctl requests: NewIfc makes an unbound interface, BindIfc
// attaches a medium to one, UnbindIfc detaches it and its addresses,
// AddAddr and RemoveAddr add and remove logical interfaces (mtu is 0
// when not given).
type IfcConfig interface {
	IfcLister
	NewIfc() (int, error)
	BindIfc(n int, medium string, args []string) error
	UnbindIfc(n int) error
	AddAddr(n int, a IfcAddr, mtu int) error
	RemoveAddr(n int, a IfcAddr) error
}

// IfcMulticaster is an IfcConfig whose interfaces join and leave
// multicast groups, as ipifc's addmulti and remmulti.
type IfcMulticaster interface {
	AddMulti(n int, group [16]byte) error
	RemMulti(n int, group [16]byte) error
}

// Route is a route of #I's iproute: destination, mask and gateway, as
// IfcAddr's, and its tag.
type Route struct {
	Addr, Mask, Gate [16]byte
	Tag              string
}

// IPRouter is a NetStack whose routing #I controls (ip(3)'s iproute
// and iprouting). BootRoutes are the routes it was set up with, which
// start iproute's table. SetRoutes gives it the whole table after each
// change; it routes by each route whose gateway an interface reaches,
// and again as interfaces change. SetForwarding turns forwarding of
// packets between interfaces on or off.
type IPRouter interface {
	BootRoutes() []Route
	SetRoutes(routes []Route)
	SetForwarding(on bool) error
}

// MulticastSocket is a Socket that joins multicast groups, as a
// conversation's addmulti and remmulti: group on the interface with
// local address ifc. It leaves them when it is closed.
type MulticastSocket interface {
	AddMulti(group, ifc [16]byte) error
	RemMulti(group, ifc [16]byte) error
}

// ProtoStatser gives the counters of a protocol's stats file, in the
// order of Inferno's statnames: tcp.c's for TCP (MaxConn, which #I
// fills in, ActiveOpens, ...), udp.c's for UDP (InDatagrams, NoPorts, InErrors, OutDatagrams),
// icmp.c's for ICMP (InMsgs ... HlenErrs) then the messages received and
// sent of each type 0 to 18, icmp6.c's for ICMPv6 (InMsgs ...
// RouterAddrErrs) then those of each type 0 to 137. ok is false for a
// protocol it keeps no counters for.
type ProtoStatser interface {
	ProtoStats(kind int) (v []uint64, ok bool)
}

// IPStatser gives the IP counters /net/ipifc/stats shows, in the order
// of Inferno's ip.c statnames (Forwarding, DefaultTTL, InReceives, ...).
type IPStatser interface {
	IPStats() [19]uint64
}

// IfcSnooper is an IfcLister that copies the packets interface n's
// medium sends and receives, for /net/ipifc/n/snoop: SnoopIfc calls
// deliver with each until stop is called. deliver must not block.
type IfcSnooper interface {
	SnoopIfc(n int, deliver func(pkt []byte)) (stop func(), err error)
}

// Socket is one socket, the so_* calls of Inferno's ipif. Addresses are
// 16-byte IPv6 (IPv4 as v4-mapped). Calls that block end early, with
// ErrInterrupted, when stop is closed.
type Socket interface {
	Bind(addr [16]byte, port uint16, restricted bool) error
	Listen() error
	Accept(stop <-chan struct{}) (Socket, [16]byte, uint16, error)
	Connect(stop <-chan struct{}, addr [16]byte, port uint16) error
	// Recv reads into buf; for a datagram socket with a header buffer,
	// the sender's address goes in hdr in Inferno's udp header form.
	Recv(stop <-chan struct{}, buf, hdr []byte) (int, error)
	Send(stop <-chan struct{}, buf, hdr []byte) (int, error)
	LocalAddr() ([16]byte, uint16)
	Hangup() error
	Close() error
	Keepalive(ms int) error
}

// ErrInterrupted is returned by a socket call ended by its stop channel.
var ErrInterrupted = interrupted{}

type interrupted struct{}

func (interrupted) Error() string { return "interrupted" }

// Clipboard is implemented by platforms with a host snarf buffer (#^).
type Clipboard interface {
	ClipRead() []byte
	ClipWrite(b []byte)
}
