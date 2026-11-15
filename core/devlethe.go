package core

import (
	"context"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/lethe"
	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/styx"
)

// #λ: a Lethe tree as files (docs/lethe.md).
//
// #λ holds one file, clone. A write of a connection's settings to it
//
//	dial tcp!host!port
//	user <name>
//	key <seed hex>          (32 bytes classical, 64 hybrid)
//	cert <pgcert line>      (optional: pgcert1 or pgcert2)
//	proto hybrid|classical  (optional, default hybrid)
//	path <subtree>          (optional, default /)
//	cache <ms>              (optional, default 1000)
//
// dials the server and gets a ticket, so a refused key fails the write
// with the server's error. proto names the ticket exchange: hybrid
// (X25519 with ML-KEM-768, hybrid signatures) or classical (the Rust
// sibling's); the key must be of that kind, and the device never uses
// the other exchange by itself. With a noauth line in place of user, key and
// cert, the connection uses Lethe's clear mode: no ticket, nothing
// sealed, for a server started with -A. A connection keeps the mode it
// was made with; it never changes it because of what a server answers.
// A read of the same open clone then gives the connection's number n,
// and #λn is the tree at path, ready to bind. The connection lives as
// long as something holds a file of it.
//
// Nothing here holds server state: a walk is a walk (or a stat) of the
// joined path, a read is a range request, a directory read pages through
// readdir. Connections are pooled and replaceable: a request whose
// connection fails is sent again on a new one, a few times with a short
// pause. What the device learns is kept for cache milliseconds: entries
// and qids answer walks and stats, and 32 KiB blocks by path and version
// are served without asking while the file's version is that young.

const (
	lqroot  = 0
	lqclone = 1

	// letheBlock is the cache's block and the largest read sent.
	letheBlock = lethe.MaxCount
	// lethePage is a directory page.
	lethePage = lethe.MaxCount
	// letheCacheMax bounds one connection's data cache.
	letheCacheMax = 4 << 20
	// letheAttrsMax bounds the attribute cache.
	letheAttrsMax = 8192
	// LetheDefaultCacheMs is how long attributes and data are trusted.
	LetheDefaultCacheMs = 1000
	// letheReadahead blocks go out with a sequential reader's block.
	letheReadahead = 4
	// Tries for a request whose connection fails, and the pauses.
	letheAttempts   = 9
	letheFirstPause = 50 * time.Millisecond
	letheMaxPause   = time.Second
	// letheListings: a directory that changes while it is paged through
	// is listed again from the start, at most this many times.
	letheListings = 3
)

// lknown is what is known about one path.
type lknown struct {
	qid styx.Qid
	dir *styx.Dir
}

type lattr struct {
	known lknown
	when  time.Time
}

type lattrs struct{ m map[string]lattr }

func young(when, now time.Time, life time.Duration) bool {
	d := now.Sub(when)
	return d >= 0 && d < life
}

// room makes room for one more. When nothing is old, evict drops the
// older half; without it nothing is added (false).
func (a *lattrs) room(now time.Time, life time.Duration, evict bool) bool {
	if len(a.m) < letheAttrsMax {
		return true
	}
	for k, v := range a.m {
		if !young(v.when, now, life) {
			delete(a.m, k)
		}
	}
	if len(a.m) < letheAttrsMax {
		return true
	}
	if !evict {
		return false
	}
	whens := make([]time.Time, 0, len(a.m))
	for _, v := range a.m {
		whens = append(whens, v.when)
	}
	sort.Slice(whens, func(i, j int) bool { return whens[i].Before(whens[j]) })
	cut := whens[len(whens)/2]
	for k, v := range a.m {
		if !v.when.After(cut) {
			delete(a.m, k)
		}
	}
	return len(a.m) < letheAttrsMax
}

func (a *lattrs) put(p string, k lknown, now time.Time, life time.Duration) {
	if a.m == nil {
		a.m = map[string]lattr{}
	}
	if _, ok := a.m[p]; ok || a.room(now, life, true) {
		a.m[p] = lattr{k, now}
	}
}

func (a *lattrs) walk(p string, now time.Time, life time.Duration) (styx.Qid, bool) {
	v, ok := a.m[p]
	if !ok || !young(v.when, now, life) {
		return styx.Qid{}, false
	}
	return v.known.qid, true
}

func (a *lattrs) stat(p string, now time.Time, life time.Duration) (styx.Dir, bool) {
	v, ok := a.m[p]
	if !ok || v.known.dir == nil || !young(v.when, now, life) {
		return styx.Dir{}, false
	}
	return *v.known.dir, true
}

// version is the version confirmed within life, and the length if the
// entry is known (perhaps older).
func (a *lattrs) version(p string, now time.Time, life time.Duration) (vers uint32, current bool, length uint64, known bool) {
	v, ok := a.m[p]
	if !ok {
		return 0, false, 0, false
	}
	if young(v.when, now, life) {
		vers, current = v.known.qid.Vers, true
	}
	if v.known.dir != nil {
		length, known = v.known.dir.Length, true
	}
	return
}

// confirm: the server just gave qid for p on a read. The entry is kept
// only if it is for that version.
func (a *lattrs) confirm(p string, qid styx.Qid, now time.Time, life time.Duration) {
	var dir *styx.Dir
	if v, ok := a.m[p]; ok && v.known.dir != nil && v.known.dir.Qid.Vers == qid.Vers {
		dir = v.known.dir
	}
	a.put(p, lknown{qid, dir}, now, life)
}

type lblockKey struct {
	path string
	n    uint64
}

type lblock struct {
	vers uint32
	data []byte
	tick uint64
	// ahead: read ahead and not used yet, good for one use however old.
	ahead bool
}

// lcache is the data cache: blocks by path and number, least recently
// used out first.
type lcache struct {
	blocks map[lblockKey]*lblock
	bytes  int
	tick   uint64
}

func (c *lcache) get(p string, b uint64) (vers uint32, data []byte, ahead, ok bool) {
	blk := c.blocks[lblockKey{p, b}]
	if blk == nil {
		return 0, nil, false, false
	}
	c.tick++
	blk.tick = c.tick
	ahead, blk.ahead = blk.ahead, false
	return blk.vers, blk.data, ahead, true
}

func (c *lcache) has(p string, b uint64, vers uint32) bool {
	blk := c.blocks[lblockKey{p, b}]
	return blk != nil && blk.vers == vers
}

func (c *lcache) contains(p string, b uint64) bool { return c.blocks[lblockKey{p, b}] != nil }

func (c *lcache) remove(k lblockKey) {
	if blk := c.blocks[k]; blk != nil {
		c.bytes -= len(blk.data)
		delete(c.blocks, k)
	}
}

func (c *lcache) put(p string, vers uint32, b uint64, data []byte, ahead bool) {
	if c.blocks == nil {
		c.blocks = map[lblockKey]*lblock{}
	}
	// A new version: every block of the old one goes.
	for k, blk := range c.blocks {
		if k.path == p && blk.vers != vers {
			c.remove(k)
		}
	}
	c.remove(lblockKey{p, b})
	for c.bytes+len(data) > letheCacheMax && len(c.blocks) > 0 {
		var oldest lblockKey
		var t uint64 = ^uint64(0)
		for k, blk := range c.blocks {
			if blk.tick < t {
				oldest, t = k, blk.tick
			}
		}
		c.remove(oldest)
	}
	c.tick++
	c.blocks[lblockKey{p, b}] = &lblock{vers: vers, data: slices.Clone(data), tick: c.tick, ahead: ahead}
	c.bytes += len(data)
}

// letheSettings is what the clone write said.
type letheSettings struct {
	host string
	port uint16
	// creds is what to authenticate with; nil for the clear mode.
	creds  *letheCreds
	prefix string
	life   time.Duration
}

type letheCreds struct {
	creds  lethe.Creds
	hybrid bool
}

func parseLethe(text string) (*letheSettings, error) {
	bad := Error(abi.Ebadarg)
	var dial, user, seed, cert, prefix, proto *string
	noauth := false
	life := time.Duration(LetheDefaultCacheMs) * time.Millisecond
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "noauth" {
			noauth = true
			continue
		}
		k, v, ok := strings.Cut(line, " ")
		if !ok {
			return nil, bad
		}
		switch k {
		case "dial":
			dial = &v
		case "user":
			user = &v
		case "key":
			if (len(v) != 64 && len(v) != 128) || !isHex(v) {
				return nil, bad
			}
			seed = &v
		case "proto":
			if v != "hybrid" && v != "classical" {
				return nil, bad
			}
			proto = &v
		case "cert":
			cert = &v
		case "path":
			prefix = &v
		case "cache":
			ms, err := strconv.ParseUint(v, 10, 64)
			if err != nil || ms > uint64(1<<62)/uint64(time.Millisecond) {
				return nil, bad
			}
			life = time.Duration(ms) * time.Millisecond
		default:
			return nil, bad
		}
	}
	if dial == nil {
		return nil, bad
	}
	parts := strings.Split(*dial, "!")
	if len(parts) != 3 || parts[0] != "tcp" || parts[1] == "" {
		return nil, bad
	}
	port, err := strconv.ParseUint(parts[2], 10, 16)
	if err != nil {
		return nil, bad
	}
	s := &letheSettings{host: parts[1], port: uint16(port), prefix: "/", life: life}
	if prefix != nil {
		s.prefix = *prefix
	}
	if !lethe.ValidPath(s.prefix) {
		return nil, Error(abi.Efilename)
	}
	// The mode is one or the other: noauth with a user or a key is a
	// mistake, not a request to try both.
	if noauth {
		if user != nil || seed != nil || cert != nil || proto != nil {
			return nil, bad
		}
		return s, nil
	}
	if user == nil || seed == nil {
		return nil, bad
	}
	b := make([]byte, len(*seed)/2)
	for i := range b {
		b[i] = unhex((*seed)[2*i])<<4 | unhex((*seed)[2*i+1])
	}
	key, err := pgauth.NewPrivateKey(b)
	if err != nil {
		return nil, bad
	}
	// The exchange is the one asked for, hybrid by default; a key of the
	// other kind is a mistake, not a reason to use the other exchange.
	c := &letheCreds{creds: lethe.Creds{User: *user, Key: key}, hybrid: proto == nil || *proto == "hybrid"}
	if key.Hybrid() != c.hybrid {
		return nil, Error(string(pgauth.EWrongAlg))
	}
	if cert != nil {
		ct, err := pgauth.ParseCert([]byte(*cert))
		if err != nil || !ct.Key.Equal(key.Public()) {
			return nil, bad
		}
		c.creds.Cert = ct
	}
	s.creds = c
	return s, nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// lconn is one connection to the server.
type lconn struct{ s platform.Socket }

func (c *lconn) Read(b []byte) (int, error) {
	n, err := c.s.Recv(nil, b, nil)
	if err == nil && n == 0 && len(b) > 0 {
		return 0, Error(abi.Ehungup)
	}
	return n, err
}

func (c *lconn) writeAll(ctx context.Context, b []byte) error {
	stop, done := stopper(ctx)
	defer done()
	for len(b) > 0 {
		n, err := c.s.Send(stop, b, nil)
		if err != nil {
			return ipErr(ctx, err)
		}
		if n == 0 {
			return Error(abi.Ehungup)
		}
		b = b[n:]
	}
	return nil
}

func (c *lconn) readFrame(ctx context.Context) ([]byte, error) {
	stop, done := stopper(ctx)
	defer done()
	f, err := lethe.ReadFrame(stopReader{c.s, stop})
	if err != nil {
		return nil, ipErr(ctx, err)
	}
	return f, nil
}

type stopReader struct {
	s    platform.Socket
	stop <-chan struct{}
}

func (r stopReader) Read(b []byte) (int, error) {
	n, err := r.s.Recv(r.stop, b, nil)
	if err == nil && n == 0 && len(b) > 0 {
		return 0, Error(abi.Ehungup)
	}
	return n, err
}

func (c *lconn) exchange(ctx context.Context, f []byte) ([]byte, error) {
	if err := c.writeAll(ctx, f); err != nil {
		return nil, err
	}
	return c.readFrame(ctx)
}

func (c *lconn) hangup() { _ = c.s.Hangup(); _ = c.s.Close() }

// letheRemote is one connection's settings, its ticket and its caches.
type letheRemote struct {
	id       uint32
	platform platform.Platform
	net      platform.NetStack
	settings *letheSettings
	tickets  letheTickets

	pool  *lpool
	mu    sync.Mutex // guards attrs, cache
	attrs lattrs
	cache lcache
	tag   atomic.Uint32
	// noWalk is set once the server says Twalk is not implemented.
	noWalk atomic.Bool
}

// letheTickets is the sealed mode's side of a connection: it frames each
// request and opens its reply. The clear mode has none.
type letheTickets interface {
	// frame gets a ticket over c if it needs one and frames op. ok is
	// false with the error that ends the attempt; transport says whether
	// that error is the connection's.
	frame(ctx context.Context, c *lconn, id uint32, op lethe.Op) (f []byte, open func([]byte) (lethe.Reply, error), err error, transport bool)
	// renew drops the ticket.
	renew()
}

// ltickets is the sealed mode: the credentials and the current ticket.
type ltickets struct {
	r       *letheRemote
	c       *letheCreds
	mu      sync.Mutex
	session *lethe.Session
}

func newLetheTickets(r *letheRemote, c *letheCreds) (letheTickets, error) {
	return &ltickets{r: r, c: c}, nil
}

func (t *ltickets) entropy(b []byte) { t.r.random(b) }

func (t *ltickets) now() uint64 { return uint64(time.Now().Unix()) }

// get returns the current ticket, or a new one got over c.
func (t *ltickets) get(ctx context.Context, c *lconn) (*lethe.Session, error, bool) {
	t.mu.Lock()
	s := t.session
	t.mu.Unlock()
	if s != nil && !s.Expiring(t.now()) {
		return s, nil, false
	}
	f, p, err := lethe.AuthFrame(&t.c.creds, t.r.nextTag(), lethe.RightRead, t.r.settings.prefix, t.c.hybrid, t.entropy)
	if err != nil {
		return nil, err, false
	}
	reply, err := c.exchange(ctx, f)
	if err != nil {
		return nil, err, true
	}
	s, err = p.Finish(reply, t.now())
	if err != nil {
		return nil, Error(err.Error()), false
	}
	t.mu.Lock()
	t.session = s
	t.mu.Unlock()
	return s, nil, false
}

func (t *ltickets) frame(ctx context.Context, c *lconn, id uint32, op lethe.Op) ([]byte, func([]byte) (lethe.Reply, error), error, bool) {
	s, err, transport := t.get(ctx, c)
	if err != nil {
		return nil, nil, err, transport
	}
	var n [lethe.NonceLen]byte
	t.entropy(n[:])
	f, p := s.Request(id, t.now(), op, n)
	return f, func(b []byte) (lethe.Reply, error) { return s.Reply(p, b) }, nil, false
}

func (t *ltickets) renew() {
	t.mu.Lock()
	t.session = nil
	t.mu.Unlock()
}

func (r *letheRemote) nextTag() uint32 { return r.tag.Add(1) }

func (r *letheRemote) random(b []byte) { r.platform.Entropy(b) }

// lpool holds a connection's idle sockets. It is apart from the
// letheRemote so the cleanup that hangs them up when the connection is
// gone does not keep it alive.
type lpool struct {
	mu   sync.Mutex
	idle []*lconn
}

func (p *lpool) get() *lconn {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.idle)
	if n == 0 {
		return nil
	}
	c := p.idle[n-1]
	p.idle = p.idle[:n-1]
	return c
}

func (p *lpool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.idle {
		c.hangup()
	}
	p.idle = nil
}

func (r *letheRemote) conn(ctx context.Context) (*lconn, error) {
	if c := r.pool.get(); c != nil {
		return c, nil
	}
	if r.net == nil {
		return nil, Error("network unavailable")
	}
	addr, ok := ParseIP(r.settings.host)
	if !ok {
		hs, err := r.net.LookupHost(r.settings.host)
		if err != nil || len(hs) == 0 {
			return nil, Error(abi.Enetaddr)
		}
		if addr, ok = ParseIP(hs[0]); !ok {
			return nil, Error(abi.Enetaddr)
		}
	}
	s, err := r.net.Socket(platform.TCP)
	if err != nil {
		return nil, ipErr(ctx, err)
	}
	stop, done := stopper(ctx)
	err = s.Connect(stop, addr, r.settings.port)
	done()
	if err != nil {
		_ = s.Close()
		return nil, ipErr(ctx, err)
	}
	return &lconn{s: s}, nil
}

func (r *letheRemote) putIdle(c *lconn) {
	r.pool.mu.Lock()
	r.pool.idle = append(r.pool.idle, c)
	r.pool.mu.Unlock()
}

// The outcome of one attempt.
const (
	lDone = iota
	lTransport
	lRenew
	// lResend: the server no longer holds the ticket a reference named;
	// resend with the ticket (the session already knows to).
	lResend
)

// frame frames op in the connection's mode over c.
func (r *letheRemote) frame(ctx context.Context, c *lconn, op lethe.Op) (uint32, []byte, func([]byte) (lethe.Reply, error), error, bool) {
	id := r.nextTag()
	if r.tickets == nil {
		return id, lethe.ClearFrame(id, op), func(b []byte) (lethe.Reply, error) { return lethe.ClearReply(id, b) }, nil, false
	}
	f, open, err, transport := r.tickets.frame(ctx, c, id, op)
	return id, f, open, err, transport
}

// outcome classifies a reply's error.
func (r *letheRemote) outcome(err error) int {
	if f, ok := err.(*lethe.Failure); ok && f.Kind == lethe.FailClear && r.tickets != nil {
		switch {
		case lethe.Renew(f.Text):
			return lRenew
		case lethe.Resend(f.Text):
			return lResend
		}
	}
	return lDone
}

func (r *letheRemote) attempt(ctx context.Context, c *lconn, op lethe.Op) (lethe.Reply, error, int) {
	_, f, open, err, transport := r.frame(ctx, c, op)
	if err != nil {
		if transport {
			return nil, err, lTransport
		}
		return nil, err, lDone
	}
	b, err := c.exchange(ctx, f)
	if err != nil {
		return nil, err, lTransport
	}
	x, err := open(b)
	return x, err, r.outcome(err)
}

func pause(ctx context.Context, d time.Duration) error {
	_, err := ParkTimeout(ctx, &Rendez{}, func() bool { return false }, int(d/time.Millisecond))
	return err
}

func (r *letheRemote) call(ctx context.Context, op lethe.Op) (lethe.Reply, error) {
	return r.callTries(ctx, op, letheAttempts)
}

// callTries sends op until it is answered, trying at most attempts
// connections.
func (r *letheRemote) callTries(ctx context.Context, op lethe.Op, attempts int) (lethe.Reply, error) {
	p := letheFirstPause
	renewed, resent := false, false
	var last error = Error(abi.Ehungup)
	for tries := 0; tries < attempts; {
		if err := Check(ctx); err != nil {
			return nil, err
		}
		c, err := r.conn(ctx)
		if err != nil {
			if e, ok := err.(Error); ok && e == abi.Eintr {
				return nil, err
			}
			last = err
			tries++
			if tries < attempts {
				if err := pause(ctx, p); err != nil {
					return nil, err
				}
				p = min(2*p, letheMaxPause)
			}
			continue
		}
		x, err, how := r.attempt(ctx, c, op)
		switch {
		case how == lDone:
			r.putIdle(c)
			if f, ok := err.(*lethe.Failure); ok {
				return nil, Error(f.Text)
			}
			return x, err
		case how == lResend && !resent:
			resent = true
			r.putIdle(c)
		case how == lResend:
			r.putIdle(c)
			return nil, Error(lethe.ETicketRef)
		case how == lRenew && !renewed:
			renewed = true
			r.tickets.renew()
			r.putIdle(c)
		case how == lRenew:
			r.putIdle(c)
			return nil, Error(lethe.ETicket)
		default:
			c.hangup()
			if e, ok := err.(Error); ok && e == abi.Eintr {
				return nil, err
			}
			last = err
			tries++
			// A pooled connection may simply be stale: try a fresh one
			// at once before pausing.
			if tries > 1 && tries < attempts {
				if err := pause(ctx, p); err != nil {
					return nil, err
				}
				p = min(2*p, letheMaxPause)
			}
		}
	}
	return nil, last
}

// pipeline sends every op on one connection before reading any reply
// (matched by id). One try only: nil if the connection or the ticket
// fails, and the caller goes on without these answers.
func (r *letheRemote) pipeline(ctx context.Context, ops []lethe.Op) []lethe.Reply {
	c, err := r.conn(ctx)
	if err != nil {
		return nil
	}
	type waiting struct {
		id   uint32
		open func([]byte) (lethe.Reply, error)
	}
	var out []byte
	ws := make([]waiting, 0, len(ops))
	for _, op := range ops {
		id, f, open, err, transport := r.frame(ctx, c, op)
		if err != nil {
			if transport {
				c.hangup()
			} else {
				r.putIdle(c)
			}
			return nil
		}
		out = append(out, f...)
		ws = append(ws, waiting{id, open})
	}
	answers := make([]lethe.Reply, len(ops))
	errs := make([]error, len(ops))
	if err := c.writeAll(ctx, out); err != nil {
		c.hangup()
		return nil
	}
	renew, resend := false, false
	for range ops {
		b, err := c.readFrame(ctx)
		if err != nil {
			c.hangup()
			return nil
		}
		id := lethe.FrameID(b)
		i := slices.IndexFunc(ws, func(w waiting) bool { return w.id == id })
		if i < 0 {
			c.hangup()
			return nil
		}
		answers[i], errs[i] = ws[i].open(b)
		switch r.outcome(errs[i]) {
		case lRenew:
			renew = true
		case lResend:
			// The caller's ordinary call sends the ticket again.
			resend = true
		}
	}
	r.putIdle(c)
	if renew {
		r.tickets.renew()
		return nil
	}
	if resend {
		return nil
	}
	for i := range errs {
		if errs[i] != nil {
			answers[i] = nil
		}
	}
	return answers
}

func (r *letheRemote) fix(d styx.Dir) styx.Dir {
	d.Type = uint16('λ')
	d.Dev = r.id
	return d
}

func (r *letheRemote) life() time.Duration { return r.settings.life }

func (r *letheRemote) stat(ctx context.Context, p string) (styx.Dir, error) {
	return r.statTries(ctx, p, letheAttempts)
}

func (r *letheRemote) statTries(ctx context.Context, p string, attempts int) (styx.Dir, error) {
	r.mu.Lock()
	d, ok := r.attrs.stat(p, time.Now(), r.life())
	r.mu.Unlock()
	if ok {
		return d, nil
	}
	x, err := r.callTries(ctx, &lethe.Stat{P: p}, attempts)
	if err != nil {
		return styx.Dir{}, err
	}
	s, ok := x.(*lethe.Rstat)
	if !ok {
		return styx.Dir{}, Error(lethe.EMessage)
	}
	d = r.fix(s.Dir)
	r.found(p, d)
	return d, nil
}

func (r *letheRemote) found(p string, d styx.Dir) {
	r.mu.Lock()
	r.attrs.put(p, lknown{d.Qid, &d}, time.Now(), r.life())
	r.mu.Unlock()
}

// walk1 is the qid of dir's child name, from the attribute cache when it
// has it, else from the server.
func (r *letheRemote) walk1(ctx context.Context, dir, name string) (styx.Qid, error) {
	p := lethe.Join(dir, name)
	r.mu.Lock()
	q, ok := r.attrs.walk(p, time.Now(), r.life())
	r.mu.Unlock()
	if ok {
		return q, nil
	}
	d, err := r.stat(ctx, p)
	return d.Qid, err
}

// walkNames walks names from dir: from the attribute cache when it knows
// them all, else in one Twalk (or a name with a stat, from a server
// without Twalk). It returns the qids walked and, when the walk stopped
// short, why. It fails when the first name cannot be walked.
func (r *letheRemote) walkNames(ctx context.Context, dir string, names []string) ([]styx.Qid, error, error) {
	var qids []styx.Qid
	p := dir
	now := time.Now()
	r.mu.Lock()
	for _, n := range names {
		if !lethe.ValidElem(n) {
			break
		}
		q := lethe.Join(p, n)
		qid, ok := r.attrs.walk(q, now, r.life())
		if !ok {
			break
		}
		qids = append(qids, qid)
		p = q
	}
	r.mu.Unlock()
	if len(qids) == len(names) {
		return qids, nil, nil
	}
	return r.walkServer(ctx, dir, names)
}

func (r *letheRemote) walkServer(ctx context.Context, dir string, names []string) ([]styx.Qid, error, error) {
	var wanted []string
	total := len(dir)
	for _, n := range names {
		if !lethe.ValidElem(n) || len(wanted) == lethe.MaxWalk {
			break
		}
		wanted = append(wanted, n)
		total += len(n) + 1
	}
	if len(wanted) == 0 {
		return nil, nil, Error(abi.Enonexist)
	}
	if len(wanted) >= 2 && total <= lethe.MaxPath && !r.noWalk.Load() {
		x, err := r.call(ctx, &lethe.Walk{P: dir, Names: wanted})
		if err == nil {
			w, ok := x.(*lethe.Rwalk)
			if !ok || len(w.Qids) > len(wanted) {
				return nil, nil, Error(lethe.EMessage)
			}
			now := time.Now()
			r.mu.Lock()
			p := dir
			n := len(w.Qids)
			for i, q := range w.Qids {
				p = lethe.Join(p, wanted[i])
				var d *styx.Dir
				if i+1 == n && w.Error == "" && w.Stat != nil {
					fd := r.fix(*w.Stat)
					d = &fd
				}
				r.attrs.put(p, lknown{q, d}, now, r.life())
			}
			r.mu.Unlock()
			var why error
			if w.Error != "" {
				why = Error(w.Error)
			}
			if n == 0 {
				if why == nil {
					why = Error(abi.Enonexist)
				}
				return nil, nil, why
			}
			if n < len(names) {
				if why == nil {
					why = Error(abi.Enonexist)
				}
				return w.Qids, why, nil
			}
			return w.Qids, nil, nil
		}
		if e, ok := err.(Error); ok && string(e) == abi.Enotimpl {
			r.noWalk.Store(true)
		} else {
			return nil, nil, err
		}
	}
	q, err := r.walk1(ctx, dir, wanted[0])
	if err != nil {
		return nil, nil, err
	}
	return []styx.Qid{q}, nil, nil
}

// read reads into buf at off. Blocks come from the cache while the file's
// version is confirmed (within the lifetime, or by this read's first
// request); the rest from the server, with readahead for a sequential
// reader.
func (r *letheRemote) read(ctx context.Context, p string, buf []byte, off int64) (int, error) {
	if off < 0 {
		return 0, Error(abi.Enegoff)
	}
	r.mu.Lock()
	vers, current, length, known := r.attrs.version(p, time.Now(), r.life())
	r.mu.Unlock()
	n := 0
	for n < len(buf) {
		at := uint64(off) + uint64(n)
		b := at / letheBlock
		r.mu.Lock()
		cv, data, ahead, cached := r.cache.get(p, b)
		r.mu.Unlock()
		if !(cached && ((current && cv == vers) || (ahead && !current))) {
			var err error
			cv, data, err = r.fetch(ctx, p, b, cached, cv, data, length, known)
			if err != nil {
				return 0, err
			}
		}
		vers, current = cv, true
		skip := int(at - b*letheBlock)
		if skip >= len(data) {
			break
		}
		k := copy(buf[n:], data[skip:])
		n += k
		if len(data) < letheBlock {
			break
		}
	}
	return n, nil
}

// fetch gets block b from the server (conditional when cached), and the
// blocks after it when the reader looks sequential: it starts at 0 with
// a known length, or the block before is cached.
func (r *letheRemote) fetch(ctx context.Context, p string, b uint64, cached bool, cv uint32, cdata []byte, length uint64, known bool) (uint32, []byte, error) {
	op := func(blk uint64, cond bool, have uint32) lethe.Op {
		return &lethe.Read{P: p, Offset: blk * letheBlock, Count: letheBlock, Cond: cond, Have: have}
	}
	ops := []lethe.Op{op(b, cached, cv)}
	r.mu.Lock()
	sequential := (b == 0 && known) || (b > 0 && r.cache.contains(p, b-1))
	if sequential {
		for k := b + 1; k <= b+letheReadahead; k++ {
			if (known && k*letheBlock >= length) || r.cache.contains(p, k) {
				break
			}
			ops = append(ops, op(k, false, 0))
		}
	}
	r.mu.Unlock()
	var answers []lethe.Reply
	if len(ops) > 1 {
		answers = r.pipeline(ctx, ops)
	}
	var first lethe.Reply
	if answers != nil && answers[0] != nil {
		first = answers[0]
	} else {
		answers = nil
		x, err := r.call(ctx, ops[0])
		if err != nil {
			return 0, nil, err
		}
		first = x
	}
	rr, ok := first.(*lethe.Rread)
	if !ok {
		return 0, nil, Error(lethe.EMessage)
	}
	data := rr.Data
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if rr.Unchanged {
		if !cached || cv != rr.Qid.Vers {
			return 0, nil, Error(lethe.EMessage)
		}
		data = cdata
	} else {
		r.cache.put(p, rr.Qid.Vers, b, data, false)
	}
	r.attrs.confirm(p, rr.Qid, now, r.life())
	if answers != nil && len(data) == letheBlock {
		for i, a := range answers[1:] {
			ar, ok := a.(*lethe.Rread)
			if !ok || ar.Qid.Vers != rr.Qid.Vers {
				break
			}
			k := b + uint64(i) + 1
			if !r.cache.has(p, k, ar.Qid.Vers) {
				r.cache.put(p, ar.Qid.Vers, k, ar.Data, true)
			}
			if len(ar.Data) < letheBlock {
				break
			}
		}
	}
	return rr.Qid.Vers, data, nil
}

func (r *letheRemote) list(ctx context.Context, p string) ([]styx.Dir, error) {
	tries := 0
again:
	var out []styx.Dir
	var cookie []byte
	var seen *styx.Qid
	for {
		x, err := r.call(ctx, &lethe.Readdir{P: p, Cookie: cookie, Count: lethePage})
		if err != nil {
			return nil, err
		}
		rd, ok := x.(*lethe.Rreaddir)
		if !ok {
			return nil, Error(lethe.EMessage)
		}
		if seen != nil && *seen != rd.Qid && tries+1 < letheListings {
			tries++
			goto again
		}
		q := rd.Qid
		seen = &q
		ds, err := lethe.Dirs(rd.Entries)
		if err != nil {
			return nil, Error(lethe.EMessage)
		}
		for _, d := range ds {
			out = append(out, r.fix(d))
		}
		if len(rd.Cookie) == 0 {
			break
		}
		cookie = rd.Cookie
	}
	// The entries answer walks and stats of these names for a while, as
	// NFS's READDIRPLUS does.
	if r.life() > 0 {
		now := time.Now()
		r.mu.Lock()
		if r.attrs.m == nil {
			r.attrs.m = map[string]lattr{}
		}
		for i := range out {
			k := lethe.Join(p, out[i].Name)
			if _, ok := r.attrs.m[k]; !ok && !r.attrs.room(now, r.life(), false) {
				break
			}
			d := out[i]
			r.attrs.put(k, lknown{d.Qid, &d}, now, r.life())
		}
		r.mu.Unlock()
	}
	return out, nil
}

func (r *letheRemote) close() { r.pool.close() }

// lnode is a Chan's Aux: a file of a connection, by its path on the
// server. #λ and its clone have none.
type lnode struct {
	remote *letheRemote
	path   string
}

// lpending is the open clone's state: the connection its write made.
type lpending struct {
	mu     sync.Mutex
	remote *letheRemote
}

// DevLethe is #λ.
type DevLethe struct {
	k   *Kernel
	net platform.NetStack

	// remotes are the live connections. A connection lives as long as
	// something holds a file of it (the open clone, or a bind), so the
	// table holds them weakly, and their idle sockets are hung up when
	// they go.
	mu      sync.Mutex
	remotes map[uint32]weak.Pointer[letheRemote]
	next    uint32
}

// NewDevLethe makes #λ, dialling through net (nil: no network).
func NewDevLethe(k *Kernel, net platform.NetStack) *DevLethe {
	return &DevLethe{k: k, net: net, remotes: map[uint32]weak.Pointer[letheRemote]{}}
}

func (d *DevLethe) DC() rune     { return 'λ' }
func (d *DevLethe) Name() string { return "lethe" }

func (d *DevLethe) topDir(q uint64) styx.Dir {
	user := d.k.Owner
	if q == lqroot {
		return styx.Dir{Type: uint16('λ'), Qid: styx.Qid{Type: styx.QTDIR, Path: lqroot},
			Mode: styx.DMDIR | 0o555, Name: "#λ", UID: user, GID: user, MUID: user}
	}
	return styx.Dir{Type: uint16('λ'), Qid: styx.Qid{Path: lqclone}, Mode: 0o666,
		Name: "clone", UID: user, GID: user, MUID: user}
}

func (d *DevLethe) connect(ctx context.Context, text string) (*letheRemote, error) {
	s, err := parseLethe(text)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.next++
	id := d.next
	d.mu.Unlock()
	r := &letheRemote{id: id, platform: d.k.Platform, net: d.net, settings: s, pool: &lpool{}}
	if s.creds != nil {
		t, err := newLetheTickets(r, s.creds)
		if err != nil {
			return nil, err
		}
		r.tickets = t
	}
	// The attach point must be there, and the ticket good. A server that
	// does not answer fails the attach at once: the retries are for one
	// that goes away later.
	dir, err := r.statTries(ctx, s.prefix, 1)
	if err != nil {
		r.close()
		return nil, err
	}
	if dir.Mode&styx.DMDIR == 0 {
		r.close()
		return nil, Error(abi.Enotdir)
	}
	runtime.AddCleanup(r, func(p *lpool) { p.close() }, r.pool)
	d.mu.Lock()
	for k, w := range d.remotes {
		if w.Value() == nil {
			delete(d.remotes, k)
		}
	}
	d.remotes[id] = weak.Make(r)
	d.mu.Unlock()
	return r, nil
}

func (d *DevLethe) Attach(ctx context.Context, spec string) (Walked, error) {
	if spec == "" {
		return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: lqroot}}, nil
	}
	n, err := strconv.ParseUint(spec, 10, 32)
	if err != nil {
		return Walked{}, Error(abi.Ebadarg)
	}
	d.mu.Lock()
	r := d.remotes[uint32(n)].Value()
	d.mu.Unlock()
	if r == nil {
		return Walked{}, Error(abi.Enonexist)
	}
	p := r.settings.prefix
	dir, err := r.stat(ctx, p)
	if err != nil {
		return Walked{}, err
	}
	return Walked{Qid: dir.Qid, Aux: &lnode{r, p}}, nil
}

func (d *DevLethe) Devno(spec string) uint32 {
	n, _ := strconv.ParseUint(spec, 10, 32)
	return uint32(n)
}

func (d *DevLethe) Walk(ctx context.Context, c *Chan, name string) (Walked, error) {
	n, ok := c.Aux.(*lnode)
	if !ok {
		if c.Qid.Path == lqroot && name == "clone" {
			return Walked{Qid: styx.Qid{Path: lqclone}}, nil
		}
		return Walked{}, Error(abi.Enonexist)
	}
	if !lethe.ValidElem(name) {
		return Walked{}, Error(abi.Enonexist)
	}
	q, err := n.remote.walk1(ctx, n.path, name)
	if err != nil {
		return Walked{}, err
	}
	return Walked{Qid: q, Aux: &lnode{n.remote, lethe.Join(n.path, name)}}, nil
}

func (d *DevLethe) MaxWalk() int { return lethe.MaxWalk }

// WalkNames walks a whole path in one Twalk.
func (d *DevLethe) WalkNames(ctx context.Context, c *Chan, names []string) (WalkedNames, error) {
	n, ok := c.Aux.(*lnode)
	if !ok {
		if len(names) == 0 {
			return WalkedNames{}, Error(abi.Enonexist)
		}
		w, err := d.Walk(ctx, c, names[0])
		if err != nil {
			return WalkedNames{}, err
		}
		return WalkedNames{Qids: []styx.Qid{w.Qid}, Aux: w.Aux, Reached: true}, nil
	}
	qids, why, err := n.remote.walkNames(ctx, n.path, names)
	if err != nil {
		return WalkedNames{}, err
	}
	if why != nil {
		return WalkedNames{Qids: qids, Why: why}, nil
	}
	if len(qids) == 0 {
		return WalkedNames{}, Error(abi.Enonexist)
	}
	p := n.path
	for _, name := range names[:len(qids)] {
		p = lethe.Join(p, name)
	}
	return WalkedNames{Qids: qids, Aux: &lnode{n.remote, p}, Reached: true}, nil
}

func (d *DevLethe) Stat(ctx context.Context, c *Chan) (styx.Dir, error) {
	if n, ok := c.Aux.(*lnode); ok {
		return n.remote.stat(ctx, n.path)
	}
	return d.topDir(c.Qid.Path), nil
}

func (d *DevLethe) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	_, isFile := c.Aux.(*lnode)
	if !isFile && c.Qid.Path == lqclone {
		c.State = &lpending{}
		return c, nil
	}
	acc := mode & 3
	if acc == abi.OWRITE || acc == abi.ORDWR || mode&abi.OTRUNC != 0 {
		if c.IsDir() {
			return nil, Error(abi.Eisdir)
		}
		return nil, Error(abi.Eperm)
	}
	return c, nil
}

func (d *DevLethe) Close(c *Chan) {}

func (d *DevLethe) Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	if s, ok := c.State.(*lpending); ok {
		s.mu.Lock()
		r := s.remote
		s.mu.Unlock()
		if r == nil {
			return 0, Error(abi.Ebadusefd)
		}
		return ReadStr([]byte(strconv.FormatUint(uint64(r.id), 10)), buf, off), nil
	}
	n, ok := c.Aux.(*lnode)
	if !ok {
		return 0, Error(abi.Enonexist)
	}
	return n.remote.read(ctx, n.path, buf, off)
}

func (d *DevLethe) DirRead(ctx context.Context, c *Chan) ([]styx.Dir, error) {
	if n, ok := c.Aux.(*lnode); ok {
		return n.remote.list(ctx, n.path)
	}
	return []styx.Dir{d.topDir(lqclone)}, nil
}

func (d *DevLethe) Write(ctx context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	s, ok := c.State.(*lpending)
	if !ok {
		return 0, Error(abi.Eperm)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remote != nil {
		return 0, Error(abi.Einuse)
	}
	r, err := d.connect(ctx, string(buf))
	if err != nil {
		return 0, err
	}
	s.remote = r
	return len(buf), nil
}

func (d *DevLethe) Create(context.Context, *Chan, string, uint32) (Walked, error) {
	return Walked{}, Error(abi.Eperm)
}
func (d *DevLethe) Remove(context.Context, *Chan) error           { return Error(abi.Eperm) }
func (d *DevLethe) Wstat(context.Context, *Chan, *styx.Dir) error { return Error(abi.Eperm) }

func (d *DevLethe) Iounit(c *Chan) uint32 {
	if _, ok := c.Aux.(*lnode); ok {
		return letheBlock
	}
	return 0
}
