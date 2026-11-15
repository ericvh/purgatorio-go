package netstack

import (
	"bytes"
	"math/bits"
	"net/netip"
	"slices"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
)

// The stack's interfaces, as #I's /net/ipifc shows and configures them
// (ip(3)): each of gVisor's NICs is a medium, which an interface binds
// ("bind ether /net/ether0", "bind loopback"); the logical interfaces
// added to it are the NIC's addresses, IPv4 or IPv6, each with a route
// to its network. gVisor's route table is those routes and the routes
// of /net/iproute whose gateways the interfaces reach (routes.go).

// medium is one of gVisor's NICs.
type medium struct {
	name  string // the medium's type, as bind names it
	dev   string // the device name an interface bound to it shows
	nic   tcpip.NICID
	mtu   int
	bound *ifc
}

// ifc is one interface of /net/ipifc.
type ifc struct {
	m     *medium
	dev   string
	mtu   int
	addrs []platform.IfcAddr
	multi [][16]byte // groups added by addmulti
}

// v4maskpfx is the first 96 bits of an IPv4 mask in 16 bytes.
var v4maskpfx = [12]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

// addmedium adds a medium and binds it to interface at (appended when
// at is past the end) with the address given.
func (s *Stack) addmedium(m *medium, at int, addr netip.Prefix) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := &ifc{m: m, dev: m.dev, mtu: m.mtu}
	m.bound = f
	s.media = append(s.media, m)
	s.ifcs = slices.Insert(s.ifcs, min(at, len(s.ifcs)), f)
	a := platform.IfcAddr{IP: netip.AddrFrom4(addr.Addr().As4()).As16()}
	copy(a.Mask[:12], v4maskpfx[:])
	m4 := ^uint32(0) << (32 - addr.Bits())
	a.Mask[12], a.Mask[13], a.Mask[14], a.Mask[15] = byte(m4>>24), byte(m4>>16), byte(m4>>8), byte(m4)
	a.Remote = netip.AddrFrom4(addr.Masked().Addr().As4()).As16()
	f.addrs = append(f.addrs, a)
}

func (s *Stack) Interfaces() []platform.Ifc {
	info := s.g.Stack.NICInfo()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]platform.Ifc, len(s.ifcs))
	for i, f := range s.ifcs {
		out[i] = platform.Ifc{Dev: f.dev, MTU: f.mtu, Addrs: slices.Clone(f.addrs), Multi: slices.Clone(f.multi)}
		if f.m != nil {
			var conv [][16]byte
			for m := range s.cmulti {
				if g := fromAddr(m.group); m.nic == f.m.nic && !slices.Contains(out[i].Multi, g) && !slices.Contains(conv, g) {
					conv = append(conv, g)
				}
			}
			slices.SortFunc(conv, func(a, b [16]byte) int { return bytes.Compare(a[:], b[:]) })
			out[i].Multi = append(out[i].Multi, conv...)
			st := info[f.m.nic].Stats
			out[i].In, out[i].Out = st.Rx.Packets.Value(), st.Tx.Packets.Value()
			out[i].InErr = st.MalformedL4RcvdPackets.Value()
			out[i].OutErr = st.TxPacketsDroppedNoBufferSpace.Value()
		}
	}
	return out
}

func (s *Stack) NewIfc() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ifcs = append(s.ifcs, &ifc{})
	return len(s.ifcs) - 1, nil
}

func (s *Stack) getifc(n int) (*ifc, error) {
	if n < 0 || n >= len(s.ifcs) {
		return nil, core.Error(abi.Enonexist)
	}
	return s.ifcs[n], nil
}

// BindIfc attaches a medium: "ether" with its device name, or
// "loopback".
func (s *Stack) BindIfc(n int, name string, args []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.getifc(n)
	if err != nil {
		return err
	}
	if f.m != nil {
		return core.Error("interface already bound")
	}
	var m *medium
	known := false
	for _, x := range s.media {
		if x.name != name {
			continue
		}
		known = true
		if len(args) == 0 || args[0] == x.dev {
			m = x
			break
		}
	}
	switch {
	case !known:
		return core.Error("unknown interface type")
	case m == nil:
		return core.Error(abi.Enonexist)
	case m.bound != nil:
		return core.Error(abi.Einuse)
	}
	f.m, f.dev, f.mtu = m, m.dev, m.mtu
	m.bound = f
	return nil
}

// UnbindIfc detaches the medium and removes the interface's addresses.
func (s *Stack) UnbindIfc(n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.getifc(n)
	if err != nil {
		return err
	}
	if f.m == nil {
		return nil
	}
	for len(f.addrs) > 0 {
		if err := s.remaddr(f, 0); err != nil {
			return err
		}
	}
	for _, g := range f.multi {
		ga := toAddr(g)
		_ = s.g.Stack.LeaveGroup(netproto(ga), f.m.nic, ga)
	}
	f.m.bound = nil
	*f = ifc{}
	s.syncroutes()
	return nil
}

// ifcaddr gives a logical interface's address, IPv4 or IPv6, with its
// mask as a prefix length; the null address (::, as ip/dhcp adds while
// it has none) is 0.0.0.0/0.
func ifcaddr(a platform.IfcAddr) (tcpip.ProtocolAddress, bool, error) {
	ip := netip.AddrFrom16(a.IP)
	if ip.IsUnspecified() {
		return tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4([4]byte{})}}, true, nil
	}
	plen, ok := prefixlen(a.Mask, ip.Is4In6())
	if !ok {
		return tcpip.ProtocolAddress{}, false, core.Error(abi.Ebadarg)
	}
	ta := toAddr(a.IP)
	return tcpip.ProtocolAddress{Protocol: netproto(ta), AddressWithPrefix: tcpip.AddressWithPrefix{Address: ta, PrefixLen: plen}}, false, nil
}

// prefixlen gives a 16-byte mask as a prefix length, of the IPv4
// address for an IPv4 mask (the first 96 bits set); ok is false if it
// is not a prefix.
func prefixlen(m [16]byte, v4 bool) (int, bool) {
	if v4 {
		if [12]byte(m[:12]) != v4maskpfx {
			return 0, false
		}
		m4 := uint32(m[12])<<24 | uint32(m[13])<<16 | uint32(m[14])<<8 | uint32(m[15])
		n := bits.LeadingZeros32(^m4)
		return n, m4<<n == 0
	}
	n := 0
	for n < 128 && m[n/8]&(0x80>>(n%8)) != 0 {
		n++
	}
	for i := n; i < 128; i++ {
		if m[i/8]&(0x80>>(i%8)) != 0 {
			return 0, false
		}
	}
	return n, true
}

// netproto is an address's network protocol.
func netproto(a tcpip.Address) tcpip.NetworkProtocolNumber {
	if a.Len() == 16 {
		return ipv6.ProtocolNumber
	}
	return ipv4.ProtocolNumber
}

// route is the route to a logical interface's network.
func route(f *ifc, a platform.IfcAddr, plen int) tcpip.Route {
	rem := tcpip.AddressWithPrefix{Address: toAddr(a.Remote), PrefixLen: plen}
	if rem.Address.Len() == 0 {
		rem.Address = tcpip.AddrFrom4([4]byte{})
		if !netip.AddrFrom16(a.IP).Is4In6() {
			rem.Address = tcpip.AddrFrom16([16]byte{})
		}
	}
	return tcpip.Route{Destination: rem.Subnet(), NIC: f.m.nic}
}

func (s *Stack) AddAddr(n int, a platform.IfcAddr, mtu int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.getifc(n)
	if err != nil {
		return err
	}
	if f.m == nil {
		return core.Error("ipifc not yet bound to device")
	}
	pa, null, err := ifcaddr(a)
	if err != nil {
		return err
	}
	if mtu >= 68 && mtu <= f.m.mtu {
		f.mtu = mtu
	}
	for _, x := range f.addrs {
		if x.IP == a.IP {
			return nil
		}
	}
	if !null && netip.AddrFrom16(a.Remote).Is4In6() != netip.AddrFrom16(a.IP).Is4In6() {
		return core.Error(abi.Ebadarg)
	}
	if err := s.g.Stack.AddProtocolAddress(f.m.nic, pa, stack.AddressProperties{}); err != nil {
		return soerr(err)
	}
	f.addrs = append(f.addrs, a)
	s.syncroutes()
	return nil
}

func (s *Stack) RemoveAddr(n int, a platform.IfcAddr) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.getifc(n)
	if err != nil {
		return err
	}
	i := slices.Index(f.addrs, a)
	if i < 0 {
		return core.Error("address not on this interface")
	}
	return s.remaddr(f, i)
}

// remaddr removes f's logical interface i and, unless another of the
// NIC's addresses still needs it, its route.
func (s *Stack) remaddr(f *ifc, i int) error {
	pa, _, err := ifcaddr(f.addrs[i])
	if err != nil {
		return err
	}
	if err := s.g.Stack.RemoveAddress(f.m.nic, pa.AddressWithPrefix.Address); err != nil {
		if _, ok := err.(*tcpip.ErrBadLocalAddress); !ok {
			return soerr(err)
		}
	}
	f.addrs = slices.Delete(f.addrs, i, i+1)
	s.syncroutes()
	return nil
}

// AddMulti joins interface n to a multicast group, as ipifc's addmulti.
func (s *Stack) AddMulti(n int, group [16]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.getifc(n)
	if err != nil {
		return err
	}
	if f.m == nil {
		return core.Error("ipifc not yet bound to device")
	}
	if slices.Contains(f.multi, group) {
		return nil
	}
	ga := toAddr(group)
	if err := s.g.Stack.JoinGroup(netproto(ga), f.m.nic, ga); err != nil {
		return soerr(err)
	}
	f.multi = append(f.multi, group)
	return nil
}

// RemMulti takes interface n out of a multicast group, as ipifc's
// remmulti.
func (s *Stack) RemMulti(n int, group [16]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.getifc(n)
	if err != nil {
		return err
	}
	i := slices.Index(f.multi, group)
	if i < 0 || f.m == nil {
		return core.Error("multicast address not on this interface")
	}
	f.multi = slices.Delete(f.multi, i, i+1)
	ga := toAddr(group)
	return soerr(s.g.Stack.LeaveGroup(netproto(ga), f.m.nic, ga))
}

// SetForwarding turns IPv4 and IPv6 forwarding on or off on every NIC,
// as ipifc's iprouting.
func (s *Stack) SetForwarding(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range []tcpip.NetworkProtocolNumber{ipv4.ProtocolNumber, ipv6.ProtocolNumber} {
		if err := s.g.Stack.SetForwardingDefaultAndAllNICs(p, on); err != nil {
			return soerr(err)
		}
	}
	s.forward = on
	return nil
}

// IPStats gives gVisor's IP counters under Inferno's names.
func (s *Stack) IPStats() [19]uint64 {
	ip := s.g.Stack.Stats().IP
	var v [19]uint64
	v[0] = 2 // Forwarding: not a gateway
	s.mu.Lock()
	if s.forward {
		v[0] = 1
	}
	s.mu.Unlock()
	v[1] = 255
	v[2] = ip.PacketsReceived.Value()
	v[3] = ip.MalformedPacketsReceived.Value()
	v[4] = ip.InvalidDestinationAddressesReceived.Value() + ip.InvalidSourceAddressesReceived.Value()
	v[8] = ip.PacketsDelivered.Value()
	v[9] = ip.PacketsSent.Value()
	v[10] = ip.OutgoingPacketErrors.Value()
	return v
}
