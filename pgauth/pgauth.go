// Package pgauth is Purgatorio's own authentication, beside Inferno's
// keyring (sdk/keyring): keys, certificates, key lists, the mount
// handshake, the record layer that protects a conversation after it, and
// the server's proof (docs/auth.md).
//
// It has two families of algorithms:
//
//   - classical: Ed25519 keys, pgcert1 certificates and the version 3
//     handshake (X25519). These are byte-compatible with the Rust
//     sibling's purg-auth crate, for interoperation with Rust peers.
//   - hybrid, the default: a key is an Ed25519 key and an ML-DSA-65 key,
//     and a signature is both signatures (both must verify); pgcert2
//     certificates; the version 4 handshake, whose ephemeral exchange is
//     X25519 and ML-KEM-768. It holds if either half does. These formats
//     are this package's own: the Rust sibling's later hybrid formats
//     differ (docs/auth.md, "The Rust sibling's hybrid formats").
//
// Both families share the record layer (ChaCha20-Poly1305) and the file
// formats' shape. The package is portable: the kernel's mount and export
// use it, and so do the guests.
package pgauth

import (
	"crypto/ed25519"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/sha256"
)

// Err is an error string as a server or client reports it.
type Err string

func (e Err) Error() string { return string(e) }

// The errors of docs/auth.md. The first group is the Rust sibling's,
// worded the same; the hybrid ones are this implementation's own.
const (
	ENotRequired  Err = "authentication not required"
	ERequired     Err = "authentication required"
	EFailed       Err = "authentication failed"
	ENoKey        Err = "no key"
	EExpired      Err = "certificate expired"
	ERevoked      Err = "certificate revoked"
	EUntrusted    Err = "untrusted signer"
	ENameMismatch Err = "certificate name mismatch"
	ENoSession    Err = "no session key offered"
	EServerNone   Err = "server did not authenticate"
	EServerFailed Err = "server authentication failed"
	EEncryption   Err = "encryption required"
	EEncrypted    Err = "conversation is encrypted"
	ENoTrust      Err = "server trusts no keys"

	// A client that uses the hybrid handshake found a server that offers
	// only the classical one (an older or a Rust server).
	ENoHybrid Err = "no hybrid key offered"
	// A server that takes only the hybrid handshake got a classical
	// proof (version 1, 2 or 3).
	EHybridRequired Err = "hybrid authentication required"
	// A server that takes only the classical handshake got a hybrid
	// proof.
	EClassicalRequired Err = "classical authentication required"

	EBadCert    Err = "bad certificate"
	EBadName    Err = "bad user name"
	EDupSig     Err = "already signed by that key"
	ETooMany    Err = "too many signatures"
	EBadInfo    Err = "bad authinfo"
	EBadKey     Err = "bad key"
	EWrongAlg   Err = "key of the wrong algorithm"
	EBadPublic  Err = "bad public key"
	EBadSession Err = "bad session key"
)

// Sizes of classical (Ed25519) keys and signatures.
const (
	SeedSize      = ed25519.SeedSize      // 32
	PublicSize    = ed25519.PublicKeySize // 32
	SignatureSize = ed25519.SignatureSize // 64
)

// Sizes of hybrid keys and signatures: the Ed25519 part first, then the
// ML-DSA-65 part.
const (
	HybridSeedSize      = SeedSize + mldsa.PrivateKeySize            // 64
	HybridPublicSize    = PublicSize + mldsa.MLDSA65PublicKeySize    // 1984
	HybridSignatureSize = SignatureSize + mldsa.MLDSA65SignatureSize // 3373
)

const (
	hybridContext      = "pghybrid1\x00" // prefixes what both halves of a hybrid signature sign
	fingerprintContext = "pgkey2\x00"    // prefixes a hybrid key's fingerprint
)

// PrivateKey is a signing key: Ed25519 alone (classical) or Ed25519 and
// ML-DSA-65 (hybrid).
type PrivateKey struct {
	seed []byte
	ed   ed25519.PrivateKey
	ml   *mldsa.PrivateKey
	pub  PublicKey
}

// NewPrivateKey makes a key from its seed: 32 bytes for a classical key
// (the Rust sibling's seed files), 64 for a hybrid one (the Ed25519 seed,
// then the ML-DSA-65 seed).
func NewPrivateKey(seed []byte) (*PrivateKey, error) {
	switch len(seed) {
	case SeedSize:
		ed := ed25519.NewKeyFromSeed(seed)
		return &PrivateKey{seed: clone(seed), ed: ed, pub: PublicKey(clone(ed[SeedSize:]))}, nil
	case HybridSeedSize:
		ed := ed25519.NewKeyFromSeed(seed[:SeedSize])
		ml, err := mldsa.NewPrivateKey(mldsa.MLDSA65(), seed[SeedSize:])
		if err != nil {
			return nil, EBadKey
		}
		pub := make([]byte, 0, HybridPublicSize)
		pub = append(pub, ed[SeedSize:]...)
		pub = append(pub, ml.PublicKey().Bytes()...)
		return &PrivateKey{seed: clone(seed), ed: ed, ml: ml, pub: PublicKey(pub)}, nil
	}
	return nil, EBadKey
}

// GenerateKey makes a new random key, hybrid unless classical is set.
func GenerateKey(hybrid bool) (*PrivateKey, error) {
	n := SeedSize
	if hybrid {
		n = HybridSeedSize
	}
	seed := make([]byte, n)
	rand.Read(seed)
	return NewPrivateKey(seed)
}

// Seed is what a seed file holds.
func (k *PrivateKey) Seed() []byte { return clone(k.seed) }

// Public is the key's public half.
func (k *PrivateKey) Public() PublicKey { return k.pub }

// Hybrid reports whether the key is hybrid.
func (k *PrivateKey) Hybrid() bool { return k.ml != nil }

// Sign signs msg: an Ed25519 signature for a classical key; for a hybrid
// key the Ed25519 and the ML-DSA-65 signature, both over hybridContext
// and msg. Both are deterministic, so the same key and message give the
// same bytes on every host (module signatures depend on it).
func (k *PrivateKey) Sign(msg []byte) []byte {
	if k.ml == nil {
		return ed25519.Sign(k.ed, msg)
	}
	m := hybridMessage(msg)
	sig := make([]byte, 0, HybridSignatureSize)
	sig = append(sig, ed25519.Sign(k.ed, m)...)
	ml, err := k.ml.SignDeterministic(m, nil)
	if err != nil {
		panic("pgauth: ML-DSA signing failed: " + err.Error())
	}
	return append(sig, ml...)
}

func hybridMessage(msg []byte) []byte {
	m := make([]byte, 0, len(hybridContext)+len(msg))
	m = append(m, hybridContext...)
	return append(m, msg...)
}

// PublicKey is a public key: 32 bytes (Ed25519) or HybridPublicSize
// bytes (Ed25519, then ML-DSA-65).
type PublicKey []byte

// ParsePublicKey checks the length and, for a hybrid key, the ML-DSA
// encoding.
func ParsePublicKey(b []byte) (PublicKey, error) {
	switch len(b) {
	case PublicSize:
		return PublicKey(clone(b)), nil
	case HybridPublicSize:
		if _, err := mldsa.NewPublicKey(mldsa.MLDSA65(), b[PublicSize:]); err != nil {
			return nil, EBadPublic
		}
		return PublicKey(clone(b)), nil
	}
	return nil, EBadPublic
}

// Hybrid reports whether p is a hybrid key.
func (p PublicKey) Hybrid() bool { return len(p) == HybridPublicSize }

// Valid reports whether p has one of the two lengths.
func (p PublicKey) Valid() bool { return len(p) == PublicSize || len(p) == HybridPublicSize }

// Equal compares two keys.
func (p PublicKey) Equal(q PublicKey) bool { return string(p) == string(q) }

// Classical is the Ed25519 half of a key (all of a classical one).
func (p PublicKey) Classical() PublicKey {
	if len(p) < PublicSize {
		return nil
	}
	return p[:PublicSize]
}

// ID is what key lists name the key by: a classical key itself, or a
// hybrid key's fingerprint, SHA-256 of fingerprintContext and the key.
func (p PublicKey) ID() KeyID {
	if p.Hybrid() {
		h := sha256.New()
		h.Write([]byte(fingerprintContext))
		h.Write(p)
		var id KeyID
		id.Hybrid = true
		h.Sum(id.Sum[:0])
		return id
	}
	var id KeyID
	copy(id.Sum[:], p)
	return id
}

// Verify reports whether sig is p's signature over msg. A hybrid
// signature verifies only when both halves do.
func (p PublicKey) Verify(msg, sig []byte) bool {
	switch {
	case len(p) == PublicSize && len(sig) == SignatureSize:
		return ed25519.Verify(ed25519.PublicKey(p), msg, sig)
	case len(p) == HybridPublicSize && len(sig) == HybridSignatureSize:
		m := hybridMessage(msg)
		if !ed25519.Verify(ed25519.PublicKey(p[:PublicSize]), m, sig[:SignatureSize]) {
			return false
		}
		pk, err := mldsa.NewPublicKey(mldsa.MLDSA65(), p[PublicSize:])
		if err != nil {
			return false
		}
		return mldsa.Verify(pk, m, sig[SignatureSize:], nil) == nil
	}
	return false
}

// SignatureLen is the length of a signature by p.
func (p PublicKey) SignatureLen() int {
	if p.Hybrid() {
		return HybridSignatureSize
	}
	return SignatureSize
}

// KeyID names a key in a key list: a classical key's 32 bytes, or a
// hybrid key's fingerprint. A revocation list also holds module digests
// as classical IDs.
type KeyID struct {
	Hybrid bool
	Sum    [32]byte
}

// String is the ID as a key list writes it: 64 hex digits, prefixed with
// "pq:" for a hybrid key.
func (id KeyID) String() string {
	if id.Hybrid {
		return HybridPrefix + Hex(id.Sum[:])
	}
	return Hex(id.Sum[:])
}

// HybridPrefix marks a hybrid key's fingerprint in a key list.
const HybridPrefix = "pq:"

// ParseKeyID reads an ID as String writes it.
func ParseKeyID(s string) (KeyID, bool) {
	var id KeyID
	if len(s) > len(HybridPrefix) && s[:len(HybridPrefix)] == HybridPrefix {
		id.Hybrid = true
		s = s[len(HybridPrefix):]
	}
	b, ok := hexArray(s, 32)
	if !ok {
		return KeyID{}, false
	}
	copy(id.Sum[:], b)
	return id, true
}

// DigestID is the revocation list's name for a module or image digest.
func DigestID(d [32]byte) KeyID { return KeyID{Sum: d} }

func clone(b []byte) []byte { return append([]byte(nil), b...) }
