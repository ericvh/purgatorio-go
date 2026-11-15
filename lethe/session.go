package lethe

import (
	"crypto/mlkem"
	"sync/atomic"

	"github.com/ericvh/purgatorio-go/pgauth"
)

// The client's sealed side: the ticket exchange and sealed requests.
// Nothing here keeps a connection: a Session is only the ticket and its
// keys, so a request can go out on any connection, to any replica.

// RenewMargin: a ticket is renewed this many seconds before it expires.
const RenewMargin = 5

// Creds are what a client signs with.
type Creds struct {
	User string
	Key  *pgauth.PrivateKey
	// Cert is a certificate for Key, if the client has one.
	Cert *pgauth.Cert
}

// Session is a ticket and what goes with it.
type Session struct {
	Ticket []byte
	Keys   Keys
	// Skew is the server's clock minus the client's, in seconds.
	Skew    int64
	Expires uint64
	Rights  uint8
	Prefix  string
	// refs is whether the server offered references (RightRefs in the
	// grant), tref the ticket's reference, and held whether the server
	// has answered a request carrying the ticket, so requests send the
	// reference from then on (cleared on ETicketRef).
	refs bool
	tref [TrefLen]byte
	held atomic.Bool
}

// AuthPending is an exchange waiting for its Rauth or Rauth2.
type AuthPending struct {
	id     uint32
	hybrid bool
	eph    *Ephemeral
	dk     *mlkem.DecapsulationKey768
	signed []byte
}

func proofBody(c *Creds) (uint8, []byte) {
	pub := c.Key.Public()
	if c.Cert != nil && c.Cert.Key.Equal(pub) {
		return pgauth.KindCert, []byte(c.Cert.Encode())
	}
	return pgauth.KindKey, pub
}

// AuthFrame starts the ticket exchange: the classical Tauth (the Rust
// one) or, with hybrid, the Tauth2. random fills the ephemeral secrets.
// The key must be of the exchange's kind.
func AuthFrame(c *Creds, id uint32, rights uint8, prefix string, hybrid bool, random func([]byte)) ([]byte, *AuthPending, error) {
	if c.Key.Hybrid() != hybrid {
		return nil, nil, pgauth.EWrongAlg
	}
	var r [32]byte
	random(r[:])
	eph := NewEphemeral(r)
	kind, body := proofBody(c)
	p := &AuthPending{id: id, hybrid: hybrid, eph: eph}
	if !hybrid {
		t := &Tauth{Eph: eph.Public, Rights: rights, Prefix: prefix, Uname: c.User, Kind: kind, Body: body}
		p.signed = t.Message()
		copy(t.Signature[:], c.Key.Sign(p.signed))
		return Encode(id, t), p, nil
	}
	seed := make([]byte, mlkem.SeedSize)
	random(seed)
	dk, err := mlkem.NewDecapsulationKey768(seed)
	if err != nil {
		return nil, nil, err
	}
	p.dk = dk
	t := &Tauth2{Eph: eph.Public, Rights: rights, Prefix: prefix, Uname: c.User, Kind: kind, Body: body}
	copy(t.EK[:], dk.EncapsulationKey().Bytes())
	p.signed = t.Message()
	t.Signature = c.Key.Sign(p.signed)
	return Encode(id, t), p, nil
}

// Finish makes the session from the server's reply; local is the
// client's clock in seconds. An Rerror gives its string.
func (p *AuthPending) Finish(raw []byte, local uint64) (*Session, error) {
	id, f, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	if id != p.id {
		return nil, Error(EMessage)
	}
	var master [32]byte
	var keys Keys
	var plain []byte
	switch f := f.(type) {
	case *Rerror:
		// A server that does not know the hybrid exchange refuses its
		// frame as a bad message.
		if p.hybrid && f.Ename == EMessage {
			return nil, Error(ENoHybrid)
		}
		return nil, Error(f.Ename)
	case *Rauth:
		if p.hybrid {
			return nil, Error(EMessage)
		}
		shared, ok := p.eph.Shared(f.Eph)
		if !ok {
			return nil, Error(EAuthFailed)
		}
		master = Master(shared, append(append([]byte{}, p.signed...), f.Eph[:]...))
		keys = KeysFrom(&master)
		if plain, err = OpenRauth(&keys, raw, f); err != nil {
			return nil, err
		}
	case *Rauth2:
		if !p.hybrid {
			return nil, Error(EMessage)
		}
		xs, ok := p.eph.Shared(f.Eph)
		if !ok {
			return nil, Error(EAuthFailed)
		}
		ks, err := p.dk.Decapsulate(f.CT[:])
		if err != nil {
			return nil, Error(EAuthFailed)
		}
		tr := append(append(append([]byte{}, p.signed...), f.Eph[:]...), f.CT[:]...)
		master = Master2(ks, xs, tr)
		keys = KeysFrom(&master)
		if plain, err = OpenRauth2(&keys, raw, f); err != nil {
			return nil, err
		}
	default:
		return nil, Error(EMessage)
	}
	g, err := DecodeGrant(plain)
	if err != nil {
		return nil, err
	}
	if !ValidPath(g.Prefix) {
		return nil, Error(EMessage)
	}
	return &Session{Ticket: g.Ticket, Keys: keys, Skew: int64(g.Now) - int64(local),
		Expires: g.Expires, Rights: g.Rights, Prefix: g.Prefix,
		refs: g.Rights&RightRefs != 0, tref: Tref(g.Ticket)}, nil
}

// ServerTime is the client's estimate of the server's clock.
func (s *Session) ServerTime(local uint64) uint64 {
	t := int64(local) + s.Skew
	if t < 0 {
		return 0
	}
	return uint64(t)
}

// Expiring reports whether the ticket should be renewed first.
func (s *Session) Expiring(local uint64) bool { return s.ServerTime(local)+RenewMargin >= s.Expires }

// ReqPending is a sealed request waiting for its reply.
type ReqPending struct {
	id    uint32
	nonce [NonceLen]byte
	byRef bool
}

// Request is a Treq for op, timed by local, sealed with nonce (random).
func (s *Session) Request(id uint32, local uint64, op Op, nonce [NonceLen]byte) ([]byte, *ReqPending) {
	plain := (&Request{Time: s.ServerTime(local), Op: op}).Encode()
	return s.Seal(id, plain, nonce)
}

// Seal is a Treq carrying plain, an encoded request, as it is; once the
// server holds the ticket, a Treqref with the ticket's reference.
func (s *Session) Seal(id uint32, plain []byte, nonce [NonceLen]byte) ([]byte, *ReqPending) {
	if s.ByRef() {
		return TreqrefFrame(&s.Keys, id, &s.tref, &nonce, plain), &ReqPending{id: id, nonce: nonce, byRef: true}
	}
	return TreqFrame(&s.Keys, id, s.Ticket, &nonce, plain), &ReqPending{id: id, nonce: nonce}
}

// ByRef reports whether requests now carry the ticket's reference.
func (s *Session) ByRef() bool { return s.refs && s.held.Load() }

// Reply opens the reply to p. The error is a *Failure.
func (s *Session) Reply(p *ReqPending, raw []byte) (Reply, error) {
	id, f, err := Decode(raw)
	if err != nil {
		return nil, &Failure{FailBad, err.Error()}
	}
	if id != p.id {
		return nil, &Failure{FailBad, EMessage}
	}
	switch f := f.(type) {
	case *Rreq:
		plain, err := OpenRreq(&s.Keys, raw, &p.nonce, f)
		if err != nil {
			return nil, &Failure{FailBad, err.Error()}
		}
		// A sealed answer to the ticket itself: the server holds it now.
		if s.refs && !p.byRef {
			s.held.Store(true)
		}
		r, err := DecodeReply(plain)
		if err != nil {
			return nil, &Failure{FailBad, err.Error()}
		}
		if e, ok := r.(*RError); ok {
			return nil, &Failure{FailSealed, e.Ename}
		}
		return r, nil
	case *Rerror:
		// The server let the ticket go: send it again next time.
		if p.byRef && f.Ename == ETicketRef {
			s.held.Store(false)
		}
		return nil, &Failure{FailClear, f.Ename}
	}
	return nil, &Failure{FailBad, EMessage}
}

// Client drives the exchange and sealed requests over a transport,
// getting a ticket when it has none and renewing it when the server
// refuses it.
type Client struct {
	Creds  *Creds
	Rights uint8
	Prefix string
	Hybrid bool
	// Session is the current ticket, if any.
	Session *Session
	// Tickets counts the tickets obtained, renewals included.
	Tickets int
	next    uint32
}

// Exchange moves one frame to the server and its reply back.
type Exchange func(frame []byte) ([]byte, error)

func (c *Client) id() uint32 {
	c.next++
	if c.next == 0 {
		c.next = 1
	}
	return c.next
}

// Authenticate gets a ticket.
func (c *Client) Authenticate(x Exchange, local uint64, random func([]byte)) error {
	f, p, err := AuthFrame(c.Creds, c.id(), c.Rights, c.Prefix, c.Hybrid, random)
	if err != nil {
		return err
	}
	r, err := x(f)
	if err != nil {
		return err
	}
	s, err := p.Finish(r, local)
	if err != nil {
		return err
	}
	c.Session = s
	c.Tickets++
	return nil
}

// Call sends op, getting or renewing the ticket as needed. now reads the
// client's clock in seconds. A server's answer is a *Failure.
func (c *Client) Call(x Exchange, op Op, now func() uint64, random func([]byte)) (Reply, error) {
	resent := false
	for attempt := 0; attempt < 2; attempt++ {
		if c.Session == nil || c.Session.Expiring(now()) {
			if err := c.Authenticate(x, now(), random); err != nil {
				return nil, err
			}
		}
		var n [NonceLen]byte
		random(n[:])
		f, p := c.Session.Request(c.id(), now(), op, n)
		r, err := x(f)
		if err != nil {
			return nil, err
		}
		rep, err := c.Session.Reply(p, r)
		// The server let the ticket go: resend with it, once.
		if f, ok := err.(*Failure); ok && f.Kind == FailClear && !resent && Resend(f.Text) {
			resent = true
			attempt--
			continue
		}
		if f, ok := err.(*Failure); ok && f.Kind == FailClear && attempt == 0 && Renew(f.Text) {
			c.Session = nil
			continue
		}
		return rep, err
	}
	return nil, Error(ETicket)
}
