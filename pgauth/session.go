package pgauth

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
)

// The session keys a version 3 or 4 handshake agrees (docs/auth.md).
//
// Version 3 (the Rust sibling's): each side makes an ephemeral X25519 key;
// the server offers its public half with the challenge, the client sends
// its own in the proof, and
//
//	shared = X25519(s, C)                       all zeros refused
//	prk    = HKDF-Extract(SHA-256(message), shared)
//	c2s    = HKDF-Expand(prk, "pgauth3 c2s", 32)
//	s2c    = HKDF-Expand(prk, "pgauth3 s2c", 32)
//	bind   = HKDF-Expand(prk, "pgauth3 server", 32)
//
// Version 4 adds ML-KEM-768. The server's offer also carries an
// ephemeral encapsulation key; the client encapsulates to it and sends
// the ciphertext in the proof beside its X25519 key. Both shared secrets
// go into one HKDF, ML-KEM's first as in TLS's X25519MLKEM768, and the
// salt is the hash of the signed message, which fixes both public
// X25519 keys, the encapsulation key and the ciphertext:
//
//	prk    = HKDF-Extract(SHA-256(message), ss_mlkem || ss_x25519)
//	c2s    = HKDF-Expand(prk, "pgauth4 c2s", 32)
//	s2c    = HKDF-Expand(prk, "pgauth4 s2c", 32)
//	bind   = HKDF-Expand(prk, "pgauth4 server", 32)
//
// The session keys are secret as long as either exchange is.

// Keys are the two directions' record keys, and what a server's proof
// signs.
type Keys struct {
	Version    int
	C2S, S2C   [32]byte
	Transcript [32]byte // SHA-256 of the client's signed message
	Bind       [32]byte // only the two ends of the exchange can compute it
}

// DeriveKeys derives the keys of a version 3 (ikm the X25519 secret) or
// version 4 (ikm the ML-KEM secret then the X25519 secret) exchange.
func DeriveKeys(version int, ikm, message []byte) *Keys {
	label := "pgauth3 "
	if version == 4 {
		label = "pgauth4 "
	}
	k := &Keys{Version: version, Transcript: sha256.Sum256(message)}
	prk, err := hkdf.Extract(sha256.New, ikm, k.Transcript[:])
	if err != nil {
		panic(err)
	}
	for _, o := range []struct {
		dst  *[32]byte
		info string
	}{{&k.C2S, "c2s"}, {&k.S2C, "s2c"}, {&k.Bind, "server"}} {
		b, err := hkdf.Expand(sha256.New, prk, label+o.info, 32)
		if err != nil {
			panic(err)
		}
		copy(o.dst[:], b)
	}
	return k
}

// Client returns the client's ends: it seals with c2s, opens with s2c.
func (k *Keys) Client() (*Sealer, *Opener) { return NewSealer(k.C2S[:]), NewOpener(k.S2C[:]) }

// Server returns the server's ends.
func (k *Keys) Server() (*Sealer, *Opener) { return NewSealer(k.S2C[:]), NewOpener(k.C2S[:]) }

// Session is the 64 bytes a server proof's signer is given: the
// transcript, then the bound value.
func (k *Keys) Session() []byte {
	b := make([]byte, 0, 64)
	b = append(b, k.Transcript[:]...)
	return append(b, k.Bind[:]...)
}

func x25519(priv *ecdh.PrivateKey, peer []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(peer)
	if err != nil {
		return nil, EBadSession
	}
	s, err := priv.ECDH(pub) // fails for an all-zero result
	if err != nil {
		return nil, EBadSession
	}
	return s, nil
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// Server is the server's side of one exchange.
type Server struct {
	x     *ecdh.PrivateKey
	kem   *mlkem.DecapsulationKey768 // nil: classical only
	offer []byte
}

// NewServer makes a fresh exchange. A hybrid server offers Offer4Size
// bytes; its first Offer3Size are a classical offer, so one offer serves
// a server that takes both. A classical server offers Offer3Size bytes.
func NewServer(hybrid bool) *Server {
	var seed []byte
	if hybrid {
		seed = random(mlkem.SeedSize)
	}
	s, err := NewServerFrom(random(ChallengeSize), random(32), seed)
	if err != nil {
		panic(err)
	}
	return s
}

// NewServerFrom is NewServer from given randomness, for tests: the
// challenge, the X25519 secret, and the ML-KEM seed (nil for classical).
func NewServerFrom(challenge, xsecret, kemSeed []byte) (*Server, error) {
	x, err := ecdh.X25519().NewPrivateKey(xsecret)
	if err != nil || len(challenge) != ChallengeSize {
		return nil, EBadSession
	}
	s := &Server{x: x}
	s.offer = append(append([]byte(nil), challenge...), x.PublicKey().Bytes()...)
	if kemSeed != nil {
		if s.kem, err = mlkem.NewDecapsulationKey768(kemSeed); err != nil {
			return nil, EBadSession
		}
		s.offer = append(s.offer, s.kem.EncapsulationKey().Bytes()...)
	}
	return s, nil
}

// Offer is what the auth fid reads.
func (s *Server) Offer() []byte { return s.offer }

// Hybrid reports whether the server offers the hybrid exchange.
func (s *Server) Hybrid() bool { return s.kem != nil }

// Keys are the session keys for a version 3 or 4 proof written for this
// offer. It does not check the proof: Check does.
func (s *Server) Keys(uname, aname string, proof []byte) (*Keys, error) {
	p, msg, ok := Transcript(s.offer, uname, aname, proof)
	if !ok || (p.Version == 4 && s.kem == nil) {
		return nil, EBadSession
	}
	xs, err := x25519(s.x, p.ClientKey)
	if err != nil {
		return nil, err
	}
	if p.Version == 3 {
		return DeriveKeys(3, xs, msg), nil
	}
	ks, err := s.kem.Decapsulate(p.Ciphertext)
	if err != nil {
		return nil, EBadSession
	}
	return DeriveKeys(4, append(ks, xs...), msg), nil
}

// Client is the client's side of one exchange.
type Client struct {
	x      *ecdh.PrivateKey
	shared []byte // ML-KEM's secret, then X25519's (version 4), or X25519's
	signed []byte
}

// NewClient answers the server's offer: with the hybrid exchange when
// hybrid is set, which needs a hybrid offer (else ENoHybrid), or with the
// classical one, which needs Offer3Size bytes (else ENoSession). It never
// falls back from one to the other.
func NewClient(offer []byte, hybrid bool) (*Client, error) {
	return newClient(offer, hybrid, random(32), nil)
}

// NewClientFrom is NewClient from a given X25519 secret, for tests of the
// classical exchange (the hybrid one encapsulates with fresh randomness).
func NewClientFrom(offer []byte, hybrid bool, xsecret []byte) (*Client, error) {
	return newClient(offer, hybrid, xsecret, nil)
}

func newClient(offer []byte, hybrid bool, xsecret []byte, encap func(*mlkem.EncapsulationKey768) ([]byte, []byte)) (*Client, error) {
	if hybrid && len(offer) < Offer4Size {
		return nil, ENoHybrid
	}
	if len(offer) < Offer3Size {
		return nil, ENoSession
	}
	x, err := ecdh.X25519().NewPrivateKey(xsecret)
	if err != nil {
		return nil, EBadSession
	}
	xs, err := x25519(x, offer[ChallengeSize:Offer3Size])
	if err != nil {
		return nil, err
	}
	c := &Client{x: x}
	if !hybrid {
		c.shared = xs
		c.signed = append(append([]byte(nil), offer[:Offer3Size]...), x.PublicKey().Bytes()...)
		return c, nil
	}
	ek, err := mlkem.NewEncapsulationKey768(offer[Offer3Size:Offer4Size])
	if err != nil {
		return nil, EBadSession
	}
	var ks, ct []byte
	if encap != nil {
		ks, ct = encap(ek)
	} else {
		ks, ct = ek.Encapsulate()
	}
	c.shared = append(ks, xs...)
	c.signed = make([]byte, 0, Signed4Size)
	c.signed = append(c.signed, offer[:Offer4Size]...)
	c.signed = append(c.signed, x.PublicKey().Bytes()...)
	c.signed = append(c.signed, ct...)
	return c, nil
}

// Signed is what the client's proof signs.
func (c *Client) Signed() []byte { return c.signed }

// Hybrid reports whether this is a version 4 exchange.
func (c *Client) Hybrid() bool { return len(c.signed) == Signed4Size }

// Keys are the session keys once proof, made over Signed, is sent.
func (c *Client) Keys(uname, aname string, proof []byte) (*Keys, error) {
	off := Offer3Size
	if c.Hybrid() {
		off = Offer4Size
	}
	p, msg, ok := Transcript(c.signed[:off], uname, aname, proof)
	if !ok {
		return nil, EBadSession
	}
	s, _ := p.signedFor(c.signed[:off])
	if string(s) != string(c.signed) {
		return nil, EBadSession
	}
	return DeriveKeys(p.Version, c.shared, msg), nil
}
