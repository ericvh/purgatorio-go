package core

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/styx"
)

// Qid layout of #I, as devip.c: type in the low 5 bits, then the
// conversation, then the protocol.
const (
	ipQtopdir = 1 + iota
	ipQarp
	ipQndb
	ipQprotodir
	ipQclone
	ipQstats
	ipQconvdir
	ipQctl
	ipQdata
	ipQlisten
	ipQlocal
	ipQremote
	ipQstatus
	ipQiproute

	ipLogtype    = 5
	ipMasktype   = 1<<ipLogtype - 1
	ipLogconv    = 12
	ipMaskconv   = 1<<ipLogconv - 1
	ipShiftconv  = ipLogtype
	ipShiftproto = ipLogtype + ipLogconv
	ipMaxconv    = 4096
)

func ipqid(p, c, t int) uint64 { return uint64(p)<<ipShiftproto | uint64(c)<<ipShiftconv | uint64(t) }
func iptype(q styx.Qid) int    { return int(q.Path & ipMasktype) }
func ipconv(q styx.Qid) int    { return int(q.Path>>ipShiftconv) & ipMaskconv }
func ipproto(q styx.Qid) int   { return int(q.Path >> ipShiftproto) }

const (
	ipIdle = iota
	ipAnnouncing
	ipAnnounced
	ipConnecting
	ipConnected
	ipHungup
)

var ipstates = []string{"Closed", "Announcing", "Announced", "Connecting", "Established", "Closed"}

// Udp header lengths (ip.h).
const (
	ipaddrlen    = 16
	udphdrlen    = 3*ipaddrlen + 2*2
	oudphdrlen   = 2*ipaddrlen + 2*2
	oudphdrlenv4 = 2*4 + 2*2
)

type ipconvT struct {
	mu         sync.Mutex
	x          int
	p          *ipprotoT
	laddr      [16]byte
	raddr      [16]byte
	lport      uint16
	rport      uint16
	restricted bool
	owner      string
	perm       uint32
	inuse      int
	state      int
	headers    int
	sock       platform.Socket
	listenq    sync.Mutex
	wlock      sync.Mutex
}

type ipprotoT struct {
	mu    sync.Mutex
	x     int
	name  string
	stype int
	conv  []*ipconvT
}

// DevIP is #I, the IP device (devip.c): /net/tcp, /net/udp and
// /net/icmp, each with clone and numbered conversations of ctl, data,
// listen, local, remote and status, over the platform's sockets; and
// /net/ipifc, the platform's interfaces (ipifc.go).
type DevIP struct {
	DevBase
	k      *Kernel
	net    platform.NetStack
	protos []*ipprotoT
	ndbmu  sync.Mutex
	ndb    string
	routes routeTable
	// ifcmu serializes ipifc's clone and ctl requests; ifcopen counts
	// the open ctl files of each interface.
	ifcmu   sync.Mutex
	ifcopen map[int]int
}

const network = "network"

func NewDevIP(k *Kernel, net platform.NetStack) *DevIP {
	d := &DevIP{k: k, net: net, ifcopen: map[int]int{}}
	d.protos = []*ipprotoT{{x: 0, name: "udp", stype: platform.UDP}, {x: 1, name: "tcp", stype: platform.TCP}, {x: 2, name: "icmp", stype: platform.ICMP}}
	return d
}

func (d *DevIP) DC() rune     { return 'I' }
func (d *DevIP) Name() string { return "ip" }

func (d *DevIP) Attach(_ context.Context, spec string) (Walked, error) {
	if n, _ := strconv.Atoi(spec); n != 0 {
		return Walked{}, Error("bad specification")
	}
	return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: ipqid(0, 0, ipQtopdir)}}, nil
}

func (d *DevIP) conv(q styx.Qid) *ipconvT {
	p := d.protos[ipproto(q)]
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := ipconv(q); c < len(p.conv) {
		return p.conv[c]
	}
	return nil
}

// gen lists the entries of directory q, as ipgen does.
func (d *DevIP) gen(q styx.Qid) []styx.Dir {
	mk := func(path uint64, qt uint8, name, owner string, perm uint32, length uint64) styx.Dir {
		if qt == styx.QTDIR {
			perm |= styx.DMDIR
		}
		return styx.Dir{Type: 'I', Qid: styx.Qid{Type: qt, Path: path}, Name: name, UID: owner, GID: owner, Mode: perm, Length: length}
	}
	if ipproto(q) == ipIfcproto {
		return d.ifcgen(q)
	}
	var out []styx.Dir
	switch iptype(q) {
	case ipQtopdir:
		for _, p := range d.protos {
			out = append(out, mk(ipqid(p.x, 0, ipQprotodir), styx.QTDIR, p.name, network, 0o555, 0))
		}
		out = append(out, mk(ipqid(ipIfcproto, 0, ipQprotodir), styx.QTDIR, "ipifc", network, 0o555, 0))
		out = append(out, mk(ipqid(0, 0, ipQarp), 0, "arp", network, 0o664, 0))
		d.ndbmu.Lock()
		out = append(out, mk(ipqid(0, 0, ipQndb), 0, "ndb", network, 0o664, uint64(len(d.ndb))))
		d.ndbmu.Unlock()
		out = append(out, mk(ipqid(0, 0, ipQiproute), 0, "iproute", network, 0o666, 0))
	case ipQprotodir:
		p := d.protos[ipproto(q)]
		p.mu.Lock()
		for _, c := range p.conv {
			out = append(out, mk(ipqid(p.x, c.x, ipQconvdir), styx.QTDIR, strconv.Itoa(c.x), c.owner, 0o555, 0))
		}
		p.mu.Unlock()
		out = append(out, mk(ipqid(p.x, 0, ipQclone), 0, "clone", network, 0o666, 0))
		out = append(out, mk(ipqid(p.x, 0, ipQstats), 0, "stats", network, 0o444, 0))
	case ipQconvdir:
		c := d.conv(q)
		if c == nil {
			return nil
		}
		c.mu.Lock()
		owner, perm := c.owner, c.perm
		c.mu.Unlock()
		p, x := ipproto(q), ipconv(q)
		for _, f := range []struct {
			t    int
			name string
		}{{ipQctl, "ctl"}, {ipQdata, "data"}, {ipQlisten, "listen"}, {ipQlocal, "local"}, {ipQremote, "remote"}, {ipQstatus, "status"}} {
			pm := perm
			if f.t >= ipQlocal {
				pm = 0o444
			}
			out = append(out, mk(ipqid(p, x, f.t), 0, f.name, owner, pm, 0))
		}
	}
	return out
}

func (d *DevIP) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	if !c.IsDir() {
		return Walked{}, Error(abi.Enotdir)
	}
	for _, e := range d.gen(c.Qid) {
		if e.Name == name {
			return Walked{Qid: e.Qid}, nil
		}
	}
	return Walked{}, Error(abi.Enonexist)
}

func (d *DevIP) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	switch iptype(c.Qid) {
	case ipQtopdir:
		return styx.Dir{Type: 'I', Qid: c.Qid, Name: "#I0", UID: network, GID: network, Mode: styx.DMDIR | 0o555}, nil
	case ipQprotodir:
		name := "ipifc"
		if ipproto(c.Qid) != ipIfcproto {
			name = d.protos[ipproto(c.Qid)].name
		}
		return styx.Dir{Type: 'I', Qid: c.Qid, Name: name, UID: network, GID: network, Mode: styx.DMDIR | 0o555}, nil
	}
	parent := styx.Qid{Type: styx.QTDIR}
	switch iptype(c.Qid) {
	case ipQarp, ipQndb, ipQiproute:
		parent.Path = ipqid(0, 0, ipQtopdir)
	case ipQclone, ipQstats:
		parent.Path = ipqid(ipproto(c.Qid), 0, ipQprotodir)
	case ipQconvdir:
		parent.Path = ipqid(ipproto(c.Qid), 0, ipQprotodir)
	default:
		parent.Path = ipqid(ipproto(c.Qid), ipconv(c.Qid), ipQconvdir)
	}
	for _, e := range d.gen(parent) {
		if e.Qid.Path == c.Qid.Path {
			return e, nil
		}
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevIP) DirRead(_ context.Context, c *Chan) ([]styx.Dir, error) { return d.gen(c.Qid), nil }

func (d *DevIP) user(ctx context.Context) string {
	if p := ProgFrom(ctx); p != nil {
		return p.User()
	}
	return d.k.Owner
}

func (d *DevIP) iseve(ctx context.Context) bool { return d.user(ctx) == d.k.Owner }

// stopper returns a channel closed when the prog behind ctx is killed or
// interrupted, for socket calls that block.
func stopper(ctx context.Context) (<-chan struct{}, func()) {
	n := NoteFrom(ctx)
	if n == nil {
		return nil, func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		select {
		case <-n.killed:
		case <-n.intr.waitch():
		case <-done:
			return
		}
		close(stop)
	}()
	return stop, func() { close(done) }
}

func ipErr(ctx context.Context, err error) error {
	if err == platform.ErrInterrupted {
		_ = Check(ctx)
		return Error(abi.Eintr)
	}
	if _, ok := err.(Error); ok {
		return err
	}
	return Error(err.Error())
}

var m2p = [4]uint32{4, 2, 6, 1}

func (d *DevIP) clone(ctx context.Context, p *ipprotoT, user string, sock platform.Socket) (*ipconvT, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var c *ipconvT
	for _, x := range p.conv {
		if x.mu.TryLock() {
			if x.inuse == 0 {
				c = x
				break
			}
			x.mu.Unlock()
		}
	}
	if c == nil {
		if len(p.conv) >= ipMaxconv {
			return nil, Error(abi.Enodev)
		}
		c = &ipconvT{x: len(p.conv), p: p}
		c.mu.Lock()
		p.conv = append(p.conv, c)
	}
	defer c.mu.Unlock()
	if sock == nil {
		s, err := d.net.Socket(p.stype)
		if err != nil {
			return nil, ipErr(ctx, err)
		}
		sock = s
	}
	c.inuse, c.owner, c.perm, c.state = 1, user, 0o660, ipIdle
	c.laddr, c.raddr, c.lport, c.rport = [16]byte{}, [16]byte{}, 0, 0
	c.restricted, c.headers, c.sock = false, 0, sock
	return c, nil
}

func (d *DevIP) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	if ipproto(c.Qid) == ipIfcproto {
		return d.ifcOpen(ctx, c, mode)
	}
	perm := m2p[mode&3]
	switch iptype(c.Qid) {
	case ipQtopdir, ipQprotodir, ipQconvdir, ipQstatus, ipQremote, ipQlocal, ipQstats:
		if mode != abi.OREAD {
			return nil, Error(abi.Eperm)
		}
	case ipQndb:
		if mode&(abi.OWRITE|abi.OTRUNC) != 0 && !d.iseve(ctx) {
			return nil, Error(abi.Eperm)
		}
		if mode&(abi.OWRITE|abi.OTRUNC) == abi.OWRITE|abi.OTRUNC {
			d.ndbmu.Lock()
			d.ndb = ""
			d.ndbmu.Unlock()
		}
	case ipQiproute:
		c.State = &routeTag{tag: "none"}
	case ipQclone:
		p := d.protos[ipproto(c.Qid)]
		cv, err := d.clone(ctx, p, d.user(ctx), nil)
		if err != nil {
			return nil, err
		}
		c.Qid = styx.Qid{Path: ipqid(p.x, cv.x, ipQctl)}
	case ipQdata, ipQctl:
		cv := d.conv(c.Qid)
		if cv == nil {
			return nil, Error(abi.Enonexist)
		}
		cv.mu.Lock()
		defer cv.mu.Unlock()
		if perm&(cv.perm>>6) != perm {
			if d.user(ctx) != cv.owner || perm&cv.perm != perm {
				return nil, Error(abi.Eperm)
			}
		}
		cv.inuse++
		if cv.inuse == 1 {
			cv.owner, cv.perm = d.user(ctx), 0o660
			if cv.sock == nil {
				s, err := d.net.Socket(cv.p.stype)
				if err != nil {
					cv.inuse--
					return nil, ipErr(ctx, err)
				}
				cv.sock = s
			}
		}
	case ipQlisten:
		cv := d.conv(c.Qid)
		if cv == nil {
			return nil, Error(abi.Enonexist)
		}
		cv.mu.Lock()
		if perm&(cv.perm>>6) != perm && (d.user(ctx) != cv.owner || perm&cv.perm != perm) {
			cv.mu.Unlock()
			return nil, Error(abi.Eperm)
		}
		state, sock := cv.state, cv.sock
		cv.mu.Unlock()
		if state != ipAnnounced {
			return nil, Error(abi.Enolisten)
		}
		cv.listenq.Lock()
		defer cv.listenq.Unlock()
		stop, done := stopper(ctx)
		ns, raddr, rport, err := sock.Accept(stop)
		done()
		if err != nil {
			return nil, ipErr(ctx, err)
		}
		nc, err := d.clone(ctx, cv.p, d.user(ctx), ns)
		if err != nil {
			ns.Close()
			return nil, err
		}
		nc.mu.Lock()
		nc.raddr, nc.rport = raddr, rport
		nc.laddr, nc.lport = ns.LocalAddr()
		nc.state = ipConnected
		nc.mu.Unlock()
		c.Qid = styx.Qid{Path: ipqid(ipproto(c.Qid), nc.x, ipQctl)}
	}
	return c, nil
}

func (d *DevIP) closeconv(cv *ipconvT) {
	cv.mu.Lock()
	defer cv.mu.Unlock()
	cv.inuse--
	if cv.inuse > 0 {
		return
	}
	cv.owner, cv.perm = network, 0o660
	cv.state = ipIdle
	cv.restricted = false
	if cv.sock != nil {
		cv.sock.Close()
		cv.sock = nil
	}
}

func (d *DevIP) Close(c *Chan) {
	if ipproto(c.Qid) == ipIfcproto {
		d.ifcClose(c)
		return
	}
	switch iptype(c.Qid) {
	case ipQdata, ipQctl:
		if cv := d.conv(c.Qid); cv != nil {
			d.closeconv(cv)
		}
	}
}

// FormatIP prints an address as Inferno's %I does: IPv4 (v4-mapped) as a
// dotted quad, the null address as ::, others as IPv6.
func FormatIP(a [16]byte) string {
	ip := netip.AddrFrom16(a)
	if ip.Is4In6() {
		return ip.Unmap().String()
	}
	return ip.String()
}

// ParseIP parses an IPv4 or IPv6 address into 16 bytes.
func ParseIP(s string) ([16]byte, bool) {
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return [16]byte{}, false
	}
	return ip.As16(), true
}

func (d *DevIP) Read(ctx context.Context, ch *Chan, buf []byte, off int64) (int, error) {
	if ipproto(ch.Qid) == ipIfcproto {
		return d.ifcRead(ch, buf, off)
	}
	str := func(s string) (int, error) { return ReadStr([]byte(s), buf, off), nil }
	switch iptype(ch.Qid) {
	case ipQndb:
		d.ndbmu.Lock()
		defer d.ndbmu.Unlock()
		return str(d.ndb)
	case ipQiproute:
		return str(d.routes.text())
	case ipQctl:
		return str(strconv.Itoa(ipconv(ch.Qid)))
	case ipQremote, ipQlocal, ipQstatus:
		c := d.conv(ch.Qid)
		if c == nil {
			return 0, Error(abi.Enonexist)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		switch iptype(ch.Qid) {
		case ipQremote:
			return str(fmt.Sprintf("%s!%d\n", FormatIP(c.raddr), c.rport))
		case ipQlocal:
			return str(fmt.Sprintf("%s!%d\n", FormatIP(c.laddr), c.lport))
		}
		return str(ipstates[c.state] + "\n")
	case ipQdata:
		c := d.conv(ch.Qid)
		if c == nil {
			return 0, Error(abi.Enonexist)
		}
		c.mu.Lock()
		sock, headers := c.sock, c.headers
		c.mu.Unlock()
		if sock == nil {
			return 0, Error(abi.Ehungup)
		}
		stop, done := stopper(ctx)
		defer done()
		if headers > 0 {
			if len(buf) < headers {
				return 0, Error(abi.Ebadarg)
			}
			n, err := sock.Recv(stop, buf[headers:], buf[:headers])
			if err != nil {
				return 0, ipErr(ctx, err)
			}
			return n + headers, nil
		}
		n, err := sock.Recv(stop, buf, nil)
		if err != nil {
			return 0, ipErr(ctx, err)
		}
		return n, nil
	case ipQstats:
		return 0, Error("stats not implemented")
	}
	return 0, Error(abi.Eperm)
}

func portno(s string) (uint16, error) {
	n, err := strconv.ParseUint(s, 0, 16)
	if err != nil {
		return 0, Error("non-numeric port number")
	}
	return uint16(n), nil
}

func (d *DevIP) setlport(c *ipconvT) error {
	if err := c.sock.Bind(c.laddr, c.lport, c.restricted); err != nil {
		return err
	}
	la, lp := c.sock.LocalAddr()
	if c.lport == 0 {
		c.lport = lp
	}
	if c.laddr == ([16]byte{}) {
		c.laddr = la
	}
	return nil
}

// setladdrport sets the local address from [address!]port[!r].
func (d *DevIP) setladdrport(ctx context.Context, c *ipconvT, str string, announcing bool) error {
	addr, port, has := strings.Cut(str, "!")
	if has && port == "r" {
		has = false
	}
	c.lport = 0
	if !has {
		if announcing {
			c.laddr = [16]byte{}
		}
		port = addr
	} else if addr == "*" {
		c.laddr = [16]byte{}
	} else {
		a, ok := ParseIP(addr)
		if !ok {
			return Error("invalid IP address")
		}
		c.laddr = a
	}
	if i := strings.IndexByte(port, '!'); i >= 0 {
		port = port[:i]
	}
	if announcing && port == "*" {
		if !d.iseve(ctx) {
			return Error(abi.Eperm)
		}
		return d.setlport(c)
	}
	p, err := portno(port)
	if err != nil {
		return err
	}
	c.lport = p
	return d.setlport(c)
}

func setraddrport(c *ipconvT, str string) error {
	addr, rest, ok := strings.Cut(str, "!")
	if !ok {
		return Error("malformed address")
	}
	a, okp := ParseIP(addr)
	if !okp {
		return Error("invalid IP address")
	}
	c.raddr = a
	port, flags, _ := strings.Cut(rest, "!")
	p, err := portno(port)
	if err != nil {
		return err
	}
	c.rport = p
	if strings.Contains("!"+flags, "!r") {
		c.restricted = true
	}
	return nil
}

func (d *DevIP) Write(ctx context.Context, ch *Chan, buf []byte, off int64) (int, error) {
	if ipproto(ch.Qid) == ipIfcproto {
		return d.ifcWrite(ctx, ch, buf)
	}
	switch iptype(ch.Qid) {
	case ipQdata:
		c := d.conv(ch.Qid)
		if c == nil {
			return 0, Error(abi.Enonexist)
		}
		c.mu.Lock()
		sock, headers := c.sock, c.headers
		c.mu.Unlock()
		if sock == nil {
			return 0, Error(abi.Ehungup)
		}
		c.wlock.Lock()
		defer c.wlock.Unlock()
		stop, done := stopper(ctx)
		defer done()
		if headers > 0 {
			if len(buf) < headers {
				return 0, Error(abi.Eshort)
			}
			n, err := sock.Send(stop, buf[headers:], buf[:headers])
			if err != nil {
				return 0, ipErr(ctx, err)
			}
			return n + headers, nil
		}
		n, err := sock.Send(stop, buf, nil)
		if err != nil {
			return 0, ipErr(ctx, err)
		}
		return n, nil
	case ipQarp:
		return 0, Error("bad arp request")
	case ipQndb:
		d.ndbmu.Lock()
		defer d.ndbmu.Unlock()
		if off > int64(len(d.ndb)) || off+int64(len(buf)) >= 1023 {
			return 0, Error(abi.Eio)
		}
		d.ndb = d.ndb[:off] + string(buf)
		return len(buf), nil
	case ipQctl:
		return d.ctl(ctx, ch, buf)
	case ipQiproute:
		rt, _ := ch.State.(*routeTag)
		if rt == nil {
			rt = &routeTag{tag: "none"}
		}
		if err := d.routes.write(rt, string(buf)); err != nil {
			return 0, err
		}
		return len(buf), nil
	}
	return 0, Error(abi.Eperm)
}

func (d *DevIP) ctl(ctx context.Context, ch *Chan, buf []byte) (int, error) {
	c := d.conv(ch.Qid)
	if c == nil {
		return 0, Error(abi.Enonexist)
	}
	f := strings.Fields(string(buf))
	if len(f) < 1 {
		return 0, Error("short control request")
	}
	c.mu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			c.mu.Unlock()
		}
	}()
	switch f[0] {
	case "connect":
		if c.state != ipIdle {
			return 0, Error(abi.Econinuse)
		}
		if len(f) != 2 && len(f) != 3 {
			return 0, Error("bad args to connect")
		}
		c.state = ipConnecting
		if err := setraddrport(c, f[1]); err != nil {
			return 0, err
		}
		if len(f) == 3 {
			p, err := portno(f[2])
			if err != nil {
				return 0, err
			}
			c.lport = p
			if err := d.setlport(c); err != nil {
				return 0, ipErr(ctx, err)
			}
		}
		sock, ra, rp := c.sock, c.raddr, c.rport
		c.mu.Unlock()
		stop, done := stopper(ctx)
		err := sock.Connect(stop, ra, rp)
		done()
		c.mu.Lock()
		if err != nil {
			c.state = ipConnected // sic, as Inferno
			return 0, ipErr(ctx, err)
		}
		c.laddr, c.lport = sock.LocalAddr()
		c.state = ipConnected
	case "announce":
		if c.state != ipIdle {
			return 0, Error(abi.Econinuse)
		}
		if len(f) != 2 {
			return 0, Error("bad args to announce")
		}
		if c.p.stype == platform.ICMP {
			return 0, Error("protocol not supported")
		}
		c.state = ipAnnouncing
		c.raddr, c.rport = [16]byte{}, 0
		if err := d.setladdrport(ctx, c, f[1], true); err != nil {
			c.state = ipIdle
			return 0, ipErr(ctx, err)
		}
		if c.p.stype != platform.UDP {
			if err := c.sock.Listen(); err != nil {
				c.state = ipAnnounced
				return 0, ipErr(ctx, err)
			}
		}
		c.state = ipAnnounced
	case "bind":
		if len(f) != 2 {
			return 0, Error("bad args to bind")
		}
		if err := d.setladdrport(ctx, c, f[1], false); err != nil {
			return 0, ipErr(ctx, err)
		}
	case "ttl", "tos", "ignoreadvice":
	case "headers4", "oldheaders", "headers":
		if c.p.stype != platform.UDP {
			return 0, Error(abi.Enoctl)
		}
		c.headers = map[string]int{"headers4": oudphdrlenv4, "oldheaders": oudphdrlen, "headers": udphdrlen}[f[0]]
	case "hangup":
		if c.p.stype != platform.TCP {
			return 0, Error(abi.Enoctl)
		}
		if c.sock != nil {
			if err := c.sock.Hangup(); err != nil {
				return 0, ipErr(ctx, err)
			}
		}
		c.sock = nil
		c.state = ipHungup
	case "keepalive":
		if c.p.stype != platform.TCP {
			return 0, Error(abi.Enoctl)
		}
		if c.sock == nil {
			return 0, Error("not connected")
		}
		ms := 0
		if len(f) > 1 {
			ms, _ = strconv.Atoi(f[1])
		}
		_ = c.sock.Keepalive(ms)
	default:
		return 0, Error(abi.Enoctl)
	}
	return len(buf), nil
}

func (d *DevIP) Wstat(ctx context.Context, ch *Chan, dir *styx.Dir) error {
	if ipproto(ch.Qid) == ipIfcproto {
		return Error(abi.Eperm)
	}
	switch iptype(ch.Qid) {
	case ipQctl, ipQdata:
	default:
		return Error(abi.Eperm)
	}
	c := d.conv(ch.Qid)
	if c == nil {
		return Error(abi.Enonexist)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !d.iseve(ctx) && d.user(ctx) != c.owner {
		return Error(abi.Eperm)
	}
	if dir.UID != "" {
		c.owner = dir.UID
	}
	if !NullMode(dir) {
		c.perm = dir.Mode & 0o777
	}
	return nil
}

func (d *DevIP) Ready(ch *Chan, write bool) bool { return true }
