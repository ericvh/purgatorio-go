// Command ndb/dns is the domain name resolver (Inferno dns(8)). It binds
// #s before /net and serves /net/dns: a query written to it, a name and
// a record type (ip, ns, cname, soa, ptr, mx, hinfo or all), is answered
// with one record per read:
//
//	www.example.com ip	93.184.216.34
//
// Names are tried first in the host's resolver (unless -h), then in the
// cache, then by asking the name servers the data base gives for the
// domain or, failing those, those in dns= (as RFC 1034's resolver does).
// A dns= entry may name a port as address!port.
//
// Requests are answered one at a time: a guest is single-threaded.
package main

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/feed"
	"github.com/ericvh/purgatorio-go/sdk/ndb"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const self = "/bin/ndb/dns.wasm"

type reply struct {
	fid   int
	query string
	attr  string
	addrs []string
	err   string
}

var (
	rlist    []*reply
	dnsfile  = "/lib/ndb/local"
	myname   string
	mntpt    = "/net"
	debug    bool
	referdns bool
	usehost  = true
	now      int64
	stderr   = os.Stderr
)

const dnsport = 53

func main() {
	feed.Serve()
	a := arg.New(os.Args)
	a.SetUsage("dns [-Drh] [-f dnsfile] [-x mntpt]")
	svcname := "#sdns"
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'D':
			debug = true
		case 'f':
			dnsfile = a.EArg()
		case 'h':
			usehost = false
		case 'r':
			referdns = true
		case 'x':
			mntpt = a.EArg()
			svcname = "#sdns" + strings.ReplaceAll(mntpt, "/", "_")
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	sys.Pctl(sys.NEWPGRP|sys.FORKFD, nil)
	dnsid = rand.IntN(1 << 16)
	myname = sysname()
	now = time.Now().Unix()
	readservers()
	sys.Remove(svcname + "/dns")
	sys.Unmount(svcname, mntpt)
	publish(svcname)
	if err := sys.Bind(svcname, mntpt, sys.MBEFORE); err != nil {
		fatal("can't bind #s on " + mntpt + ": " + sys.Errstr())
	}
	file, err := sys.File2chan(mntpt, "dns")
	if err != nil {
		fatal("can't make " + mntpt + "/dns: " + sys.Errstr())
	}
	sys.Detach()
	serve(file)
}

func fatal(s string) {
	stderr.WriteString("dns: " + s + "\n")
	sys.Exits("fail:error")
}

func publish(dir string) {
	d := sys.NullDir()
	d.Mode = 0o777
	if err := sys.Wstat(dir, &d); err != nil && debug {
		stderr.WriteString("dns: can't publish " + dir + ": " + sys.Errstr() + "\n")
	}
}

func serve(file *sys.FileIO) {
	for {
		req, err := file.Next()
		if err != nil {
			return
		}
		now = time.Now().Unix()
		switch r := req.(type) {
		case *sys.WriteReq:
			cleanfid(r.Fid) // each write cancels earlier requests
			if r.Closed() {
				continue
			}
			rp := &reply{fid: r.Fid}
			request(rp, string(r.Data))
			if rp.err != "" {
				if debug {
					stderr.WriteString("dns: " + string(r.Data) + ": " + rp.err + "\n")
				}
				r.Reply(0, "dns: "+rp.err)
				continue
			}
			rlist = append(rlist, rp)
			r.Reply(len(r.Data), "")
		case *sys.ReadReq:
			if r.Closed() {
				cleanfid(r.Fid)
				continue
			}
			rp := findfid(r.Fid)
			if rp == nil {
				r.Reply(nil, "unknown request")
				continue
			}
			if rp.err != "" || len(rp.addrs) == 0 {
				r.Reply(nil, rp.err)
				continue
			}
			addr := []byte(rp.addrs[0])
			rp.addrs = rp.addrs[1:]
			// The offset is ignored: each read returns the next record.
			r.Reply(addr[:min(len(addr), r.Count)], "")
		}
	}
}

func findfid(fid int) *reply {
	for _, r := range rlist {
		if r.fid == fid {
			return r
		}
	}
	return nil
}

func cleanfid(fid int) {
	l := rlist[:0]
	for _, r := range rlist {
		if r.fid != fid {
			l = append(l, r)
		}
	}
	clear(rlist[len(l):])
	rlist = l
}

func request(r *reply, query string) {
	name, attr, _ := strings.Cut(query, " ")
	r.query = name
	r.attr = strings.TrimLeft(attr, " ")
	t := rrtype(r.attr)
	if t < 0 {
		r.err = "unknown type"
	} else {
		r.addrs, r.err = dnslookup(r.query, t)
	}
	if r.addrs == nil && r.err == "" {
		r.err = "not found"
	}
}

func sysname() string {
	if t := rf("/dev/sysname"); t != "" {
		return t
	}
	t := rf("#e/sysname")
	if t == "" {
		if s := rf(mntpt + "/ndb"); s != "" {
			db := &ndb.DB{Files: []*ndb.File{ndb.OpenString(s)}}
			if e, _ := db.Find(nil, "sys"); e != nil {
				t = e.FindFirst("sys")
			}
		}
	}
	return t
}

func rf(name string) string {
	b, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(b)
}

//
// The data base:
//	dnsdomain=	suffix to add to unqualified unrooted names
//	dns=		dns server to try
//	dom=		domain name
//	ip=		IP address
//	ns=		name server
//	infernosite=	set of site-wide parameters
//

var (
	laststat   int64
	dnsdb      *ndb.DB
	servers    []string
	dnsdomains []string
)

func readservers() []string {
	if laststat != 0 && now < laststat+2*60 {
		return servers
	}
	laststat = now
	db, err := ndb.Open(dnsfile)
	if err != nil {
		stderr.WriteString("dns: can't open " + dnsfile + ": " + sys.Errstr() + "\n")
		return nil
	}
	if dyn, err := ndb.Open(mntpt + "/ndb"); err == nil {
		db = &ndb.DB{Files: append(dyn.Files, db.Files...)}
	}
	dnsdb = db
	l := dblooknet("sys", myname, "dnsdomain")
	if l == nil {
		l = dblook("infernosite", "", "dnsdomain")
	}
	dnsdomains = append([]string{""}, l...)
	if l = dblooknet("sys", myname, "dns"); l == nil {
		l = dblook("infernosite", "", "dns")
	}
	servers = l
	if debug {
		printlist("dnsdomains", dnsdomains)
		printlist("servers", servers)
	}
	return servers
}

func printlist(w string, l []string) {
	fmt.Printf("%s:", w)
	for _, s := range l {
		fmt.Printf(" %s", quote(s))
	}
	fmt.Printf("\n")
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func dblookns(dom string) []*RR {
	var hosts []*RR
	for _, s := range dblook("dom", dom, "ns") {
		if debug {
			fmt.Printf("dns db: dom=%s ns=%s\n", dom, s)
		}
		var ipl []*RR
		for _, a := range dblook("dom", s, "ip") {
			if ip := parseip(a); ip != nil {
				ipl = append(ipl, &RR{name: s, rtype: Ta, class: Cin, ttl: now + 60, kind: kA, rdata: ip})
				if debug {
					fmt.Printf("dom=%s ip=%s\n", s, a)
				}
			}
		}
		if ipl != nil {
			// only use ones for which we've got addresses
			cacheput(ipl, 0)
			hosts = append(hosts, &RR{name: dom, rtype: Tns, class: Cin, ttl: now + 60, kind: kHost, host: s})
		}
	}
	if hosts == nil {
		if debug {
			fmt.Printf("dns: no ns for dom=%s in db\n", dom)
		}
		return nil
	}
	cacheput(hosts, 0)
	return hosts
}

// defaultresolvers returns the servers named by dns=, each as an
// address or a domain name with an address in the data base, with an
// optional !port.
func defaultresolvers() []*NS {
	var al []*RR
	for _, nm := range readservers() {
		host, port := nm, dnsport
		if h, p, ok := strings.Cut(nm, "!"); ok {
			fmt.Sscan(p, &port)
			host = h
		}
		mk := func(a []byte) *RR {
			return &RR{name: "defaultns", rtype: Ta, class: Cin, ttl: now + 60, kind: kA, rdata: a, port: port}
		}
		if a := parseip(host); a != nil {
			al = append(al, mk(a))
			continue
		}
		for _, s := range dblook("dom", host, "ip") {
			if a := parseip(s); a != nil {
				al = append(al, mk(a))
			}
		}
	}
	if al == nil {
		if debug {
			fmt.Printf("dns: no default resolvers\n")
		}
		return nil
	}
	return []*NS{{name: "defaultns", addr: al, canrecur: true, ttl: now + 60}}
}

func dblook(attr, val, rattr string) []string {
	var rl []string
	var p *ndb.Ptr
	for {
		var e *ndb.Entry
		if e, p = dnsdb.FindByAttr(p, attr, val, rattr); e == nil {
			break
		}
		for _, m := range e.FindByAttr(attr, val, rattr) {
			for _, a := range m.Attrs {
				if !inlist(a.Val, rl) {
					rl = append(rl, a.Val)
				}
			}
		}
	}
	return rl
}

// dblooknet starts from the ip= of the entry with attr=val and returns
// the values of rattr in the nearest network (ipnet=) entries that
// contain it, most specific first, as ipattr's findnetattrs.
func dblooknet(attr, val, rattr string) []string {
	var rl []string
	var nets []netentry
	for _, s := range dblook(attr, val, "ip") {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		var p *ndb.Ptr
		for {
			var e *ndb.Entry
			if e, p = dnsdb.Find(p, "ipnet"); e == nil {
				break
			}
			if bits, ok := netcontains(e, ip); ok {
				nets = append(nets, netentry{bits, e})
			}
		}
	}
	for bits := 128; bits >= 0; bits-- {
		for _, n := range nets {
			if n.bits != bits {
				continue
			}
			for _, m := range n.e.Find(rattr) {
				for _, a := range m.Line.Find(rattr) {
					if a.Val != "" && !inlist(a.Val, rl) {
						rl = append(rl, a.Val)
					}
				}
			}
		}
	}
	if rl == nil {
		return dblook(attr, val, rattr)
	}
	return rl
}

type netentry struct {
	bits int
	e    *ndb.Entry
}

// netcontains reports whether the network of an ipnet entry (its ip=
// and ipmask=) holds ip, and the length of its mask.
func netcontains(e *ndb.Entry, ip netip.Addr) (int, bool) {
	nip, err := netip.ParseAddr(e.FindFirst("ip"))
	if err != nil {
		return 0, false
	}
	bits := 8
	if nip.Is4() {
		switch b := nip.As4()[0]; {
		case b >= 192:
			bits = 24
		case b >= 128:
			bits = 16
		}
	}
	if ms := e.FindFirst("ipmask"); ms != "" {
		if m, err := netip.ParseAddr(ms); err == nil {
			bits = 0
			for _, b := range m.AsSlice() {
				for ; b&0x80 != 0; b <<= 1 {
					bits++
				}
			}
		} else if n, err := fmt.Sscanf(ms, "/%d", &bits); n != 1 || err != nil {
			return 0, false
		}
	}
	pf, err := nip.Prefix(bits)
	if err != nil || ip.Is4() != nip.Is4() {
		return 0, false
	}
	return bits, pf.Contains(ip)
}

func inlist(s string, l []string) bool {
	for _, t := range l {
		if t == s {
			return true
		}
	}
	return false
}

func dnslookup(name string, attr int) ([]string, string) {
	switch attr {
	case Ta:
		switch dbattr(name) {
		case "sys", "dom":
		default:
			return nil, "invalid host name"
		}
		if usehost { // try the host's map first
			if l, _ := sys.Iph2a(name); l != nil {
				return fullresult(name, "ip", l), ""
			}
		}
	case Tptr:
		if usehost {
			if l, _ := sys.Ipa2h(arpa2addr(name)); l != nil {
				return fullresult(name, "ptr", l), ""
			}
		}
	}
	return dnslookup1(name, attr)
}

func fullresult(name, attr string, l []string) []string {
	var rl []string
	for _, s := range l {
		rl = append(rl, name+" "+attr+"\t"+s)
	}
	return rl
}

func arpa2addr(a string) string {
	var rl []string
	for _, s := range strings.FieldsFunc(a, func(r rune) bool { return r == '.' }) {
		if strings.ToLower(s) == "in-addr" {
			break
		}
		rl = append([]string{s}, rl...)
	}
	return strings.Join(rl, ".")
}

func dnslookup1(label string, attr int) ([]string, string) {
	rrl, err := fulldnsquery(label, attr, 0)
	if err != "" || rrl == nil {
		return nil, err
	}
	var r []string
	for _, rr := range rrl {
		r = append(r, rr.text())
	}
	return r, ""
}

func trimdot(s string) string { return strings.TrimRight(s, ".") }

func parent(s string) string {
	if s == "" {
		return "."
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return ""
}

func rootservers() []*NS {
	return []*NS{{name: "a.root-servers.net", addr: []*RR{{name: "a.root-servers.net", rtype: Ta, class: Cin, ttl: 1 << 31, kind: kA, rdata: []byte{198, 41, 0, 4}}}, ttl: 1 << 31}}
}

// NS is a name server and its addresses.
type NS struct {
	name     string
	addr     []*RR
	canrecur bool
	ttl      int64
}

// fulldnsquery broadly follows the algorithm of RFC 1034 as adjusted by
// later RFCs: label is 1034's SNAME, attr its STYPE.
func fulldnsquery(label string, attr, depth int) ([]*RR, string) {
	if depth > 10 {
		return nil, "dns loop"
	}
	var fd *udpconn
	defer func() {
		if fd != nil {
			fd.close()
		}
	}()
	ncname := 0
step1:
	for tries := 0; tries < 10; tries++ {
		// 1. see if in local information, and if so, return it
		x, err := lookcache(label, attr, Cin)
		if x != nil {
			return x, ""
		}
		if err != "" {
			return nil, err
		}
		if attr != Tcname {
			if ncname++; ncname > 10 {
				return nil, "cname alias loop"
			}
			if x, _ = lookcache(label, Tcname, Cin); x != nil && x[0].kind == kHost {
				label = x[0].host
				continue
			}
		}

		// 2. find the best servers to ask
		var slist []*NS
		d := trimdot(label)
		for ; d != "."; d = parent(d) {
			nsl, _ := lookcache(d, Tns, Cin)
			if nsl == nil {
				nsl = dblookns(d)
			}
			// add each to slist; put ones with known addresses first
			var known []*NS
			for _, ns := range nsl {
				if ns.kind != kHost {
					continue
				}
				addrs, err2 := lookcache(ns.host, Ta, Cin)
				if addrs != nil {
					known = append([]*NS{{name: ns.host, addr: addrs, ttl: 1 << 31}}, known...)
				} else if err2 == "" {
					slist = append([]*NS{{name: ns.host, ttl: 1 << 31}}, slist...)
				}
			}
			for _, k := range known {
				slist = append([]*NS{k}, slist...)
			}
			if slist != nil {
				break
			}
		}
		// if no servers, resort to safety belt
		if slist == nil {
			if slist = defaultresolvers(); slist == nil {
				slist = rootservers()
			}
		}
		id, query, err1 := mkquery(attr, Cin, label)
		if err1 != "" {
			stderr.WriteString("dns: " + err1 + "\n")
			return nil, err1
		}
		if debug {
			printnslist("ns for "+d+": ", slist)
		}

		// 3. send them queries until one returns a response
		for qi, ns := range slist {
			if ns.addr == nil {
				if debug {
					fmt.Printf("recursive[%d] query for %s address\n", depth+1, ns.name)
				}
				if ns.addr, _ = fulldnsquery(ns.name, Ta, depth+1); ns.addr == nil {
					continue
				}
			}
			if fd == nil {
				if fd = udpport(); fd == nil {
					return nil, sys.Errstr()
				}
			}
			dm, err2 := udpquery(fd, id, query, ns.name, ns.addr[0])
			if dm == nil {
				stderr.WriteString("dns: " + ns.name + ": " + err2 + "\n")
				continue
			}
			// 4. analyse the response
			last := qi == len(slist)-1
			auth := dm.flags&Fauth != 0
			var soa *RR
			soa, dm.ns = soaof(dm.ns)
			if dm.flags&Rmask != Rok {
				// don't repeat the request on an error
				if !last && (dm.flags&Rmask != Rname || !auth) {
					continue
				}
				cause := reason(dm.flags & Rmask)
				if auth && soa != nil {
					// rfc2308: cache the soa with cached negatives, and the
					// negative for all types if the name does not exist
					ttl := now + 10*60
					if m := int64(soa.soa.minttl); m > 0 {
						ttl = m + now
					}
					a := attr
					if dm.flags&Rmask == Rname {
						a = Tall
					}
					cacheput([]*RR{{name: label, rtype: a, class: Cin, ttl: ttl, kind: kError, reason: cause}, soa}, Fauth)
				}
				return nil, cause
			}
			if dm.an != nil {
				if dm.ns != nil {
					cacheput(dm.ns, 0)
				}
				if dm.ar != nil {
					cacheput(dm.ar, 0)
				}
				fl := 0
				if auth {
					fl = Fauth
				}
				cacheput(dm.an, fl)
				if dm.an[0].rtype == attr {
					return dm.an, ""
				}
				if attr != Tcname && dm.an[0].rtype == Tcname && dm.an[0].kind == kHost {
					if ncname++; ncname > 10 {
						return nil, "cname alias loop"
					}
					label = dm.an[0].host
					continue step1
				}
			}
			if auth {
				ttl := now + 10*60
				if soa != nil && soa.soa.minttl > 0 {
					ttl = int64(soa.soa.minttl) + now
				}
				l := []*RR{{name: label, rtype: attr, class: Cin, ttl: ttl, kind: kError, reason: "resource does not exist"}}
				if soa != nil {
					l = append(l, soa)
				}
				cacheput(l, Fauth)
				return nil, "resource does not exist"
			}
			if isdelegation(dm) {
				// cache valid name servers and hints
				cacheput(dm.ns, 0)
				if dm.ar != nil {
					cacheput(dm.ar, 0)
				}
				continue step1
			}
		}
	}
	return nil, "server failed"
}

func soaof(rrl []*RR) (*RR, []*RR) {
	for i, r := range rrl {
		if r.kind == kSoa {
			rest := append(append([]*RR(nil), rrl[:i]...), rrl[i+1:]...)
			return r, rest
		}
	}
	return nil, rrl
}

func isdelegation(dm *DNSmsg) bool {
	if dm.an != nil {
		return false
	}
	for _, r := range dm.ns {
		if r.rtype == Tns {
			return true
		}
	}
	return false
}

func printnslist(prefix string, nsl []*NS) {
	s := prefix
	for _, ns := range nsl {
		s += fmt.Sprintf(" [%s %s]", ns.name, rrsettext(ns.addr))
	}
	fmt.Printf("%s\n", s)
}

func rrtype(s string) int {
	switch s {
	case "ip":
		return Ta
	case "ns":
		return Tns
	case "cname":
		return Tcname
	case "soa":
		return Tsoa
	case "ptr":
		return Tptr
	case "mx":
		return Tmx
	case "hinfo":
		return Thinfo
	case "all", "any":
		return Tall
	}
	return -1
}

func rrtypename(t int) string {
	switch t {
	case Ta:
		return "ip"
	case Tns:
		return "ns"
	case Tcname:
		return "cname"
	case Tsoa:
		return "soa"
	case Tptr:
		return "ptr"
	case Tmx:
		return "mx"
	case Tall:
		return "all"
	case Thinfo:
		return "hinfo"
	}
	return fmt.Sprint(t)
}

func parseip(s string) []byte {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	a = a.Unmap()
	if !a.Is4() {
		return nil
	}
	b := a.As4()
	return b[:]
}

// dbattr guesses what kind of name s is: ip, dom or sys.
func dbattr(s string) string {
	var dot, alpha, hex, colon bool
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F':
			hex = true
		case c == '.':
			dot = true
		case c == ':':
			colon = true
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '&':
			alpha = true
		}
	}
	switch {
	case alpha:
		if dot {
			return "dom"
		}
		return "sys"
	case colon:
		return "ip"
	case dot && !hex:
		return "ip"
	case dot:
		return "dom"
	}
	return "sys"
}

//
// The UDP conversation, in headers mode.
//

const (
	udphdrsize = 52
	udpraddr   = 0
	udprport   = 48
)

var dnsid = 1

type udpconn struct {
	conn *dial.Connection
	feed *feed.Reader
}

func (u *udpconn) close() {
	u.feed.Close()
	u.conn.Dfd.Close()
	u.conn.Cfd.Close()
}

func udpport() *udpconn {
	c, err := dial.Announce(mntpt + "/udp!*!0")
	if err != nil {
		return nil
	}
	if _, err := c.Cfd.Write([]byte("headers")); err != nil {
		stderr.WriteString("dns: can't set headers mode: " + sys.Errstr() + "\n")
		c.Cfd.Close()
		return nil
	}
	fd, err := sys.Open(c.Dir+"/data", sys.ORDWR)
	if err != nil {
		stderr.WriteString("dns: can't open " + c.Dir + "/data: " + sys.Errstr() + "\n")
		c.Cfd.Close()
		return nil
	}
	c.Dfd = os.NewFile(uintptr(fd), "data")
	r, err := feed.Start(self, fd)
	if err != nil {
		c.Dfd.Close()
		c.Cfd.Close()
		return nil
	}
	return &udpconn{conn: c, feed: r}
}

func mkquery(qtype, qclass int, name string) (int, []byte, string) {
	dm := &DNSmsg{id: dnsid, flags: Oquery, qd: []*QR{{name, qtype, qclass}}}
	dnsid++
	if referdns || !debug {
		dm.flags |= Frecurse
	}
	a := dm.pack(udphdrsize)
	if a == nil {
		return 0, nil, "dns: bad query message" // should only happen if a name is ridiculous
	}
	return dm.id & 0xFFFF, a, ""
}

func udpquery(fd *udpconn, id int, query []byte, sname string, addr *RR) (*DNSmsg, string) {
	if debug {
		fmt.Printf("udp query %s\n", sname)
	}
	if addr.kind != kA || len(addr.rdata) != 4 {
		return nil, "not A resource"
	}
	clear(query[:udphdrsize])
	query[udpraddr+10], query[udpraddr+11] = 0xff, 0xff
	copy(query[udpraddr+12:], addr.rdata)
	port := addr.port
	if port == 0 {
		port = dnsport
	}
	query[udprport], query[udprport+1] = byte(port>>8), byte(port)
	var dm *DNSmsg
	for ntries := 0; ntries < 8; ntries++ {
		if debug {
			ipa := query[udpraddr+12:]
			fmt.Printf("send udp!%d.%d.%d.%d!%d [%d] %d\n", ipa[0], ipa[1], ipa[2], ipa[3], port, ntries, len(query))
		}
		if n, _ := fd.conn.Dfd.Write(query); n != len(query) {
			return nil, "udp write err: " + sys.Errstr()
		}
		deadline := time.Now().Add(5 * time.Second)
		got := false
		for !got {
			wait := int(time.Until(deadline) / time.Millisecond)
			if wait <= 0 {
				break
			}
			buf, _, err := fd.feed.Read(wait)
			if err != nil {
				return nil, "udp read err: " + err.Error()
			}
			if buf == nil {
				break
			}
			if len(buf) < udphdrsize {
				continue
			}
			m := unpack(buf[udphdrsize:])
			if m.flags&Fresp != 0 && m.id == id {
				dm = m
				if m.flags&Ftrunc != 0 && m.ns == nil {
					if debug {
						fmt.Printf("id=%d was truncated\n", m.id)
					}
					continue
				}
				got = true
			} else if debug {
				fmt.Printf("id=%d got flags #%x id %d\n", id, m.flags, m.id)
			}
		}
		if got {
			break
		}
		if debug {
			fmt.Printf("timeout\n")
		}
	}
	if dm == nil {
		return nil, "no reply"
	}
	if dm.err != "" {
		stderr.WriteString("bad reply: " + dm.err + "\n")
		return nil, dm.err
	}
	if debug {
		fmt.Printf("reply: %s\n", dm.text())
	}
	return dm, ""
}
