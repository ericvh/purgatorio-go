package pglogin

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"

	"github.com/ericvh/purgatorio-go/libsec"
)

// Password-sealed blobs (keyfs's file, secstore's files), the Rust
// sibling's format:
//
//	"pgseal1\0" || kdf (m, t, p u32 LE, salt[16]) || nonce[12] || ChaCha20-Poly1305(data)
//
// with everything before the ciphertext as associated data.
const SealMagic = "pgseal1\x00"

const sealHeader = 8 + 12 + SaltSize + 12

var (
	ErrBadSealed = errors.New("bad sealed file")
	ErrWrongSeal = errors.New("wrong password or damaged file")
)

// Seal seals data under password with fresh parameters at cost m, t.
func Seal(password []byte, m, t uint32, data []byte) ([]byte, error) {
	kdf, err := NewKdf(m, t)
	if err != nil {
		return nil, err
	}
	key, err := kdf.Derive(password)
	if err != nil {
		return nil, err
	}
	return SealWith(kdf, key, data, rand.Reader)
}

// SealWith seals with an already derived key, taking the nonce from rnd.
func SealWith(kdf Kdf, key [32]byte, data []byte, rnd io.Reader) ([]byte, error) {
	out := append([]byte(SealMagic), kdf.bytes()...)
	var n [12]byte
	if _, err := io.ReadFull(rnd, n[:]); err != nil {
		return nil, err
	}
	out = append(out, n[:]...)
	a, _ := libsec.NewChaCha20Poly1305(key[:])
	return a.Seal(out, n[:], data, out), nil
}

// SealedKdf is the parameters a blob was sealed with.
func SealedKdf(blob []byte) (Kdf, error) {
	if len(blob) < sealHeader+16 || string(blob[:8]) != SealMagic {
		return Kdf{}, ErrBadSealed
	}
	return kdfFrom(blob[8 : 8+12+SaltSize])
}

// Open opens a blob and returns its data, parameters and key.
func Open(password, blob []byte) ([]byte, Kdf, [32]byte, error) {
	kdf, err := SealedKdf(blob)
	if err != nil {
		return nil, kdf, [32]byte{}, err
	}
	key, err := kdf.Derive(password)
	if err != nil {
		return nil, kdf, key, err
	}
	data, err := OpenWith(key, blob)
	return data, kdf, key, err
}

// OpenWith opens a blob with a derived key.
func OpenWith(key [32]byte, blob []byte) ([]byte, error) {
	if _, err := SealedKdf(blob); err != nil {
		return nil, err
	}
	head, ct := blob[:sealHeader], blob[sealHeader:]
	a, _ := libsec.NewChaCha20Poly1305(key[:])
	p, err := a.Open(nil, head[sealHeader-12:], ct, head)
	if err != nil {
		return nil, ErrWrongSeal
	}
	return p, nil
}

// crypt's chunked stream, the Rust sibling's format: a header
// "pgcrypt1" || kdf || prefix[7], then chunks of a u32 LE length and
// ChaCha20-Poly1305 ciphertext. Chunk i's nonce is prefix || i (u32 BE)
// || last, so chunks cannot be reordered, dropped or cut off unnoticed.
const (
	CryptMagic  = "pgcrypt1"
	CryptChunk  = 64 * 1024
	CryptHeader = 8 + 12 + SaltSize + 7
)

var (
	ErrNotCrypt   = errors.New("not an encrypted file")
	ErrWrongCrypt = errors.New("wrong secret or damaged file")
)

// Cipher is one direction of crypt's stream.
type Cipher struct {
	seal   func(dst, nonce, plain, aad []byte) []byte
	open   func(dst, nonce, ct, aad []byte) ([]byte, error)
	prefix [7]byte
	next   uint32
}

func newCipher(key [32]byte, prefix [7]byte) *Cipher {
	a, _ := libsec.NewChaCha20Poly1305(key[:])
	return &Cipher{seal: a.Seal, open: a.Open, prefix: prefix}
}

// EncryptStream starts an encryption under secret at cost m, t, taking
// the salt and prefix from rnd. It returns the cipher and the header.
func EncryptStream(secret []byte, m, t uint32, rnd io.Reader) (*Cipher, []byte, error) {
	kdf := Kdf{M: m, T: t, P: 1}
	if _, err := io.ReadFull(rnd, kdf.Salt[:]); err != nil {
		return nil, nil, err
	}
	key, err := kdf.Derive(secret)
	if err != nil {
		return nil, nil, err
	}
	var prefix [7]byte
	if _, err := io.ReadFull(rnd, prefix[:]); err != nil {
		return nil, nil, err
	}
	h := append([]byte(CryptMagic), kdf.bytes()...)
	return newCipher(key, prefix), append(h, prefix[:]...), nil
}

// DecryptStream reads a header for decryption.
func DecryptStream(secret, header []byte) (*Cipher, error) {
	if len(header) != CryptHeader || string(header[:8]) != CryptMagic {
		return nil, ErrNotCrypt
	}
	kdf, err := kdfFrom(header[8 : 8+12+SaltSize])
	if err != nil {
		return nil, err
	}
	key, err := kdf.Derive(secret)
	if err != nil {
		return nil, err
	}
	var prefix [7]byte
	copy(prefix[:], header[CryptHeader-7:])
	return newCipher(key, prefix), nil
}

func (c *Cipher) nonce(last bool) ([]byte, error) {
	if c.next == ^uint32(0) {
		return nil, ErrNotCrypt
	}
	n := make([]byte, 12)
	copy(n, c.prefix[:])
	binary.BigEndian.PutUint32(n[7:], c.next)
	if last {
		n[11] = 1
	}
	c.next++
	return n, nil
}

// SealChunk seals the next chunk (at most CryptChunk bytes), with its
// length.
func (c *Cipher) SealChunk(data []byte, last bool) ([]byte, error) {
	n, err := c.nonce(last)
	if err != nil {
		return nil, err
	}
	ct := c.seal(nil, n, data, nil)
	return append(binary.LittleEndian.AppendUint32(nil, uint32(len(ct))), ct...), nil
}

// OpenChunk opens the next chunk's ciphertext (without its length).
func (c *Cipher) OpenChunk(ct []byte, last bool) ([]byte, error) {
	n, err := c.nonce(last)
	if err != nil {
		return nil, err
	}
	p, err := c.open(nil, n, ct, nil)
	if err != nil {
		return nil, ErrWrongCrypt
	}
	return p, nil
}
