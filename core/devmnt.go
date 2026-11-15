package core

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/styx"
)

// maxrpc is the largest message a mount asks for, as Inferno's MAXRPC.
const maxrpc = 8192 + styx.IOHDRSZ

// Mnt is one 9P conversation on a Chan, Inferno's Mnt. Requests from many
// progs are multiplexed by tag; as in devmnt.c, there is no reader
// process: one waiting prog at a time reads replies and hands each to the
// prog whose tag it carries.
type Mnt struct {
	c       *Chan
	msize   uint32
	version string

	wmu sync.Mutex // one message written at a time

	mu    sync.Mutex
	queue map[uint16]*mntrpc
	rip   *mntrpc // the rpc whose prog is reading
	ripr  Rendez  // woken when rip clears
	buf   []byte  // bytes read but not yet a whole message
	dead  error
	tag   uint16

	fid atomic.Uint32

	// Records, after a version 3 or 4 proof (docs/auth.md). seal is used
	// under wmu; open by the one prog reading. pending is installed as
	// open when the reply with tag swtag arrives.
	seal    *pgauth.Sealer
	open    *pgauth.Opener
	pending *pgauth.Opener
	swtag   uint16

	d     *DevMnt
	nfids atomic.Int32 // live mntfids; the last one releases c
	// holds counts the name space entries, current directories, open
	// files and exports that refer to the conversation (Holder); the
	// last to go ends it, as Inferno's last cclose of a #M Chan does.
	holds  atomic.Int32
	closed atomic.Bool
}

type mntrpc struct {
	tag     uint16
	reply   *styx.Fcall
	done    bool
	orphan  bool // the prog gave up (killed); drop the reply
	r       Rendez
	flushed *mntrpc
}

// mntfid is a #M Chan's Aux: a fid on a conversation. Clones of an
// unopened Chan share it and only walk or stat through it; opens,
// creates and removes use a fresh fid cloned from it. It is clunked when
// no Chan refers to it any more.
type mntfid struct {
	m    *Mnt
	fid  uint32
	root bool   // the fid an attach made
	src  string // name of the conversation's file, for ns
	spec string
	// open is set when the fid itself is open (after create).
	open bool
	// cleanup clunks the fid when no Chan refers to it any more; an
	// explicit clunk stops it, so a fid is clunked once.
	cleanup runtime.Cleanup
	clunked atomic.Bool
	// auth is set on an auth fid (Fauth).
	auth *mntAuth
}

func (m *Mnt) newfid(fid uint32, root bool, src, spec string) *mntfid {
	f := &mntfid{m: m, fid: fid, root: root, src: src, spec: spec}
	m.nfids.Add(1)
	f.cleanup = runtime.AddCleanup(f, func(x struct {
		m   *Mnt
		fid uint32
	}) {
		go func() {
			x.m.clunk(context.Background(), x.fid)
			x.m.unref()
		}()
	}, struct {
		m   *Mnt
		fid uint32
	}{m, fid})
	return f
}

// clunkNow clunks f before returning, unless it already was, and stops
// its cleanup.
func (f *mntfid) clunkNow(ctx context.Context) {
	if !f.clunked.CompareAndSwap(false, true) {
		return
	}
	f.cleanup.Stop()
	f.m.clunk(ctx, f.fid)
	f.m.unref()
}

func (m *Mnt) clunk(ctx context.Context, fid uint32) {
	_, _ = m.rpc(ctx, &styx.Fcall{Type: styx.Tclunk, Fid: fid})
}

func (m *Mnt) allocfid() uint32 { return m.fid.Add(1) }

// unref drops a mntfid's reference. When none is left the conversation
// ends. This catches a conversation that nothing held (a mount that
// failed after its attach, or a name space never released): its fids
// go with the garbage collector.
func (m *Mnt) unref() {
	if m.nfids.Add(-1) == 0 {
		m.hangup(context.Background())
	}
}

func (m *Mnt) hold() { m.holds.Add(1) }

func (m *Mnt) unhold(ctx context.Context) {
	if m.holds.Add(-1) == 0 {
		m.hangup(ctx)
	}
}

// hangupWait bounds how long hangup waits for the replies to requests
// still outstanding.
const hangupWait = 5 * time.Second

// hangup ends the conversation: the mount driver forgets it and lets go
// of its Chan, so the server sees its client hang up once no one else
// has that file open, as muxclose and the cclose of the Mnt's Chan do in
// Inferno. Inferno closes a conversation only when no request on it is
// outstanding; here a request can be (a clunk the garbage collector
// sent, a request a killed prog left), so hangup first reads the replies
// still to come, and the server never writes into a closed pipe. The wait
// ends early if the prog behind ctx is killed, and after hangupWait.
// Requests made after the hangup fail.
func (m *Mnt) hangup(ctx context.Context) {
	if m.d == nil || !m.closed.CompareAndSwap(false, true) {
		return
	}
	m.drain(ctx)
	m.fail(Error(abi.Ehungup))
	m.d.mu.Lock()
	if m.d.conns[m.c] == m {
		delete(m.d.conns, m.c)
	}
	m.d.mu.Unlock()
	m.c.DecRef()
}

// drain reads replies until no request is outstanding, the conversation
// fails, the prog behind ctx is killed or hangupWait passes.
func (m *Mnt) drain(ctx context.Context) {
	n := NewNote()
	dctx := WithNote(context.Background(), n)
	stop := make(chan struct{})
	defer close(stop)
	var dead <-chan struct{}
	if cn := NoteFrom(ctx); cn != nil {
		dead = cn.Dead()
	}
	t := time.NewTimer(hangupWait)
	defer t.Stop()
	go func() {
		select {
		case <-t.C:
		case <-dead:
		case <-stop:
			return
		}
		n.Kill()
	}()
	for {
		m.mu.Lock()
		if len(m.queue) == 0 || m.dead != nil {
			m.mu.Unlock()
			return
		}
		if m.rip == nil {
			// Read for everyone, as a waiting prog would (mntgate).
			m.rip = &mntrpc{}
			m.mu.Unlock()
			f, err := m.readMsg(dctx)
			m.mu.Lock()
			m.rip = nil
			for _, q := range m.queue {
				q.r.Wakeup()
			}
			m.mu.Unlock()
			m.ripr.Wakeup()
			if err != nil {
				return
			}
			m.mux(f)
			continue
		}
		m.mu.Unlock()
		err := Park(dctx, &m.ripr, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.rip == nil || len(m.queue) == 0 || m.dead != nil
		})
		if err != nil {
			return
		}
	}
}

// writeMsg sends one message.
func (m *Mnt) writeMsg(ctx context.Context, f *styx.Fcall) error {
	return m.writeMsgSwitch(ctx, f, nil)
}

// writeMsgSwitch sends one message, sealed once the conversation has
// switched to records; with sw it sends it in the clear and switches
// after it.
func (m *Mnt) writeMsgSwitch(ctx context.Context, f *styx.Fcall, sw *mntSwitch) error {
	b, err := styx.Marshal(nil, f)
	if err != nil {
		return Error(abi.Emountrpc)
	}
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if m.seal != nil {
		if b, err = m.seal.Seal(nil, b); err != nil {
			return err
		}
	}
	if sw != nil && m.seal == nil {
		defer func() { m.seal = sw.seal }()
	}
	for len(b) > 0 {
		n, err := m.c.Dev.Write(ctx, m.c, b, m.c.Offset())
		if err != nil {
			return err
		}
		if n <= 0 {
			return Error(abi.Emountrpc)
		}
		b = b[n:]
	}
	return nil
}

// readMsg reads one whole message from the conversation, keeping partial
// bytes in m.buf across interrupted reads.
func (m *Mnt) readMsg(ctx context.Context) (*styx.Fcall, error) {
	tmp := make([]byte, m.msize+pgauth.RecordOverhead)
	for {
		m.mu.Lock()
		if m.open != nil && len(m.buf) >= pgauth.RecordHeader {
			n, err := pgauth.RecordSize(m.buf, int(m.msize))
			if err != nil {
				m.mu.Unlock()
				return nil, err
			}
			if n <= len(m.buf) {
				rec := append([]byte(nil), m.buf[:n]...)
				m.buf = append([]byte(nil), m.buf[n:]...)
				msg, err := m.open.Open(rec)
				m.mu.Unlock()
				if err != nil {
					return nil, err
				}
				f, err := styx.Unmarshal(msg, m.msize)
				if err != nil {
					return nil, Error(abi.Emountrpc)
				}
				return f, nil
			}
		} else if n := styx.Size(m.buf); m.open == nil && n != 0 && int(n) <= len(m.buf) {
			msg := m.buf[:n]
			m.buf = append([]byte(nil), m.buf[n:]...)
			f, err := styx.Unmarshal(msg, m.msize)
			if err == nil && m.pending != nil && f.Tag == m.swtag {
				// The proof's reply: records from here on.
				m.open, m.pending = m.pending, nil
			}
			m.mu.Unlock()
			if err != nil {
				return nil, Error(abi.Emountrpc)
			}
			return f, nil
		}
		m.mu.Unlock()
		k, err := m.c.Dev.Read(ctx, m.c, tmp, m.c.Offset())
		if err != nil {
			return nil, err
		}
		if k == 0 {
			return nil, Error(abi.Ehungup)
		}
		m.mu.Lock()
		m.buf = append(m.buf, tmp[:k]...)
		m.mu.Unlock()
	}
}

// fail ends the conversation: every waiting rpc gets err.
func (m *Mnt) fail(err error) {
	m.mu.Lock()
	if m.dead == nil {
		m.dead = err
	}
	for _, r := range m.queue {
		r.done = true
		r.r.Wakeup()
	}
	m.mu.Unlock()
}

func (m *Mnt) alloctag() *mntrpc {
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		m.tag++
		if m.tag == styx.NOTAG {
			m.tag = 0
		}
		if _, used := m.queue[m.tag]; !used {
			r := &mntrpc{tag: m.tag}
			m.queue[m.tag] = r
			return r
		}
	}
}

// mountio sends f and waits for its reply, reading replies for everyone
// while no one else is (mntgate). When the prog is interrupted it flushes
// the request; when killed it abandons it.
func (m *Mnt) mountio(ctx context.Context, f *styx.Fcall) (*styx.Fcall, error) {
	return m.mountioSwitch(ctx, f, nil)
}

func (m *Mnt) mountioSwitch(ctx context.Context, f *styx.Fcall, sw *mntSwitch) (*styx.Fcall, error) {
	m.mu.Lock()
	if m.dead != nil {
		dead := m.dead
		m.mu.Unlock()
		return nil, deadErr(dead)
	}
	m.mu.Unlock()
	r := m.alloctag()
	f.Tag = r.tag
	if sw != nil {
		m.mu.Lock()
		if m.open == nil && m.pending == nil {
			m.pending, m.swtag = sw.open, r.tag
			m.c.encrypted.Store(true)
		} else {
			sw = nil // a conversation switches once
		}
		m.mu.Unlock()
	}
	if err := m.writeMsgSwitch(ctx, f, sw); err != nil {
		m.mu.Lock()
		delete(m.queue, r.tag)
		m.mu.Unlock()
		return nil, Error(abi.Emountrpc)
	}
	if err := m.wait(ctx, r); err != nil {
		return nil, m.flush(ctx, r, err)
	}
	m.mu.Lock()
	reply, dead := r.reply, m.dead
	m.mu.Unlock()
	if reply == nil {
		if dead != nil {
			return nil, deadErr(dead)
		}
		return nil, Error(abi.Emountrpc)
	}
	return reply, nil
}

// deadErr is what requests on a dead conversation fail with: the record
// layer's error when that is what ended it, else a mount rpc error.
func deadErr(dead error) error {
	if _, ok := dead.(pgauth.Err); ok {
		return dead
	}
	return Error(abi.Emountrpc)
}

// wait waits for r's reply, reading for everyone when nobody else is.
func (m *Mnt) wait(ctx context.Context, r *mntrpc) error {
	for {
		m.mu.Lock()
		if r.done {
			m.mu.Unlock()
			return nil
		}
		if m.rip == nil {
			m.rip = r
			m.mu.Unlock()
			err := m.readFor(ctx, r)
			m.mu.Lock()
			m.rip = nil
			// Hand the gate to the next waiter.
			for _, q := range m.queue {
				q.r.Wakeup()
			}
			m.mu.Unlock()
			m.ripr.Wakeup()
			if err != nil {
				return err
			}
			continue
		}
		m.mu.Unlock()
		err := Park(ctx, &r.r, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return r.done || m.rip == nil
		})
		if err != nil {
			return err
		}
	}
}

// readFor reads and dispatches replies until r's arrives.
func (m *Mnt) readFor(ctx context.Context, r *mntrpc) error {
	for {
		m.mu.Lock()
		done := r.done
		m.mu.Unlock()
		if done {
			return nil
		}
		f, err := m.readMsg(ctx)
		if err != nil {
			if Errstr(err) == abi.Eintr {
				return err
			}
			m.fail(err)
			return nil
		}
		m.mux(f)
	}
}

// mux delivers a reply to the rpc with its tag.
func (m *Mnt) mux(f *styx.Fcall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q, ok := m.queue[f.Tag]
	if !ok {
		return
	}
	delete(m.queue, f.Tag)
	if len(m.queue) == 0 {
		m.ripr.Wakeup() // a hangup may wait for this
	}
	q.reply = f
	q.done = true
	// A flush's reply finishes the request it flushed.
	if q.flushed != nil {
		if o, ok := m.queue[q.flushed.tag]; ok && o == q.flushed {
			delete(m.queue, o.tag)
			o.done = true
			o.r.Wakeup()
		}
	}
	q.r.Wakeup()
}

// flush sends Tflush for r after its prog was interrupted or killed. An
// interrupted prog waits for the Rflush (or for r's own reply); a killed
// one leaves it.
func (m *Mnt) flush(ctx context.Context, r *mntrpc, why error) error {
	n := NoteFrom(ctx)
	killed := n != nil && n.Killed()
	fr := m.alloctag()
	fr.flushed = r
	if err := m.writeMsg(context.Background(), &styx.Fcall{Type: styx.Tflush, Tag: fr.tag, Oldtag: r.tag}); err != nil {
		m.fail(Error(abi.Emountrpc))
		return why
	}
	if killed {
		m.mu.Lock()
		r.orphan, fr.orphan = true, true
		m.mu.Unlock()
		return why
	}
	// Wait for the flush without being interrupted again; a kill still
	// ends the wait.
	kctx := context.Background()
	if n != nil {
		nn := &Note{killed: n.killed}
		kctx = WithNote(kctx, nn)
	}
	_ = m.wait(kctx, fr)
	return why
}

// rpc runs one request and checks the reply type.
func (m *Mnt) rpc(ctx context.Context, f *styx.Fcall) (*styx.Fcall, error) {
	return m.rpcSwitch(ctx, f, nil)
}

// rpcSwitch is rpc that switches the conversation to records at f.
func (m *Mnt) rpcSwitch(ctx context.Context, f *styx.Fcall, sw *mntSwitch) (*styx.Fcall, error) {
	t := f.Type
	r, err := m.mountioSwitch(ctx, f, sw)
	if err != nil {
		return nil, err
	}
	switch r.Type {
	case styx.Rerror:
		return nil, Error(r.Ename)
	case styx.Rflush:
		return nil, Error(abi.Eintr)
	case t + 1:
		return r, nil
	}
	return nil, Error(abi.Emountrpc)
}

// DevMnt is #M, the mount driver (devmnt.c): it turns a 9P conversation on
// a file into files. Mounts reach it through Prog.Mount, never by name.
type DevMnt struct {
	DevBase
	mu    sync.Mutex
	conns map[*Chan]*Mnt
	devno atomic.Uint32
}

func NewDevMnt() *DevMnt { return &DevMnt{conns: map[*Chan]*Mnt{}} }

func (d *DevMnt) DC() rune     { return 'M' }
func (d *DevMnt) Name() string { return "mnt" }

func (d *DevMnt) Attach(context.Context, string) (Walked, error) {
	return Walked{}, Error(abi.Enoattach)
}

// Version exchanges Tversion on c and makes the conversation, as
// mntversion does; a conversation already made is reused.
func (d *DevMnt) Version(ctx context.Context, c *Chan, version string, msize uint32) (*Mnt, error) {
	d.mu.Lock()
	if m, ok := d.conns[c]; ok {
		d.mu.Unlock()
		if version != "" && len(version) >= len(m.version) && version[:len(m.version)] != m.version {
			return nil, Error("incompatible 9P versions " + m.version + " " + version)
		}
		return m, nil
	}
	d.mu.Unlock()
	if msize == 0 {
		msize = maxrpc
	}
	if iu, ok := c.Dev.(Iouniter); ok {
		if n := iu.Iounit(c); n != 0 && msize > n {
			msize = n
		}
	}
	if version == "" {
		version = styx.Version
	}
	if len(version) < len(styx.Version) || version[:len(styx.Version)] != styx.Version {
		return nil, Error("bad 9P version specification")
	}
	if c.encrypted.Load() {
		// The conversation went to records and its session has ended:
		// a Tversion in the clear would read as a broken record.
		return nil, pgauth.EEncrypted
	}
	m := &Mnt{c: c, msize: msize, queue: map[uint16]*mntrpc{}}
	if err := m.writeMsg(ctx, &styx.Fcall{Type: styx.Tversion, Tag: styx.NOTAG, Msize: msize, Version: version}); err != nil {
		return nil, Error("short write in fversion")
	}
	f, err := m.readMsg(ctx)
	if err != nil {
		return nil, Error("EOF receiving fversion reply")
	}
	switch {
	case f.Type == styx.Rerror:
		return nil, Error(f.Ename)
	case f.Type != styx.Rversion:
		return nil, Error("unexpected reply type in fversion")
	case f.Msize > msize:
		return nil, Error("server tries to increase msize in fversion")
	case f.Msize < 256 || f.Msize > 1024*1024:
		return nil, Error("nonsense value of msize in fversion")
	case len(f.Version) > len(version) || version[:len(f.Version)] != f.Version:
		return nil, Error("bad 9P version returned from server")
	}
	m.msize, m.version = f.Msize, f.Version
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.conns[c]; ok {
		return old, nil
	}
	// The conversation holds its Chan, as mntversion increfs it: the fd
	// it came from may be closed while mounts still use it.
	m.d = d
	c.IncRef()
	d.conns[c] = m
	return m, nil
}

// MountAttach attaches to the server on c as user with attach name spec,
// authenticated by afid when it is not nil, and returns the root Chan.
func (d *DevMnt) MountAttach(ctx context.Context, c *Chan, afid *Chan, user, spec string) (*Chan, error) {
	m, err := d.Version(ctx, c, "", 0)
	if err != nil {
		return nil, err
	}
	fid := m.allocfid()
	af := styx.NOFID
	if afid == nil && c.encrypted.Load() {
		// Only an auth fd of this session can attach on it.
		return nil, pgauth.EEncrypted
	}
	if afid != nil {
		mf, ok := afid.Aux.(*mntfid)
		if !ok || mf.m != m {
			return nil, Error(abi.Ebadusefd)
		}
		af = mf.fid
	}
	r, err := m.rpc(ctx, &styx.Fcall{Type: styx.Tattach, Fid: fid, Afid: af, Uname: user, Aname: spec})
	if err != nil {
		return nil, err
	}
	aux := m.newfid(fid, true, c.Name, spec)
	return NewChan(d, d.devnoOf(m), r.Qid, "#M", aux), nil
}

// Auth starts authentication with Tauth and returns the auth file.
func (d *DevMnt) Auth(ctx context.Context, c *Chan, user, spec string) (*Chan, error) {
	m, err := d.Version(ctx, c, "", 0)
	if err != nil {
		return nil, err
	}
	fid := m.allocfid()
	r, err := m.rpc(ctx, &styx.Fcall{Type: styx.Tauth, Afid: fid, Uname: user, Aname: spec})
	if err != nil {
		return nil, err
	}
	aux := m.newfid(fid, false, c.Name, spec)
	aux.open = true
	aux.auth = &mntAuth{uname: user, aname: spec}
	ac := NewChan(d, d.devnoOf(m), r.Qid, "#M", aux)
	ac.Mode, ac.Open = abi.ORDWR, true
	ac.State = aux
	m.hold() // until the auth file is closed
	return ac, nil
}

var mntdevs sync.Map // *Mnt -> uint32

func (d *DevMnt) devnoOf(m *Mnt) uint32 {
	if v, ok := mntdevs.Load(m); ok {
		return v.(uint32)
	}
	n := d.devno.Add(1)
	v, _ := mntdevs.LoadOrStore(m, n)
	return v.(uint32)
}

func mfid(c *Chan) *mntfid { return c.Aux.(*mntfid) }

// opened is the fid for I/O on an open Chan.
func opened(c *Chan) *mntfid {
	if f, ok := c.State.(*mntfid); ok {
		return f
	}
	return mfid(c)
}

func (d *DevMnt) Walk(ctx context.Context, c *Chan, name string) (Walked, error) {
	w, err := d.WalkNames(ctx, c, []string{name})
	if err != nil {
		return Walked{}, err
	}
	if !w.Reached {
		if w.Why != nil {
			return Walked{}, w.Why
		}
		return Walked{}, Error(abi.Enonexist)
	}
	return Walked{Qid: w.Qids[0], Aux: w.Aux}, nil
}

func (d *DevMnt) MaxWalk() int { return styx.MAXWELEM }

func (d *DevMnt) WalkNames(ctx context.Context, c *Chan, names []string) (WalkedNames, error) {
	f := mfid(c)
	m := f.m
	nf := m.allocfid()
	r, err := m.rpc(ctx, &styx.Fcall{Type: styx.Twalk, Fid: f.fid, Newfid: nf, Wname: names})
	if err != nil {
		return WalkedNames{}, err
	}
	if len(r.Wqid) == 0 {
		return WalkedNames{}, Error(abi.Enonexist)
	}
	if len(r.Wqid) < len(names) {
		// A short walk establishes no fid.
		return WalkedNames{Qids: r.Wqid}, nil
	}
	return WalkedNames{Qids: r.Wqid, Aux: m.newfid(nf, false, f.src, f.spec), Reached: true}, nil
}

func (d *DevMnt) fixdir(c *Chan, dir *styx.Dir) {
	dir.Type = 'M'
	dir.Dev = c.Devno
}

func (d *DevMnt) Stat(ctx context.Context, c *Chan) (styx.Dir, error) {
	f := opened(c)
	r, err := f.m.rpc(ctx, &styx.Fcall{Type: styx.Tstat, Fid: f.fid})
	if err != nil {
		return styx.Dir{}, err
	}
	dir, err := styx.UnmarshalDir(r.Stat)
	if err != nil {
		return styx.Dir{}, Error(abi.Ebadstat)
	}
	d.fixdir(c, dir)
	return *dir, nil
}

// clone makes a fresh fid for the same file.
func (f *mntfid) clone(ctx context.Context) (*mntfid, error) {
	nf := f.m.allocfid()
	if _, err := f.m.rpc(ctx, &styx.Fcall{Type: styx.Twalk, Fid: f.fid, Newfid: nf}); err != nil {
		return nil, err
	}
	return f.m.newfid(nf, false, f.src, f.spec), nil
}

// Hold and Unhold count the references to c's conversation (Holder).
func (d *DevMnt) Hold(c *Chan) {
	if f, ok := c.Aux.(*mntfid); ok {
		f.m.hold()
	}
}

func (d *DevMnt) Unhold(ctx context.Context, c *Chan) {
	if f, ok := c.Aux.(*mntfid); ok {
		f.m.unhold(ctx)
	}
}

func (d *DevMnt) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	f := mfid(c)
	if f.open {
		c.State = f
		f.m.hold()
		return c, nil
	}
	nf, err := f.clone(ctx)
	if err != nil {
		return nil, err
	}
	r, err := f.m.rpc(ctx, &styx.Fcall{Type: styx.Topen, Fid: nf.fid, Mode: uint8(mode)})
	if err != nil {
		return nil, err
	}
	nf.open = true
	c.Qid = r.Qid
	c.State = nf
	f.m.hold() // an open file holds the conversation until Close
	return c, nil
}

func (d *DevMnt) Create(ctx context.Context, dir *Chan, name string, perm uint32) (Walked, error) {
	f := mfid(dir)
	nf, err := f.clone(ctx)
	if err != nil {
		return Walked{}, err
	}
	// The core opens the result in the mode it was asked for; the create
	// opens it here, for reading and writing unless it is a directory.
	mode := uint8(abi.ORDWR)
	if perm&styx.DMDIR != 0 {
		mode = abi.OREAD
	}
	r, err := f.m.rpc(ctx, &styx.Fcall{Type: styx.Tcreate, Fid: nf.fid, Name: name, Perm: perm, Mode: mode})
	if err != nil {
		return Walked{}, err
	}
	nf.open = true
	return Walked{Qid: r.Qid, Aux: nf}, nil
}

func (d *DevMnt) Close(c *Chan) {
	// Clunk the open fid before returning, as mntclunk does: a server
	// may act on the clunk (ftpfs stores a written file), and what the
	// closing prog does next must see that. A created file's open fid
	// is its Aux too.
	if f, ok := c.State.(*mntfid); ok {
		f.clunkNow(context.Background())
		f.m.unhold(context.Background())
	}
}

func (d *DevMnt) Iounit(c *Chan) uint32 {
	return opened(c).m.msize - styx.IOHDRSZ
}

func (d *DevMnt) Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	f := opened(c)
	if f.auth != nil {
		if n, done, err := d.authRead(ctx, f, buf, off); done {
			return n, err
		}
	}
	return fidrdwr(ctx, f, buf, off, styx.Tread)
}

func (d *DevMnt) Write(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	f := opened(c)
	if f.auth != nil {
		if n, done, err := d.authWrite(ctx, f, buf, off); done {
			return n, err
		}
	}
	return fidrdwr(ctx, f, buf, off, styx.Twrite)
}

// fidrdwr splits I/O into messages of at most iounit bytes and stops at a
// short count, as mntrdwr does.
func fidrdwr(ctx context.Context, f *mntfid, buf []byte, off int64, t uint8) (int, error) {
	iounit := int(f.m.msize - styx.IOHDRSZ)
	done := 0
	for done < len(buf) || (len(buf) == 0 && done == 0) {
		n := min(len(buf)-done, iounit)
		req := &styx.Fcall{Type: t, Fid: f.fid, Offset: uint64(off) + uint64(done), Count: uint32(n)}
		if t == styx.Twrite {
			req.Data = buf[done : done+n]
		}
		r, err := f.m.rpc(ctx, req)
		if err != nil {
			if done > 0 {
				return done, nil
			}
			return 0, err
		}
		k := int(r.Count)
		if t == styx.Tread {
			k = copy(buf[done:], r.Data)
		}
		if k > n {
			return 0, Error(abi.Emountrpc)
		}
		done += k
		if k < n || len(buf) == 0 {
			break
		}
	}
	return done, nil
}

func (d *DevMnt) DirRead(ctx context.Context, c *Chan) ([]styx.Dir, error) {
	f := mfid(c)
	nf := f
	if !f.open || c.State == nil {
		var err error
		if nf, err = f.clone(ctx); err != nil {
			return nil, err
		}
		if _, err := f.m.rpc(ctx, &styx.Fcall{Type: styx.Topen, Fid: nf.fid, Mode: abi.OREAD}); err != nil {
			return nil, err
		}
		nf.open = true
	}
	var out []styx.Dir
	var off uint64
	iounit := f.m.msize - styx.IOHDRSZ
	for {
		r, err := f.m.rpc(ctx, &styx.Fcall{Type: styx.Tread, Fid: nf.fid, Offset: off, Count: iounit})
		if err != nil {
			return nil, err
		}
		if len(r.Data) == 0 {
			break
		}
		off += uint64(len(r.Data))
		b := r.Data
		for len(b) >= 2 {
			n := int(uint16(b[0])|uint16(b[1])<<8) + 2
			if n > len(b) {
				return nil, Error(abi.Ebadstat)
			}
			dir, err := styx.UnmarshalDir(b[:n])
			if err != nil {
				return nil, Error(abi.Ebadstat)
			}
			d.fixdir(c, dir)
			out = append(out, *dir)
			b = b[n:]
		}
	}
	return out, nil
}

func (d *DevMnt) Remove(ctx context.Context, c *Chan) error {
	f := opened(c)
	nf := f
	if c.State == nil {
		var err error
		if nf, err = f.clone(ctx); err != nil {
			return err
		}
	}
	_, err := f.m.rpc(ctx, &styx.Fcall{Type: styx.Tremove, Fid: nf.fid})
	return err
}

func (d *DevMnt) Wstat(ctx context.Context, c *Chan, dir *styx.Dir) error {
	f := opened(c)
	_, err := f.m.rpc(ctx, &styx.Fcall{Type: styx.Twstat, Fid: f.fid, Stat: styx.MarshalDir(nil, dir)})
	return err
}

func (d *DevMnt) Aname(c *Chan) string { return mfid(c).spec }

func (d *DevMnt) MountSource(c *Chan) (string, bool) {
	f := mfid(c)
	return f.src, f.root
}
