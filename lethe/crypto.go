package lethe

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"

	"github.com/ericvh/purgatorio-go/libsec"
)

// The key schedule and the sealing of frames (the Rust crypto.rs).

const (
	labelMaster = "lethe1 master"
	labelC2S    = "lethe1 c2s"
	labelS2C    = "lethe1 s2c"
)

// Keys are a ticket's two directions' keys.
type Keys struct{ C2S, S2C [32]byte }

// Master is the session's master secret: HKDF-Extract with the SHA-256
// of the transcript as salt and the shared secret as input, expanded
// with "lethe1 master".
func Master(shared, transcript []byte) [32]byte {
	salt := sha256.Sum256(transcript)
	prk, _ := hkdf.Extract(sha256.New, shared, salt[:])
	m, _ := hkdf.Expand(sha256.New, prk, labelMaster, 32)
	return [32]byte(m)
}

// KeysFrom expands the master, which is already a pseudorandom key.
func KeysFrom(master *[32]byte) Keys {
	c, _ := hkdf.Expand(sha256.New, master[:], labelC2S, 32)
	s, _ := hkdf.Expand(sha256.New, master[:], labelS2C, 32)
	return Keys{C2S: [32]byte(c), S2C: [32]byte(s)}
}

func xaead(key *[32]byte) cipher.AEAD {
	a, _ := libsec.NewXChaCha20Poly1305(key[:])
	return a
}

// Seal is XChaCha20-Poly1305: the ciphertext and its tag.
func Seal(key *[32]byte, nonce *[NonceLen]byte, aad, plain []byte) []byte {
	return xaead(key).Seal(nil, nonce[:], plain, aad)
}

// Open opens what Seal made; a bad tag is "bad record mac".
func Open(key *[32]byte, nonce *[NonceLen]byte, aad, sealed []byte) ([]byte, error) {
	p, err := xaead(key).Open(nil, nonce[:], sealed, aad)
	if err != nil {
		return nil, Error(EMac)
	}
	return p, nil
}

// sealedFrame is a frame of type ty whose body is prefix then plain
// sealed under key, with the header and aadTail as associated data.
func sealedFrame(ty uint8, id uint32, prefix [][]byte, key *[32]byte, nonce *[NonceLen]byte, aadTail, plain []byte) []byte {
	size := Header + len(plain) + TagLen
	for _, p := range prefix {
		size += len(p)
	}
	h := HeaderBytes(ty, id, size)
	out := make([]byte, 0, size)
	out = append(out, h[:]...)
	for _, p := range prefix {
		out = append(out, p...)
	}
	aad := append(h[:], aadTail...)
	return xaead(key).Seal(out, nonce[:], plain, aad)
}

func headerOf(frame []byte) []byte { return frame[:Header:Header] }

// TreqFrame seals request under c2s with the header and the ticket as
// associated data.
func TreqFrame(k *Keys, id uint32, ticket []byte, nonce *[NonceLen]byte, request []byte) []byte {
	l := le.AppendUint16(nil, uint16(len(ticket)))
	return sealedFrame(FTreq, id, [][]byte{l, ticket, nonce[:]}, &k.C2S, nonce, ticket, request)
}

// TreqrefFrame is TreqFrame with the ticket's reference in place of the
// ticket, in the frame and in the associated data.
func TreqrefFrame(k *Keys, id uint32, ref *[TrefLen]byte, nonce *[NonceLen]byte, request []byte) []byte {
	return sealedFrame(FTreqref, id, [][]byte{ref[:], nonce[:]}, &k.C2S, nonce, ref[:], request)
}

// OpenTreq opens a Treq's (or a Treqref's) request.
func OpenTreq(k *Keys, frame []byte, f *Treq) ([]byte, error) {
	tail := f.Ticket
	if f.Ref != nil {
		tail = f.Ref[:]
	}
	return Open(&k.C2S, &f.Nonce, append(headerOf(frame), tail...), f.Sealed)
}

// RreqFrame seals reply under s2c with the header and the request's
// nonce as associated data, so a reply cannot be moved onto another
// request.
func RreqFrame(k *Keys, id uint32, reqNonce, nonce *[NonceLen]byte, reply []byte) []byte {
	return sealedFrame(FRreq, id, [][]byte{nonce[:]}, &k.S2C, nonce, reqNonce[:], reply)
}

// OpenRreq opens an Rreq's reply.
func OpenRreq(k *Keys, frame []byte, reqNonce *[NonceLen]byte, f *Rreq) ([]byte, error) {
	return Open(&k.S2C, &f.Nonce, append(headerOf(frame), reqNonce[:]...), f.Sealed)
}

// RauthFrame seals the grant under s2c with the header and the server's
// ephemeral key as associated data.
func RauthFrame(k *Keys, id uint32, eph *[EphLen]byte, nonce *[NonceLen]byte, grant []byte) []byte {
	return sealedFrame(FRauth, id, [][]byte{eph[:], nonce[:]}, &k.S2C, nonce, eph[:], grant)
}

// OpenRauth opens an Rauth's grant.
func OpenRauth(k *Keys, frame []byte, f *Rauth) ([]byte, error) {
	return Open(&k.S2C, &f.Nonce, append(headerOf(frame), f.Eph[:]...), f.Sealed)
}

// Ephemeral is an X25519 key pair from 32 random bytes, clamped as
// X25519 clamps.
type Ephemeral struct {
	priv   *ecdh.PrivateKey
	Public [EphLen]byte
}

// NewEphemeral makes the pair.
func NewEphemeral(random [32]byte) *Ephemeral {
	k, err := ecdh.X25519().NewPrivateKey(random[:])
	if err != nil {
		panic(err) // any 32 bytes are an X25519 key
	}
	return &Ephemeral{priv: k, Public: [32]byte(k.PublicKey().Bytes())}
}

// Shared is the secret shared with peer; ok is false for a peer of small
// order, which would make it all zeros.
func (e *Ephemeral) Shared(peer [EphLen]byte) ([]byte, bool) {
	pk, err := ecdh.X25519().NewPublicKey(peer[:])
	if err != nil {
		return nil, false
	}
	s, err := e.priv.ECDH(pk)
	if err != nil {
		return nil, false
	}
	return s, true
}

const labelMaster2 = "lethe2 master"

// Master2 is the hybrid exchange's master secret: as Master, with the
// ML-KEM secret before the X25519 one as input (as TLS's X25519MLKEM768
// orders them) and "lethe2 master" as the label.
func Master2(kemShared, xShared, transcript []byte) [32]byte {
	salt := sha256.Sum256(transcript)
	ikm := append(append([]byte{}, kemShared...), xShared...)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt[:])
	m, _ := hkdf.Expand(sha256.New, prk, labelMaster2, 32)
	return [32]byte(m)
}

// Rauth2Frame seals the grant under s2c with the header, the server's
// ephemeral key and the ML-KEM ciphertext as associated data.
func Rauth2Frame(k *Keys, id uint32, eph *[EphLen]byte, ct *[KEMCtLen]byte, nonce *[NonceLen]byte, grant []byte) []byte {
	tail := append(append([]byte{}, eph[:]...), ct[:]...)
	return sealedFrame(FRauth2, id, [][]byte{eph[:], ct[:], nonce[:]}, &k.S2C, nonce, tail, grant)
}

// OpenRauth2 opens an Rauth2's grant.
func OpenRauth2(k *Keys, frame []byte, f *Rauth2) ([]byte, error) {
	aad := append(append(headerOf(frame), f.Eph[:]...), f.CT[:]...)
	return Open(&k.S2C, &f.Nonce, aad, f.Sealed)
}
