package core

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/styx"
)

// ipifc is #I's interface protocol (ip(3), ipifc.c): /net/ipifc with
// clone, stats and a directory n of ctl, local, snoop and status for
// each of the platform's interfaces; snoop only where the platform
// implements platform.IfcSnooper. A platform that only lists its interfaces
// (the emu, whose host owns them) refuses clone and writes to ctl; one
// that implements platform.IfcConfig takes add, try, remove, connect,
// bind and unbind; iprouting with platform.IPRouter, addmulti and
// remmulti with platform.IfcMulticaster.

// ipIfcproto is the protocol number in ipifc's qids, after udp, tcp and
// icmp.
const ipIfcproto = 3

// ipQsnoop is the type of an interface's snoop file in its qid, clear
// of #I's other types.
const ipQsnoop = ipMasktype

// snoopLimit bounds the packets queued for an interface's snoopers, as
// ipifc.c's 2*QMAX.
const snoopLimit = 2 * 64 * 1024

// ifcsnoop is an interface's snoop queue while it has readers: all of
// them share it, as Inferno's sq, and it gets copies of the packets
// the medium sends and receives (IfcSnooper).
type ifcsnoop struct {
	q    *Queue
	refs int
	stop func()
}

// Defaults of a bound interface's router parameters (ipifcbind).
const (
	ifcMaxraint = 600000
	ifcMinraint = 200000
	ifcTTL      = 255
)

func (d *DevIP) ifclister() platform.IfcLister {
	l, _ := d.net.(platform.IfcLister)
	return l
}

func (d *DevIP) ifcconfig() platform.IfcConfig {
	c, _ := d.net.(platform.IfcConfig)
	return c
}

func (d *DevIP) ifcs() []platform.Ifc {
	if l := d.ifclister(); l != nil {
		return l.Interfaces()
	}
	return nil
}

func (d *DevIP) ifc(n int) (platform.Ifc, bool) {
	l := d.ifcs()
	if n < 0 || n >= len(l) {
		return platform.Ifc{}, false
	}
	return l[n], true
}

// ifcgen lists ipifc's directory and an interface's.
func (d *DevIP) ifcgen(q styx.Qid) []styx.Dir {
	mk := func(path uint64, qt uint8, name string, perm uint32) styx.Dir {
		if qt == styx.QTDIR {
			perm |= styx.DMDIR
		}
		return styx.Dir{Type: 'I', Qid: styx.Qid{Type: qt, Path: path}, Name: name, UID: d.k.Owner, GID: d.k.Owner, Mode: perm}
	}
	var out []styx.Dir
	switch iptype(q) {
	case ipQprotodir:
		for i := range d.ifcs() {
			out = append(out, mk(ipqid(ipIfcproto, i, ipQconvdir), styx.QTDIR, strconv.Itoa(i), 0o555))
		}
		out = append(out, mk(ipqid(ipIfcproto, 0, ipQclone), 0, "clone", 0o666))
		out = append(out, mk(ipqid(ipIfcproto, 0, ipQstats), 0, "stats", 0o444))
	case ipQconvdir:
		n := ipconv(q)
		if _, ok := d.ifc(n); !ok {
			return nil
		}
		out = append(out, mk(ipqid(ipIfcproto, n, ipQctl), 0, "ctl", 0o660))
		out = append(out, mk(ipqid(ipIfcproto, n, ipQlocal), 0, "local", 0o444))
		if _, ok := d.net.(platform.IfcSnooper); ok {
			d.ifcmu.Lock()
			var qlen uint64
			if s := d.snoops[n]; s != nil {
				qlen = uint64(s.q.Len())
			}
			d.ifcmu.Unlock()
			sn := mk(ipqid(ipIfcproto, n, ipQsnoop), 0, "snoop", 0o400)
			sn.Length = qlen
			out = append(out, sn)
		}
		out = append(out, mk(ipqid(ipIfcproto, n, ipQstatus), 0, "status", 0o444))
	}
	return out
}

func (d *DevIP) ifcOpen(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	switch iptype(c.Qid) {
	case ipQclone:
		cfg := d.ifcconfig()
		if cfg == nil || !d.iseve(ctx) {
			return nil, Error(abi.Eperm)
		}
		d.ifcmu.Lock()
		defer d.ifcmu.Unlock()
		// Reuse an unbound interface no one has open, as Fsprotoclone
		// reuses a conversation not in use.
		n := -1
		for i, f := range cfg.Interfaces() {
			if f.Dev == "" && d.ifcopen[i] == 0 {
				n = i
				break
			}
		}
		if n < 0 {
			var err error
			if n, err = cfg.NewIfc(); err != nil {
				return nil, ipErr(ctx, err)
			}
		}
		d.ifcopen[n]++
		c.Qid = styx.Qid{Path: ipqid(ipIfcproto, n, ipQctl)}
	case ipQctl:
		if _, ok := d.ifc(ipconv(c.Qid)); !ok {
			return nil, Error(abi.Enonexist)
		}
		if mode&3 != abi.OREAD && (d.ifcconfig() == nil || !d.iseve(ctx)) {
			return nil, Error(abi.Eperm)
		}
		d.ifcmu.Lock()
		d.ifcopen[ipconv(c.Qid)]++
		d.ifcmu.Unlock()
	case ipQsnoop:
		if mode != abi.OREAD || !d.iseve(ctx) {
			return nil, Error(abi.Eperm)
		}
		if err := d.snoopopen(ipconv(c.Qid)); err != nil {
			return nil, ipErr(ctx, err)
		}
	default:
		if mode != abi.OREAD {
			return nil, Error(abi.Eperm)
		}
	}
	return c, nil
}

// snoopopen adds a reader to interface n's snoop queue, starting the
// copies with the first.
func (d *DevIP) snoopopen(n int) error {
	d.ifcmu.Lock()
	defer d.ifcmu.Unlock()
	if _, ok := d.ifc(n); !ok {
		return Error(abi.Enonexist)
	}
	sn, ok := d.net.(platform.IfcSnooper)
	if !ok {
		return Error(abi.Enonexist)
	}
	s := d.snoops[n]
	if s == nil {
		s = &ifcsnoop{q: NewMsgQueue(snoopLimit)}
		stop, err := sn.SnoopIfc(n, s.q.Produce)
		if err != nil {
			return err
		}
		s.stop = stop
		d.snoops[n] = s
	}
	s.refs++
	return nil
}

func (d *DevIP) ifcClose(c *Chan) {
	n := ipconv(c.Qid)
	switch iptype(c.Qid) {
	case ipQsnoop:
		d.ifcmu.Lock()
		defer d.ifcmu.Unlock()
		if s := d.snoops[n]; s != nil {
			if s.refs--; s.refs <= 0 {
				s.stop()
				s.q.Hangup()
				delete(d.snoops, n)
			}
		}
		return
	case ipQctl:
	default:
		return
	}
	d.ifcmu.Lock()
	defer d.ifcmu.Unlock()
	if d.ifcopen[n]--; d.ifcopen[n] <= 0 {
		delete(d.ifcopen, n)
	}
}

// ipstatnames are the IP counters' names, as ip.c's statnames.
var ipstatnames = [19]string{"Forwarding", "DefaultTTL", "InReceives", "InHdrErrors", "InAddrErrors",
	"ForwDatagrams", "InUnknownProtos", "InDiscards", "InDelivers", "OutRequests", "OutDiscards",
	"OutNoRoutes", "ReasmTimeout", "ReasmReqds", "ReasmOKs", "ReasmFails", "FragOKs", "FragFails", "FragCreates"}

func (d *DevIP) ifcRead(ctx context.Context, ch *Chan, buf []byte, off int64) (int, error) {
	str := func(s string) (int, error) { return ReadStr([]byte(s), buf, off), nil }
	if iptype(ch.Qid) == ipQsnoop {
		d.ifcmu.Lock()
		s := d.snoops[ipconv(ch.Qid)]
		d.ifcmu.Unlock()
		if s == nil {
			return 0, Error(abi.Ehungup)
		}
		return s.q.Read(ctx, buf)
	}
	if iptype(ch.Qid) == ipQstats {
		st, ok := d.net.(platform.IPStatser)
		if !ok {
			return 0, Error("stats not implemented")
		}
		v := st.IPStats()
		var b strings.Builder
		for i, name := range ipstatnames {
			fmt.Fprintf(&b, "%s: %d\n", name, v[i])
		}
		return str(b.String())
	}
	f, ok := d.ifc(ipconv(ch.Qid))
	if !ok {
		return 0, Error(abi.Enonexist)
	}
	switch iptype(ch.Qid) {
	case ipQctl:
		return str(strconv.Itoa(ipconv(ch.Qid)))
	case ipQstatus:
		return str(ifcstatus(f))
	case ipQlocal:
		return str(ifclocal(f))
	}
	return 0, Error(abi.Eperm)
}

// ifcstatus is an interface's status, as ipifcstate: the device and its
// parameters, then a line for each logical interface.
func ifcstatus(f platform.Ifc) string {
	var b strings.Builder
	maxra, minra, ttl := 0, 0, 0
	if f.Dev != "" {
		maxra, minra, ttl = ifcMaxraint, ifcMinraint, ifcTTL
	}
	fmt.Fprintf(&b, "device %s maxtu %d sendra %d recvra %d mflag %d oflag %d maxraint %d minraint %d linkmtu %d reachtime %d rxmitra %d ttl %d routerlt %d pktin %d pktout %d errin %d errout %d\n",
		f.Dev, f.MTU, 0, 0, 0, 0, maxra, minra, 0, 0, 0, ttl, 3*maxra, f.In, f.Out, f.InErr, f.OutErr)
	for _, a := range f.Addrs {
		// The valid and preferred lifetimes are always infinite here.
		fmt.Fprintf(&b, "\t%-40s %-10s %-40s %-12d %-12d\n", FormatIP(a.IP), formatMask(a.Mask), FormatIP(a.Remote), uint32(1<<32-1), uint32(1<<32-1))
	}
	if len(f.Addrs) == 0 {
		b.WriteString("\n")
	}
	return b.String()
}

// ifclocal lists each logical interface's address and the addresses
// that reach it, as ipifclocal: the self cache entries ipifcadd makes
// and the multicast groups added, most recent first.
func ifclocal(f platform.Ifc) string {
	var b strings.Builder
	for _, a := range f.Addrs {
		fmt.Fprintf(&b, "%-40.40s ->", FormatIP(a.IP))
		for i := len(f.Multi) - 1; i >= 0; i-- {
			if isv4(f.Multi[i]) == isv4(a.IP) {
				fmt.Fprintf(&b, " %-40.40s", FormatIP(f.Multi[i]))
			}
		}
		for _, s := range selfaddrs(a) {
			fmt.Fprintf(&b, " %-40.40s", FormatIP(s))
		}
		b.WriteString("\n")
	}
	return b.String()
}

var (
	v4bcast      = [16]byte{10: 0xff, 11: 0xff, 12: 0xff, 13: 0xff, 14: 0xff, 15: 0xff}
	v6loopback   = [16]byte{15: 1}
	v6allnodesN  = [16]byte{0: 0xff, 1: 0x01, 15: 1}
	v6allnodesL  = [16]byte{0: 0xff, 1: 0x02, 15: 1}
	v6solicitpfx = [13]byte{0: 0xff, 1: 0x02, 11: 1, 12: 0xff}
)

// selfaddrs are the addresses ipifcadd puts in the self cache for a:
// for IPv4 (and the null address) the address, the subnet and network
// broadcast and network addresses and the IPv4 broadcast address; for
// IPv6 the address and its multicast groups.
func selfaddrs(a platform.IfcAddr) [][16]byte {
	var order [][16]byte
	add := func(x [16]byte) {
		for _, y := range order {
			if y == x {
				return
			}
		}
		order = append(order, x)
	}
	add(a.IP)
	if isv4(a.IP) || a.IP == ([16]byte{}) {
		for _, m := range [][16]byte{a.Mask, defmask(a.IP)} {
			var bc, nt [16]byte
			for i := range bc {
				bc[i] = a.IP[i]&m[i] | ^m[i]
				nt[i] = a.IP[i] & m[i]
			}
			add(bc)
			add(nt)
		}
		add(v4bcast)
	} else {
		if a.IP == v6loopback {
			add(v6allnodesN)
		}
		add(v6allnodesL)
		var sm [16]byte
		copy(sm[:], v6solicitpfx[:])
		copy(sm[13:], a.IP[13:])
		add(sm)
	}
	// The self cache keeps the most recent first.
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	return order
}

// defmask is an address's default mask, as Inferno's defmask: the
// class mask for IPv4, all bits for the IPv6 loopback, /64 otherwise.
func defmask(ip [16]byte) [16]byte {
	var m [16]byte
	if isv4(ip) {
		bits := []int{8, 8, 16, 24}[ip[12]>>6]
		m, _ = parseipmask("/" + strconv.Itoa(96+bits))
		return m
	}
	if ip == v6loopback {
		m, _ = parseipmask("/128")
		return m
	}
	m, _ = parseipmask("/64")
	return m
}

// ifcaddrargs parses add's arguments: ip [mask [remote [mtu]]], with
// the defaults of ipifcadd.
func ifcaddrargs(f []string) (platform.IfcAddr, int, error) {
	var a platform.IfcAddr
	mtu := 0
	if len(f) < 2 || len(f) > 6 {
		return a, 0, Error(abi.Ebadarg)
	}
	ip, ok := ParseIP(f[1])
	if !ok {
		return a, 0, Error(abi.Ebadarg)
	}
	a.IP, a.Mask = ip, defmask(ip)
	if len(f) > 2 {
		m, err := parseipmask(f[2])
		if err != nil {
			return a, 0, err
		}
		a.Mask = m
	}
	a.Remote = mask16(ip, a.Mask)
	if len(f) > 3 {
		r, ok := ParseIP(f[3])
		if !ok {
			return a, 0, Error(abi.Ebadarg)
		}
		a.Remote = r
	}
	if len(f) > 4 {
		n, err := strconv.ParseUint(f[4], 0, 32)
		if err != nil {
			return a, 0, Error(abi.Ebadarg)
		}
		mtu = int(n)
	}
	return a, mtu, nil
}

func (d *DevIP) ifcWrite(ctx context.Context, ch *Chan, buf []byte) (int, error) {
	if iptype(ch.Qid) != ipQctl {
		return 0, Error(abi.Eperm)
	}
	cfg := d.ifcconfig()
	if cfg == nil {
		return 0, Error(abi.Eperm)
	}
	n := ipconv(ch.Qid)
	f := strings.Fields(string(buf))
	if len(f) < 1 {
		return 0, Error("short control request")
	}
	d.ifcmu.Lock()
	defer d.ifcmu.Unlock()
	var err error
	switch f[0] {
	case "add", "try":
		var a platform.IfcAddr
		var mtu int
		if a, mtu, err = ifcaddrargs(f); err == nil {
			err = cfg.AddAddr(n, a, mtu)
		}
	case "connect":
		var a platform.IfcAddr
		var mtu int
		if a, mtu, err = ifcaddrargs(f); err != nil {
			break
		}
		ifc, ok := d.ifc(n)
		if !ok {
			return 0, Error(abi.Enonexist)
		}
		if ifc.Dev == "" {
			return 0, Error("ipifc not yet bound to device")
		}
		for _, old := range slices.Clone(ifc.Addrs) {
			if err = cfg.RemoveAddr(n, old); err != nil {
				break
			}
		}
		if err == nil {
			err = cfg.AddAddr(n, a, mtu)
		}
	case "remove":
		if len(f) < 3 {
			return 0, Error(abi.Ebadarg)
		}
		var a platform.IfcAddr
		var ok bool
		if a.IP, ok = ParseIP(f[1]); !ok {
			return 0, Error(abi.Ebadarg)
		}
		if a.Mask, err = parseipmask(f[2]); err != nil {
			return 0, err
		}
		a.Remote = mask16(a.IP, a.Mask)
		if len(f) > 3 {
			if a.Remote, ok = ParseIP(f[3]); !ok {
				return 0, Error(abi.Ebadarg)
			}
		}
		err = cfg.RemoveAddr(n, a)
	case "bind":
		if len(f) < 2 {
			return 0, Error(abi.Ebadarg)
		}
		err = cfg.BindIfc(n, f[1], f[2:])
	case "unbind":
		err = cfg.UnbindIfc(n)
	case "iprouting":
		r, ok := d.net.(platform.IPRouter)
		if !ok {
			return 0, Error("unsupported ctl")
		}
		on := true
		if len(f) > 1 {
			v, _ := strconv.Atoi(f[1])
			on = v != 0
		}
		err = r.SetForwarding(on)
	case "addmulti", "remmulti":
		m, ok := d.net.(platform.IfcMulticaster)
		if !ok {
			return 0, Error("unsupported ctl")
		}
		if len(f) < 2 {
			return 0, Error(abi.Ebadarg)
		}
		ma, ok := ParseIP(f[1])
		if !ok || !ismulticast(ma) {
			return 0, Error(abi.Ebadarg)
		}
		if f[0] == "addmulti" {
			err = m.AddMulti(n, ma)
		} else {
			err = m.RemMulti(n, ma)
		}
	default:
		return 0, Error("unsupported ctl")
	}
	if err != nil {
		return 0, ipErr(ctx, err)
	}
	return len(buf), nil
}
