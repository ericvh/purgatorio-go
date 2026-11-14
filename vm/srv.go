package vm

import (
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
)

// The hosted-only Srv module of Inferno (iph2a, ipa2h, ipn2p), which cs
// uses to look names up through the host. Lists come back as strings
// separated by NUL bytes; a too-small buffer gets the length needed.

func srvFuncs() []Func {
	f := func(name, params, results string, call hostFn) Func {
		return Func{Module: abi.Module, Name: name, Params: sig(params), Results: sig(results), Call: call}
	}
	return []Func{
		f("srv_iph2a", "iiii", "i", func(in *Instance, m Memory, a []uint64) ([]uint64, error) {
			return srvList(in, m, a, func(n platform.NetStack, s string) ([]string, error) { return n.LookupHost(s) })
		}),
		f("srv_ipa2h", "iiii", "i", func(in *Instance, m Memory, a []uint64) ([]uint64, error) {
			return srvList(in, m, a, func(n platform.NetStack, s string) ([]string, error) { return n.LookupAddr(s) })
		}),
		f("srv_ipn2p", "iiiiii", "i", pIpn2p),
	}
}

func (in *Instance) netstack() (platform.NetStack, error) {
	if nw, ok := in.M.Kernel.Platform.(platform.Networker); ok {
		return nw.Net(), nil
	}
	return nil, core.Error(abi.Enodev)
}

func srvList(in *Instance, m Memory, a []uint64, f func(platform.NetStack, string) ([]string, error)) ([]uint64, error) {
	s, okp := in.str(m, a[0], a[1])
	if !okp {
		return ret(-1)
	}
	n, err := in.netstack()
	if err != nil {
		return in.pfail(err)
	}
	l, err := f(n, s)
	if err != nil {
		return in.pfail(err)
	}
	return putStr(in, m, a[2], a[3], strings.Join(l, "\x00"))
}

func pIpn2p(in *Instance, m Memory, a []uint64) ([]uint64, error) {
	netw, ok1 := in.str(m, a[0], a[1])
	svc, ok2 := in.str(m, a[2], a[3])
	if !ok1 || !ok2 {
		return ret(-1)
	}
	n, err := in.netstack()
	if err != nil {
		return in.pfail(err)
	}
	p, err := n.LookupPort(netw, svc)
	if err != nil {
		return in.pfail(err)
	}
	return putStr(in, m, a[4], a[5], strconv.Itoa(p))
}
