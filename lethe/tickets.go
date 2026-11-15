package lethe

import (
	"crypto/mlkem"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/pgauth"
)

// TicketHybrid marks, in a ticket's rights, a ticket issued by the
// hybrid exchange. It is never granted to a client: a server that takes
// only hybrid tickets refuses a ticket without it, so a replica that
// shares the ticket key but issues classical tickets cannot widen it.
const TicketHybrid = 0x40

// TicketServer issues and opens tickets: the Rust exchange (Tauth,
// classical) and the hybrid one (Tauth2), as Accept says.
type TicketServer struct {
	// Keys are the ticket keys: the first seals new tickets, the others
	// still open old ones.
	Keys []TicketKey
	// Accept is the exchanges taken; 0 is hybrid only.
	Accept pgauth.Accept
	// Refs holds the tickets requests opened under, for Treqref; nil
	// keeps none, and then grants do not offer references.
	Refs *RefCache
}

// RefCache holds tickets by their reference (Tref), so a client may send
// the reference in place of the ticket. It is soft state: losing an
// entry costs the client one ETicketRef and a resend with the ticket.
type RefCache struct {
	mu  sync.Mutex
	max int
	m   map[[TrefLen]byte]*Ticket
}

// NewRefCache holds at most max tickets.
func NewRefCache(max int) *RefCache {
	return &RefCache{max: max, m: make(map[[TrefLen]byte]*Ticket)}
}

// Get is the ticket with reference r, if held and not expired at now.
func (c *RefCache) Get(r [TrefLen]byte, now uint64) *Ticket {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.m[r]; t != nil && now < t.Expires {
		return t
	}
	return nil
}

// Put holds t under r, letting expired tickets go first when full.
func (c *RefCache) Put(r [TrefLen]byte, t *Ticket, now uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[r]; !ok && len(c.m) >= c.max {
		for k, v := range c.m {
			if now >= v.Expires {
				delete(c.m, k)
			}
		}
		for k := range c.m {
			if len(c.m) < c.max {
				break
			}
			delete(c.m, k)
		}
	}
	c.m[r] = t
}

// NewTicketServer makes a ticket layer with one key.
func NewTicketServer(key [32]byte, accept pgauth.Accept) *TicketServer {
	return &TicketServer{Keys: []TicketKey{NewTicketKey(key)}, Accept: accept}
}

func (ts *TicketServer) accept() pgauth.Accept {
	if ts.Accept == 0 {
		return pgauth.AcceptHybrid
	}
	return ts.Accept
}

// authFields are what both exchanges' Tauth carry.
type authFields struct {
	rights        uint8
	prefix, uname string
	kind          uint8
	body, sig     []byte
	signed        []byte
}

// check checks who asks and their signature, as an export checks a
// proof: a key held directly, or a certificate from a trusted signer;
// neither revoked.
func check(pol *pgauth.Policy, a *authFields, hybrid bool) error {
	switch a.kind {
	case pgauth.KindKey:
		key := pgauth.PublicKey(a.body)
		if !key.Valid() || key.Hybrid() != hybrid || !pgauth.Contains(pol.Keys, key.ID()) || !key.Verify(a.signed, a.sig) {
			return Error(EAuthFailed)
		}
		// A damaged revocation list fails closed, as in Rust.
		if pol.Damaged || pgauth.Contains(pol.Revoked, key.ID()) ||
			(key.Hybrid() && pgauth.Contains(pol.Revoked, key.Classical().ID())) {
			return Error(ERevoked)
		}
		return nil
	case pgauth.KindCert:
		c, err := pgauth.ParseCert(a.body)
		if err != nil || c.Hybrid() != hybrid || !c.Key.Verify(a.signed, a.sig) {
			return Error(EAuthFailed)
		}
		return pgauth.CheckCert(pol, c, a.uname, true)
	}
	return Error(EAuthFailed)
}

// Auth answers a Tauth or a Tauth2.
func (ts *TicketServer) Auth(s *Server, env *Env, id uint32, raw []byte, f Frame) ([]byte, Event, error) {
	var a authFields
	hybrid := false
	switch t := f.(type) {
	case *Tauth:
		a = authFields{t.Rights, t.Prefix, t.Uname, t.Kind, t.Body, t.Signature[:], t.Message()}
	case *Tauth2:
		a = authFields{t.Rights, t.Prefix, t.Uname, t.Kind, t.Body, t.Signature, t.Message()}
		hybrid = true
	default:
		return nil, Event{}, Error(EMessage)
	}
	if !ValidPath(a.prefix) || !pgauth.ValidName(a.uname) {
		return nil, Event{}, Error(EAuthFailed)
	}
	pol := env.Trust(a.uname)
	pol.Now = env.Now
	// No key, nobody: refused as an export that trusts nothing refuses.
	// A server that answers clear requests says so instead; the client
	// decides what to do, and ours never falls back by itself.
	if !pol.RequiresAuth() {
		if s.Clear {
			return nil, Event{}, Error(EAuthNotRequired)
		}
		return nil, Event{}, Error(ENoTrust)
	}
	switch {
	case hybrid && ts.accept()&pgauth.AcceptHybrid == 0:
		return nil, Event{}, Error(EClassicalRequired)
	case !hybrid && ts.accept()&pgauth.AcceptClassical == 0:
		return nil, Event{}, Error(EHybridRequired)
	}
	if err := check(&pol, &a, hybrid); err != nil {
		return nil, Event{}, Error(err.Error())
	}
	granted := a.rights & s.Config.Allow & Grantable
	if granted == 0 {
		return nil, Event{}, Error(abi.Eperm)
	}
	var random [32]byte
	env.Random(random[:])
	eph := NewEphemeral(random)
	var master [32]byte
	var ct []byte
	switch t := f.(type) {
	case *Tauth:
		shared, ok := eph.Shared(t.Eph)
		if !ok {
			return nil, Event{}, Error(EAuthFailed)
		}
		master = Master(shared, append(append([]byte{}, a.signed...), eph.Public[:]...))
	case *Tauth2:
		xs, ok := eph.Shared(t.Eph)
		if !ok {
			return nil, Event{}, Error(EAuthFailed)
		}
		ek, err := mlkem.NewEncapsulationKey768(t.EK[:])
		if err != nil {
			return nil, Event{}, Error(EAuthFailed)
		}
		var ks []byte
		ks, ct = ek.Encapsulate()
		tr := append(append(append([]byte{}, a.signed...), eph.Public[:]...), ct...)
		master = Master2(ks, xs, tr)
	}
	expires := env.Now + s.Config.Lifetime
	tk := &Ticket{Master: master, Issued: env.Now, Expires: expires, Rights: granted,
		Uname: a.uname, Prefix: a.prefix}
	if hybrid {
		tk.Rights |= TicketHybrid
	}
	var n [NonceLen]byte
	env.Random(n[:])
	sealedTicket := ts.Keys[0].Seal(tk, &n)
	// A server that holds tickets says so in the grant, so a client
	// sends references only to a server that takes them.
	offered := granted
	if ts.Refs != nil {
		offered |= RightRefs
	}
	grant := (&Grant{Now: env.Now, Expires: expires, Rights: offered, Prefix: a.prefix, Ticket: sealedTicket}).Encode()
	env.Random(n[:])
	keys := KeysFrom(&master)
	var reply []byte
	if hybrid {
		reply = Rauth2Frame(&keys, id, &eph.Public, (*[KEMCtLen]byte)(ct), &n, grant)
	} else {
		reply = RauthFrame(&keys, id, &eph.Public, &n, grant)
	}
	return reply, Event{Kind: "issued", Uname: a.uname, Rights: granted, Prefix: a.prefix}, nil
}

// Request opens a Treq, answers it and seals the reply.
func (ts *TicketServer) Request(s *Server, tree Tree, env *Env, id uint32, raw []byte, f *Treq) ([]byte, Event, error) {
	var tk *Ticket
	if f.Ref != nil {
		if ts.Refs != nil {
			tk = ts.Refs.Get(*f.Ref, env.Now)
		}
		if tk == nil {
			return nil, Event{}, Error(ETicketRef)
		}
	} else {
		var err error
		if tk, err = OpenTicket(ts.Keys, f.Ticket); err != nil {
			return nil, Event{}, err
		}
	}
	if env.Now >= tk.Expires {
		return nil, Event{}, Error(ETicketExpired)
	}
	switch hybrid := tk.Rights&TicketHybrid != 0; {
	case hybrid && ts.accept()&pgauth.AcceptHybrid == 0:
		return nil, Event{}, Error(EClassicalRequired)
	case !hybrid && ts.accept()&pgauth.AcceptClassical == 0:
		return nil, Event{}, Error(EHybridRequired)
	}
	keys := KeysFrom(&tk.Master)
	plain, err := OpenTreq(&keys, raw, f)
	if err != nil {
		return nil, Event{}, err
	}
	// A ticket a request opened under is one the client holds: keep it,
	// so the client may send its reference from now on.
	if f.Ref == nil && ts.Refs != nil {
		ts.Refs.Put(Tref(f.Ticket), tk, env.Now)
	}
	t, ok, op, derr := DecodeRequest(plain)
	if !ok || absDiff(t, env.Now) > s.Config.Window {
		return nil, Event{}, Error(ERequestExpiry)
	}
	who := &Who{Uname: tk.Uname, Rights: tk.Rights, Prefix: tk.Prefix, Owner: true}
	reply, name, p, ename := s.Decoded(tree, who, op, derr)
	var n [NonceLen]byte
	env.Random(n[:])
	return RreqFrame(&keys, id, &f.Nonce, &n, reply), Event{Kind: "answered", Uname: tk.Uname, Op: name, Path: p, Error: ename}, nil
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}
