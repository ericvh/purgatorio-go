//go:build wasip1

package sys

import (
	"strconv"
	"strings"
	"unsafe"
)

//go:wasmimport purgatorio srv_iph2a
func srvIph2a(n unsafe.Pointer, nl uint32, buf unsafe.Pointer, bl uint32) int32

//go:wasmimport purgatorio srv_ipa2h
func srvIpa2h(n unsafe.Pointer, nl uint32, buf unsafe.Pointer, bl uint32) int32

//go:wasmimport purgatorio srv_ipn2p
func srvIpn2p(net unsafe.Pointer, netl uint32, svc unsafe.Pointer, svcl uint32, buf unsafe.Pointer, bl uint32) int32

func srvstr(f func(buf []byte) int32) (string, error) {
	buf := make([]byte, 256)
	for {
		n := f(buf)
		if n < 0 {
			return "", lastErr()
		}
		if int(n) > len(buf) {
			buf = make([]byte, n)
			continue
		}
		return string(buf[:n]), nil
	}
}

func srvlist(s string, err error) ([]string, error) {
	if err != nil || s == "" {
		return nil, err
	}
	return strings.Split(s, "\x00"), nil
}

// Iph2a is Srv->iph2a: the host's addresses for name.
func Iph2a(name string) ([]string, error) {
	np, nl := sp(name)
	return srvlist(srvstr(func(b []byte) int32 { return srvIph2a(np, nl, bp(b), uint32(len(b))) }))
}

// Ipa2h is Srv->ipa2h: the host's names for addr.
func Ipa2h(addr string) ([]string, error) {
	ap, al := sp(addr)
	return srvlist(srvstr(func(b []byte) int32 { return srvIpa2h(ap, al, bp(b), uint32(len(b))) }))
}

// Ipn2p is Srv->ipn2p: the port number of service on network net.
func Ipn2p(net, service string) (string, error) {
	np, nl := sp(net)
	vp, vl := sp(service)
	s, err := srvstr(func(b []byte) int32 { return srvIpn2p(np, nl, vp, vl, bp(b), uint32(len(b))) })
	if err != nil {
		return "", err
	}
	if _, err := strconv.Atoi(s); err != nil {
		return "", Error("bad port " + s)
	}
	return s, nil
}
