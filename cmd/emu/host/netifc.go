package host

import (
	"net"

	"github.com/ericvh/purgatorio-go/platform"
)

// Interfaces lists the host's interfaces for /net/ipifc, read-only: the
// host owns them, so #I refuses clone and writes to ctl. Each shows the
// host's name for it, its MTU and its addresses; the host's packet
// counts are not read.
func (Net) Interfaces() []platform.Ifc {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []platform.Ifc
	for _, ifc := range ifs {
		f := platform.Ifc{Dev: ifc.Name, MTU: ifc.MTU}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			var x platform.IfcAddr
			copy(x.IP[:], ipn.IP.To16())
			if len(ipn.Mask) == net.IPv4len {
				copy(x.Mask[:], net.CIDRMask(96, 128))
				copy(x.Mask[12:], ipn.Mask)
			} else {
				copy(x.Mask[:], ipn.Mask)
			}
			for i := range x.Remote {
				x.Remote[i] = x.IP[i] & x.Mask[i]
			}
			f.Addrs = append(f.Addrs, x)
		}
		out = append(out, f)
	}
	return out
}
