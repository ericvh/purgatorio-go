package ndb

import (
	"net/netip"
	"strconv"
	"strings"
)

// IPInfo returns the values of the attributes rattrs that apply to the
// IP address addr, as Inferno's ipattr findnetattrs: first those of the
// entries with ip=addr, then those of the network entries (ipnet=, with
// ip= and ipmask=) whose network holds addr, the most specific first.
// Each attribute's values come in that order.
func (db *DB) IPInfo(addr string, rattrs ...string) map[string][]string {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return nil
	}
	ip = ip.Unmap()
	type netent struct {
		bits int
		e    *Entry
	}
	var ents []netent
	var p *Ptr
	for {
		var e *Entry
		if e, p = db.FindPair(p, "ip", addr); e == nil {
			break
		}
		if e.Find("ipnet") == nil {
			ents = append(ents, netent{129, e})
		}
	}
	p = nil
	for {
		var e *Entry
		if e, p = db.Find(p, "ipnet"); e == nil {
			break
		}
		if bits, ok := netholds(e, ip); ok {
			ents = append(ents, netent{bits, e})
		}
	}
	out := map[string][]string{}
	for bits := 129; bits >= 0; bits-- {
		for _, n := range ents {
			if n.bits != bits {
				continue
			}
			for _, ra := range rattrs {
				for _, m := range n.e.Find(ra) {
					for _, a := range m.Line.Find(ra) {
						out[ra] = append(out[ra], a.Val)
					}
				}
			}
		}
	}
	return out
}

// netholds reports whether the network of an ipnet entry holds ip, and
// the length of the network's mask (by the address's class when the
// entry has no ipmask).
func netholds(e *Entry, ip netip.Addr) (int, bool) {
	nip, err := netip.ParseAddr(e.FindFirst("ip"))
	if err != nil {
		return 0, false
	}
	nip = nip.Unmap()
	if nip.Is4() != ip.Is4() {
		return 0, false
	}
	bits := 64
	if nip.Is4() {
		switch b := nip.As4()[0]; {
		case b >= 192:
			bits = 24
		case b >= 128:
			bits = 16
		default:
			bits = 8
		}
	}
	if ms := e.FindFirst("ipmask"); ms != "" {
		if strings.HasPrefix(ms, "/") {
			n, err := strconv.Atoi(ms[1:])
			if err != nil {
				return 0, false
			}
			bits = n
			if nip.Is4() && n > 32 {
				bits = n - 96
			}
		} else if m, err := netip.ParseAddr(ms); err == nil {
			m = m.Unmap()
			bits = 0
			for _, b := range m.AsSlice() {
				for ; b&0x80 != 0; b <<= 1 {
					bits++
				}
			}
		} else {
			return 0, false
		}
	}
	pf, err := nip.Prefix(bits)
	if err != nil {
		return 0, false
	}
	return bits, pf.Contains(ip)
}
