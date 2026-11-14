package core

import (
	"context"
	"crypto/cipher"
	"crypto/des"
	"crypto/md5"
	"crypto/sha1"
	"encoding/binary"
	"hash"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/libsec"
	"github.com/ericvh/purgatorio-go/styx"
)

// DevSSL is #D, the secure sockets layer (devssl.c, ssl(3)). Each
// conversation pushes the SSLv2 record format onto an fd: in clear mode
// it only delimits messages; with "alg" it digests, encrypts, or both,
// with the secrets written to secretin and secretout.
//
//	#D/clone
//	#D/n/ctl data secretin secretout encalgs hashalgs
type DevSSL struct {
	DevBase
	k     *Kernel
	mu    sync.Mutex
	ds    []*dstate
	hiwat int
}

// NewDevSSL returns #D.
func NewDevSSL(k *Kernel) *DevSSL { return &DevSSL{k: k} }

func (d *DevSSL) DC() rune     { return 'D' }
func (d *DevSSL) Name() string { return "ssl" }

const (
	sslQtopdir = 1 + iota
	sslQclonus
	sslQconvdir
	sslQdata
	sslQctl
	sslQsecretin
	sslQsecretout
	sslQencalgs
	sslQhashalgs
)

// Connection states.
const (
	sIncomplete = 0
	sClear      = 1
	sEncrypting = 2
	sDigesting  = 4
	sDigenc     = sEncrypting | sDigesting
)

const (
	maxdmsg   = 1 << 16
	maxdstate = 1 << 10
)

func ssltype(q styx.Qid) int    { return int(q.Path & 0xf) }
func sslconv(q styx.Qid) int    { return int(q.Path>>4) & (maxdstate - 1) }
func sslqid(c, t int) uint64    { return uint64(c)<<4 | uint64(t) }
func sslfile(c, t int) styx.Qid { return styx.Qid{Type: styx.QTFILE, Path: sslqid(c, t)} }

// oneway is one direction's secret and cipher state.
type oneway struct {
	secret []byte
	block  cipher.Block // DES or IDEA, with ivec
	ivec   []byte
	rc4    *libsec.RC4
	mid    uint32
}

func (w *oneway) setsecret(b []byte) { w.secret = append([]byte(nil), b...) }

type dstate struct {
	c     *Chan // the pushed fd
	state int
	ref   int

	encalg   string // "", "des", "desecb", "rc4", "idea", "ideaecb"
	blocklen int
	diglen   int
	hf       func() hash.Hash

	max, maxpad int

	inq     sync.Mutex // serialises reads
	inctl   sync.Mutex // input algorithm state
	outq    sync.Mutex // serialises writes and algorithm changes
	in, out oneway

	unprocessed []byte
	processed   []byte

	user string
	perm uint32
}

type hashalg struct {
	name   string
	diglen int
	hf     func() hash.Hash
}

var sslhashtab = []hashalg{
	{"md4", 16, libsec.NewMD4},
	{"md5", md5.Size, md5.New},
	{"sha1", sha1.Size, sha1.New},
	{"sha", sha1.Size, sha1.New},
}

type encalg struct {
	name     string
	blocklen int
	alg      string
	keyinit  func(w *oneway) error
}

var sslenctab = []encalg{
	{"descbc", 8, "descbc", initDESkey},
	{"desecb", 8, "desecb", initDESkey},
	{"des_56_cbc", 8, "descbc", initDESkey},
	{"des_56_ecb", 8, "desecb", initDESkey},
	{"des_40_cbc", 8, "descbc", initDESkey40},
	{"des_40_ecb", 8, "desecb", initDESkey40},
	{"rc4", 1, "rc4", initRC4key(5)}, // deprecated: rc4_40
	{"rc4_256", 1, "rc4", initRC4key(0)},
	{"rc4_128", 1, "rc4", initRC4key(16)},
	{"rc4_40", 1, "rc4", initRC4key(5)},
	{"ideacbc", 8, "ideacbc", initIDEAkey},
	{"ideaecb", 8, "ideaecb", initIDEAkey},
}

var sslencalgs, sslhashalgs = func() (string, string) {
	var e, h []string
	for _, a := range sslenctab {
		e = append(e, a.name)
	}
	for _, a := range sslhashtab {
		h = append(h, a.name)
	}
	return strings.Join(e, " "), strings.Join(h, " ")
}()

func initDESkey(w *oneway) error {
	if len(w.secret) < 8 {
		return Error("secret too short")
	}
	return setblock(w, w.secret[:8], 16)
}

// initDESkey40 masks 16 bits of the key: 40-bit DES is 56-bit DES with
// a weak key.
func initDESkey40(w *oneway) error {
	if len(w.secret) < 8 {
		return Error("secret too short")
	}
	key := append([]byte(nil), w.secret[:8]...)
	for i := 0; i < 8; i += 2 {
		key[i] &= 0x0f
	}
	return setblock(w, key, 16)
}

// setblock sets up DES with the ivec after the key, when the secret is
// long enough to hold one.
func setblock(w *oneway, key []byte, ivecat int) error {
	b, err := des.NewCipher(key)
	if err != nil {
		return Error(abi.Ebadarg)
	}
	w.block, w.ivec = b, make([]byte, 8)
	if len(w.secret) >= ivecat {
		copy(w.ivec, w.secret[8:16])
	}
	return nil
}

func initIDEAkey(w *oneway) error {
	if len(w.secret) < 16 {
		return Error("secret too short")
	}
	w.block, w.ivec = libsec.NewIDEA(w.secret[:16]), make([]byte, 8)
	if len(w.secret) >= 24 {
		copy(w.ivec, w.secret[16:24])
	}
	return nil
}

// initRC4key uses at most n bytes of the secret, all of it for n 0.
func initRC4key(n int) func(w *oneway) error {
	return func(w *oneway) error {
		k := w.secret
		if n > 0 && len(k) > n {
			k = k[:n]
		}
		w.rc4 = libsec.NewRC4(k)
		return nil
	}
}

func (d *DevSSL) conv(q styx.Qid) *dstate {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := sslconv(q); n < len(d.ds) {
		return d.ds[n]
	}
	return nil
}

func (d *DevSSL) user(ctx context.Context) string {
	if p := ProgFrom(ctx); p != nil {
		return p.User()
	}
	return d.k.Owner
}

func (d *DevSSL) Attach(context.Context, string) (Walked, error) {
	return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: sslqid(0, sslQtopdir)}}, nil
}

var sslconvfiles = []struct {
	name string
	t    int
}{
	{"ctl", sslQctl}, {"data", sslQdata}, {"secretin", sslQsecretin},
	{"secretout", sslQsecretout}, {"encalgs", sslQencalgs}, {"hashalgs", sslQhashalgs},
}

func (d *DevSSL) Walk(ctx context.Context, c *Chan, name string) (Walked, error) {
	switch ssltype(c.Qid) {
	case sslQtopdir:
		if name == "clone" {
			return Walked{Qid: sslfile(0, sslQclonus)}, nil
		}
		n, err := strconv.Atoi(name)
		d.mu.Lock()
		hi := d.hiwat
		d.mu.Unlock()
		if err != nil || n < 0 || n >= hi || strconv.Itoa(n) != name {
			return Walked{}, Error(abi.Enonexist)
		}
		return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: sslqid(n, sslQconvdir)}}, nil
	case sslQconvdir:
		for _, f := range sslconvfiles {
			if f.name == name {
				return Walked{Qid: sslfile(sslconv(c.Qid), f.t)}, nil
			}
		}
		return Walked{}, Error(abi.Enonexist)
	}
	return Walked{}, Error(abi.Enotdir)
}

func (d *DevSSL) owner(n int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n < len(d.ds) && d.ds[n] != nil {
		return d.ds[n].user
	}
	return d.k.Owner
}

func (d *DevSSL) dir(q styx.Qid, name string, user string, perm uint32) styx.Dir {
	return styx.Dir{Type: 'D', Qid: q, Mode: perm, Name: name, UID: user, GID: user}
}

func (d *DevSSL) gen(q styx.Qid) styx.Dir {
	n := sslconv(q)
	switch t := ssltype(q); t {
	case sslQtopdir:
		return d.dir(q, ".", d.k.Owner, styx.DMDIR|0o555)
	case sslQclonus:
		return d.dir(q, "clone", d.k.Owner, 0o666)
	case sslQconvdir:
		return d.dir(q, strconv.Itoa(n), d.owner(n), styx.DMDIR|0o555)
	default:
		for _, f := range sslconvfiles {
			if f.t == t {
				return d.dir(q, f.name, d.owner(n), 0o660)
			}
		}
	}
	return styx.Dir{}
}

func (d *DevSSL) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	return d.gen(c.Qid), nil
}

func (d *DevSSL) DirRead(_ context.Context, c *Chan) ([]styx.Dir, error) {
	var out []styx.Dir
	switch ssltype(c.Qid) {
	case sslQtopdir:
		d.mu.Lock()
		hi := d.hiwat
		d.mu.Unlock()
		for i := 0; i < hi; i++ {
			out = append(out, d.gen(styx.Qid{Type: styx.QTDIR, Path: sslqid(i, sslQconvdir)}))
		}
		out = append(out, d.gen(sslfile(0, sslQclonus)))
	case sslQconvdir:
		for _, f := range sslconvfiles {
			out = append(out, d.gen(sslfile(sslconv(c.Qid), f.t)))
		}
	}
	return out, nil
}

// dsnew makes conversation n; d.mu is held.
func (d *DevSSL) dsnew(ctx context.Context, n int) *dstate {
	for len(d.ds) <= n {
		d.ds = append(d.ds, nil)
	}
	s := &dstate{state: sIncomplete, ref: 1, user: d.user(ctx), perm: 0o660}
	d.ds[n] = s
	if n >= d.hiwat {
		d.hiwat = n + 1
	}
	return s
}

func (d *DevSSL) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	omode := mode & 3
	perm := [4]uint32{4, 2, 6, 0}[omode]
	switch t := ssltype(c.Qid); t {
	case sslQtopdir, sslQconvdir, sslQencalgs, sslQhashalgs:
		if omode != abi.OREAD {
			return nil, Error(abi.Eperm)
		}
	case sslQclonus:
		d.mu.Lock()
		n := 0
		for n < len(d.ds) && d.ds[n] != nil {
			n++
		}
		if n >= maxdstate {
			d.mu.Unlock()
			return nil, Error(abi.Enodev)
		}
		d.dsnew(ctx, n)
		d.mu.Unlock()
		c.Qid = sslfile(n, sslQctl)
	default:
		n := sslconv(c.Qid)
		d.mu.Lock()
		defer d.mu.Unlock()
		var s *dstate
		if n < len(d.ds) {
			s = d.ds[n]
		}
		if s == nil {
			d.dsnew(ctx, n)
			break
		}
		if perm&(s.perm>>6) != perm && (d.user(ctx) != s.user || perm&s.perm != perm) {
			return nil, Error(abi.Eperm)
		}
		s.ref++
	}
	return c, nil
}

func (d *DevSSL) Wstat(ctx context.Context, c *Chan, dir *styx.Dir) error {
	s := d.conv(c.Qid)
	if s == nil {
		return Error(abi.Ebadusefd)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.user != d.user(ctx) {
		return Error(abi.Eperm)
	}
	if dir.UID != "" {
		s.user = dir.UID
	}
	if !NullMode(dir) {
		s.perm = dir.Mode
	}
	return nil
}

func (d *DevSSL) Close(c *Chan) {
	switch ssltype(c.Qid) {
	case sslQctl, sslQdata, sslQsecretin, sslQsecretout:
	default:
		return
	}
	n := sslconv(c.Qid)
	d.mu.Lock()
	if n >= len(d.ds) || d.ds[n] == nil {
		d.mu.Unlock()
		return
	}
	s := d.ds[n]
	if s.ref--; s.ref > 0 {
		d.mu.Unlock()
		return
	}
	d.ds[n] = nil
	d.mu.Unlock()
	s.hangup()
	if s.c != nil {
		s.c.DecRef()
	}
}

func (s *dstate) hangup() {
	s.inq.Lock()
	s.processed, s.unprocessed = nil, nil
	s.state = sIncomplete
	s.inq.Unlock()
}

func (d *DevSSL) Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	switch ssltype(c.Qid) {
	case sslQctl:
		return ReadStr([]byte(strconv.Itoa(sslconv(c.Qid))), buf, off), nil
	case sslQencalgs:
		return ReadStr([]byte(sslencalgs), buf, off), nil
	case sslQhashalgs:
		return ReadStr([]byte(sslhashalgs), buf, off), nil
	case sslQdata:
		s := d.conv(c.Qid)
		if s == nil {
			return 0, Error(abi.Ebadusefd)
		}
		return s.read(ctx, buf)
	}
	return 0, Error(abi.Ebadusefd)
}

// ensure reads from the pushed fd until n bytes are unprocessed.
func (s *dstate) ensure(ctx context.Context, n int) error {
	tmp := make([]byte, maxdmsg)
	for len(s.unprocessed) < n {
		k, err := s.c.Dev.Read(ctx, s.c, tmp, s.c.Offset())
		if err != nil {
			return err
		}
		if k <= 0 {
			return Error(abi.Ehungup)
		}
		s.c.addOffset(int64(k))
		s.unprocessed = append(s.unprocessed, tmp[:k]...)
	}
	return nil
}

// read returns data from the current record, reading the next one when
// it is used up. A record is consumed only once all of it has arrived,
// so an interrupted read leaves the stream intact.
func (s *dstate) read(ctx context.Context, buf []byte) (int, error) {
	s.inq.Lock()
	defer s.inq.Unlock()
	if s.state == sIncomplete {
		return 0, Error(abi.Ebadusefd)
	}
	if len(s.processed) == 0 {
		if err := s.readrecord(ctx); err != nil {
			if !Interrupted(ctx) {
				s.processed, s.unprocessed = nil, nil
				s.state = sIncomplete
			}
			return 0, err
		}
	}
	n := copy(buf, s.processed)
	s.processed = s.processed[n:]
	return n, nil
}

func (s *dstate) readrecord(ctx context.Context) error {
	if err := s.ensure(ctx, 2); err != nil {
		return err
	}
	var l, hdr, pad int
	if s.unprocessed[0]&0x80 != 0 {
		l, hdr = int(s.unprocessed[0]&0x7f)<<8|int(s.unprocessed[1]), 2
	} else {
		l, hdr = int(s.unprocessed[0]&0x3f)<<8|int(s.unprocessed[1]), 3
	}
	if err := s.ensure(ctx, hdr+l); err != nil {
		return err
	}
	if hdr == 3 {
		pad = int(s.unprocessed[2])
		if pad > l {
			return Error("bad pad in ssl message")
		}
	}
	rec := append([]byte(nil), s.unprocessed[hdr:hdr+l]...)
	s.unprocessed = s.unprocessed[hdr+l:]

	s.inctl.Lock()
	defer s.inctl.Unlock()
	switch s.state {
	case sEncrypting:
		if err := s.decrypt(rec); err != nil {
			return err
		}
	case sDigesting:
		if len(rec) < s.diglen {
			return Error("ssl message too short")
		}
		if err := s.checkdigest(rec); err != nil {
			return err
		}
		rec = rec[s.diglen:]
	case sDigenc:
		if err := s.decrypt(rec); err != nil {
			return err
		}
		if len(rec) < s.diglen {
			return Error("ssl message too short")
		}
		if err := s.checkdigest(rec); err != nil {
			return err
		}
		rec = rec[s.diglen:]
		l -= s.diglen
	}
	s.in.mid++
	if pad > 0 && l-pad >= 0 && l-pad < len(rec) {
		rec = rec[:l-pad]
	}
	s.processed = rec
	return nil
}

func (d *DevSSL) Write(ctx context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	s := d.conv(c.Qid)
	if s == nil {
		return 0, Error(abi.Ebadusefd)
	}
	t := ssltype(c.Qid)
	if t == sslQdata {
		s.outq.Lock()
		defer s.outq.Unlock()
		if s.state == sIncomplete {
			return 0, Error(abi.Ebadusefd)
		}
		for p := buf; ; {
			m := min(len(p), s.max)
			if err := s.writerecord(ctx, p[:m]); err != nil {
				s.hangup()
				return 0, err
			}
			p = p[m:]
			if len(p) == 0 {
				break
			}
		}
		return len(buf), nil
	}

	s.inctl.Lock()
	defer s.inctl.Unlock()
	s.outq.Lock()
	defer s.outq.Unlock()
	switch t {
	case sslQsecretin:
		s.in.setsecret(buf)
		return len(buf), nil
	case sslQsecretout:
		s.out.setsecret(buf)
		return len(buf), nil
	case sslQctl:
	default:
		return 0, Error(abi.Ebadusefd)
	}
	if len(buf) >= 32 {
		return 0, Error(abi.Ebadarg)
	}
	cmd := string(buf)
	if i := strings.IndexByte(cmd, '\n'); i >= 0 {
		cmd = cmd[:i]
	}
	cmd, arg, hasarg := strings.Cut(cmd, " ")
	switch {
	case cmd == "fd":
		nc, err := d.fdchan(ctx, arg, hasarg)
		if err != nil {
			return 0, err
		}
		if s.c != nil {
			s.c.DecRef()
		}
		s.c = nc
		s.state = sClear
		s.blocklen, s.diglen = 1, 0
		s.max = 1<<15 - 1
		s.maxpad = s.max
		s.in.mid, s.out.mid = 0, 0
	case cmd == "alg" && hasarg:
		if err := s.setalg(arg); err != nil {
			return 0, err
		}
	case (cmd == "secretin" || cmd == "secretout") && hasarg:
		w := &s.in
		if cmd == "secretout" {
			w = &s.out
		}
		w.setsecret(dec64(arg))
	default:
		return 0, Error(abi.Ebadarg)
	}
	return len(buf), nil
}

// fdchan is buftochan: the Chan open on the writer's fd.
func (d *DevSSL) fdchan(ctx context.Context, arg string, hasarg bool) (*Chan, error) {
	if !hasarg {
		return nil, Error(abi.Ebadarg)
	}
	p := ProgFrom(ctx)
	if p == nil {
		return nil, Error(abi.Ebadusefd)
	}
	fd, err := strconv.ParseInt(strings.TrimSpace(arg), 0, 32)
	if err != nil || fd < 0 {
		return nil, Error(abi.Ebadarg)
	}
	c, err := fdtochan(p.Fgrp(), int(fd), -1)
	if err != nil {
		return nil, err
	}
	return c.IncRef(), nil
}

// setalg handles "alg clear" and "alg name[ name]...", names separated
// by spaces or a slash.
func (s *dstate) setalg(p string) error {
	s.blocklen, s.diglen = 1, 0
	if s.c == nil {
		return Error("must set fd before algorithm")
	}
	if p == "clear" {
		s.state = sClear
		s.max = 1<<15 - 1
		s.maxpad = s.max
		return nil
	}
	if s.in.secret != nil && s.out.secret == nil {
		s.out.setsecret(s.in.secret)
	}
	if s.out.secret != nil && s.in.secret == nil {
		s.in.setsecret(s.out.secret)
	}
	if s.in.secret == nil || s.out.secret == nil {
		return Error("algorithm but no secret")
	}
	// devssl.c keeps the previous state's bits here, so a digest
	// dropped by a new alg message is still applied with no hash.
	s.hf, s.encalg, s.state = nil, "", sClear
	for {
		name, rest, more := strings.Cut(p, " ")
		if !more {
			name, rest, more = strings.Cut(p, "/")
		}
		if err := s.parsealg(name); err != nil {
			return err
		}
		if !more {
			break
		}
		p = rest
	}
	if s.hf == nil && s.encalg == "" {
		return Error(abi.Ebadarg)
	}
	if s.blocklen != 1 {
		s.max = 1<<15 - s.diglen - 1
		s.max -= s.max % s.blocklen
		s.maxpad = 1<<14 - s.diglen - 1
		s.maxpad -= s.maxpad % s.blocklen
	} else {
		s.max = 1<<15 - s.diglen - 1
		s.maxpad = s.max
	}
	return nil
}

func (s *dstate) parsealg(name string) error {
	for _, h := range sslhashtab {
		if h.name == name {
			s.hf, s.diglen = h.hf, h.diglen
			s.state = s.state&^sClear | sDigesting
			return nil
		}
	}
	for _, e := range sslenctab {
		if e.name == name {
			s.encalg, s.blocklen = e.alg, e.blocklen
			if err := e.keyinit(&s.in); err != nil {
				return err
			}
			if err := e.keyinit(&s.out); err != nil {
				return err
			}
			s.state = s.state&^sClear | sEncrypting
			return nil
		}
	}
	return Error(abi.Ebadarg)
}

// writerecord sends one record: a two-byte count with the top bit set,
// or a three-byte count and pad length when the data is padded to the
// cipher's block size, then the digest, the data and the pad.
func (s *dstate) writerecord(ctx context.Context, data []byte) error {
	m := len(data)
	h := s.diglen + 2
	pad := 0
	if s.blocklen != 1 {
		pad = (m + s.diglen) % s.blocklen
		if pad != 0 {
			if m > s.maxpad {
				// Too long for a padded record: send unpadded what
				// fills whole blocks and leave the rest. (devssl.c sends
				// maxpad bytes, which leaves an encrypted record that is
				// not a whole number of blocks when the digest length is
				// not a multiple of the block size.)
				m -= pad
				pad = 0
			} else {
				pad = s.blocklen - pad
				h++
			}
		}
	}
	rec := make([]byte, h+m+pad)
	copy(rec[h:], data[:m])
	for i := h + m; i < len(rec); i++ {
		rec[i] = byte(rand.N(256))
	}
	n := m + s.diglen + pad
	off := 2
	if pad != 0 {
		rec[0], rec[1], rec[2] = byte(n>>8), byte(n), byte(pad)
		off = 3
	} else {
		rec[0], rec[1] = byte(n>>8)|0x80, byte(n)
	}
	if s.state&sDigesting != 0 {
		s.digest(rec, off)
	}
	if s.state&sEncrypting != 0 {
		s.encrypt(rec[off:])
	}
	s.out.mid++
	for len(rec) > 0 {
		k, err := s.c.Dev.Write(ctx, s.c, rec, s.c.Offset())
		if err != nil {
			return err
		}
		if k <= 0 {
			return Error(abi.Ehungup)
		}
		s.c.addOffset(int64(k))
		rec = rec[k:]
	}
	if m < len(data) {
		return s.writerecord(ctx, data[m:])
	}
	return nil
}

func msgid(mid uint32) []byte { return binary.BigEndian.AppendUint32(nil, mid) }

// digest fills in the digest of the secret, the data and pad after it,
// and the message id.
func (s *dstate) digest(rec []byte, off int) {
	h := s.hf()
	h.Write(s.out.secret)
	h.Write(rec[off+s.diglen:])
	h.Write(msgid(s.out.mid))
	copy(rec[off:], h.Sum(nil))
}

func (s *dstate) checkdigest(rec []byte) error {
	h := s.hf()
	h.Write(s.in.secret)
	h.Write(rec[s.diglen:])
	h.Write(msgid(s.in.mid))
	sum := h.Sum(nil)
	for i := 0; i < s.diglen; i++ {
		if sum[i] != rec[i] {
			return Error("bad digest")
		}
	}
	return nil
}

func (s *dstate) encrypt(p []byte) {
	w := &s.out
	switch s.encalg {
	case "rc4":
		w.rc4.XOR(p)
	case "descbc", "ideacbc":
		for ; len(p) >= 8; p = p[8:] {
			for i := 0; i < 8; i++ {
				p[i] ^= w.ivec[i]
			}
			w.block.Encrypt(p, p)
			copy(w.ivec, p[:8])
		}
	case "desecb", "ideaecb":
		for ; len(p) >= 8; p = p[8:] {
			w.block.Encrypt(p, p)
		}
	}
}

func (s *dstate) decrypt(p []byte) error {
	w := &s.in
	if s.blocklen > 1 && len(p)%s.blocklen != 0 {
		return Error("ssl encrypted message too short")
	}
	switch s.encalg {
	case "rc4":
		w.rc4.XOR(p)
	case "descbc", "ideacbc":
		var tmp [8]byte
		for ; len(p) >= 8; p = p[8:] {
			copy(tmp[:], p[:8])
			w.block.Decrypt(p, p)
			for i := 0; i < 8; i++ {
				p[i] ^= w.ivec[i]
			}
			copy(w.ivec, tmp[:])
		}
	case "desecb", "ideaecb":
		for ; len(p) >= 8; p = p[8:] {
			w.block.Decrypt(p, p)
		}
	}
	return nil
}

// dec64 is lib9's dec64, for the secretin and secretout ctl messages.
func dec64(in string) []byte {
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out []byte
	var v uint32
	n := 0
	for i := 0; i < len(in); i++ {
		c := strings.IndexByte(alpha, in[i])
		if c < 0 {
			continue
		}
		v = v<<6 | uint32(c)
		if n++; n == 4 {
			out = append(out, byte(v>>16), byte(v>>8), byte(v))
			v, n = 0, 0
		}
	}
	switch n {
	case 2:
		out = append(out, byte(v>>4))
	case 3:
		out = append(out, byte(v>>10), byte(v>>2))
	}
	return out
}
