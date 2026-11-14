package core

import (
	"context"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/styx"
)

// The kernel's side of pgauth's mount handshake (docs/auth.md): an
// export that authenticates its clients (versions 1 to 4) and protects
// the conversation with records after a version 3 or 4 proof, and #M's
// auth fid, which makes the client's ephemeral keys, switches the
// conversation to records at the proof and fetches the server's proof.
// Trust files are read in the namespace of the prog that checks: the
// exporter for its clients, the mounter for its servers.

// maxTrustFile bounds what is read of one trust file.
const maxTrustFile = 1 << 20

// readNSFile reads path in pgrp. exists is false when the file is not
// there; err is set when it is there but cannot be read.
func readNSFile(ctx context.Context, pgrp *Pgrp, path string) (data []byte, exists bool, err error) {
	c, err := pgrp.Namec(ctx, path, "/")
	if err != nil {
		return nil, false, nil
	}
	oc, err := OpenC(ctx, c, abi.OREAD)
	if err != nil {
		return nil, true, err
	}
	defer oc.DecRef()
	buf := make([]byte, 8192)
	var off int64
	for len(data) < maxTrustFile {
		n, err := oc.Dev.Read(ctx, oc, buf, off)
		if err != nil {
			return nil, true, err
		}
		if n == 0 {
			break
		}
		data = append(data, buf[:n]...)
		off += int64(n)
	}
	return data, true, nil
}

func nsList(ctx context.Context, pgrp *Pgrp, path string) []pgauth.KeyID {
	b, _, _ := readNSFile(ctx, pgrp, path)
	return pgauth.ParseListLenient(b)
}

// revocations reads /lib/keys/revoked: a list that is there but cannot
// be read or does not parse is damaged, and refuses everything.
func revocations(ctx context.Context, pgrp *Pgrp) ([]pgauth.KeyID, bool) {
	b, exists, err := readNSFile(ctx, pgrp, "/lib/keys/revoked")
	if !exists {
		return nil, false
	}
	if err != nil {
		return nil, true
	}
	l, ok := pgauth.ParseList(b)
	return l, !ok
}

func nowSecs(k *Kernel) uint64 {
	if k == nil || k.Platform == nil {
		return 0
	}
	return uint64(k.Platform.Now() / 1e9)
}

// ServerPolicy is what an exporter in pgrp trusts for a client that
// names itself uname: /usr/<uname>/key.pub, the keys /lib/keys/users
// gives for uname or *, the signers in /lib/keys/signers, and
// /lib/keys/revoked.
func ServerPolicy(ctx context.Context, pgrp *Pgrp, uname string, now uint64, accept pgauth.Accept) *pgauth.Policy {
	pol := &pgauth.Policy{Now: now, Accept: accept}
	if pgauth.ValidName(uname) {
		if b, _, _ := readNSFile(ctx, pgrp, "/usr/"+uname+"/key.pub"); b != nil {
			if k, err := pgauth.ParsePublicKey(b); err == nil {
				pol.Keys = append(pol.Keys, k.ID())
			}
		}
	}
	users, _, _ := readNSFile(ctx, pgrp, "/lib/keys/users")
	for _, id := range pgauth.KeysFor(pgauth.ParseNamed(users), uname, false) {
		if !pgauth.Contains(pol.Keys, id) {
			pol.Keys = append(pol.Keys, id)
		}
	}
	pol.Signers = nsList(ctx, pgrp, "/lib/keys/signers")
	pol.Revoked, pol.Damaged = revocations(ctx, pgrp)
	return pol
}

// ClientPolicy is what a mounter in pgrp trusts as a server, for any
// host: every key in /lib/keys/servers, the signers in
// /lib/keys/serversigners (or, when it is not there, /lib/keys/signers),
// and /lib/keys/revoked.
func ClientPolicy(ctx context.Context, pgrp *Pgrp, now uint64) *pgauth.Policy {
	pol := &pgauth.Policy{Now: now}
	servers, _, _ := readNSFile(ctx, pgrp, "/lib/keys/servers")
	pol.Keys = pgauth.KeysFor(pgauth.ParseNamed(servers), "", true)
	if b, exists, _ := readNSFile(ctx, pgrp, "/lib/keys/serversigners"); exists {
		pol.Signers = pgauth.ParseListLenient(b)
	} else {
		pol.Signers = nsList(ctx, pgrp, "/lib/keys/signers")
	}
	pol.Revoked, pol.Damaged = revocations(ctx, pgrp)
	return pol
}

// UserKey is the key a user proves with in pgrp, of the family asked
// for: the authinfo /usr/<user>/keyring/default (with its certificate),
// else the seed file /usr/<user>/key.
func UserKey(ctx context.Context, pgrp *Pgrp, user string, hybrid bool) (*pgauth.PrivateKey, *pgauth.Cert) {
	if !pgauth.ValidName(user) {
		return nil, nil
	}
	if b, _, _ := readNSFile(ctx, pgrp, "/usr/"+user+"/keyring/default"); b != nil {
		if a, err := pgauth.ParseAuthInfo(b); err == nil && a.Key.Hybrid() == hybrid {
			return a.Key, a.Cert
		}
	}
	if b, _, _ := readNSFile(ctx, pgrp, "/usr/"+user+"/key"); b != nil {
		if k, err := pgauth.NewPrivateKey(b); err == nil && k.Hybrid() == hybrid {
			return k, nil
		}
	}
	return nil, nil
}

// ParseKeyInfo reads what export -k names: an authinfo, or a bare seed.
func ParseKeyInfo(b []byte) (*pgauth.PrivateKey, *pgauth.Cert, error) {
	if a, err := pgauth.ParseAuthInfo(b); err == nil {
		return a.Key, a.Cert, nil
	}
	if k, err := pgauth.NewPrivateKey(b); err == nil {
		return k, nil, nil
	}
	return nil, nil, pgauth.EBadInfo
}

// ExportAuth is an export's authentication.
type ExportAuth struct {
	Accept  pgauth.Accept // the handshakes taken; never 0
	Encrypt bool          // refuse conversations left in the clear
	// The server proof's key, from export -k; nil means the exporting
	// user's key in the exporter's namespace.
	Key  *pgauth.PrivateKey
	Cert *pgauth.Cert
}

// exAfid is an auth fid of an authenticating export.
type exAfid struct {
	uname, aname string
	pol          *pgauth.Policy
	srv          *pgauth.Server
	proof        []byte
	done         bool // the proof is complete and was checked
	accepted     bool
	version      int // of the accepted proof
	keys         *pgauth.Keys
	srvProof     []byte
	srvMade      bool
}

// exAuth is an export's authentication state.
type exAuth struct {
	cfg   *ExportAuth
	now   func() uint64
	path  uint64 // qid paths of auth fids
	afids map[uint32]*exAfid
	open  *pgauth.Opener // the reading side, once switched (reader only)
	seal  *pgauth.Sealer // under Exporter.wmu
	sw    bool           // the conversation switched (reader only)
}

// isAuthMsg reports whether t goes to the inline auth handler: Tauth,
// and Tread, Twrite and Tclunk on an auth fid.
func (e *Exporter) isAuthMsg(t *styx.Fcall) bool {
	switch t.Type {
	case styx.Tauth:
		return true
	case styx.Tread, styx.Twrite, styx.Tclunk:
		if e.auth == nil {
			return false
		}
		e.mu.Lock()
		_, ok := e.auth.afids[t.Fid]
		e.mu.Unlock()
		return ok
	}
	return false
}

// serveAuth handles an auth message in the reading goroutine, so that a
// proof's switch to records happens at its place in the stream.
func (e *Exporter) serveAuth(ctx context.Context, t *styx.Fcall) {
	a := e.auth
	if a == nil {
		e.reply(rerr(t, string(pgauth.ENotRequired)))
		return
	}
	r := &styx.Fcall{Type: t.Type + 1, Tag: t.Tag}
	switch t.Type {
	case styx.Tauth:
		pol := ServerPolicy(ctx, e.pgrp, t.Uname, a.now(), a.cfg.Accept)
		if !pol.RequiresAuth() {
			e.reply(rerr(t, string(pgauth.ENoTrust)))
			return
		}
		e.mu.Lock()
		_, inuse := e.fids[t.Afid]
		_, inuse2 := a.afids[t.Afid]
		if inuse || inuse2 || t.Afid == styx.NOFID {
			e.mu.Unlock()
			e.reply(rerr(t, edupfid))
			return
		}
		a.path++
		a.afids[t.Afid] = &exAfid{uname: t.Uname, aname: t.Aname, pol: pol,
			srv: pgauth.NewServer(a.cfg.Accept&pgauth.AcceptHybrid != 0)}
		r.Qid = styx.Qid{Type: styx.QTAUTH, Path: a.path}
		e.mu.Unlock()
		e.reply(r)
	case styx.Tclunk:
		e.mu.Lock()
		delete(a.afids, t.Fid)
		e.mu.Unlock()
		e.reply(r)
	case styx.Tread:
		e.mu.Lock()
		f := a.afids[t.Fid]
		e.mu.Unlock()
		var src []byte
		off := int64(t.Offset)
		if f.keys != nil && f.accepted && off >= pgauth.ServerProofOffset(f.version) {
			src = e.serverProof(ctx, f)
			off -= pgauth.ServerProofOffset(f.version)
		} else {
			src = f.srv.Offer()
		}
		if off < int64(len(src)) {
			r.Data = src[off:min(int64(len(src)), off+int64(t.Count))]
		}
		e.reply(r)
	case styx.Twrite:
		e.mu.Lock()
		f := a.afids[t.Fid]
		e.mu.Unlock()
		keys, err := e.takeProof(f, t)
		if err != nil {
			r = rerr(t, err.Error())
		} else {
			r.Count = uint32(len(t.Data))
		}
		if keys == nil || a.sw {
			e.reply(r)
			return
		}
		// The proof's reply goes in the clear; everything after it, each
		// way, is records.
		a.sw = true
		seal, open := keys.Server()
		a.open = open
		e.wmu.Lock()
		e.writeLocked(r)
		a.seal = seal
		e.wmu.Unlock()
	}
}

// takeProof adds a write to f's proof. When the proof is complete it is
// checked; it returns the session keys of a version 3 or 4 proof (which
// switch the conversation whether the proof is accepted or not) and the
// check's error.
func (e *Exporter) takeProof(f *exAfid, t *styx.Fcall) (*pgauth.Keys, error) {
	if f.done {
		// A later proof on the same fid is refused.
		return nil, pgauth.EFailed
	}
	if int(t.Offset) != len(f.proof) || len(f.proof)+len(t.Data) > pgauth.MaxProof4 {
		f.done = true
		return nil, pgauth.EFailed
	}
	f.proof = append(f.proof, t.Data...)
	p := f.proof
	if len(p) > 0 && p[0] == 4 && len(p) != pgauth.V1Len {
		if len(p) < 5 {
			return nil, nil
		}
		n := pgauth.ProofLen(p)
		if n == 0 || len(p) > n {
			f.done = true
			return nil, pgauth.EFailed
		}
		if len(p) < n {
			return nil, nil // more to come
		}
	}
	f.done = true
	var keys *pgauth.Keys
	if len(p) != pgauth.V1Len && len(p) > 0 && (p[0] == 3 || p[0] == 4) {
		keys, _ = f.srv.Keys(f.uname, f.aname, p)
	}
	if e.auth.cfg.Encrypt && (len(p) == pgauth.V1Len || (len(p) > 0 && p[0] == 2)) {
		return keys, pgauth.EEncryption
	}
	if err := pgauth.Check(f.pol, f.srv.Offer(), f.uname, f.aname, p); err != nil {
		return keys, err
	}
	e.mu.Lock()
	f.accepted, f.keys = true, keys
	f.version = 2
	if keys != nil {
		f.version = keys.Version
	}
	e.mu.Unlock()
	return keys, nil
}

// serverProof is the export's proof for f's session, made once: with the
// export -k key, else the exporting user's key, of the session's family.
// Empty when there is no such key.
func (e *Exporter) serverProof(ctx context.Context, f *exAfid) []byte {
	if f.srvMade {
		return f.srvProof
	}
	f.srvMade = true
	hybrid := f.keys.Version == 4
	k, c := e.auth.cfg.Key, e.auth.cfg.Cert
	if k == nil || k.Hybrid() != hybrid {
		if p := factotumServerProof(ctx, e.pgrp, f.keys); p != nil {
			f.srvProof = p
			return p
		}
		k, c = UserKey(ctx, e.pgrp, e.user, hybrid)
	}
	if k == nil {
		return nil
	}
	f.srvProof, _ = pgauth.MakeServerProof(k, c, f.keys)
	return f.srvProof
}

// checkAttach decides whether an authenticating export lets an attach
// in: it must name an auth fid whose proof was accepted for the same
// uname and aname.
func (e *Exporter) checkAttach(ctx context.Context, t *styx.Fcall) error {
	a := e.auth
	if a == nil {
		return nil
	}
	if t.Afid == styx.NOFID {
		if !ServerPolicy(ctx, e.pgrp, t.Uname, 0, a.cfg.Accept).RequiresAuth() {
			return pgauth.ENoTrust
		}
		if a.cfg.Encrypt {
			return pgauth.EEncryption
		}
		return pgauth.ERequired
	}
	e.mu.Lock()
	f := a.afids[t.Afid]
	e.mu.Unlock()
	if f == nil || !f.accepted || f.uname != t.Uname || f.aname != t.Aname {
		return pgauth.ERequired
	}
	if a.cfg.Encrypt && f.version < 3 {
		return pgauth.EEncryption
	}
	return nil
}

// mntAuth is #M's state for one auth fid.
type mntAuth struct {
	mu           sync.Mutex
	uname, aname string
	client       *pgauth.Client
	keys         *pgauth.Keys // after the proof was sent
	srvProof     []byte
	srvFetched   bool
	srvErr       error
}

// authRead serves a read of an auth fid: SIGNED bytes for the proof
// (Signed3Size or Signed4Size: the offer and the client's ephemeral
// keys, made once), the server's proof after a version 3 or 4 proof, or
// what the server gives (a bare challenge).
func (d *DevMnt) authRead(ctx context.Context, f *mntfid, buf []byte, off int64) (int, bool, error) {
	a := f.auth
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.keys != nil && off >= pgauth.ServerProofOffset(a.keys.Version) {
		p, err := a.serverProof(ctx, f)
		if err != nil {
			return 0, true, err
		}
		off -= pgauth.ServerProofOffset(a.keys.Version)
		if off >= int64(len(p)) {
			return 0, true, nil
		}
		return copy(buf, p[off:]), true, nil
	}
	if off != 0 || (len(buf) != pgauth.Signed3Size && len(buf) != pgauth.Signed4Size) || a.keys != nil {
		return 0, false, nil
	}
	hybrid := len(buf) == pgauth.Signed4Size
	if a.client == nil {
		offer := make([]byte, pgauth.Offer4Size)
		n, err := fidrdwr(ctx, f, offer, 0, styx.Tread)
		if err != nil {
			return 0, true, err
		}
		c, err := pgauth.NewClient(offer[:n], hybrid)
		if err != nil {
			return 0, true, err
		}
		a.client = c
	}
	if a.client.Hybrid() != hybrid {
		return 0, true, Error(abi.Ebadarg)
	}
	return copy(buf, a.client.Signed()), true, nil
}

// serverProof fetches the server's proof once, at its offset, in as
// many reads as it takes; the mount's own check and every read use
// those bytes.
func (a *mntAuth) serverProof(ctx context.Context, f *mntfid) ([]byte, error) {
	if a.srvFetched {
		return a.srvProof, a.srvErr
	}
	base := pgauth.ServerProofOffset(a.keys.Version)
	var p []byte
	buf := make([]byte, f.m.msize-styx.IOHDRSZ)
	for len(p) <= pgauth.MaxProof4 {
		n, err := fidrdwr(ctx, f, buf, base+int64(len(p)), styx.Tread)
		if err != nil {
			if Errstr(err) == abi.Eintr {
				return nil, err
			}
			a.srvErr = err
			break
		}
		if n == 0 {
			break
		}
		p = append(p, buf[:n]...)
	}
	a.srvFetched, a.srvProof = true, p
	return p, a.srvErr
}

// authWrite sends a write of an auth fid. A version 3 or 4 proof over
// this fid's SIGNED bytes is written in as many messages as it takes,
// and the last one switches the conversation to records: the client
// seals everything it writes after it and opens everything it reads
// after its reply.
func (d *DevMnt) authWrite(ctx context.Context, f *mntfid, buf []byte, off int64) (int, bool, error) {
	a := f.auth
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == nil || a.keys != nil || off != 0 {
		return 0, false, nil
	}
	keys, err := a.client.Keys(a.uname, a.aname, buf)
	if err != nil {
		return 0, false, nil
	}
	m := f.m
	iounit := int(m.msize - styx.IOHDRSZ)
	done := 0
	for done < len(buf) {
		n := min(len(buf)-done, iounit)
		req := &styx.Fcall{Type: styx.Twrite, Fid: f.fid, Offset: uint64(done), Count: uint32(n), Data: buf[done : done+n]}
		var sw *mntSwitch
		if done+n == len(buf) {
			seal, open := keys.Client()
			sw = &mntSwitch{seal: seal, open: open}
			a.keys = keys
		}
		r, err := m.rpcSwitch(ctx, req, sw)
		if err != nil {
			return done, true, err
		}
		if int(r.Count) != n {
			return done, true, Error(abi.Emountrpc)
		}
		done += n
	}
	return done, true, nil
}

// mntSwitch is the switch to records at one request.
type mntSwitch struct {
	seal *pgauth.Sealer
	open *pgauth.Opener
}

// checkServer checks the server's proof on afid's session against what
// the mounter trusts (mount -S).
func (d *DevMnt) checkServer(ctx context.Context, p *Prog, afid *Chan) error {
	f, ok := afid.State.(*mntfid)
	if !ok || f.auth == nil {
		return pgauth.EServerNone
	}
	a := f.auth
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.keys == nil {
		return pgauth.EServerNone
	}
	proof, err := a.serverProof(ctx, f)
	if err != nil || len(proof) == 0 {
		return pgauth.EServerNone
	}
	return pgauth.CheckServer(ClientPolicy(ctx, p.Pgrp(), nowSecs(p.Kernel)), a.keys, proof)
}

// factotumServerProof asks the guest factotum mounted at /mnt/factotum in
// pgrp for the server's proof of the session (role=server), so the
// exporting prog need not be able to read the key. nil when there is no
// factotum or it has no such key.
func factotumServerProof(ctx context.Context, pgrp *Pgrp, keys *pgauth.Keys) []byte {
	c, err := pgrp.Namec(ctx, "/mnt/factotum/rpc", "/")
	if err != nil {
		return nil
	}
	oc, err := OpenC(ctx, c, abi.ORDWR)
	if err != nil {
		return nil
	}
	defer oc.DecRef()
	alg := "alg=classical"
	if keys.Version == 4 {
		alg = "alg=hybrid"
	}
	buf := make([]byte, 64*1024)
	var reply []byte
	for _, req := range [][]byte{
		[]byte("start proto=ed25519 role=server " + alg),
		append([]byte("write "), keys.Session()...),
		[]byte("read"),
	} {
		if _, err := oc.Dev.Write(ctx, oc, req, 0); err != nil {
			return nil
		}
		n, err := oc.Dev.Read(ctx, oc, buf, 0)
		if err != nil || n < 2 || string(buf[:2]) != "ok" {
			return nil
		}
		reply = buf[:n]
	}
	if len(reply) < 4 {
		return nil
	}
	return append([]byte(nil), reply[3:]...)
}
