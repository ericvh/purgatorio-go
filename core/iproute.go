package core

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
)

// routeTable is #I's iproute (ip(3)): routes added, removed and flushed
// by writes, read back in Inferno's format. The host's routing is the
// host OS's: like the Rust sibling's emu, the table is only kept and
// listed, for programs such as ip/rip that maintain it.
type routeTable struct {
	mu     sync.Mutex
	routes []route
}

type route struct {
	addr, mask, gate [16]byte
	tag              string
}

// routeTag is the tag of the routes added through one open iproute.
type routeTag struct{ tag string }

var v4prefix = [12]byte{10: 0xff, 11: 0xff}

func isv4(a [16]byte) bool { return [12]byte(a[:12]) == v4prefix }

// parseipmask parses a mask as /bits (of the 16-byte address) or as a
// dotted IPv4 mask, as Inferno's parseipmask.
func parseipmask(s string) ([16]byte, error) {
	var m [16]byte
	if strings.HasPrefix(s, "/") {
		n, err := strconv.Atoi(s[1:])
		if err != nil || n < 0 {
			return m, Error(abi.Ebadarg)
		}
		n = min(n, 128)
		for i := 0; n > 0; i++ {
			if n >= 8 {
				m[i] = 0xff
			} else {
				m[i] = byte(0xff << (8 - n))
			}
			n -= 8
		}
		return m, nil
	}
	a, ok := ParseIP(s)
	if !ok {
		return m, Error(abi.Ebadarg)
	}
	if isv4(a) {
		for i := range 12 {
			a[i] = 0xff
		}
	}
	return a, nil
}

// formatMask prints a mask as Inferno's %M: /bits when it is a prefix.
func formatMask(m [16]byte) string {
	n := 0
	for _, b := range m {
		if b == 0xff {
			n += 8
			continue
		}
		for b&0x80 != 0 {
			n++
			b <<= 1
		}
		if b != 0 {
			return FormatIP(m)
		}
		break
	}
	for i := n/8 + 1; i < 16; i++ {
		if m[i] != 0 {
			return FormatIP(m)
		}
	}
	return "/" + strconv.Itoa(n)
}

func (t *routeTable) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	for _, r := range t.routes {
		typ := "6   "
		if isv4(r.addr) {
			typ = "4   "
		}
		fmt.Fprintf(&b, "%-15s %-4s %-15s %4.4s %4.4s %3s\n", FormatIP(r.addr), formatMask(r.mask), FormatIP(r.gate), typ, r.tag, "-")
	}
	return b.String()
}

func cmproute(a, b route) int {
	if c := bytes.Compare(a.addr[:], b.addr[:]); c != 0 {
		return c
	}
	return bytes.Compare(a.mask[:], b.mask[:])
}

func (t *routeTable) remove(addr, mask [16]byte) {
	t.routes = slices.DeleteFunc(t.routes, func(r route) bool { return r.addr == addr && r.mask == mask })
}

// write carries out one iproute command: add ip mask gw [tag],
// remove ip mask, flush [tag] or tag tag. Others are ignored, as
// Inferno's routewrite does.
func (t *routeTable) write(rt *routeTag, cmd string) error {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return Error(abi.Ebadarg)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch f[0] {
	case "flush":
		if len(f) < 2 {
			t.routes = nil
			break
		}
		t.routes = slices.DeleteFunc(t.routes, func(r route) bool { return r.tag == trimtag(f[1]) })
	case "remove":
		if len(f) < 3 {
			return Error(abi.Ebadarg)
		}
		a, ok := ParseIP(f[1])
		if !ok {
			return Error(abi.Ebadarg)
		}
		m, err := parseipmask(f[2])
		if err != nil {
			return err
		}
		t.remove(mask16(a, m), m)
	case "add":
		if len(f) < 4 {
			return Error(abi.Ebadarg)
		}
		a, ok := ParseIP(f[1])
		m, err := parseipmask(f[2])
		g, okg := ParseIP(f[3])
		if !ok || !okg || err != nil {
			return Error(abi.Ebadarg)
		}
		r := route{addr: mask16(a, m), mask: m, gate: g, tag: rt.tag}
		if len(f) > 4 {
			r.tag = trimtag(f[4])
		}
		t.remove(r.addr, r.mask)
		i, _ := slices.BinarySearchFunc(t.routes, r, cmproute)
		t.routes = slices.Insert(t.routes, i, r)
	case "tag":
		if len(f) < 2 {
			return Error(abi.Ebadarg)
		}
		rt.tag = trimtag(f[1])
	}
	return nil
}

func mask16(a, m [16]byte) [16]byte {
	for i := range a {
		a[i] &= m[i]
	}
	return a
}

// trimtag keeps the 4 characters of a tag that Inferno keeps.
func trimtag(s string) string {
	if len(s) > 4 {
		return s[:4]
	}
	return s
}
