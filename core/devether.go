package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// #l is Inferno's ether(3): each Ethernet interface (an Ether) is a
// netif (netif.c), a directory etherN with clone, addr, stats, ifstats
// and a directory n for each connection, holding data, ctl, stats, type
// and ifstats. A connection's ctl takes "connect type" (-1 for every
// type), promiscuous, bridge, headersonly, addmulti, remmulti, scanbs
// and nonblocking; its data reads the frames of its type, whole, and
// writes frames out (devether.c's etheriq and etheroq).
//
// In Inferno the IP stack is itself a client of the device, with its own
// connections for IP and ARP. Here the platform's IP stack sits beside
// the device instead: every frame from the wire goes to it, and so does
// a frame written to a data file that is for the interface's own
// address or broadcast, as Inferno's loopback would deliver it to the
// IP stack's connection. So types 0x800 and 0x806 are free for
// programs to connect.

// netif's qid layout: the type in the low five bits, the connection
// above them.
const (
	nCloneqid = 1 + iota
	nAddrqid
	n2ndqid
	n3rdqid
	nDataqid
	nCtlqid
	nStatqid
	nTypeqid
	nIfstatqid
)

func etherqtype(p uint64) int    { return int(p & 0x1f) }
func etherid(p uint64) int       { return int(p >> 5) }
func etherqid(i, t int) uint64   { return uint64(i)<<5 | uint64(t) }
func etherBcast(d []byte) bool   { return string(d[:6]) == "\xff\xff\xff\xff\xff\xff" }
func etherType(f []byte) int     { return int(f[12])<<8 | int(f[13]) }
func etherAddr(a [6]byte) string { return fmt.Sprintf("%x", a[:]) }

// Ethernet sizes (netif.h).
const (
	etherMinTU   = 60
	etherMaxTU   = 1514
	etherHdrSize = 14
	// etherNtypes is the number of connections an interface has, as
	// Inferno's Ntypes.
	etherNtypes = 8
	// etherLimit bounds the frames queued for a connection.
	etherLimit = 64 * 1024
)

// Ether is one Ethernet interface: its address, how it sends a frame on
// the wire, and the connections programs have made to it. The platform
// gives it each frame received (Receive) and each frame its IP stack
// sends (Output), and tells it where frames for the stack go (SetStack).
type Ether struct {
	name     string
	ea       [6]byte
	mbps     int
	transmit func(frame []byte)
	start    time.Time

	mu    sync.Mutex
	stack func(frame []byte)
	f     []*netfile
	prom  int // promiscuous connections
	all   int // connections of type -1
	maddr map[[6]byte]int

	inpackets, outpackets, soverflows int
}

// netfile is one connection.
type netfile struct {
	inuse       int
	owner       string
	mode        uint32
	typ         int
	prom        bool
	scan        int
	bridge      bool
	headersonly bool
	maddr       map[[6]byte]bool
	in          *Queue
}

// NewEther makes interface name (ether0) with address ea and speed mbps
// that sends frames with transmit.
func NewEther(name string, ea [6]byte, mbps int, transmit func(frame []byte)) *Ether {
	return &Ether{name: name, ea: ea, mbps: mbps, transmit: transmit, start: time.Now(), maddr: map[[6]byte]int{}}
}

// SetStack sets where frames for the IP stack go.
func (e *Ether) SetStack(stack func(frame []byte)) {
	e.mu.Lock()
	e.stack = stack
	e.mu.Unlock()
}

// Receive takes a frame from the wire: the connections that want it get
// copies, and the IP stack gets it.
func (e *Ether) Receive(frame []byte) {
	if len(frame) < etherHdrSize {
		return
	}
	e.iq(frame, true)
	e.mu.Lock()
	stack := e.stack
	e.mu.Unlock()
	if stack != nil {
		stack(frame)
	}
}

// Output sends a frame from the IP stack.
func (e *Ether) Output(frame []byte) {
	if len(frame) >= etherHdrSize {
		e.oq(frame, true)
	}
}

// iq passes a frame to every connection that wants it, as etheriq.
func (e *Ether) iq(pkt []byte, fromwire bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inpackets++
	typ := etherType(pkt)
	multi := pkt[0]&1 != 0
	if multi && !etherBcast(pkt) && e.prom == 0 && e.maddr[[6]byte(pkt[:6])] == 0 {
		return
	}
	tome := [6]byte(pkt[:6]) == e.ea
	fromme := [6]byte(pkt[6:12]) == e.ea
	for _, f := range e.f {
		if f == nil || f.inuse == 0 || (f.typ != typ && f.typ >= 0) || !(tome || multi || f.prom) {
			continue
		}
		// Don't hear bridged packets.
		if f.bridge && !fromwire && !fromme {
			continue
		}
		b := pkt
		if f.headersonly {
			b = e.rtrace(pkt)
		}
		if f.in.Len()+len(b) > etherLimit || f.in.Hungup() {
			e.soverflows++
			continue
		}
		f.in.Produce(b)
	}
}

// rtrace is a headers-only record of a frame, as etherrtrace: its first
// 58 bytes, its length and the time in milliseconds.
func (e *Ether) rtrace(pkt []byte) []byte {
	b := make([]byte, 64)
	copy(b[:58], pkt)
	n := len(pkt)
	ms := uint32(time.Since(e.start).Milliseconds())
	b[58], b[59] = byte(n>>8), byte(n)
	b[60], b[61], b[62], b[63] = byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	return b
}

// oq sends a frame, as etheroq: a frame for the interface itself, a
// broadcast, or any frame while a connection is promiscuous comes back
// to the connections; a frame for the interface itself does not go on
// the wire. A frame from a connection that is for the interface or
// broadcast also goes to the IP stack.
func (e *Ether) oq(pkt []byte, fromStack bool) {
	e.mu.Lock()
	e.outpackets++
	prom := e.prom > 0
	stack := e.stack
	e.mu.Unlock()
	loopback := [6]byte(pkt[:6]) == e.ea
	bcast := etherBcast(pkt)
	if loopback || bcast || prom {
		e.iq(pkt, false)
	}
	if !fromStack && (loopback || bcast) && stack != nil {
		stack(pkt)
	}
	if !loopback {
		e.transmit(pkt)
	}
}

// DevEther is #l: #l0 is the first interface, #l1 the second.
type DevEther struct {
	DevBase
	k      *Kernel
	ethers []*Ether
}

func NewDevEther(k *Kernel, ethers ...*Ether) *DevEther {
	return &DevEther{k: k, ethers: ethers}
}

func (d *DevEther) DC() rune     { return 'l' }
func (d *DevEther) Name() string { return "ether" }

func (d *DevEther) Devno(spec string) uint32 {
	n, _ := strconv.Atoi(spec)
	return uint32(n)
}

func (d *DevEther) Attach(_ context.Context, spec string) (Walked, error) {
	n := 0
	if spec != "" {
		var err error
		if n, err = strconv.Atoi(spec); err != nil || n < 0 {
			return Walked{}, Error(abi.Ebadarg)
		}
	}
	if n >= len(d.ethers) {
		return Walked{}, Error(abi.Enodev)
	}
	return Walked{Qid: styx.Qid{Type: styx.QTDIR}}, nil
}

func (d *DevEther) ether(c *Chan) *Ether { return d.ethers[c.Devno] }

func (d *DevEther) user(ctx context.Context) string {
	if p := ProgFrom(ctx); p != nil {
		return p.User()
	}
	return d.k.Owner
}

// gen lists directory q, as netifgen.
func (d *DevEther) gen(e *Ether, q styx.Qid) []styx.Dir {
	eve := d.k.Owner
	mk := func(path uint64, name, owner string, perm uint32) styx.Dir {
		qt := uint8(styx.QTFILE)
		if perm&styx.DMDIR != 0 {
			qt = styx.QTDIR
		}
		return styx.Dir{Type: 'l', Qid: styx.Qid{Type: qt, Path: path}, Name: name, UID: owner, GID: owner, Mode: perm}
	}
	if q.Path == 0 {
		return []styx.Dir{mk(n2ndqid, e.name, eve, styx.DMDIR|0o555)}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch t := etherqtype(q.Path); t {
	case n2ndqid, nCloneqid, nAddrqid:
		out := []styx.Dir{
			mk(nCloneqid, "clone", eve, 0o666),
			mk(nAddrqid, "addr", eve, 0o666),
			mk(nStatqid, "stats", eve, 0o444),
			mk(nIfstatqid, "ifstats", eve, 0o444),
		}
		for i, f := range e.f {
			if f != nil {
				out = append(out, mk(etherqid(i, n3rdqid), strconv.Itoa(i), eve, styx.DMDIR|0o555))
			}
		}
		return out
	default:
		i := etherid(q.Path)
		if i >= len(e.f) || e.f[i] == nil {
			return nil
		}
		f := e.f[i]
		o, perm := eve, uint32(0o666)
		if f.owner != "" {
			o, perm = f.owner, f.mode
		}
		return []styx.Dir{
			mk(etherqid(i, nDataqid), "data", o, perm),
			mk(etherqid(i, nCtlqid), "ctl", o, perm),
			mk(etherqid(i, nStatqid), "stats", eve, 0o444),
			mk(etherqid(i, nTypeqid), "type", eve, 0o444),
			mk(etherqid(i, nIfstatqid), "ifstats", eve, 0o444),
		}
	}
}

func (d *DevEther) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	if !c.IsDir() {
		return Walked{}, Error(abi.Enotdir)
	}
	for _, e := range d.gen(d.ether(c), c.Qid) {
		if e.Name == name {
			return Walked{Qid: e.Qid}, nil
		}
	}
	return Walked{}, Error(abi.Enonexist)
}

func (d *DevEther) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	e := d.ether(c)
	var parent styx.Qid
	switch t := etherqtype(c.Qid.Path); {
	case c.Qid.Path == 0:
		return styx.Dir{Type: 'l', Qid: c.Qid, Name: "#l" + strconv.Itoa(int(c.Devno)), UID: d.k.Owner, GID: d.k.Owner, Mode: styx.DMDIR | 0o555}, nil
	case t == n2ndqid:
		parent = styx.Qid{Type: styx.QTDIR}
	case t == nCloneqid || t == nAddrqid || t == n3rdqid || etherid(c.Qid.Path) == 0 && (t == nStatqid || t == nIfstatqid):
		parent = styx.Qid{Type: styx.QTDIR, Path: n2ndqid}
	default:
		parent = styx.Qid{Type: styx.QTDIR, Path: etherqid(etherid(c.Qid.Path), n3rdqid)}
	}
	for _, x := range d.gen(e, parent) {
		if x.Qid.Path == c.Qid.Path {
			return x, nil
		}
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevEther) DirRead(_ context.Context, c *Chan) ([]styx.Dir, error) {
	return d.gen(d.ether(c), c.Qid), nil
}

// netown checks user's access to f in mode, as netown: the first user
// to open a connection owns it, with mode 0660.
func (d *DevEther) netown(f *netfile, user string, omode int) bool {
	if f.owner == "" {
		f.owner, f.mode = user, 0o660
		return true
	}
	var mode uint32
	switch {
	case user == f.owner:
		mode = f.mode
	case user == d.k.Owner:
		mode = f.mode << 3
	default:
		mode = f.mode << 6
	}
	t := [4]uint32{0o400, 0o200, 0o600, 0o100}[omode&3]
	return t&mode == t
}

// openfile adds a reference to connection id, or with id < 0 finds one
// not in use, as openfile.
func (d *DevEther) openfile(e *Ether, id int, user string) (int, error) {
	if id >= 0 {
		if id >= len(e.f) || e.f[id] == nil {
			return 0, Error(abi.Enodev)
		}
		f := e.f[id]
		if f.inuse == 0 {
			f.in = NewMsgQueue(etherLimit)
		}
		f.inuse++
		return id, nil
	}
	for i := 0; i < etherNtypes; i++ {
		if i == len(e.f) {
			e.f = append(e.f, &netfile{})
		}
		f := e.f[i]
		if f.inuse > 0 {
			continue
		}
		f.inuse = 1
		f.in = NewMsgQueue(etherLimit)
		d.netown(f, user, 0)
		return i, nil
	}
	return 0, Error(abi.Enodev)
}

func (d *DevEther) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, Error(abi.Eperm)
		}
		return c, nil
	}
	e := d.ether(c)
	user := d.user(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	id := 0
	switch etherqtype(c.Qid.Path) {
	case nDataqid, nCtlqid:
		var err error
		if id, err = d.openfile(e, etherid(c.Qid.Path), user); err != nil {
			return nil, err
		}
	case nCloneqid:
		var err error
		if id, err = d.openfile(e, -1, user); err != nil {
			return nil, err
		}
		c.Qid = styx.Qid{Path: etherqid(id, nCtlqid)}
	default:
		if mode != abi.OREAD {
			return nil, Error(abi.Ebadarg)
		}
		return c, nil
	}
	if !d.netown(e.f[id], user, mode&7) {
		d.closefile(e, e.f[id])
		return nil, Error(abi.Eperm)
	}
	return c, nil
}

func (d *DevEther) Close(c *Chan) {
	t := etherqtype(c.Qid.Path)
	if c.IsDir() || t != nDataqid && t != nCtlqid {
		return
	}
	e := d.ether(c)
	e.mu.Lock()
	defer e.mu.Unlock()
	if i := etherid(c.Qid.Path); i < len(e.f) && e.f[i] != nil {
		d.closefile(e, e.f[i])
	}
}

// closefile drops a reference to f and resets it with the last, as
// netifclose.
func (d *DevEther) closefile(e *Ether, f *netfile) {
	if f.inuse--; f.inuse > 0 {
		return
	}
	if f.prom {
		e.prom--
	}
	for a := range f.maddr {
		e.multi(f, a, false)
	}
	if f.typ < 0 {
		e.all--
	}
	f.owner, f.typ, f.prom, f.scan, f.bridge, f.headersonly = "", 0, false, 0, false, false
	f.in.Hangup()
}

// multi adds or removes f's interest in multicast address a, as
// netmulti. The interface is told nothing: it receives every
// multicast frame.
func (e *Ether) multi(f *netfile, a [6]byte, add bool) {
	if add {
		if !f.maddr[a] {
			if f.maddr == nil {
				f.maddr = map[[6]byte]bool{}
			}
			f.maddr[a] = true
			e.maddr[a]++
		}
		return
	}
	if f.maddr[a] {
		delete(f.maddr, a)
		if e.maddr[a]--; e.maddr[a] <= 0 {
			delete(e.maddr, a)
		}
	}
}

func (d *DevEther) Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	e := d.ether(c)
	str := func(s string) (int, error) { return ReadStr([]byte(s), buf, off), nil }
	num := func(n int) (int, error) { return str(fmt.Sprintf("%*d ", numSize-1, n)) }
	id := etherid(c.Qid.Path)
	switch etherqtype(c.Qid.Path) {
	case nDataqid:
		e.mu.Lock()
		q := e.f[id].in
		e.mu.Unlock()
		return q.Read(ctx, buf)
	case nCtlqid:
		return num(id)
	case nStatqid:
		e.mu.Lock()
		var b strings.Builder
		fmt.Fprintf(&b, "in: %d\n", e.inpackets)
		fmt.Fprintf(&b, "link: %d\n", 1)
		fmt.Fprintf(&b, "out: %d\n", e.outpackets)
		fmt.Fprintf(&b, "crc errs: %d\n", 0)
		fmt.Fprintf(&b, "overflows: %d\n", 0)
		fmt.Fprintf(&b, "soft overflows: %d\n", e.soverflows)
		fmt.Fprintf(&b, "framing errs: %d\n", 0)
		fmt.Fprintf(&b, "buffer errs: %d\n", 0)
		fmt.Fprintf(&b, "output errs: %d\n", 0)
		fmt.Fprintf(&b, "prom: %d\n", e.prom)
		fmt.Fprintf(&b, "mbps: %d\n", e.mbps)
		fmt.Fprintf(&b, "addr: %s\n", etherAddr(e.ea))
		e.mu.Unlock()
		return str(b.String())
	case nAddrqid:
		return str(etherAddr(e.ea))
	case nTypeqid:
		e.mu.Lock()
		t := e.f[id].typ
		e.mu.Unlock()
		return num(t)
	case nIfstatqid:
		// The device has no statistics of its own.
		return 0, nil
	}
	return 0, Error(abi.Ebadarg)
}

// numSize is the width of a number read from a netif file, as NUMSIZE.
const numSize = 12

func (d *DevEther) Write(ctx context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	e := d.ether(c)
	if etherqtype(c.Qid.Path) != nDataqid {
		if etherqtype(c.Qid.Path) != nCtlqid {
			return 0, Error(abi.Eperm)
		}
		return d.ctl(e, etherid(c.Qid.Path), buf)
	}
	n := len(buf)
	if n > etherMaxTU {
		return 0, Error(abi.Etoobig)
	}
	if n < etherMinTU {
		return 0, Error(abi.Etoosmall)
	}
	pkt := make([]byte, n)
	copy(pkt, buf)
	copy(pkt[6:12], e.ea[:])
	e.oq(pkt, false)
	return n, nil
}

// matchtoken returns what follows token at the start of s, as
// matchtoken.
func matchtoken(s, token string) (string, bool) {
	rest, ok := strings.CutPrefix(s, token)
	if !ok {
		return "", false
	}
	if rest != "" && !strings.ContainsRune(" \t\n", rune(rest[0])) {
		return "", false
	}
	return strings.TrimLeft(rest, " \t\n"), true
}

// atoi is C's atoi: an optional sign and the digits that follow.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t\n")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for ; s != "" && s[0] >= '0' && s[0] <= '9'; s = s[1:] {
		n = n*10 + int(s[0]-'0')
	}
	if neg {
		return -n
	}
	return n
}

// parseether parses an Ethernet address: twelve hex digits, with or
// without colons.
func parseether(s string) ([6]byte, bool) {
	var a [6]byte
	for i := range a {
		if len(s) < 2 {
			return a, false
		}
		v, err := strconv.ParseUint(s[:2], 16, 8)
		if err != nil {
			return a, false
		}
		a[i] = byte(v)
		s = strings.TrimPrefix(s[2:], ":")
	}
	return a, true
}

// ctl is a connection's control request, as netifwrite and etherwrite.
func (d *DevEther) ctl(e *Ether, id int, buf []byte) (int, error) {
	s := string(buf[:min(len(buf), 63)])
	e.mu.Lock()
	defer e.mu.Unlock()
	f := e.f[id]
	if p, ok := matchtoken(s, "connect"); ok {
		t := atoi(p)
		if t > 0 {
			for _, x := range e.f {
				if x != nil && x.inuse > 0 && x.typ == t {
					return 0, Error(abi.Einuse)
				}
			}
		}
		if f.typ < 0 {
			e.all--
		}
		f.typ = t
		if t < 0 {
			e.all++
		}
	} else if _, ok := matchtoken(s, "promiscuous"); ok {
		if !f.prom {
			f.prom = true
			e.prom++
		}
	} else if p, ok := matchtoken(s, "scanbs"); ok {
		// There are no base stations to scan for.
		if f.scan == 0 {
			f.scan = max(atoi(p), 5)
		}
	} else if _, ok := matchtoken(s, "bridge"); ok {
		f.bridge = true
	} else if _, ok := matchtoken(s, "headersonly"); ok {
		f.headersonly = true
	} else if p, ok := matchtoken(s, "addmulti"); ok {
		a, ok := parseether(p)
		if !ok {
			return 0, Error("bad address")
		}
		e.multi(f, a, true)
	} else if p, ok := matchtoken(s, "remmulti"); ok {
		a, ok := parseether(p)
		if !ok {
			return 0, Error("bad address")
		}
		e.multi(f, a, false)
	} else if fs := strings.Fields(s); len(fs) > 0 && fs[0] == "nonblocking" {
		// Output never blocks for long: the driver queues each frame.
	} else {
		return 0, Error(abi.Ebadctl)
	}
	return len(buf), nil
}

func (d *DevEther) Wstat(ctx context.Context, c *Chan, dir *styx.Dir) error {
	e := d.ether(c)
	e.mu.Lock()
	defer e.mu.Unlock()
	i := etherid(c.Qid.Path)
	if c.IsDir() || i >= len(e.f) || e.f[i] == nil {
		return Error(abi.Enonexist)
	}
	f := e.f[i]
	if !d.netown(f, d.user(ctx), abi.OWRITE) {
		return Error(abi.Eperm)
	}
	if dir.UID != "" {
		f.owner = dir.UID
	}
	if !NullMode(dir) {
		f.mode = dir.Mode
	}
	return nil
}

func (d *DevEther) queue(c *Chan) *Queue {
	if c.IsDir() || etherqtype(c.Qid.Path) != nDataqid {
		return nil
	}
	e := d.ether(c)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.f[etherid(c.Qid.Path)].in
}

func (d *DevEther) Ready(c *Chan, write bool) bool {
	if q := d.queue(c); q != nil && !write {
		return q.Readable()
	}
	return true
}

func (d *DevEther) Watch(c *Chan, write bool, r *Rendez) {
	if q := d.queue(c); q != nil {
		q.Watch(r)
	}
}

func (d *DevEther) Unwatch(c *Chan, r *Rendez) {
	if q := d.queue(c); q != nil {
		q.Unwatch(r)
	}
}
