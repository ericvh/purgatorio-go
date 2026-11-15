// Command ip/dhcp configures an IP interface by DHCP (Inferno dhcp(8)),
// or BOOTP with -b. ifcdir is the interface's directory in /net/ipifc:
// its status file names the network device, whose addr file gives the
// hardware address; the address got is written to its ctl (not with
// -n), the default route to /net/iproute and the configuration to
// /net/ndb. -p prints the configuration, -m keeps renewing the lease,
// -r retries until it succeeds, -d traces the messages.
//
// On the kernel the interface is /net/ipifc/0, bound to /net/ether0.
// The emu lists the host's interfaces in /net/ipifc but cannot change
// them: opening ctl is refused, so there dhcp needs -n. -S addr, not in
// Inferno, asks the server at addr from any local port instead of
// broadcasting from port 68, so that it can be tested on loopback.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/feed"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	self         = "/bin/ip/dhcp.wasm"
	retryTime    = 10 * time.Second
	udphdrlen    = 52
	udpraddr     = 0
	udprport     = 48
	bootprequest = 1
	bootpreply   = 2
)

// DHCP message types.
const (
	notDHCP = iota
	discover
	offer
	request
	decline
	ack
	nak
	release
	inform
)

// Options (RFC 2132).
const (
	Opad         = 0
	Oend         = 255
	Omask        = 1
	Orouter      = 3
	Odnsserver   = 6
	Ohostname    = 12
	Odomainname  = 15
	Ontpserver   = 42
	Ovendorinfo  = 43
	Oipaddr      = 50
	Olease       = 51
	Otype        = 53
	Oserverid    = 54
	Oparams      = 55
	Ovendorclass = 60
	Oclientid    = 61
	Otftpserver  = 66
	OP9fs        = 128 // in the vendor options
	OP9auth      = 129
	infinite     = 0xFFFFFFFF
)

var magic = []byte{99, 130, 83, 99}

type option struct {
	code int
	val  []byte
}

// bootconf is the configuration got.
type bootconf struct {
	ip, ipmask, ipgw string
	bootf            string
	dhcpip, siaddr   string
	serverid, bootip string
	sys, dom         string
	lease            uint32
	options          [256][]byte
	vendor           [256][]byte
}

func (c *bootconf) get(n int) []byte { return c.options[n] }

func (c *bootconf) getint(n int) uint32 {
	var v uint32
	for _, b := range c.options[n] {
		v = v<<8 | uint32(b)
	}
	return v
}

func v4text(a []byte) string { return fmt.Sprintf("%d.%d.%d.%d", a[0], a[1], a[2], a[3]) }

func getip(a []byte) string {
	if len(a) < 4 {
		return ""
	}
	return v4text(a)
}

func gets(a []byte) string {
	if i := strings.IndexByte(string(a), 0); i >= 0 {
		a = a[:i]
	}
	return string(a)
}

type dhcpmsg struct {
	udphdr         []byte
	op, htype      int
	hops           int
	xid            uint32
	secs, flags    int
	ciaddr, yiaddr netip.Addr
	siaddr, giaddr netip.Addr
	chaddr         []byte
	sname, file    string
	options        []option
	dhcpop         int
}

var (
	trace   int
	netdir  = "/net"
	server  string
	xidgen  = rand.Uint32()
	rd      *feed.Reader
	dfd     *os.File
	ifcctl  *os.File
	v4no    = netip.IPv4Unspecified()
	v4bcast = netip.AddrFrom4([4]byte{255, 255, 255, 255})
)

func main() {
	feed.Serve()
	a := arg.New(os.Args)
	a.SetUsage("dhcp [-bdmnpr] [-g ipgw] [-h hostname] [-x /net] ifcdir [ip [ipmask]]")
	var pcfg, bootp, monitor, retry, noctl bool
	cfg := &bootconf{}
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'b':
			bootp = true
		case 'd':
			trace++
		case 'g':
			cfg.ipgw = a.EArg()
		case 'h':
			cfg.options[Ohostname] = []byte(a.EArg())
		case 'm':
			monitor = true
		case 'n':
			noctl = true
		case 'p':
			pcfg = true
		case 'r':
			retry = true
		case 'x':
			netdir = a.EArg()
		case 'S':
			server = a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 || len(args) > 3 {
		a.Usage()
	}
	ifcdir := args[0]
	if len(args) > 1 {
		cfg.ip = args[1]
	}
	if len(args) > 2 {
		cfg.ipmask = args[2]
	}
	if !noctl {
		fd, err := sys.Open(ifcdir+"/ctl", sys.OWRITE)
		if err != nil {
			fatal(fmt.Sprintf("cannot open %s/ctl: %s", ifcdir, sys.Errstr()))
		}
		ifcctl = os.NewFile(uintptr(fd), "ctl")
	}
	etherdir := finddev(ifcdir)
	if etherdir == "" {
		fatal(fmt.Sprintf("cannot find network device in %s/status: %s", ifcdir, sys.Errstr()))
	}
	if etherdir[0] != '/' && etherdir[0] != '#' {
		etherdir = netdir + "/" + etherdir
	}
	for {
		var e string
		if bootp {
			var c *bootconf
			if c, e = dobootp(etherdir+"/addr", cfg); e == "" {
				applycfg(c)
				if pcfg {
					printcfg(c)
				}
				break
			}
		} else {
			var c *bootconf
			var req *dhcpmsg
			var initopt []option
			if c, req, initopt, e = dodhcp(etherdir+"/addr", cfg); e == "" {
				if pcfg {
					printcfg(c)
				}
				if c.lease > 0 && monitor {
					leasemon(c, cfg, req, initopt, pcfg)
				}
				break
			}
		}
		if !retry {
			fatal("failed to configure network: " + e)
		}
		fmt.Fprintf(os.Stderr, "dhcp: failed to configure network: %s; retrying", e)
		time.Sleep(retryTime)
	}
	if rd != nil {
		rd.Close()
	}
}

func printcfg(c *bootconf) {
	fmt.Printf("ip=%s ipmask=%s ipgw=%s iplease=%d\n", c.ip, c.ipmask, c.ipgw, c.lease)
}

func finddev(ifcdir string) string {
	b, err := os.ReadFile(ifcdir + "/status")
	if err != nil {
		return ""
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		sys.Werrstr("unexpected format for status file")
		return ""
	}
	return f[1]
}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "dhcp: %s\n", s)
	sys.Exits("fail:error")
}

func gethaddr(device string) (int, []byte, string) {
	b, err := os.ReadFile(device)
	if err != nil {
		return -1, nil, sys.Errstr()
	}
	if len(b) == 0 {
		return -1, nil, "empty address file"
	}
	s := strings.TrimSpace(string(b))
	addr, err := hex.DecodeString(s[:len(s)/2*2])
	if err != nil {
		return -1, nil, "bad address syntax"
	}
	return 1, addr, ""
}

func newrequest(dest netip.Addr, bootfile string, htype int, haddr []byte, ciaddr netip.Addr, cfg *bootconf) *dhcpmsg {
	hdr := make([]byte, udphdrlen)
	a16 := dest.As16()
	copy(hdr[udpraddr:], a16[:])
	binary.BigEndian.PutUint16(hdr[udprport:], 67)
	m := &dhcpmsg{op: bootprequest, udphdr: hdr, htype: htype, chaddr: haddr, ciaddr: ciaddr,
		yiaddr: v4no, siaddr: v4no, giaddr: v4no, file: bootfile}
	for i := 255; i >= 0; i-- {
		if cfg.options[i] != nil {
			m.options = append([]option{{i, cfg.options[i]}}, m.options...)
		}
	}
	clientid := append([]byte{byte(htype)}, haddr...)
	m.options = append([]option{{Ovendorclass, []byte("plan9_386")}, {Oclientid, clientid}}, m.options...)
	return m
}

// udpannounce opens the conversation for DHCP: port 68, or with -S any
// port, talking to the server given.
func udpannounce() string {
	addr := netdir + "/udp!*!68"
	if server != "" {
		addr = netdir + "/udp!*!0"
	}
	c, err := dial.Announce(addr)
	if err != nil {
		return "can't announce dhcp port: " + sys.Errstr()
	}
	if _, err := c.Cfd.Write([]byte("headers")); err != nil {
		return "can't set headers mode on dhcp port: " + sys.Errstr()
	}
	fd, err := sys.Open(c.Dir+"/data", sys.ORDWR)
	if err != nil {
		return fmt.Sprintf("can't open %s: %s", c.Dir+"/data", sys.Errstr())
	}
	dfd = os.NewFile(uintptr(fd), "data")
	if rd, err = feed.Start(self, fd); err != nil {
		return "can't start reader: " + sys.Errstr()
	}
	return ""
}

// serveraddr is where requests go: the broadcast address and port 67,
// or the server given with -S.
func serveraddr(dest netip.Addr) (netip.Addr, uint16) {
	if server != "" {
		f := strings.Split(server, "!")
		if len(f) == 3 {
			if a, err := netip.ParseAddr(f[1]); err == nil {
				var p uint16
				fmt.Sscan(f[2], &p)
				return a, p
			}
		}
	}
	return dest, 67
}

func ifcnoaddr(s string) {
	if ifcctl != nil {
		if _, err := ifcctl.Write([]byte(s + " :: ::")); err != nil && trace > 0 {
			fmt.Printf("dhcp: ctl %s: %s\n", s, sys.Errstr())
		}
	}
}

func setup(device string, cfg *bootconf) (*dhcpmsg, string) {
	htype, mac, err := gethaddr(device)
	if htype < 0 {
		return nil, "can't get hardware MAC address: " + err
	}
	ciaddr := v4no
	if cfg.ip != "" {
		a, e := netip.ParseAddr(cfg.ip)
		if e != nil {
			return nil, "invalid ip address: " + cfg.ip
		}
		ciaddr = a.Unmap()
	}
	if e := udpannounce(); e != "" {
		return nil, e
	}
	return newrequest(v4bcast, cfg.bootf, htype, mac, ciaddr, cfg), ""
}

func dobootp(device string, cfg *bootconf) (*bootconf, string) {
	req, err := setup(device, cfg)
	if err != "" {
		return nil, err
	}
	ifcnoaddr("add")
	xidgen++
	rep := exchange(xidgen, req, 1<<notDHCP)
	ifcnoaddr("remove")
	if rep == nil {
		return nil, "no response to BOOTP request"
	}
	return fillbootconf(cfg, rep), ""
}

var defparams = []byte{Omask, Orouter, Odnsserver, Ohostname, Odomainname, Ontpserver}

func dodhcp(device string, cfg *bootconf) (*bootconf, *dhcpmsg, []option, string) {
	req, err := setup(device, cfg)
	if err != "" {
		return nil, nil, nil, err
	}
	// RFC2131 requires the parameters to be repeated each time
	initopt := append([]option{{Oparams, defparams}}, req.options...)
	var rep *dhcpmsg
	ifcnoaddr("add")
	if req.ciaddr.IsValid() && req.ciaddr != v4no {
		rep = reacquire(req, initopt, req.ciaddr)
	}
	if rep == nil {
		rep = askround(req, initopt)
	}
	ifcnoaddr("remove")
	if rep == nil {
		return nil, nil, nil, "no response"
	}
	conf := fillbootconf(cfg, rep)
	applycfg(conf)
	req.ciaddr = rep.yiaddr
	return conf, req, initopt, ""
}

// leasemon keeps the lease, renewing it, and asks again when it is lost.
func leasemon(conf, cfg *bootconf, req *dhcpmsg, initopt []option, pcfg bool) {
	for {
		var rep *dhcpmsg
		for {
			if rep = tenancy(req, conf.lease); rep == nil {
				break
			}
			if rep.dhcpop == nak || rep.ciaddr != req.ciaddr {
				break
			}
			copy(req.udphdr[udpraddr:udpraddr+16], rep.udphdr[udpraddr:udpraddr+16])
			conf = fillbootconf(cfg, rep)
			if pcfg {
				printcfg(conf)
			}
		}
		removecfg(conf)
		ifcnoaddr("add")
		for {
			if rep = askround(req, initopt); rep != nil {
				break
			}
			fmt.Fprintf(os.Stderr, "dhcp: no response")
			time.Sleep(time.Minute)
		}
		ifcnoaddr("remove")
		conf = fillbootconf(cfg, rep)
		applycfg(conf)
		if pcfg {
			printcfg(conf)
		}
		if conf.lease == 0 {
			return
		}
		req.ciaddr = rep.yiaddr
	}
}

// reacquire is INIT-REBOOT: it asks once for an address it knows.
func reacquire(req *dhcpmsg, initopt []option, addr netip.Addr) *dhcpmsg {
	req.ciaddr = v4no
	a4 := addr.As4()
	xidgen++
	rep := dorequest(xidgen, req, append([]option{{Oipaddr, a4[:]}}, initopt...))
	if rep != nil && rep.dhcpop == ack && addr == rep.yiaddr {
		if trace > 0 {
			fmt.Printf("req: server accepted\n")
		}
		copy(req.udphdr[udpraddr:udpraddr+16], rep.udphdr[udpraddr:udpraddr+16])
		return rep
	}
	if trace > 0 {
		fmt.Printf("req: cannot reclaim\n")
	}
	return nil
}

func askround(req *dhcpmsg, initopt []option) *dhcpmsg {
	// INIT
	req.ciaddr = v4no
	b := v4bcast.As16()
	copy(req.udphdr[udpraddr:], b[:])
	for retries := 0; retries < 5; retries++ {
		// SELECTING
		req.dhcpop = discover
		req.options = initopt
		xidgen++
		rep := exchange(xidgen, req, 1<<offer)
		if rep == nil {
			break
		}
		if !rep.yiaddr.IsValid() || rep.yiaddr == v4no {
			continue // server has no idea either
		}
		serverid := getopt(rep.options, Oserverid, 4)
		if serverid == nil {
			continue // broken server
		}
		// REQUESTING
		y := rep.yiaddr.As4()
		options := append([]option{{Oserverid, serverid}, {Oipaddr, y[:]}}, initopt...)
		if lease := getlease(rep); lease != nil {
			options = append([]option{{Olease, lease}}, options...)
		}
		if rep = dorequest(rep.xid, req, options); rep != nil {
			if trace > 0 {
				fmt.Printf("req: server accepted\n")
			}
			copy(req.udphdr[udpraddr:udpraddr+16], rep.udphdr[udpraddr:udpraddr+16])
			return rep
		}
	}
	return nil
}

func dorequest(xid uint32, req *dhcpmsg, options []option) *dhcpmsg {
	req.dhcpop = request
	req.options = options
	rep := exchange(xid, req, 1<<ack|1<<nak)
	if rep == nil || rep.dhcpop == nak {
		return nil
	}
	return rep
}

func tenancy(req *dhcpmsg, leasesec uint32) *dhcpmsg {
	t3 := time.Duration(leasesec) * time.Second // lease expires; restart
	t2 := 3 * t3 / 4                            // broadcast renewal request at ¾ time
	t1 := t2 / 2                                // renew lease with original server at ½ time
	time.Sleep(t1)
	// RENEW
	if rep := renewing(req, t1, t2); rep != nil {
		return rep
	}
	// REBIND: now try broadcast
	b := v4bcast.As16()
	copy(req.udphdr[udpraddr:], b[:])
	return renewing(req, t2, t3)
}

func renewing(req *dhcpmsg, a, b time.Duration) *dhcpmsg {
	for a < b {
		if rep := exchange(req.xid, req, 1<<ack|1<<nak); rep != nil {
			return rep
		}
		delta := max((b-a)/2, time.Minute)
		time.Sleep(delta)
		a += delta
	}
	return nil
}

func getlease(m *dhcpmsg) []byte {
	lease := getopt(m.options, Olease, 4)
	if lease == nil {
		return nil
	}
	if binary.BigEndian.Uint32(lease) == 0 {
		lease = binary.BigEndian.AppendUint32(nil, 15*60)
	}
	return lease
}

func getopt(options []option, op, minlen int) []byte {
	for _, o := range options {
		if o.code == op && len(o.val) >= minlen {
			return o.val
		}
	}
	return nil
}

func parseopt(a []byte, isdhcp bool) (int, []option) {
	var opts []option
	xop := notDHCP
	for i := 0; i < len(a); {
		op := int(a[i])
		i++
		if op == Opad {
			continue
		}
		if op == Oend || i >= len(a) {
			break
		}
		l := int(a[i])
		i++
		if i+l > len(a) {
			break
		}
		if isdhcp && op == Otype {
			xop = int(a[i])
		} else {
			opts = append(opts, option{op, a[i : i+l]})
		}
		i += l
	}
	return xop, opts
}

func fillbootconf(init *bootconf, pkt *dhcpmsg) *bootconf {
	bc := &bootconf{}
	if init != nil {
		*bc = *init
	}
	for _, o := range pkt.options {
		if bc.options[o.code] == nil {
			bc.options[o.code] = o.val // give priority to first occurring
		}
	}
	if a := bc.get(Ovendorinfo); a != nil {
		_, vo := parseopt(a, false)
		for _, o := range vo {
			if bc.vendor[o.code] == nil {
				bc.vendor[o.code] = o.val
			}
		}
	}
	if pkt.yiaddr.IsValid() && pkt.yiaddr != v4no {
		bc.ip = pkt.yiaddr.String()
		if bc.ipmask = getip(bc.get(Omask)); bc.ipmask == "" {
			bc.ipmask = classmask(pkt.yiaddr)
		}
	}
	bc.bootf = pkt.file
	bc.dhcpip = netip.AddrFrom16([16]byte(pkt.udphdr[udpraddr : udpraddr+16])).Unmap().String()
	bc.siaddr = pkt.siaddr.String()
	bc.lease = bc.getint(Olease)
	if bc.lease == infinite {
		bc.lease = 0
	} else if trace > 1 {
		bc.lease = 2 * 60 // shorten time, for testing
	}
	bc.dom = gets(bc.get(Odomainname))
	s := gets(bc.get(Ohostname))
	if i := strings.IndexByte(s, '.'); i >= 0 {
		if bc.dom == "" {
			bc.dom = s[i+1:]
		}
		s = s[:i]
	}
	bc.sys = s
	if gw := getip(bc.get(Orouter)); gw != "" {
		bc.ipgw = gw
	}
	bc.bootip = getip(bc.get(Otftpserver))
	bc.serverid = getip(bc.get(Oserverid))
	return bc
}

func classmask(a netip.Addr) string {
	switch b := a.As4()[0]; {
	case b < 128:
		return "255.0.0.0"
	case b < 192:
		return "255.255.0.0"
	}
	return "255.255.255.0"
}

func put4(b []byte, a netip.Addr) {
	x := a.As4()
	copy(b, x[:])
}

func dhcpsend(xid uint32, m *dhcpmsg) {
	m.xid = xid
	abuf := make([]byte, 576+udphdrlen)
	copy(abuf, m.udphdr)
	to, port := serveraddr(netip.AddrFrom16([16]byte(m.udphdr[udpraddr : udpraddr+16])).Unmap())
	t16 := to.As16()
	copy(abuf[udpraddr:], t16[:])
	binary.BigEndian.PutUint16(abuf[udprport:], port)
	buf := abuf[udphdrlen:]
	buf[0], buf[1], buf[2], buf[3] = byte(m.op), byte(m.htype), byte(len(m.chaddr)), byte(m.hops)
	binary.BigEndian.PutUint32(buf[4:], xid)
	binary.BigEndian.PutUint16(buf[8:], uint16(m.secs))
	binary.BigEndian.PutUint16(buf[10:], uint16(m.flags))
	put4(buf[12:], m.ciaddr)
	put4(buf[16:], m.yiaddr)
	put4(buf[20:], m.siaddr)
	put4(buf[24:], m.giaddr)
	copy(buf[28:44], m.chaddr)
	copy(buf[44:107], m.sname)
	copy(buf[108:235], m.file)
	o := 236
	// RFC1542 suggests including magic and Oend as a minimum, even in BOOTP
	copy(buf[o:], magic)
	o += 4
	if m.dhcpop != notDHCP {
		buf[o], buf[o+1], buf[o+2] = Otype, 1, byte(m.dhcpop)
		o += 3
	}
	for _, opt := range m.options {
		if o+2+len(opt.val) >= len(buf) {
			break
		}
		buf[o], buf[o+1] = byte(opt.code), byte(len(opt.val))
		o += 2
		o += copy(buf[o:], opt.val)
	}
	buf[o] = Oend
	if trace > 0 {
		dumpdhcp(m, "->")
	}
	dfd.Write(abuf)
}

// readmsg returns the next BOOTP reply that came within ms
// milliseconds, or nil.
func readmsg(ms int) *dhcpmsg {
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for {
		wait := int(time.Until(deadline) / time.Millisecond)
		if wait <= 0 {
			return nil
		}
		abuf, _, err := rd.Read(wait)
		if err != nil {
			if trace > 0 {
				fmt.Printf("read error: %s\n", err)
			}
			time.Sleep(time.Duration(wait) * time.Millisecond)
			return nil
		}
		if abuf == nil {
			return nil
		}
		n := len(abuf)
		if n < udphdrlen+236 {
			if trace > 0 {
				fmt.Printf("short read: %d\n", n)
			}
			continue
		}
		buf := abuf[udphdrlen:]
		m := &dhcpmsg{op: int(buf[0])}
		if m.op != bootpreply {
			if trace > 0 {
				fmt.Printf("bootp: not reply, discarded\n")
			}
			continue
		}
		m.dhcpop = notDHCP
		if len(buf) >= 240 && string(buf[236:240]) == string(magic) {
			m.dhcpop, m.options = parseopt(buf[240:], true)
		}
		switch m.dhcpop {
		case notDHCP, ack, nak, offer:
		default:
			if trace > 0 {
				fmt.Printf("dhcp: ignore dhcp op %d\n", m.dhcpop)
			}
			continue
		}
		m.udphdr = abuf[:udphdrlen]
		m.htype = int(buf[1])
		hlen := min(int(buf[2]), 16)
		m.hops = int(buf[3])
		m.xid = binary.BigEndian.Uint32(buf[4:])
		m.secs = int(binary.BigEndian.Uint16(buf[8:]))
		m.flags = int(binary.BigEndian.Uint16(buf[10:]))
		m.ciaddr = netip.AddrFrom4([4]byte(buf[12:16]))
		m.yiaddr = netip.AddrFrom4([4]byte(buf[16:20]))
		m.siaddr = netip.AddrFrom4([4]byte(buf[20:24]))
		m.giaddr = netip.AddrFrom4([4]byte(buf[24:28]))
		m.chaddr = buf[28 : 28+hlen]
		m.sname = gets(buf[44:108])
		m.file = gets(buf[108:236])
		return m
	}
}

func exchange(xid uint32, req *dhcpmsg, accept int) *dhcpmsg {
	nsec := 3
	for count := 0; count < 5; count++ {
		dhcpsend(xid, req)
		deadline := time.Now().Add(time.Duration(nsec) * time.Second)
		for {
			wait := int(time.Until(deadline) / time.Millisecond)
			if wait <= 0 {
				break
			}
			rep := readmsg(wait)
			if rep == nil {
				break
			}
			if trace > 0 {
				dumpdhcp(rep, "<-")
			}
			if rep.op == bootpreply && rep.xid == req.xid && rep.ciaddr == req.ciaddr && string(rep.chaddr) == string(req.chaddr) {
				if accept&(1<<rep.dhcpop) == 0 {
					if trace > 0 {
						fmt.Printf("req: unexpected reply %s to %s\n", opname(rep.dhcpop), opname(req.dhcpop))
					}
					continue
				}
				return rep
			}
			if trace > 0 {
				fmt.Printf("req: mismatch\n")
			}
		}
		req.secs += nsec
		nsec++
	}
	return nil
}

// applycfg writes the configuration to the interface, iproute and ndb.
func applycfg(bc *bootconf) string {
	if bc.ip == "" {
		return "invalid address"
	}
	if ifcctl != nil {
		if _, err := ifcctl.Write([]byte("add " + bc.ip + " " + bc.ipmask)); err != nil {
			return "add interface: " + sys.Errstr()
		}
	}
	if bc.ipgw != "" {
		if fd, err := sys.Open(netdir+"/iproute", sys.OWRITE); err == nil {
			f := os.NewFile(uintptr(fd), "iproute")
			f.Write([]byte("add 0.0.0.0 0.0.0.0 " + bc.ipgw))
			f.Close()
		}
	}
	s := fmt.Sprintf("ip=%s ipmask=%s", bc.ip, bc.ipmask)
	if bc.ipgw != "" {
		s += " ipgw=" + bc.ipgw
	}
	s += "\n"
	if bc.sys != "" {
		s += "\tsys=" + bc.sys + "\n"
	}
	if bc.dom != "" {
		s += "\tdom=" + bc.sys + "." + bc.dom + "\n"
	}
	if a := getip(bc.vendor[OP9auth]); a != "" {
		s += "\tauth=" + a + "\n"
	}
	if a := getip(bc.vendor[OP9fs]); a != "" {
		s += "\tfs=" + a + "\n"
	}
	if a := getip(bc.get(Odnsserver)); a != "" {
		s += "\tdns=" + a + "\n"
	}
	if fd, err := sys.Open(netdir+"/ndb", sys.OWRITE|sys.OTRUNC); err == nil {
		f := os.NewFile(uintptr(fd), "ndb")
		f.Write([]byte(s))
		f.Close()
	}
	return ""
}

func removecfg(bc *bootconf) string {
	if ifcctl != nil {
		if _, err := ifcctl.Write([]byte("remove " + bc.ip + " " + bc.ipmask)); err != nil {
			return "remove address: " + sys.Errstr()
		}
	}
	bc.ip, bc.ipgw, bc.ipmask = "", "", ""
	return ""
}

var opnames = []string{discover: "Discover", offer: "Offer", request: "Request", decline: "Decline", ack: "Ack", nak: "Nak", release: "Release", inform: "Inform"}

func opname(op int) string {
	if op > 0 && op < len(opnames) {
		return opnames[op]
	}
	return fmt.Sprintf("OP%d", op)
}

func dumpdhcp(m *dhcpmsg, dir string) {
	s := ""
	ra := netip.AddrFrom16([16]byte(m.udphdr[udpraddr : udpraddr+16])).Unmap()
	fmt.Printf("%s %s/%d: ", dir, ra, binary.BigEndian.Uint16(m.udphdr[udprport:]))
	if m.dhcpop != notDHCP {
		s = " " + opname(m.dhcpop)
	}
	fmt.Printf("op %d%s htype %d hops %d xid %d\n", m.op, s, m.htype, m.hops, m.xid)
	fmt.Printf("\tsecs %d flags 0x%.4x\n", m.secs, m.flags)
	fmt.Printf("\tciaddr %s\n\tyiaddr %s\n\tsiaddr %s\n\tgiaddr %s\n", m.ciaddr, m.yiaddr, m.siaddr, m.giaddr)
	fmt.Printf("\tchaddr %s\n", hex.EncodeToString(m.chaddr))
	if m.sname != "" {
		fmt.Printf("\tsname %s\n", m.sname)
	}
	if m.file != "" {
		fmt.Printf("\tfile %s\n", m.file)
	}
	if m.options != nil {
		fmt.Printf("\t")
		printopts(m.options, opts)
		fmt.Printf("\n")
	}
}

const (
	optbytes = iota
	optaddr
	optint
	optstr
	optopts
	opthex
)

type optdesc struct {
	code  int
	name  string
	otype int
}

var opts = []optdesc{
	{Omask, "ipmask", optaddr}, {Orouter, "ipgw", optaddr}, {Odnsserver, "dns", optaddr},
	{Ohostname, "hostname", optstr}, {Odomainname, "domain", optstr}, {Ontpserver, "ntp", optaddr},
	{Oipaddr, "requestedip", optaddr}, {Olease, "lease", optint}, {Oserverid, "serverid", optaddr},
	{Otype, "dhcpop", optint}, {Ovendorclass, "vendorclass", optstr}, {Ovendorinfo, "vendorinfo", optopts},
	{Oparams, "params", optbytes}, {Otftpserver, "tftp", optaddr}, {Oclientid, "clientid", opthex},
}

var p9opts = []optdesc{{OP9fs, "fs", optaddr}, {OP9auth, "auth", optaddr}}

func printopts(options []option, tab []optdesc) {
	for _, o := range options {
		val := o.val
		fmt.Printf("(%d %d", o.code, len(val))
		var d *optdesc
		for i := range tab {
			if tab[i].code == o.code {
				d = &tab[i]
			}
		}
		if d == nil {
			for _, v := range val {
				fmt.Printf(" %d", v)
			}
		} else {
			fmt.Printf(" %s", d.name)
			switch d.otype {
			case optbytes:
				for _, v := range val {
					fmt.Printf(" %d", v)
				}
			case opthex:
				for _, v := range val {
					fmt.Printf(" %#.2x", v)
				}
			case optaddr:
				for ; len(val) >= 4; val = val[4:] {
					fmt.Printf(" %s", v4text(val))
				}
			case optstr:
				fmt.Printf(" \"%s\"", val)
			case optint:
				n := 0
				for _, v := range val {
					n = n<<8 | int(v)
				}
				fmt.Printf(" %d", n)
			case optopts:
				_, vo := parseopt(val, false)
				printopts(vo, p9opts)
			}
		}
		fmt.Printf(")")
	}
}
