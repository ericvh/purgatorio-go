package lethe

import (
	"bytes"
	"testing"

	"github.com/ericvh/purgatorio-go/pgauth"
)

// The sealed engine tests, after the Rust engine tests: each runs with
// the classical (Rust) exchange and with the hybrid one.

type keyset struct {
	user, signer, stranger *pgauth.PrivateKey
}

func seed(n byte, hybrid bool) []byte {
	l := pgauth.SeedSize
	if hybrid {
		l = pgauth.HybridSeedSize
	}
	return bytes.Repeat([]byte{n}, l)
}

func keys(t *testing.T, hybrid bool) keyset {
	t.Helper()
	k := func(n byte) *pgauth.PrivateKey {
		p, err := pgauth.NewPrivateKey(seed(n, hybrid))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	return keyset{k(1), k(2), k(5)}
}

func (k keyset) direct() pgauth.Policy {
	return pgauth.Policy{Keys: []pgauth.KeyID{k.user.Public().ID()}}
}

func (k keyset) cert(t *testing.T, name string, expires uint64, by *pgauth.PrivateKey) *pgauth.Cert {
	t.Helper()
	c, err := pgauth.NewCert(name, k.user.Public(), expires)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Sign(by); err != nil {
		t.Fatal(err)
	}
	return c
}

func (r *rig) xchg(f []byte) ([]byte, error) { return r.send(f), nil }

func (r *rig) call(c *Client, op Op) (Reply, error) {
	return c.Call(r.xchg, op, func() uint64 { return r.now }, r.env().Random)
}

func (r *rig) read(c *Client, p string, cond bool, have uint32) (Reply, error) {
	return r.call(c, &Read{P: p, Count: 8192, Cond: cond, Have: have})
}

func both(t *testing.T, f func(t *testing.T, hybrid bool, accept pgauth.Accept)) {
	t.Run("classical", func(t *testing.T) { f(t, false, pgauth.AcceptClassical) })
	t.Run("hybrid", func(t *testing.T) { f(t, true, pgauth.AcceptHybrid) })
}

func sealedServer(accept pgauth.Accept, key byte) *Server {
	return &Server{Config: DefaultConfig(), Tickets: NewTicketServer([32]byte(bytes.Repeat([]byte{key}, 32)), accept)}
}

func TestSealedRequests(t *testing.T) {
	both(t, func(t *testing.T, hybrid bool, accept pgauth.Accept) {
		k := keys(t, hybrid)
		tr := testTree()
		r := &rig{s: sealedServer(accept, 9), tree: tr, now: t0, trust: k.direct()}
		c := &Client{Creds: &Creds{User: "glenda", Key: k.user}, Rights: RightRead, Prefix: "/", Hybrid: hybrid}
		x, err := r.call(c, &Stat{P: "/hello"})
		if err != nil || x.(*Rstat).Dir.Name != "hello" {
			t.Fatalf("stat %v %v", x, err)
		}
		if r.events[0].Kind != "issued" || r.events[0].Uname != "glenda" {
			t.Errorf("events %+v", r.events)
		}
		ty := uint8(FTauth)
		if hybrid {
			ty = FTauth2
		}
		if r.sent[0][5] != ty {
			t.Errorf("exchange frame type %d", r.sent[0][5])
		}
		x, err = r.read(c, "/hello", false, 0)
		if err != nil || string(x.(*Rread).Data) != "hello, lethe\n" {
			t.Fatalf("read %v %v", x, err)
		}
		q := x.(*Rread).Qid
		x, _ = r.read(c, "/hello", true, q.Vers)
		if !x.(*Rread).Unchanged {
			t.Error("not unchanged")
		}
		tr.change("/hello", []byte("changed\n"))
		x, _ = r.read(c, "/hello", true, q.Vers)
		if x.(*Rread).Unchanged || string(x.(*Rread).Data) != "changed\n" {
			t.Error("change not seen")
		}
		x, _ = r.call(c, &Read{P: "/hello", Offset: 3, Count: 2})
		if string(x.(*Rread).Data) != "ng" {
			t.Errorf("range %q", x.(*Rread).Data)
		}
		var cookie []byte
		n, pages := 0, 0
		for {
			x, err := r.call(c, &Readdir{P: "/many", Cookie: cookie, Count: 1000})
			if err != nil {
				t.Fatal(err)
			}
			ds, _ := Dirs(x.(*Rreaddir).Entries)
			n += len(ds)
			pages++
			if cookie = x.(*Rreaddir).Cookie; len(cookie) == 0 {
				break
			}
		}
		if n != 200 || pages < 6 {
			t.Errorf("%d names in %d pages", n, pages)
		}
		// File errors are sealed, and the user's own bits are used.
		_, err = r.call(c, &Stat{P: "/nope"})
		wantErr(t, err, "file does not exist")
		if bytes.Contains(r.send(r.sent[len(r.sent)-1]), []byte("does not exist")) {
			t.Error("file error in the clear")
		}
		_, err = r.read(c, "/secret", false, 0)
		wantErr(t, err, "permission denied")
		if _, err = r.read(c, "/mine", false, 0); err != nil {
			t.Error(err)
		}
		_, err = r.call(c, &Stat{P: "/locked/x"})
		wantErr(t, err, "permission denied")
		_, err = r.call(c, &Readdir{P: "/many", Count: 10})
		wantErr(t, err, "stat buffer too small")
		x, err = r.call(c, &Walk{P: "/", Names: []string{"lib", "a"}})
		if err != nil || len(x.(*Rwalk).Qids) != 2 {
			t.Errorf("walk %v %v", x, err)
		}
		if c.Tickets != 1 {
			t.Errorf("%d tickets", c.Tickets)
		}
	})
}

func TestCertificatesAndRefusals(t *testing.T) {
	both(t, func(t *testing.T, hybrid bool, accept pgauth.Accept) {
		k := keys(t, hybrid)
		r := &rig{s: sealedServer(accept, 9), tree: testTree(), now: t0}
		signers := pgauth.Policy{Signers: []pgauth.KeyID{k.signer.Public().ID()}}
		r.trust = signers
		try := func(cert *pgauth.Cert, user string, want string) {
			t.Helper()
			c := &Client{Creds: &Creds{User: user, Key: k.user, Cert: cert}, Rights: RightRead, Prefix: "/", Hybrid: hybrid}
			_, err := r.read(c, "/hello", false, 0)
			if want == "" {
				if err != nil {
					t.Errorf("%v", err)
				}
				return
			}
			wantErr(t, err, want)
		}
		try(k.cert(t, "glenda", t0+100, k.signer), "glenda", "")
		try(nil, "glenda", "authentication failed")
		try(k.cert(t, "glenda", t0, k.signer), "glenda", "certificate expired")
		try(k.cert(t, "glenda", 0, k.stranger), "glenda", "untrusted signer")
		r.trust.Revoked = []pgauth.KeyID{k.user.Public().ID()}
		try(k.cert(t, "glenda", 0, k.signer), "glenda", "certificate revoked")
		r.trust = signers
		try(k.cert(t, "glenda", 0, k.signer), "bob", "certificate name mismatch")
		r.trust = pgauth.Policy{}
		try(k.cert(t, "glenda", 0, k.signer), "glenda", "server trusts no keys")
		// A damaged revocation list refuses even a key held directly.
		r.trust = k.direct()
		r.trust.Damaged = true
		try(nil, "glenda", "certificate revoked")
		// A changed exchange frame no longer verifies.
		r.trust = k.direct()
		c := &Client{Creds: &Creds{User: "glenda", Key: k.user}, Rights: RightRead, Prefix: "/", Hybrid: hybrid}
		_, err := c.Call(func(f []byte) ([]byte, error) {
			f = bytes.Clone(f)
			if f[5] == FTauth || f[5] == FTauth2 {
				f[20] ^= 1
			}
			return r.send(f), nil
		}, &Stat{P: "/"}, func() uint64 { return t0 }, r.env().Random)
		wantErr(t, err, "authentication failed")
		// Narrowing: write is not granted, the prefix is.
		c = &Client{Creds: &Creds{User: "glenda", Key: k.user}, Rights: RightRead | RightWrite, Prefix: "/lib", Hybrid: hybrid}
		if _, err := r.read(c, "/lib/a", false, 0); err != nil {
			t.Fatal(err)
		}
		if c.Session.Rights != RightRead || c.Session.Prefix != "/lib" {
			t.Errorf("granted %d %q", c.Session.Rights, c.Session.Prefix)
		}
		for _, p := range []string{"/hello", "/", "/libx"} {
			_, err := r.call(c, &Stat{P: p})
			wantErr(t, err, "permission denied")
		}
		c = &Client{Creds: &Creds{User: "glenda", Key: k.user}, Rights: RightWrite, Prefix: "/", Hybrid: hybrid}
		_, err = r.read(c, "/hello", false, 0)
		wantErr(t, err, "permission denied")
	})
}

func TestRenewalWindowTamperingReplicas(t *testing.T) {
	both(t, func(t *testing.T, hybrid bool, accept pgauth.Accept) {
		k := keys(t, hybrid)
		tr := testTree()
		s := sealedServer(accept, 9)
		s.Config.Lifetime, s.Config.Window = 30, 10
		r := &rig{s: s, tree: tr, now: t0, trust: k.direct()}
		c := &Client{Creds: &Creds{User: "glenda", Key: k.user}, Rights: RightRead, Prefix: "/", Hybrid: hybrid}
		if _, err := r.read(c, "/hello", false, 0); err != nil {
			t.Fatal(err)
		}
		r.now = t0 + 40
		if _, err := r.read(c, "/hello", false, 0); err != nil || c.Tickets != 2 {
			t.Fatalf("renewal: %v, %d tickets", err, c.Tickets)
		}
		// A ticket the server says expired is renewed and resent, even
		// when the client's clock is behind.
		stale := &Client{Creds: c.Creds, Rights: RightRead, Prefix: "/", Hybrid: hybrid, Session: c.Session}
		r.now = t0 + 40 + 31
		f, _ := stale.Session.Request(5, t0+40, &Stat{P: "/"}, [NonceLen]byte{7})
		if got := r.send(f); !bytes.Equal(got, ErrorFrame(5, ETicketExpired)) {
			t.Errorf("expired: %q", got)
		}
		if _, err := stale.Call(r.xchg, &Stat{P: "/"}, func() uint64 { return t0 + 40 }, r.env().Random); err != nil || stale.Tickets != 1 {
			t.Errorf("stale: %v %d", err, stale.Tickets)
		}
		// Replays inside the window are answered under a fresh nonce,
		// after it refused.
		r.now = t0 + 100
		c = &Client{Creds: c.Creds, Rights: RightRead, Prefix: "/", Hybrid: hybrid}
		if _, err := r.read(c, "/hello", false, 0); err != nil {
			t.Fatal(err)
		}
		req := r.sent[len(r.sent)-1]
		a, b := r.send(req), r.send(req)
		if bytes.Equal(a, b) || a[5] != FRreq {
			t.Error("replay not resealed")
		}
		r.now = t0 + 111
		if got := r.send(req); !bytes.Equal(got, ErrorFrame(FrameID(req), ERequestExpiry)) {
			t.Errorf("late replay: %q", got)
		}
		r.now = t0 + 100
		for _, i := range []int{4, 5, 6, 12, 20, 60, len(req) - 30, len(req) - 1} {
			bad := bytes.Clone(req)
			bad[i] ^= 1
			if m := r.send(bad); m[5] != FRerror {
				t.Errorf("byte %d changed: answered", i)
			}
		}
		bad := bytes.Clone(req)
		bad[len(bad)-1] ^= 1
		if got := r.send(bad); !bytes.Equal(got, ErrorFrame(FrameID(req), EMac)) {
			t.Errorf("tag changed: %q", got)
		}
		// A replica with the same ticket key answers the same ticket;
		// one with another key says so, and the client authenticates
		// there.
		r2 := &rig{s: sealedServer(accept, 9), tree: tr, now: r.now, trust: k.direct()}
		n := c.Tickets
		if _, err := r2.read(c, "/hello", false, 0); err != nil || c.Tickets != n {
			t.Errorf("replica: %v", err)
		}
		r3 := &rig{s: sealedServer(accept, 8), tree: tr, now: r.now, trust: k.direct()}
		if _, err := r3.read(c, "/hello", false, 0); err != nil || c.Tickets != n+1 {
			t.Errorf("other key: %v", err)
		}
		if r3.events[0].Error != ETicketKey {
			t.Errorf("events %+v", r3.events)
		}
		// Reserved ops, sealed.
		for _, cs := range []struct {
			code uint8
			want string
		}{{0x20, "permission denied"}, {0x40, "not implemented"}, {0x16, "not implemented"}} {
			plain := append(le.AppendUint64(nil, r.now), cs.code)
			f, p := c.Session.Seal(3, plain, [NonceLen]byte{4})
			_, err := c.Session.Reply(p, r3.send(f))
			if fl, ok := err.(*Failure); !ok || fl.Kind != FailSealed || fl.Text != cs.want {
				t.Errorf("%#x: %v", cs.code, err)
			}
		}
	})
}

// The choice of exchange is the user's: neither end falls back.
func TestHybridAndClassicalDoNotMix(t *testing.T) {
	kc, kh := keys(t, false), keys(t, true)
	tr := testTree()
	trust := pgauth.Policy{Keys: []pgauth.KeyID{kc.user.Public().ID(), kh.user.Public().ID()}}
	classical := &Client{Creds: &Creds{User: "glenda", Key: kc.user}, Rights: RightRead, Prefix: "/"}
	hybrid := &Client{Creds: &Creds{User: "glenda", Key: kh.user}, Rights: RightRead, Prefix: "/", Hybrid: true}
	// The default server takes hybrid tickets only.
	r := &rig{s: sealedServer(0, 9), tree: tr, now: t0, trust: trust}
	_, err := r.read(classical, "/hello", false, 0)
	wantErr(t, err, EHybridRequired)
	if _, err := r.read(hybrid, "/hello", false, 0); err != nil {
		t.Fatal(err)
	}
	// A classical-only server refuses the hybrid exchange.
	rc := &rig{s: sealedServer(pgauth.AcceptClassical, 9), tree: tr, now: t0, trust: trust}
	_, err = rc.read(&Client{Creds: hybrid.Creds, Rights: RightRead, Prefix: "/", Hybrid: true}, "/hello", false, 0)
	wantErr(t, err, EClassicalRequired)
	// Tickets carry their exchange: a classical ticket from a replica
	// that takes both is refused by one that takes hybrid only, and the
	// other way round.
	rb := &rig{s: sealedServer(pgauth.AcceptClassical|pgauth.AcceptHybrid, 9), tree: tr, now: t0, trust: trust}
	cl := &Client{Creds: classical.Creds, Rights: RightRead, Prefix: "/"}
	if _, err := rb.read(cl, "/hello", false, 0); err != nil {
		t.Fatal(err)
	}
	f, _ := cl.Session.Request(9, t0, &Stat{P: "/"}, [NonceLen]byte{1})
	if got := r.send(f); !bytes.Equal(got, ErrorFrame(9, EHybridRequired)) {
		t.Errorf("classical ticket at a hybrid server: %q", got)
	}
	f, _ = hybrid.Session.Request(9, t0, &Stat{P: "/"}, [NonceLen]byte{1})
	if got := rc.send(f); !bytes.Equal(got, ErrorFrame(9, EClassicalRequired)) {
		t.Errorf("hybrid ticket at a classical server: %q", got)
	}
	// A key of the wrong kind for the exchange is refused by the client.
	_, _, err = AuthFrame(classical.Creds, 1, RightRead, "/", true, r.env().Random)
	if err != pgauth.EWrongAlg {
		t.Errorf("classical key, hybrid exchange: %v", err)
	}
	// A server that does not know the hybrid exchange answers its frame
	// "bad lethe message", which a hybrid client reports as such.
	p := &AuthPending{id: 1, hybrid: true}
	if _, err := p.Finish(ErrorFrame(1, EMessage), t0); err == nil || err.Error() != ENoHybrid {
		t.Errorf("old server: %v", err)
	}
	// A clear-only server tells a sealed client so, in either exchange.
	ra := &rig{s: &Server{Config: DefaultConfig(), Clear: true, Tickets: NewTicketServer([32]byte{}, 0)}, tree: tr, now: t0}
	_, err = ra.read(&Client{Creds: hybrid.Creds, Rights: RightRead, Prefix: "/", Hybrid: true}, "/hello", false, 0)
	wantErr(t, err, EAuthNotRequired)
}

// -A with trust: both forms are answered, each as who it is.
func TestMixedMode(t *testing.T) {
	k := keys(t, true)
	s := sealedServer(0, 9)
	s.Clear = true
	r := &rig{s: s, tree: testTree(), now: t0, trust: k.direct()}
	c := &Client{Creds: &Creds{User: "glenda", Key: k.user}, Rights: RightRead, Prefix: "/", Hybrid: true}
	x, err := r.read(c, "/mine", false, 0)
	if err != nil || string(x.(*Rread).Data) != "yes" {
		t.Fatalf("%v %v", x, err)
	}
	_, err = r.clearRead("/mine", false, 0)
	wantErr(t, err, "permission denied")
	if _, err := r.clearRead("/hello", false, 0); err != nil {
		t.Error(err)
	}
	var who []string
	for _, e := range r.events {
		if e.Kind == "answered" {
			who = append(who, e.Uname)
		}
	}
	if len(who) != 3 || who[0] != "glenda" || who[1] != "none" || who[2] != "none" {
		t.Errorf("answered for %v", who)
	}
	stranger := &Client{Creds: &Creds{User: "glenda", Key: k.stranger}, Rights: RightRead, Prefix: "/", Hybrid: true}
	_, err = r.read(stranger, "/hello", false, 0)
	wantErr(t, err, "authentication failed")
}

func TestTickets(t *testing.T) {
	k := NewTicketKey([32]byte(bytes.Repeat([]byte{1}, 32)))
	tk := &Ticket{Master: [32]byte(bytes.Repeat([]byte{7}, 32)), Issued: 100, Expires: 200, Rights: RightRead, Uname: "glenda", Prefix: "/lib"}
	s := k.Seal(tk, &[NonceLen]byte{2})
	if le.Uint32(s) != k.ID {
		t.Error("key id")
	}
	other := NewTicketKey([32]byte(bytes.Repeat([]byte{3}, 32)))
	if got, err := OpenTicket([]TicketKey{other, k}, s); err != nil || *got != *tk {
		t.Errorf("open: %+v %v", got, err)
	}
	if _, err := OpenTicket([]TicketKey{other}, s); err == nil || err.Error() != ETicketKey {
		t.Errorf("other key: %v", err)
	}
	for i := 4; i < len(s); i++ {
		bad := bytes.Clone(s)
		bad[i] ^= 0x10
		if _, err := OpenTicket([]TicketKey{k}, bad); err == nil || err.Error() != ETicket {
			t.Fatalf("byte %d: %v", i, err)
		}
	}
}

// TestTicketReferences: a server with a ticket cache offers references in
// the grant; the first request carries the ticket and later ones its
// 8-byte reference, about 100 bytes less. A server that let the ticket go
// answers ETicketRef and the client resends with the ticket, with no new
// ticket. A server without a cache offers none, and refuses a reference.
func TestTicketReferences(t *testing.T) {
	both(t, func(t *testing.T, hybrid bool, accept pgauth.Accept) {
		k := keys(t, hybrid)
		s := sealedServer(accept, 9)
		ts := s.Tickets.(*TicketServer)
		ts.Refs = NewRefCache(16)
		r := &rig{s: s, tree: testTree(), now: t0, trust: k.direct()}
		c := &Client{Creds: &Creds{User: "glenda", Key: k.user}, Rights: RightRead, Prefix: "/", Hybrid: hybrid}
		for i := 0; i < 3; i++ {
			if _, err := r.read(c, "/hello", false, 0); err != nil {
				t.Fatal(err)
			}
		}
		kinds := func(fs [][]byte) []uint8 {
			var out []uint8
			for _, f := range fs {
				out = append(out, f[5])
			}
			return out
		}
		if got := kinds(r.sent[1:]); !bytes.Equal(got, []byte{FTreq, FTreqref, FTreqref}) {
			t.Fatalf("frames %v", got)
		}
		if full, ref := len(r.sent[1]), len(r.sent[2]); full < ref+90 {
			t.Errorf("full %d, by reference %d", full, ref)
		}
		if !c.Session.ByRef() {
			t.Error("session not by reference")
		}

		// The server forgets: one refusal, a resend with the ticket, and
		// on by reference again, with the same ticket.
		ts.Refs = NewRefCache(16)
		n := len(r.sent)
		for i := 0; i < 2; i++ {
			if _, err := r.read(c, "/hello", false, 0); err != nil {
				t.Fatal(err)
			}
		}
		if got := kinds(r.sent[n:]); !bytes.Equal(got, []byte{FTreqref, FTreq, FTreqref}) {
			t.Fatalf("after the cache went: frames %v", got)
		}
		if r.events[n].Error != ETicketRef {
			t.Errorf("event %+v", r.events[n])
		}

		// No cache, no references; a reference anyway is refused.
		plain := &rig{s: sealedServer(accept, 9), tree: testTree(), now: t0, trust: k.direct()}
		c = &Client{Creds: &Creds{User: "glenda", Key: k.user}, Rights: RightRead, Prefix: "/", Hybrid: hybrid}
		for i := 0; i < 2; i++ {
			if _, err := plain.read(c, "/hello", false, 0); err != nil {
				t.Fatal(err)
			}
		}
		if got := kinds(plain.sent[1:]); !bytes.Equal(got, []byte{FTreq, FTreq}) {
			t.Fatalf("no cache: frames %v", got)
		}
		ref := Tref(c.Session.Ticket)
		var nonce [NonceLen]byte
		f := TreqrefFrame(&c.Session.Keys, 5, &ref, &nonce, []byte{1})
		if out := plain.send(f); out[5] != FRerror {
			t.Errorf("reference to a server without a cache: type %d", out[5])
		}
	})
}
