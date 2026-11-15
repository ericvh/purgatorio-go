// Command ip/rip is a basic RIP router (Inferno rip(8)): it understands
// RIP versions 1 and 2 and sends version 1 (or 2 with -2). Routes
// learned from replies on udp port 520 are written to /net/iproute
// (only reported with -n). With -b, or addresses of networks given, it
// also asks for routes and broadcasts its own every minute. -d reports
// what it does.
//
// It finds its interfaces and their networks in /net/ipifc: the
// host's on the emu, the IP stack's on the kernel. -a addr, not in
// Inferno, announces at addr instead of port 520 and takes replies
// from any port, for tests.
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/feed"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	self       = "/bin/ip/rip.wasm"
	udphdrsize = 52

	// rip header: op[1] version[1] pad[2]
	Oop       = 0
	Oversion  = 1
	riphdrlen = 4
	// rip route entry: type[2] tag[2] addr[4] mask[4] nexthop[4] metric[4]
	Otype     = 0
	Oaddr     = 4
	Omask     = 8
	Onexthop  = 12
	Ometric   = 16
	ipdestlen = 20

	maxripmsg = 512

	opRequest = 1
	opReply   = 2

	hopLimit    = 16 // defined by protocol as `infinity'
	ripport     = 520
	expired     = 180
	discard     = 240
	outputRate  = 60 // seconds between routing table transmissions
	networkCost = 1
	afInet      = 2
)

// ip4 is an IPv4 address or mask.
type ip4 uint32

func (a ip4) String() string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(a))
	return netip.AddrFrom4(b).String()
}

func (a ip4) classmask() ip4 {
	switch b := a >> 24; {
	case b < 128:
		return 0xFF000000
	case b < 192:
		return 0xFFFF0000
	}
	return 0xFFFFFF00
}

func (a ip4) ismulticast() bool { return a>>28 == 0xE }

const allbits ip4 = 0xFFFFFFFF

type gateway struct {
	dest, mask, gateway ip4
	metric              int
	valid               bool
	changed             bool
	local               bool
	time                int64
}

func (g *gateway) contains(a ip4) bool { return g.dest == a&g.mask }

// ifcaddr is an interface's address, network and mask.
type ifcaddr struct{ ip, net, mask ip4 }

var (
	netfd     *os.File
	routefd   *os.File
	routes    []*gateway
	defroute  *gateway
	debug     int
	nochange  bool
	quiet     = true
	myversion = 1
	netdir    = "/net"
	announce  string
	now       int64
	nets      []ifcaddr
	addrs     []ip4
)

func syslog(s string) { fmt.Printf("rip: %s\n", s) }

func main() {
	feed.Serve()
	a := arg.New(os.Args)
	a.SetUsage("ip/rip [-d] [-r]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd':
			debug++
		case 'b':
			quiet = false
		case '2':
			myversion = 2
		case 'n':
			nochange = true
		case 'x':
			netdir = a.EArg()
		case 'a':
			announce = a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) > 0 {
		quiet = false
	}
	for _, s := range args {
		ad, ok := parseip(s)
		if !ok {
			fatal("invalid address: " + s)
		}
		addrs = append(addrs, ad)
	}
	sys.Pctl(sys.NEWPGRP|sys.FORKFD|sys.FORKNS, nil)
	whereami()
	addlocal()
	fd, err := sys.Open(netdir+"/iproute", sys.ORDWR)
	if err != nil {
		fatal(fmt.Sprintf("can't open %s/iproute: %s", netdir, sys.Errstr()))
	}
	routefd = os.NewFile(uintptr(fd), "iproute")
	readroutes()
	syslog("started")
	netfd = riplisten()

	var rd *feed.Reader
	next := time.Now()
	if !quiet {
		// broadcast request for all routes
		sendall(opRequest, false)
		if rd, err = feed.Start(self, int(netfd.Fd())); err != nil {
			fatal("can't start reader: " + sys.Errstr())
		}
		next = time.Now().Add(outputRate * time.Second)
	}
	buf := make([]byte, 8192)
	for {
		var b []byte
		if rd == nil {
			n, err := netfd.Read(buf)
			if n <= 0 || err != nil {
				return
			}
			b = buf[:n]
		} else {
			wait := time.Until(next)
			if wait <= 0 {
				sendall(opReply, true)
				next = time.Now().Add(outputRate * time.Second)
				continue
			}
			var err error
			b, _, err = rd.Read(int(wait/time.Millisecond) + 1)
			if err != nil {
				return
			}
			if b == nil {
				continue
			}
		}
		packet(b)
	}
}

func packet(buf []byte) {
	nb := len(buf) - riphdrlen - udphdrsize
	if nb < 0 {
		return
	}
	raddr := ip4(binary.BigEndian.Uint32(buf[12:16]))
	rport := binary.BigEndian.Uint16(buf[48:])
	hdr := buf[udphdrsize:]
	version := int(hdr[Oversion])
	if version < 1 {
		return
	}
	bp := buf[udphdrsize+riphdrlen:]
	switch hdr[Oop] {
	case opRequest:
		// TO DO: transmit in response to request?  only if something interesting to say...
	case opReply:
		// wrong source port?
		if rport != ripport && announce == "" {
			return
		}
		// my own broadcast?
		if ismyaddr(raddr) {
			return
		}
		now = time.Now().Unix()
		if debug > 1 {
			fmt.Fprintf(os.Stderr, "from %s:\n", raddr)
		}
		for ; nb >= ipdestlen; nb -= ipdestlen {
			unpackroute(bp, version, raddr)
			bp = bp[ipdestlen:]
		}
	default:
		if debug > 0 {
			fmt.Printf("rip: unexpected op: %d\n", hdr[Oop])
		}
	}
}

// whereami finds the IPv4 addresses of the interfaces in /net/ipifc.
func whereami() {
	ds, err := os.ReadDir(netdir + "/ipifc")
	if err != nil {
		return
	}
	for _, d := range ds {
		if _, err := strconv.Atoi(d.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(netdir + "/ipifc/" + d.Name() + "/status")
		if err != nil {
			continue
		}
		// lines of: addr mask net ...
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) < 3 {
				continue
			}
			ip, ok1 := parseip(f[0])
			m, ok2 := parsemask(f[1])
			if ok1 && ok2 {
				nets = append(nets, ifcaddr{ip, ip & m, m})
			}
		}
	}
}

func ismyaddr(a ip4) bool {
	for _, n := range nets {
		if n.ip == a {
			return true
		}
	}
	return false
}

func addlocal() {
	for _, ifc := range nets {
		g := lookup(ifc.net)
		g.valid, g.local = true, true
		g.gateway, g.mask = ifc.ip, ifc.mask
		g.metric, g.time, g.changed = networkCost, 0, true
		if debug > 0 {
			syslog(fmt.Sprintf("Existing: %s & %s -> %s", g.dest, g.mask, g.gateway))
		}
	}
}

// readroutes records the routes already in iproute.
func readroutes() {
	now = time.Now().Unix()
	b := bufio.NewReader(routefd)
	for {
		l, err := b.ReadString('\n')
		if l == "" && err != nil {
			break
		}
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		flags := f[3]
		if flags == "" || flags[0] != '4' || strings.ContainsAny(flags, "ibum") {
			continue
		}
		dest, ok1 := parseip(f[0])
		mask, ok2 := parsemask(f[1])
		gw, ok3 := parseip(f[2])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		g := lookup(dest)
		g.mask, g.gateway = mask, gw
		g.metric, g.time, g.changed = hopLimit, now, true
		if debug > 0 {
			syslog(fmt.Sprintf("Existing: %s & %s -> %s", g.dest, g.mask, g.gateway))
		}
		switch {
		case g.dest == 0 && g.mask == 0:
			defroute = g
			g.local = true
		case defroute != nil && g.dest == defroute.gateway:
		default:
			g.local = !ismyaddr(g.gateway)
		}
	}
}

func unpackroute(b []byte, version int, gwa ip4) {
	// check that it's an IP route, valid metric, MBZ fields zero
	if b[0] != 0 || b[1] != afInet {
		if debug > 1 {
			fmt.Fprintf(os.Stderr, "\t-- unknown address type %x,%x\n", b[0], b[1])
		}
		return
	}
	get4 := func(o int) uint32 { return binary.BigEndian.Uint32(b[o:]) }
	dest := ip4(get4(Oaddr))
	var mask ip4
	switch version {
	case 1:
		if binary.BigEndian.Uint16(b[2:]) != 0 || get4(Omask) != 0 || get4(Onexthop) != 0 {
			if debug > 1 {
				fmt.Fprintf(os.Stderr, "\t-- non-zero MBZ\n")
			}
			return
		}
		mask = maskgen(dest)
	default:
		if m := get4(Omask); m != 0 {
			mask = ip4(m)
		} else {
			mask = maskgen(dest)
		}
		if n := get4(Onexthop); n != 0 {
			gwa = ip4(n)
		}
	}
	metric := int(int32(get4(Ometric)))
	if debug > 1 {
		fmt.Fprintf(os.Stderr, "\t%s %d\n", dest, metric)
	}
	if metric <= 0 || metric > hopLimit {
		return
	}
	// 1058/3.4.2: response processing: ignore class D or E, net 0, net
	// 127 and broadcast addresses; host routes are allowed
	if dest.ismulticast() || dest>>24 == 0 || dest>>24 == 0x7F {
		if debug > 1 {
			fmt.Fprintf(os.Stderr, "\t%s %d invalid addr\n", dest, metric)
		}
		return
	}
	if isbroadcast(dest, mask) {
		if debug > 1 {
			fmt.Fprintf(os.Stderr, "\t%s & %s -> broadcast\n", dest, mask)
		}
		return
	}
	metric = min(metric+networkCost, hopLimit)
	updateroute(dest, mask, gwa, metric)
}

func updateroute(dest, mask, gwa ip4, metric int) {
	// RFC1058 rules page 27-28, with optional replacement of expiring routes
	r := lookup(dest)
	if r.valid {
		if r.local {
			return // local, don't touch
		}
		if r.gateway == gwa {
			switch {
			case metric != hopLimit:
				r.metric = metric
				r.time = now
			case r.metric != hopLimit:
				r.metric = metric
				r.changed = true
				r.time = now - (discard - 120)
				delroute(r) // don't use it for routing
				// route remains valid but advertised with metric hopLimit
			case now >= r.time+discard:
				delroute(r) // finally dead
				r.valid = false
				r.changed = true
			}
		} else if metric < r.metric || metric != hopLimit && metric == r.metric && now > r.time+expired/2 {
			delroute(r)
			r.metric = metric
			r.gateway = gwa
			r.time = now
			addroute(r)
		}
	} else if metric < hopLimit { // new entry
		// 1058/3.4.2: don't add route-to-host if host is on net/subnet
		// for which we have at least as good a route
		if pr := findroute(dest); mask != allbits || pr == nil || metric <= pr.metric {
			r.valid, r.changed = true, true
			r.time, r.metric = now, metric
			r.dest, r.mask, r.gateway = dest, mask, gwa
			addroute(r)
		}
	}
}

func onlist(a ip4, l []ip4) bool {
	for _, x := range l {
		if x == a {
			return true
		}
	}
	return false
}

func sendall(op int, changes bool) {
	for _, n := range nets {
		if addrs != nil && !onlist(n.net, addrs) {
			continue
		}
		sendroutes(n, n.net|^n.mask, op, changes)
	}
	for _, r := range routes {
		if r != nil {
			r.changed = false
		}
	}
}

func sendroutes(ifc ifcaddr, dst ip4, op int, changes bool) {
	if debug > 1 {
		fmt.Printf("rip: send %s\n", dst)
	}
	buf := make([]byte, maxripmsg+udphdrsize)
	buf[10], buf[11] = 0xff, 0xff
	binary.BigEndian.PutUint32(buf[12:], uint32(dst))
	binary.BigEndian.PutUint16(buf[48:], ripport)
	binary.BigEndian.PutUint16(buf[50:], ripport)
	o := udphdrsize
	buf[o], buf[o+1] = byte(op), byte(myversion)
	o += riphdrlen
	if op == opRequest {
		binary.BigEndian.PutUint32(buf[o+Ometric:], hopLimit)
		o += ipdestlen
	} else {
		for _, r := range routes {
			if r == nil || !r.valid || changes && !r.changed || r == defroute {
				continue
			}
			if r.dest == ifc.net || r.dest&ifc.mask == ifc.net {
				continue
			}
			netmask := r.dest.classmask()
			subnet := r.mask != netmask
			if myversion < 2 && r.mask != allbits {
				// if not a host route, don't let a subnet route leave its net
				if subnet && netmask != ifc.ip.classmask() {
					continue
				}
			}
			if o+ipdestlen > udphdrsize+maxripmsg {
				if _, err := netfd.Write(buf[:o]); err != nil {
					fmt.Fprintf(os.Stderr, "RIP write failed: %s\n", sys.Errstr())
				}
				o = udphdrsize + riphdrlen
			}
			clear(buf[o : o+ipdestlen])
			binary.BigEndian.PutUint16(buf[o+Otype:], afInet)
			binary.BigEndian.PutUint32(buf[o+Oaddr:], uint32(r.dest))
			binary.BigEndian.PutUint32(buf[o+Ometric:], uint32(r.metric))
			if myversion == 2 && subnet {
				binary.BigEndian.PutUint32(buf[o+Omask:], uint32(r.mask))
			}
			o += ipdestlen
		}
	}
	if o > udphdrsize+riphdrlen {
		if _, err := netfd.Write(buf[:o]); err != nil {
			fmt.Fprintf(os.Stderr, "rip: network write to %s failed: %s\n", dst, sys.Errstr())
		}
	}
}

func lookup(addr ip4) *gateway {
	avail := -1
	for i, g := range routes {
		if g == nil || !g.valid {
			if avail < 0 {
				avail = i
			}
			continue
		}
		if g.dest == addr {
			return g
		}
	}
	if avail < 0 {
		avail = len(routes)
		routes = append(routes, nil)
	}
	g := routes[avail]
	if g == nil {
		g = &gateway{}
		routes[avail] = g
	}
	g.dest = addr
	return g
}

func findroute(a ip4) *gateway {
	var pr *gateway
	for _, r := range routes {
		if r == nil || !r.valid {
			continue
		}
		if r.contains(a) && (pr == nil || !maskle(r.mask, pr.mask)) {
			pr = r // more specific mask
		}
	}
	return pr
}

func maskgen(addr ip4) ip4 {
	var net *ifcaddr
	for i := range nets {
		ifc := &nets[i]
		if addr&ifc.mask == ifc.net && (net == nil || maskle(ifc.mask, net.mask)) {
			net = ifc
		}
	}
	if net != nil {
		return net.mask
	}
	return addr.classmask()
}

func isbroadcast(a, mask ip4) bool { return a&^mask == allbits&^mask }

func maskle(a, b ip4) bool { return a&b == a }

// addroute: add ipdest mask gateway.
func addroute(g *gateway) {
	switch {
	case g.mask == 0 && g.dest == 0:
		g.valid = false // don't change default route
	case defroute != nil && defroute.gateway == g.gateway:
		if debug > 0 {
			syslog(fmt.Sprintf("default %s %s", g.dest, g.mask)) // don't need a new entry
		}
		g.valid, g.changed = true, true
	default:
		cmd := fmt.Sprintf("add %s %s %s", g.dest, g.mask, g.gateway)
		if debug > 0 {
			syslog(cmd)
		}
		if nochange {
			g.valid, g.changed = true, true
		} else if n, _ := routefd.Write([]byte(cmd)); n > 0 {
			g.valid, g.changed = true, true
		}
	}
}

// delroute removes a route from iproute (ip(3)'s remove; Inferno's rip
// wrote delete, which iproute ignores).
func delroute(g *gateway) {
	if debug > 0 {
		syslog(fmt.Sprintf("delete %s %s", g.dest, g.mask))
	}
	if !nochange {
		routefd.Write([]byte(fmt.Sprintf("remove %s %s", g.dest, g.mask)))
	}
}

func parseip(s string) (ip4, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return 0, false
	}
	a = a.Unmap()
	if !a.Is4() {
		return 0, false
	}
	b := a.As4()
	return ip4(binary.BigEndian.Uint32(b[:])), true
}

// parsemask parses a mask as a dotted IPv4 mask or /bits, where bits
// above 32 count from the start of a v4-mapped address, as iproute
// gives them.
func parsemask(s string) (ip4, bool) {
	if strings.HasPrefix(s, "/") {
		n, err := strconv.Atoi(s[1:])
		if err != nil {
			return 0, false
		}
		if n > 32 {
			n -= 96
		}
		if n <= 0 || n > 32 {
			return 0, n == 0
		}
		return allbits << (32 - n), true
	}
	return parseip(s)
}

func riplisten() *os.File {
	addr := netdir + "/udp!*!rip"
	if announce != "" {
		addr = announce
	}
	c, err := dial.Announce(addr)
	if err != nil {
		fatal(fmt.Sprintf("can't announce %s: %s", addr, sys.Errstr()))
	}
	if _, err := c.Cfd.Write([]byte("headers")); err != nil {
		fatal("can't set udp headers: " + sys.Errstr())
	}
	fd, err := sys.Open(c.Dir+"/data", sys.ORDWR)
	if err != nil {
		fatal(fmt.Sprintf("can't open %s: %s", c.Dir+"/data", sys.Errstr()))
	}
	return os.NewFile(uintptr(fd), "data")
}

func fatal(s string) {
	syslog(s)
	sys.Exits("fail:error")
}
