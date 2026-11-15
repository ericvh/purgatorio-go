package netstack

import (
	"net/netip"
	"slices"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"

	"github.com/ericvh/purgatorio-go/platform"
)

// Routing, as #I's iproute gives it (platform.IPRouter): gVisor's route
// table is the route to each logical interface's network, then each
// route of iproute's table whose gateway one of those networks holds,
// through that interface, as Inferno's v4lookup and v6lookup find an
// interface for a route. A route whose gateway no interface reaches is
// kept in the table but not used until one does. A route through the
// unspecified address or one of the stack's own goes straight to its
// network: the interface is the one whose network holds the
// destination. gVisor takes the longest prefix first, as Inferno's
// lookup does; a route whose mask is not a prefix cannot be given to it
// and is not used.

// BootRoutes are the routes Attach set: the default route through the
// gateway.
func (s *Stack) BootRoutes() []platform.Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.boot)
}

// SetRoutes takes iproute's table.
func (s *Stack) SetRoutes(routes []platform.Route) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = slices.Clone(routes)
	s.syncroutes()
}

// syncroutes sets gVisor's route table from the interfaces and
// iproute's table.
func (s *Stack) syncroutes() {
	var rt []tcpip.Route
	type net struct {
		sub   tcpip.Subnet
		nic   tcpip.NICID
		local tcpip.Address
	}
	var nets []net
	for _, f := range s.ifcs {
		if f.m == nil {
			continue
		}
		for _, a := range f.addrs {
			pa, null, err := ifcaddr(a)
			if err != nil {
				continue
			}
			if null {
				// The null address's network is everything, as
				// ipifcadd makes it, for ip/dhcp's broadcasts.
				r := tcpip.Route{Destination: header.IPv4EmptySubnet, NIC: f.m.nic}
				if !slices.ContainsFunc(rt, func(x tcpip.Route) bool { return x.Equal(r) }) {
					rt = append(rt, r)
				}
				continue
			}
			r := route(f, a, pa.AddressWithPrefix.PrefixLen)
			nets = append(nets, net{r.Destination, r.NIC, pa.AddressWithPrefix.Address})
			if !slices.ContainsFunc(rt, func(x tcpip.Route) bool { return x.Equal(r) }) {
				rt = append(rt, r)
			}
		}
	}
	find := func(a tcpip.Address) (net, bool) {
		for _, n := range nets {
			if n.sub.Contains(a) {
				return n, true
			}
		}
		return net{}, false
	}
	for _, r := range s.routes {
		dst := toAddr(r.Addr)
		v4 := netip.AddrFrom16(r.Addr).Is4In6()
		if dst.Len() == 0 {
			dst = tcpip.AddrFrom4([4]byte{})
			if !v4 {
				dst = tcpip.AddrFrom16([16]byte{})
			}
		}
		plen, ok := prefixlen(r.Mask, v4)
		if !ok {
			continue
		}
		sub := tcpip.AddressWithPrefix{Address: dst, PrefixLen: plen}.Subnet()
		gw := toAddr(r.Gate)
		direct := gw.Len() == 0
		for _, n := range nets {
			if n.local == gw {
				direct = true
			}
		}
		var tr tcpip.Route
		if direct {
			n, ok := find(dst)
			if !ok {
				continue
			}
			tr = tcpip.Route{Destination: sub, NIC: n.nic}
		} else {
			n, ok := find(gw)
			if !ok || gw.Len() != dst.Len() {
				continue
			}
			tr = tcpip.Route{Destination: sub, Gateway: gw, NIC: n.nic}
		}
		if !slices.ContainsFunc(rt, func(x tcpip.Route) bool { return x.Equal(tr) }) {
			rt = append(rt, tr)
		}
	}
	s.g.Stack.SetRouteTable(rt)
}

// membership is a multicast group a socket joined on a NIC.
type membership struct {
	group tcpip.Address
	nic   tcpip.NICID
}

// AddMulti joins the socket to a multicast group on the interface with
// local address ifc (any interface that routes to the group when it is
// unspecified), as a conversation's addmulti.
func (s *sock) AddMulti(group, ifc [16]byte) error {
	m := s.membership(group, ifc)
	if m.nic == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.Contains(s.multi, m) {
		return nil
	}
	if err := s.st.g.Stack.JoinGroup(netproto(m.group), m.nic, m.group); err != nil {
		return soerr(err)
	}
	s.multi = append(s.multi, m)
	s.st.convmulti(m, 1)
	return nil
}

// RemMulti leaves a group AddMulti joined.
func (s *sock) RemMulti(group, ifc [16]byte) error {
	m := s.membership(group, ifc)
	if m.nic == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.Index(s.multi, m)
	if i < 0 {
		return nil
	}
	s.multi = slices.Delete(s.multi, i, i+1)
	s.st.convmulti(m, -1)
	return soerr(s.st.g.Stack.LeaveGroup(netproto(m.group), m.nic, m.group))
}

// membership finds the NIC for AddMulti and RemMulti. As in Inferno, a
// group on an address no interface has is not an error, it does
// nothing (m.nic is 0).
func (s *sock) membership(group, ifc [16]byte) membership {
	m := membership{group: toAddr(group)}
	p := netproto(m.group)
	if ia := toAddr(ifc); ia.Len() != 0 {
		m.nic = s.st.g.Stack.CheckLocalAddress(0, p, ia)
	} else if r, err := s.st.g.Stack.FindRoute(0, tcpip.Address{}, m.group, p, false); err == nil {
		m.nic = r.NICID()
		r.Release()
	} else {
		// No route to a multicast group: the first interface bound
		// to Ethernet.
		s.st.mu.Lock()
		for _, md := range s.st.media {
			if md.name == "ether" {
				m.nic = md.nic
				break
			}
		}
		s.st.mu.Unlock()
	}
	return m
}

// leaveall leaves the groups the socket joined; s.mu is held.
func (s *sock) leaveall() {
	for _, m := range s.multi {
		s.st.convmulti(m, -1)
		_ = s.st.g.Stack.LeaveGroup(netproto(m.group), m.nic, m.group)
	}
	s.multi = nil
}

// convmulti counts the conversations in each group on each NIC, which
// the interfaces list with their own groups, as Inferno's self cache
// has them.
func (s *Stack) convmulti(m membership, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmulti == nil {
		s.cmulti = map[membership]int{}
	}
	if s.cmulti[m] += n; s.cmulti[m] <= 0 {
		delete(s.cmulti, m)
	}
}
