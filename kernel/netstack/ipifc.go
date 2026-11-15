package netstack

import (
	"math/bits"
	"net/netip"
	"slices"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
)

// The stack's interfaces, as #I's /net/ipifc shows and configures them
// (ip(3)): each of gVisor's NICs is a medium, which an interface binds
// ("bind ether /net/ether0", "bind loopback"); the logical interfaces
// added to it are the NIC's addresses, each with a route to its
// network. The stack is IPv4 only.

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
		out[i] = platform.Ifc{Dev: f.dev, MTU: f.mtu, Addrs: slices.Clone(f.addrs)}
		if f.m != nil {
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
	f.m.bound = nil
	*f = ifc{}
	return nil
}

// ifcv4 gives an address as IPv4 and its mask as a prefix length; the null
// address (::, as ip/dhcp adds while it has none) is 0.0.0.0/0.
func ifcv4(a platform.IfcAddr) (tcpip.AddressWithPrefix, bool, error) {
	ip := netip.AddrFrom16(a.IP)
	if ip.IsUnspecified() {
		return tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4([4]byte{})}, true, nil
	}
	if !ip.Is4In6() || [12]byte(a.Mask[:12]) != v4maskpfx {
		return tcpip.AddressWithPrefix{}, false, core.Error(abi.Ebadarg)
	}
	m := uint32(a.Mask[12])<<24 | uint32(a.Mask[13])<<16 | uint32(a.Mask[14])<<8 | uint32(a.Mask[15])
	plen := bits.LeadingZeros32(^m)
	if m<<plen != 0 {
		return tcpip.AddressWithPrefix{}, false, core.Error(abi.Ebadarg)
	}
	return tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4(ip.Unmap().As4()), PrefixLen: plen}, false, nil
}

// route is the route to a logical interface's network.
func route(f *ifc, a platform.IfcAddr, plen int) tcpip.Route {
	rem := tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4(netip.AddrFrom16(a.Remote).Unmap().As4()), PrefixLen: plen}
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
	pa, null, err := ifcv4(a)
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
	if !null && !netip.AddrFrom16(a.Remote).Is4In6() {
		return core.Error(abi.Ebadarg)
	}
	if err := s.g.Stack.AddProtocolAddress(f.m.nic, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: pa}, stack.AddressProperties{}); err != nil {
		return soerr(err)
	}
	f.addrs = append(f.addrs, a)
	if null {
		// No route: the null address only lets ip/dhcp broadcast.
		return nil
	}
	r := route(f, a, pa.PrefixLen)
	rt := s.g.Stack.GetRouteTable()
	if !slices.ContainsFunc(rt, func(x tcpip.Route) bool { return x.Equal(r) }) {
		s.g.Stack.SetRouteTable(append([]tcpip.Route{r}, rt...))
	}
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

// remaddr removes f's logical interface i and its route, unless
// another of the NIC's addresses still needs it.
func (s *Stack) remaddr(f *ifc, i int) error {
	a := f.addrs[i]
	pa, null, err := ifcv4(a)
	if err != nil {
		return err
	}
	if err := s.g.Stack.RemoveAddress(f.m.nic, pa.Address); err != nil {
		if _, ok := err.(*tcpip.ErrBadLocalAddress); !ok {
			return soerr(err)
		}
	}
	f.addrs = slices.Delete(f.addrs, i, i+1)
	if null {
		return nil
	}
	r := route(f, a, pa.PrefixLen)
	for _, x := range f.addrs {
		if px, nx, err := ifcv4(x); err == nil && !nx && route(f, x, px.PrefixLen).Equal(r) {
			return nil
		}
	}
	s.g.Stack.RemoveRoutes(func(x tcpip.Route) bool { return x.Equal(r) })
	return nil
}

// IPStats gives gVisor's IP counters under Inferno's names.
func (s *Stack) IPStats() [19]uint64 {
	ip := s.g.Stack.Stats().IP
	var v [19]uint64
	v[0] = 2 // Forwarding: not a gateway
	v[1] = 255
	v[2] = ip.PacketsReceived.Value()
	v[3] = ip.MalformedPacketsReceived.Value()
	v[4] = ip.InvalidDestinationAddressesReceived.Value() + ip.InvalidSourceAddressesReceived.Value()
	v[8] = ip.PacketsDelivered.Value()
	v[9] = ip.PacketsSent.Value()
	v[10] = ip.OutgoingPacketErrors.Value()
	return v
}
