package pgauth

import (
	"crypto/mlkem"
	"encoding/binary"
)

// Mount proofs. After Tauth the client reads the server's offer from the
// auth fid and writes a proof (docs/auth.md):
//
//   - version 1, exactly V1Len bytes: signature[64] || key[32] over the
//     raw challenge;
//   - version 2: 0x02 || kind || signature[64] || body;
//   - version 3: 0x03 || kind || signature[64] || C[32] || body;
//   - version 4 (hybrid): 0x04 || len u32 LE || kind ||
//     signature[3373] || C[32] || ct[1088] || body.
//
// The body is a public key (kind 0) or a certificate line (kind 1).
// Versions 1 to 3 are the Rust sibling's, byte for byte; version 4 is
// this implementation's.
//
// What the client signs is the "signed" bytes: the server's challenge
// (version 2), Signed3Size bytes (the server's 64-byte offer and the
// client's X25519 key) for version 3, or Signed4Size bytes (the 1248-byte
// offer, the client's X25519 key and the ML-KEM ciphertext) for version
// 4. Message, Assemble and MakeProof pick the version from that length.
const (
	KindKey  = 0
	KindCert = 1

	ChallengeSize = 32
	V1Len         = SignatureSize + PublicSize // 96

	// The classical offer and what a version 3 proof signs.
	Offer3Size  = ChallengeSize + PublicSize // 64
	Signed3Size = Offer3Size + PublicSize    // 96

	// The hybrid offer: challenge, X25519 key, ML-KEM-768 encapsulation
	// key; and what a version 4 proof signs.
	Offer4Size  = ChallengeSize + PublicSize + mlkem.EncapsulationKeySize768 // 1248
	Signed4Size = Offer4Size + PublicSize + mlkem.CiphertextSize768          // 2368

	// Proofs are at most this long: the Rust limit for versions 2 and 3,
	// and room for a pgcert2 with every signature for version 4.
	MaxProof3 = 2048
	MaxProof4 = 96 << 10

	v4Fixed = 1 + 4 + 1 + HybridSignatureSize + PublicSize + mlkem.CiphertextSize768
)

// Context strings of the signed messages.
const (
	context2 = "pgauth2\x00"
	context3 = "pgauth3\x00"
	context4 = "pgauth4\x00"
)

// Message is what a version 2, 3 or 4 proof signs, by the length of
// signed (the challenge, or the Signed3Size or Signed4Size bytes).
func Message(signed []byte, uname, aname string, kind byte, body []byte) []byte {
	ctx := context2
	switch len(signed) {
	case Signed3Size:
		ctx = context3
	case Signed4Size:
		ctx = context4
	}
	m := make([]byte, 0, len(ctx)+len(signed)+5+len(uname)+len(aname)+len(body))
	m = append(m, ctx...)
	m = append(m, signed...)
	for _, s := range []string{uname, aname} {
		n := min(len(s), 0xffff)
		m = binary.LittleEndian.AppendUint16(m, uint16(n))
		m = append(m, s[:n]...)
	}
	m = append(m, kind)
	return append(m, body...)
}

// ProofBody is a proof's kind and body: the certificate when there is
// one, else the key.
func ProofBody(cert *Cert, pub PublicKey) (byte, []byte) {
	if cert != nil {
		return KindCert, []byte(cert.Encode())
	}
	return KindKey, clone(pub)
}

// Assemble builds a proof from its parts, by the length of signed.
func Assemble(signed []byte, kind byte, sig, body []byte) []byte {
	switch len(signed) {
	case Signed3Size:
		p := make([]byte, 0, 2+len(sig)+PublicSize+len(body))
		p = append(p, 3, kind)
		p = append(p, sig...)
		p = append(p, signed[Offer3Size:]...)
		return append(p, body...)
	case Signed4Size:
		n := 1 + 4 + 1 + len(sig) + PublicSize + mlkem.CiphertextSize768 + len(body)
		p := make([]byte, 0, n)
		p = append(p, 4)
		p = binary.LittleEndian.AppendUint32(p, uint32(n))
		p = append(p, kind)
		p = append(p, sig...)
		p = append(p, signed[Offer4Size:]...)
		return append(p, body...)
	}
	p := make([]byte, 0, 2+len(sig)+len(body))
	p = append(p, 2, kind)
	p = append(p, sig...)
	return append(p, body...)
}

// MakeProof is a proof signed with k, carrying cert when given: version
// 4 when signed is Signed4Size bytes (k must be hybrid), 3 for
// Signed3Size, else version 2 (k must be classical for both).
func MakeProof(k *PrivateKey, cert *Cert, signed []byte, uname, aname string) ([]byte, error) {
	if k.Hybrid() != (len(signed) == Signed4Size) {
		return nil, EWrongAlg
	}
	if cert != nil && !cert.Key.Equal(k.Public()) {
		return nil, EBadCert
	}
	kind, body := ProofBody(cert, k.Public())
	sig := k.Sign(Message(signed, uname, aname, kind, body))
	return Assemble(signed, kind, sig, body), nil
}

// MakeProofV1 is a version 1 proof: a classical signature over the raw
// challenge and the key.
func MakeProofV1(k *PrivateKey, challenge []byte) []byte {
	p := k.Sign(challenge)
	return append(p, k.Public()...)
}

// Proof is a version 2, 3 or 4 proof taken apart.
type Proof struct {
	Version    int
	Kind       byte
	Sig        []byte
	ClientKey  []byte // version 3 and 4: the client's X25519 key
	Ciphertext []byte // version 4: the ML-KEM ciphertext
	Body       []byte
}

// ProofLen is, for the first bytes of a version 4 proof, its whole
// length, so a server that takes it in several writes knows when it has
// all of it. It returns 0 when the bytes are not the start of one.
func ProofLen(head []byte) int {
	if len(head) < 5 || head[0] != 4 {
		return 0
	}
	n := int(binary.LittleEndian.Uint32(head[1:5]))
	if n < v4Fixed+1 || n > MaxProof4 {
		return 0
	}
	return n
}

// ParseProof splits a version 2, 3 or 4 proof.
func ParseProof(p []byte) (*Proof, bool) {
	if len(p) == 0 {
		return nil, false
	}
	switch p[0] {
	case 2, 3:
		if len(p) > MaxProof3 || len(p) < 2+SignatureSize {
			return nil, false
		}
		pr := &Proof{Version: int(p[0]), Kind: p[1], Sig: p[2 : 2+SignatureSize]}
		rest := p[2+SignatureSize:]
		if pr.Version == 3 {
			if len(rest) <= PublicSize {
				return nil, false
			}
			pr.ClientKey, rest = rest[:PublicSize], rest[PublicSize:]
		}
		pr.Body = rest
		return pr, true
	case 4:
		if ProofLen(p) != len(p) {
			return nil, false
		}
		pr := &Proof{Version: 4, Kind: p[5]}
		rest := p[6:]
		pr.Sig, rest = rest[:HybridSignatureSize], rest[HybridSignatureSize:]
		pr.ClientKey, rest = rest[:PublicSize], rest[PublicSize:]
		pr.Ciphertext, pr.Body = rest[:mlkem.CiphertextSize768], rest[mlkem.CiphertextSize768:]
		return pr, len(pr.Body) > 0
	}
	return nil, false
}

// signedFor is the signed bytes of a version 3 or 4 proof for offer.
func (p *Proof) signedFor(offer []byte) ([]byte, bool) {
	switch p.Version {
	case 3:
		if len(offer) < Offer3Size {
			return nil, false
		}
		s := make([]byte, 0, Signed3Size)
		s = append(s, offer[:Offer3Size]...)
		return append(s, p.ClientKey...), true
	case 4:
		if len(offer) < Offer4Size {
			return nil, false
		}
		s := make([]byte, 0, Signed4Size)
		s = append(s, offer[:Offer4Size]...)
		s = append(s, p.ClientKey...)
		return append(s, p.Ciphertext...), true
	}
	return nil, false
}

// Transcript is the message a version 3 or 4 proof signs, given the
// server's offer. Both sides hash it into the session keys.
func Transcript(offer []byte, uname, aname string, proof []byte) (*Proof, []byte, bool) {
	p, ok := ParseProof(proof)
	if !ok {
		return nil, nil, false
	}
	s, ok := p.signedFor(offer)
	if !ok {
		return nil, nil, false
	}
	return p, Message(s, uname, aname, p.Kind, p.Body), true
}

// Accept is the set of handshakes a server takes or a client uses.
type Accept uint8

const (
	// AcceptHybrid is the version 4 handshake.
	AcceptHybrid Accept = 1 << iota
	// AcceptClassical is versions 1 to 3, the Rust sibling's.
	AcceptClassical
)

// Policy is what a server trusts.
type Policy struct {
	Keys    []KeyID // keys it holds directly
	Signers []KeyID // trusted signers
	Revoked []KeyID
	// Damaged says the revocation list did not parse or could not be
	// read: every certificate then fails.
	Damaged bool
	Now     uint64 // seconds since 1970 UTC
	// Accept is the handshakes taken; 0 means AcceptHybrid.
	Accept Accept
}

func (p *Policy) accept() Accept {
	if p.Accept == 0 {
		return AcceptHybrid
	}
	return p.Accept
}

// RequiresAuth reports whether the policy can accept anything at all.
func (p *Policy) RequiresAuth() bool { return len(p.Keys) > 0 || len(p.Signers) > 0 }

// revoked reports whether k is in the revocation list: its ID, or for a
// hybrid key also its Ed25519 half.
func (p *Policy) revoked(k PublicKey) bool {
	if Contains(p.Revoked, k.ID()) {
		return true
	}
	return k.Hybrid() && Contains(p.Revoked, k.Classical().ID())
}

func (p *Policy) revokedOrDamaged(k PublicKey) bool { return p.Damaged || p.revoked(k) }

// Check checks proof against the policy for this Tauth, and returns the
// error the write fails with. offer is what the server gave on the auth
// fid: a bare challenge, the 64-byte classical offer, or the hybrid
// offer, whose first 64 bytes are a classical one.
func Check(pol *Policy, offer []byte, uname, aname string, proof []byte) error {
	acc := pol.accept()
	if len(proof) != V1Len && len(proof) > 0 && proof[0] == 4 {
		if acc&AcceptHybrid == 0 {
			return EClassicalRequired
		}
	} else if acc&AcceptClassical == 0 {
		return EHybridRequired
	}
	challenge := offer[:min(len(offer), ChallengeSize)]
	if len(proof) == V1Len {
		key := PublicKey(proof[SignatureSize:])
		if !Contains(pol.Keys, key.ID()) || !key.Verify(challenge, proof[:SignatureSize]) {
			return EFailed
		}
		if pol.revoked(key) {
			return ERevoked
		}
		return nil
	}
	p, ok := ParseProof(proof)
	if !ok {
		return EFailed
	}
	var msg []byte
	if p.Version == 2 {
		msg = Message(challenge, uname, aname, p.Kind, p.Body)
	} else {
		s, ok := p.signedFor(offer)
		if !ok {
			return EFailed
		}
		msg = Message(s, uname, aname, p.Kind, p.Body)
	}
	hybrid := p.Version == 4
	switch p.Kind {
	case KindKey:
		key := PublicKey(p.Body)
		if key.Hybrid() != hybrid || !key.Valid() || !Contains(pol.Keys, key.ID()) || !key.Verify(msg, p.Sig) {
			return EFailed
		}
		if pol.revoked(key) {
			return ERevoked
		}
		return nil
	case KindCert:
		c, err := ParseCert(p.Body)
		if err != nil || c.Hybrid() != hybrid || !c.Key.Verify(msg, p.Sig) {
			return EFailed
		}
		return CheckCert(pol, c, uname, true)
	}
	return EFailed
}

// CheckCert checks a certificate against the policy, as a server does
// once the proof's signature verifies. With checkName set, its name must
// be uname.
func CheckCert(pol *Policy, c *Cert, uname string, checkName bool) error {
	if pol.revokedOrDamaged(c.Key) {
		return ERevoked
	}
	if Contains(pol.Keys, c.Key.ID()) {
		return nil
	}
	if checkName && uname != c.Name {
		return ENameMismatch
	}
	if c.Expired(pol.Now) {
		return EExpired
	}
	trusted := false
	for _, s := range c.Sigs {
		id := s.Signer.ID()
		if Contains(pol.Signers, id) && c.SignedBy(id) {
			if !pol.revoked(s.Signer) {
				return nil
			}
			trusted = true
		}
	}
	if trusted {
		return ERevoked
	}
	return EUntrusted
}
