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

// Socket kinds, as Inferno's S_TCP and S_UDP, and ICMP echo. An ICMP
// socket sends and receives ICMP messages behind a 20-byte IPv4 header,
// as Inferno's /net/icmp data file: the header of a message sent is
// ignored, and that of one received gives its ttl, source and
// destination.
const (
	TCP  = 0
	UDP  = 1
	ICMP = 2
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
