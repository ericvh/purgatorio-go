// Command ip/bootpd answers BOOTP requests (Inferno bootpd(8)) from the
// network data base: a client is found by its Ethernet address (or the
// address it gives), and told its IP address, the boot file and, in
// Plan 9's vendor field, the subnet mask, file server, authentication
// server and gateway of its network. Replies are broadcast from port 67
// to port 68; with -t, bootpd listens on port 499 instead and answers
// the sender, for testing. -a addr, not in Inferno, does the same on
// addr, for tests that may not use a port below 1024.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/ndb"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const udphdrsize = 52

var (
	stderr  = os.Stderr
	debug   int
	sniff   bool
	verbose = true
	siaddr  netip.Addr
	myname  string
	netdir  = "/net"
	db      *ndb.DB
	ndbfile = "/lib/ndb/local"
	testing bool
	announce string
	noaddr  = netip.IPv6Unspecified()
	v4no    = netip.IPv4Unspecified()
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("bootpd [-dsqv] [-f file] [-x network] [-a addr]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd':
			debug++
		case 's':
			sniff = true
			debug = 255
		case 'q':
			verbose = false
		case 'v':
			verbose = true
		case 'x':
			netdir = a.EArg()
		case 'f':
			ndbfile = a.EArg()
		case 't':
			testing = true
			debug = 1
			verbose = true
		case 'a':
			announce = a.EArg()
			testing = true
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	sys.Pctl(sys.FORKFD|sys.FORKNS, nil)
	if !sniff {
		if err := dbread(); err != "" {
			fatal(err)
		}
	}
	myname = sysname()
	if myname == "" {
		fatal("system name not set")
	}
	var err string
	if siaddr, err = csquery(myname); err != "" {
		fatal(fmt.Sprintf("can't find IP address for %s: %s", myname, err))
	}
	if debug > 0 {
		fmt.Fprintf(stderr, "bootpd: local IP address is %s\n", siaddr)
	}
	addr := netdir + "/udp!*!67"
	if testing {
		addr = netdir + "/udp!*!499"
	}
	if announce != "" {
		addr = announce
	}
	if debug > 0 {
		fmt.Fprintf(stderr, "bootpd: announcing %s\n", addr)
	}
	c, e := dial.Announce(addr)
	if e != nil {
		fatal(fmt.Sprintf("can't announce %s: %s", addr, sys.Errstr()))
	}
	if _, e := c.Cfd.Write([]byte("headers")); e != nil {
		fatal("can't set headers mode: " + sys.Errstr())
	}
	if debug > 0 {
		fmt.Fprintf(stderr, "bootpd: opening %s/data\n", c.Dir)
	}
	fd, e := sys.Open(c.Dir+"/data", sys.ORDWR)
	if e != nil {
		fatal(fmt.Sprintf("can't open %s/data: %s", c.Dir, sys.Errstr()))
	}
	server(os.NewFile(uintptr(fd), "data"))
}

func fatal(s string) {
	stderr.WriteString("bootpd: " + s + "\n")
	sys.Exits("fail:error")
}

func server(dfd *os.File) {
	buf := make([]byte, 2048)
	badread := 0
	for {
		n, err := dfd.Read(buf)
		if err != nil {
			if badread++; badread > 10 {
				break
			}
			continue
		}
		badread = 0
		if n < udphdrsize {
			if debug > 0 {
				fmt.Fprintf(stderr, "bootpd: short Udphdr: %d bytes\n", n)
			}
			continue
		}
		hdr := append([]byte(nil), buf[:udphdrsize]...)
		raddr := netip.AddrFrom16([16]byte(hdr[0:16])).Unmap()
		rport := binary.BigEndian.Uint16(hdr[48:])
		if debug > 0 {
			fmt.Fprintf(stderr, "bootpd: received request from udp!%s!%d\n", raddr, rport)
		}
		if n < udphdrsize+300 {
			if debug > 0 {
				fmt.Fprintf(stderr, "bootpd: short request of %d bytes\n", n-udphdrsize)
			}
			continue
		}
		bp, e := unpack(buf[udphdrsize:n])
		if e != "" {
			if debug > 0 {
				fmt.Fprintf(stderr, "bootpd: can't unpack packet: %s\n", e)
			}
			continue
		}
		if debug >= 2 {
			fmt.Fprintf(stderr, "bootpd: recvd {%s}\n", bp.text())
		}
		if sniff {
			continue
		}
		if bp.htype != 1 || bp.hlen != 6 {
			// if it isn't ether, we don't do it
			if debug > 0 {
				fmt.Fprintf(stderr, "bootpd: hardware type not ether; ignoring.\n")
			}
			continue
		}
		if e := dbread(); e != "" {
			fmt.Fprintf(stderr, "bootpd: getreply: dbread failed: %s\n", e)
			continue
		}
		rec := lookup(bp)
		if rec == nil {
			// we can't answer this request
			if debug > 0 {
				fmt.Fprintf(stderr, "bootpd: cannot answer request.\n")
			}
			continue
		}
		if debug > 0 {
			fmt.Fprintf(stderr, "bootpd: found a matching entry: {%s}\n", rec.text())
		}
		mkreply(bp, rec)
		if verbose {
			fmt.Printf("bootpd: %s -> %s %s\n", ethertext(rec.ha), rec.hostname, rec.ip)
		}
		if debug > 0 {
			fmt.Fprintf(stderr, "bootpd: reply {%s}\n", bp.text())
		}
		repl := bp.pack()
		if !testing {
			arpenter(rec.ip.String(), ethertext(rec.ha))
		}
		send(dfd, hdr, repl)
	}
	fmt.Fprintf(stderr, "bootpd: %d read errors: %s\n", badread, sys.Errstr())
}

func arpenter(ip, ha string) {
	if debug > 0 {
		fmt.Fprintf(stderr, "bootpd: arp: %s -> %s\n", ip, ha)
	}
	fd, err := sys.Open(netdir+"/arp", sys.OWRITE)
	if err != nil {
		if debug > 0 {
			fmt.Fprintf(stderr, "bootpd: arp open failed: %s\n", sys.Errstr())
		}
		return
	}
	f := os.NewFile(uintptr(fd), "arp")
	defer f.Close()
	if _, err := f.Write([]byte("add " + ip + " " + ha)); err != nil && debug > 0 {
		fmt.Fprintf(stderr, "bootpd: error writing arp: %s\n", sys.Errstr())
	}
}

func sysname() string {
	if t := rf("/dev/sysname"); t != "" {
		return t
	}
	return rf("#e/sysname")
}

func rf(name string) string {
	b, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(b)
}

// csquery gets a local IP address by translating our name with cs(8).
func csquery(name string) (netip.Addr, string) {
	csfile := netdir + "/cs"
	fd, err := sys.Open(csfile, sys.ORDWR)
	if err != nil {
		return noaddr, fmt.Sprintf("can't open %s: %s", csfile, sys.Errstr())
	}
	f := os.NewFile(uintptr(fd), "cs")
	defer f.Close()
	if _, err := f.Write([]byte("net!" + name + "!0")); err != nil {
		return noaddr, fmt.Sprintf("can't translate net!%s!0: %s", name, sys.Errstr())
	}
	f.Seek(0, 0)
	b := make([]byte, 1024)
	n, _ := f.Read(b)
	if n <= 0 {
		return noaddr, "no result from " + csfile
	}
	fl := strings.Fields(string(b[:n]))
	if len(fl) != 2 {
		return noaddr, "bad cs reply format"
	}
	h, _, ok := strings.Cut(fl[1], "!")
	if !ok {
		return noaddr, "bad cs reply format"
	}
	a, err := netip.ParseAddr(h)
	if err != nil || !ipok(a.Unmap()) {
		return noaddr, "can't parse address: " + h
	}
	return a.Unmap(), ""
}

// hostinfo is what the data base says of a client.
type hostinfo struct {
	hostname string
	ha       []byte     // hardware address
	ip       netip.Addr // client IP address
	bootf    string     // boot file path
	netmask  netip.Addr // subnet mask
	ipgw     netip.Addr // gateway IP address
	fs       netip.Addr // file server IP address
	auth     netip.Addr // authentication server IP address
}

func (r *hostinfo) text() string {
	return fmt.Sprintf("ha=%s ip=%s bf=%s sm=%s gw=%s fs=%s au=%s",
		ethertext(r.ha), r.ip, r.bootf, r.netmask, r.ipgw, r.fs, r.auth)
}

func send(dfd *os.File, hdr []byte, msg []byte) {
	if announce != "" {
		// answer the sender from the announced conversation
		if _, err := dfd.Write(append(hdr, msg...)); err != nil {
			fmt.Fprintf(stderr, "bootpd: udp write error: %s\n", sys.Errstr())
		}
		return
	}
	replyaddr := netdir + "/udp!255.255.255.255!68" // TO DO: gateway
	lport := "67"
	if testing {
		raddr := netip.AddrFrom16([16]byte(hdr[0:16])).Unmap()
		replyaddr = fmt.Sprintf("udp!%s!%d", raddr, binary.BigEndian.Uint16(hdr[48:]))
		lport = "499"
	}
	c, err := dial.Dial(replyaddr, lport)
	if err != nil {
		fmt.Fprintf(stderr, "bootpd: can't dial %s for reply: %s\n", replyaddr, sys.Errstr())
		return
	}
	defer c.Dfd.Close()
	defer c.Cfd.Close()
	if n, _ := c.Dfd.Write(msg); n != len(msg) {
		fmt.Fprintf(stderr, "bootpd: udp write error: %s\n", sys.Errstr())
	}
}

func mkreply(bp *bootp, rec *hostinfo) {
	bp.op = 2 // boot reply
	bp.yiaddr = rec.ip
	bp.siaddr = siaddr
	bp.giaddr = v4no
	bp.sname = myname
	bp.file = rec.bootf
	bp.vend = []byte(fmt.Sprintf("p9  %s %s %s %s", rec.netmask, rec.fs, rec.auth, rec.ipgw))
}

func dbread() string {
	d, err := ndb.Open(ndbfile)
	if err != nil {
		if db != nil {
			return ""
		}
		return fmt.Sprintf("cannot open %s: %s", ndbfile, sys.Errstr())
	}
	db = d
	return ""
}

func ipok(a netip.Addr) bool {
	return a.Is4() && !a.IsUnspecified() && !a.IsMulticast()
}

func lookup(bp *bootp) *hostinfo {
	if db == nil {
		return nil
	}
	hwaddr := ethertext(bp.chaddr)
	if ipok(bp.ciaddr) {
		// client thinks it knows its address; check it matches the MAC address
		ipaddr := bp.ciaddr.String()
		var p *ndb.Ptr
		for {
			var e *ndb.Entry
			if e, p = db.FindByAttr(p, "ip", ipaddr, "ether"); e == nil {
				break
			}
			if inf := matchandfill(e, "ip", ipaddr, "ether", hwaddr); inf != nil {
				return inf
			}
		}
	}
	// look up an ip address associated with the MAC address
	var p *ndb.Ptr
	for {
		var e *ndb.Entry
		if e, p = db.FindByAttr(p, "ether", hwaddr, "ip"); e == nil {
			break
		}
		if inf := matchandfill(e, "ether", hwaddr, "ip", ""); inf != nil {
			return inf
		}
	}
	return nil
}

func matchandfill(e *ndb.Entry, attr, val, rattr, rval string) *hostinfo {
	for _, m := range e.FindByAttr(attr, val, rattr) {
		for _, a := range m.Attrs {
			if rval == "" || strings.EqualFold(a.Val, rval) {
				if inf := fillup(m.Line, e); inf != nil {
					return inf
				}
				break
			}
		}
	}
	return nil
}

func fillup(line *ndb.Tuples, e *ndb.Entry) *hostinfo {
	inf := &hostinfo{netmask: noaddr, ipgw: noaddr, fs: v4no, auth: v4no}
	inf.hostname = find(line, e, "sys")
	if s := find(line, e, "ether"); s != "" {
		inf.ha = etherparse(s)
	}
	s := find(line, e, "ip")
	if s == "" {
		return nil
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	inf.ip = ip.Unmap()
	info := db.IPInfo(s, "ipmask", "ipgw", "fs", "FILESERVER", "SIGNER", "auth", "bootf")
	inf.netmask = takemask(info["ipmask"], inf.netmask)
	inf.ipgw = takeip(info["ipgw"], inf.ipgw)
	inf.fs = takeip(append(info["fs"], info["FILESERVER"]...), inf.fs)
	inf.auth = takeip(append(info["auth"], info["SIGNER"]...), inf.auth)
	if b := info["bootf"]; len(b) > 0 {
		inf.bootf = b[0]
	}
	return inf
}

func takeip(vals []string, a netip.Addr) netip.Addr {
	if len(vals) == 0 || !(a == noaddr || a == v4no) {
		return a
	}
	if na, ok := parseip(vals[0]); ok {
		return na
	}
	return a
}

func takemask(vals []string, a netip.Addr) netip.Addr {
	if len(vals) == 0 || !(a == noaddr || a == v4no) {
		return a
	}
	m := vals[0]
	if strings.HasPrefix(m, "/") {
		var n int
		if _, err := fmt.Sscanf(m, "/%d", &n); err != nil {
			return a
		}
		if n > 32 {
			n -= 96
		}
		if n < 0 || n > 32 {
			return a
		}
		v := ^uint32(0) << (32 - n)
		if n == 0 {
			v = 0
		}
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		return netip.AddrFrom4(b)
	}
	na, err := netip.ParseAddr(m)
	if err != nil {
		return a
	}
	return na.Unmap()
}

// parseip parses an address, or looks it up if it is a system name.
func parseip(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil {
		if a, err = netip.ParseAddr(findbyattr("sys", s, "ip")); err != nil {
			return noaddr, false
		}
	}
	return a.Unmap(), true
}

func find(line *ndb.Tuples, e *ndb.Entry, attr string) string {
	if line != nil {
		if a := line.Find(attr); a != nil {
			return a[0].Val
		}
	}
	if e != nil {
		for _, m := range e.Find(attr) {
			if m.Attrs != nil {
				return m.Attrs[0].Val
			}
		}
	}
	return ""
}

func findbyattr(attr, val, rattr string) string {
	var p *ndb.Ptr
	for {
		var e *ndb.Entry
		if e, p = db.FindByAttr(p, attr, val, rattr); e == nil {
			return ""
		}
		if v := e.Find(rattr); v != nil {
			return v[0].Attrs[0].Val
		}
	}
}

// ethertext and etherparse are Inferno's ether module's text and parse:
// 12 hex digits, or with : between the bytes.
func ethertext(a []byte) string { return hex.EncodeToString(a) }

func etherparse(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, ":", ""))
	if err != nil {
		return nil
	}
	return b
}

func magic(cookie []byte) string {
	switch string(cookie) {
	case "p9  ":
		return "plan9"
	case "\x63\x82\x53\x63":
		return "rfc1048"
	case "CMU\x00":
		return "cmu"
	}
	var s []string
	for _, c := range cookie {
		s = append(s, fmt.Sprint(c))
	}
	return strings.Join(s, ".")
}

// bootp is a BOOTP message (RFC 951).
type bootp struct {
	op, htype, hlen, hops int
	xid                   uint32
	secs, flags           int
	ciaddr, yiaddr        netip.Addr
	siaddr, giaddr        netip.Addr
	chaddr                []byte
	sname, file           string
	vend                  []byte
}

func v4(b []byte) netip.Addr { return netip.AddrFrom4([4]byte(b)) }

func unpack(data []byte) (*bootp, string) {
	if len(data) < 300 {
		return nil, "too short"
	}
	bp := &bootp{op: int(data[0]), htype: int(data[1]), hlen: int(data[2]), hops: int(data[3])}
	if bp.hlen > 16 {
		return nil, "length error"
	}
	bp.xid = binary.BigEndian.Uint32(data[4:])
	bp.secs = int(binary.BigEndian.Uint16(data[8:]))
	bp.flags = int(binary.BigEndian.Uint16(data[10:]))
	bp.ciaddr = v4(data[12:16])
	bp.yiaddr = v4(data[16:20])
	bp.siaddr = v4(data[20:24])
	bp.giaddr = v4(data[24:28])
	bp.chaddr = append([]byte(nil), data[28:28+bp.hlen]...)
	bp.sname = ctostr(data[44:108])
	bp.file = ctostr(data[108:236])
	bp.vend = append([]byte(nil), data[236:300]...)
	return bp, ""
}

func put4(b []byte, a netip.Addr) {
	if a.Is4() {
		x := a.As4()
		copy(b, x[:])
	}
}

func (bp *bootp) pack() []byte {
	data := make([]byte, 364)
	data[0], data[1], data[2], data[3] = byte(bp.op), byte(bp.htype), byte(bp.hlen), byte(bp.hops)
	binary.BigEndian.PutUint32(data[4:], bp.xid)
	binary.BigEndian.PutUint16(data[8:], uint16(bp.secs))
	binary.BigEndian.PutUint16(data[10:], uint16(bp.flags))
	put4(data[12:], bp.ciaddr)
	put4(data[16:], bp.yiaddr)
	put4(data[20:], bp.siaddr)
	put4(data[24:], bp.giaddr)
	copy(data[28:44], bp.chaddr)
	copy(data[44:107], bp.sname)
	copy(data[108:235], bp.file)
	copy(data[236:], bp.vend)
	return data
}

func ctostr(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func (bp *bootp) text() string {
	s := fmt.Sprintf("op=%d htype=%d hlen=%d hops=%d xid=%d secs=%d ciaddr=%s yiaddr=%s",
		bp.op, bp.htype, bp.hlen, bp.hops, bp.xid, bp.secs, bp.ciaddr, bp.yiaddr)
	m := magic(bp.vend[:4])
	s += fmt.Sprintf(" server=%s gateway=%s hwaddr=%s host=%s file=%s magic=%s",
		bp.siaddr, bp.giaddr, quote.Quote(ethertext(bp.chaddr)), quote.Quote(bp.sname), quote.Quote(bp.file), quote.Quote(m))
	if m == "plan9" {
		s += "(" + ctostr(bp.vend) + ")"
	}
	return s
}
