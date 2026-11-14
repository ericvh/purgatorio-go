package libsec

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
)

// XChaCha20-Poly1305 (draft-irtf-cfrg-xchacha): ChaCha20-Poly1305 under
// the subkey HChaCha20 derives from the key and the nonce's first 16
// bytes, with the nonce 0[4] || nonce[16:24]. Its 24-byte nonce is safe
// to choose at random. Lethe seals with it.

// XChaChaNonceSize is XChaCha20-Poly1305's nonce size.
const XChaChaNonceSize = 24

// HChaCha20 derives a subkey from key and the 16-byte input.
func HChaCha20(key *[32]byte, input *[16]byte) [32]byte {
	le := binary.LittleEndian
	var x [16]uint32
	x[0], x[1], x[2], x[3] = 0x61707865, 0x3320646e, 0x79622d32, 0x6b206574
	for i := 0; i < 8; i++ {
		x[4+i] = le.Uint32(key[4*i:])
	}
	for i := 0; i < 4; i++ {
		x[12+i] = le.Uint32(input[4*i:])
	}
	for range 10 {
		x[0], x[4], x[8], x[12] = quarter(x[0], x[4], x[8], x[12])
		x[1], x[5], x[9], x[13] = quarter(x[1], x[5], x[9], x[13])
		x[2], x[6], x[10], x[14] = quarter(x[2], x[6], x[10], x[14])
		x[3], x[7], x[11], x[15] = quarter(x[3], x[7], x[11], x[15])
		x[0], x[5], x[10], x[15] = quarter(x[0], x[5], x[10], x[15])
		x[1], x[6], x[11], x[12] = quarter(x[1], x[6], x[11], x[12])
		x[2], x[7], x[8], x[13] = quarter(x[2], x[7], x[8], x[13])
		x[3], x[4], x[9], x[14] = quarter(x[3], x[4], x[9], x[14])
	}
	var out [32]byte
	for i := 0; i < 4; i++ {
		le.PutUint32(out[4*i:], x[i])
		le.PutUint32(out[16+4*i:], x[12+i])
	}
	return out
}

type xchacha struct{ key [32]byte }

// NewXChaCha20Poly1305 returns XChaCha20-Poly1305 for a 32-byte key.
func NewXChaCha20Poly1305(key []byte) (cipher.AEAD, error) {
	if len(key) != ChaChaKeySize {
		return nil, errors.New("xchacha20poly1305: bad key length")
	}
	return &xchacha{key: [32]byte(key)}, nil
}

func (x *xchacha) NonceSize() int { return XChaChaNonceSize }
func (x *xchacha) Overhead() int  { return Poly1305TagSize }

func (x *xchacha) inner(nonce []byte) (cipher.AEAD, []byte) {
	if len(nonce) != XChaChaNonceSize {
		panic("xchacha20poly1305: bad nonce length")
	}
	sub := HChaCha20(&x.key, (*[16]byte)(nonce[:16]))
	a, _ := NewChaCha20Poly1305(sub[:])
	n := make([]byte, ChaChaNonceSize)
	copy(n[4:], nonce[16:])
	return a, n
}

func (x *xchacha) Seal(dst, nonce, plain, aad []byte) []byte {
	a, n := x.inner(nonce)
	return a.Seal(dst, n, plain, aad)
}

func (x *xchacha) Open(dst, nonce, sealed, aad []byte) ([]byte, error) {
	a, n := x.inner(nonce)
	return a.Open(dst, n, sealed, aad)
}
