package libsec

import (
	"crypto/cipher"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"math/bits"
)

// ChaCha20-Poly1305 (RFC 8439) key, nonce and tag sizes in bytes.
const (
	ChaChaKeySize   = 32
	ChaChaNonceSize = 12
	Poly1305TagSize = 16
)

var errOpen = errors.New("chacha20poly1305: message authentication failed")

// chachaAEAD is ChaCha20-Poly1305 as RFC 8439 defines it. Go's standard
// library has it only inside crypto/tls; the record layer of the mount
// handshake (pgauth) needs it in the portable core, so it is here.
type chachaAEAD struct {
	key [8]uint32
}

// NewChaCha20Poly1305 returns the RFC 8439 AEAD for a 32-byte key.
func NewChaCha20Poly1305(key []byte) (cipher.AEAD, error) {
	if len(key) != ChaChaKeySize {
		return nil, errors.New("chacha20poly1305: bad key length")
	}
	a := &chachaAEAD{}
	for i := range a.key {
		a.key[i] = binary.LittleEndian.Uint32(key[4*i:])
	}
	return a, nil
}

func (a *chachaAEAD) NonceSize() int { return ChaChaNonceSize }
func (a *chachaAEAD) Overhead() int  { return Poly1305TagSize }

func (a *chachaAEAD) Seal(dst, nonce, plaintext, aad []byte) []byte {
	if len(nonce) != ChaChaNonceSize {
		panic("chacha20poly1305: bad nonce length")
	}
	ret, out := sliceForAppend(dst, len(plaintext)+Poly1305TagSize)
	ct := out[:len(plaintext)]
	var polyKey [64]byte
	a.block(&polyKey, nonce, 0)
	a.xor(ct, plaintext, nonce, 1)
	tag := polyTag(polyKey[:32], aad, ct)
	copy(out[len(plaintext):], tag[:])
	return ret
}

func (a *chachaAEAD) Open(dst, nonce, ciphertext, aad []byte) ([]byte, error) {
	if len(nonce) != ChaChaNonceSize {
		panic("chacha20poly1305: bad nonce length")
	}
	if len(ciphertext) < Poly1305TagSize {
		return nil, errOpen
	}
	ct := ciphertext[:len(ciphertext)-Poly1305TagSize]
	var polyKey [64]byte
	a.block(&polyKey, nonce, 0)
	tag := polyTag(polyKey[:32], aad, ct)
	if subtle.ConstantTimeCompare(tag[:], ciphertext[len(ct):]) != 1 {
		return nil, errOpen
	}
	ret, out := sliceForAppend(dst, len(ct))
	a.xor(out, ct, nonce, 1)
	return ret, nil
}

func sliceForAppend(in []byte, n int) (head, tail []byte) {
	if total := len(in) + n; cap(in) >= total {
		head = in[:total]
	} else {
		head = make([]byte, total)
		copy(head, in)
	}
	return head, head[len(in):]
}

func quarter(a, b, c, d uint32) (uint32, uint32, uint32, uint32) {
	a += b
	d = bits.RotateLeft32(d^a, 16)
	c += d
	b = bits.RotateLeft32(b^c, 12)
	a += b
	d = bits.RotateLeft32(d^a, 8)
	c += d
	b = bits.RotateLeft32(b^c, 7)
	return a, b, c, d
}

// block writes the ChaCha20 key stream block for counter into out.
func (a *chachaAEAD) block(out *[64]byte, nonce []byte, counter uint32) {
	var s [16]uint32
	s[0], s[1], s[2], s[3] = 0x61707865, 0x3320646e, 0x79622d32, 0x6b206574
	copy(s[4:12], a.key[:])
	s[12] = counter
	s[13] = binary.LittleEndian.Uint32(nonce[0:])
	s[14] = binary.LittleEndian.Uint32(nonce[4:])
	s[15] = binary.LittleEndian.Uint32(nonce[8:])
	x := s
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
	for i := range x {
		binary.LittleEndian.PutUint32(out[4*i:], x[i]+s[i])
	}
}

// xor sets dst to src XOR the key stream starting at counter.
func (a *chachaAEAD) xor(dst, src, nonce []byte, counter uint32) {
	var ks [64]byte
	for len(src) > 0 {
		a.block(&ks, nonce, counter)
		counter++
		n := min(len(src), 64)
		subtle.XORBytes(dst[:n], src[:n], ks[:n])
		dst, src = dst[n:], src[n:]
	}
}

// poly1305 is the one-time authenticator of RFC 8439, Section 2.5, with
// the 130-bit accumulator in three 64-bit limbs.
type poly1305 struct {
	h0, h1, h2 uint64
	r0, r1     uint64
	s0, s1     uint64
	buf        [16]byte
	n          int
}

func newPoly1305(key []byte) *poly1305 {
	return &poly1305{
		r0: binary.LittleEndian.Uint64(key[0:]) & 0x0FFFFFFC0FFFFFFF,
		r1: binary.LittleEndian.Uint64(key[8:]) & 0x0FFFFFFC0FFFFFFC,
		s0: binary.LittleEndian.Uint64(key[16:]),
		s1: binary.LittleEndian.Uint64(key[24:]),
	}
}

// blockInto adds the 16-byte block m (with hibit set for a full block)
// to the accumulator and multiplies by r modulo 2^130-5.
func (p *poly1305) blockInto(m []byte, hibit uint64) {
	var c uint64
	h0, h1, h2 := p.h0, p.h1, p.h2
	h0, c = bits.Add64(h0, binary.LittleEndian.Uint64(m[0:]), 0)
	h1, c = bits.Add64(h1, binary.LittleEndian.Uint64(m[8:]), c)
	h2 += c + hibit

	r0, r1 := p.r0, p.r1
	h0r0hi, h0r0lo := bits.Mul64(h0, r0)
	h1r0hi, h1r0lo := bits.Mul64(h1, r0)
	h2r0hi, h2r0lo := bits.Mul64(h2, r0)
	h0r1hi, h0r1lo := bits.Mul64(h0, r1)
	h1r1hi, h1r1lo := bits.Mul64(h1, r1)
	h2r1hi, h2r1lo := bits.Mul64(h2, r1)

	// The clamped r keeps every sum below from overflowing 128 bits.
	m1lo, c := bits.Add64(h1r0lo, h0r1lo, 0)
	m1hi, _ := bits.Add64(h1r0hi, h0r1hi, c)
	m2lo, c := bits.Add64(h2r0lo, h1r1lo, 0)
	m2hi, _ := bits.Add64(h2r0hi, h1r1hi, c)
	m3lo, m3hi := h2r1lo, h2r1hi

	t0 := h0r0lo
	t1, c := bits.Add64(m1lo, h0r0hi, 0)
	t2, c := bits.Add64(m2lo, m1hi, c)
	t3, _ := bits.Add64(m3lo, m2hi, c)
	_ = m3hi

	// t = t3:t2:t1:t0. Fold everything above 2^130 back in: since
	// 2^130 = 5 mod p, add (t >> 130) * 5 = (t >> 130) * 4 + (t >> 130).
	h0, h1, h2 = t0, t1, t2&3
	cclo, cchi := t2&^3, t3
	h0, c = bits.Add64(h0, cclo, 0)
	h1, c = bits.Add64(h1, cchi, c)
	h2 += c
	cclo, cchi = cclo>>2|cchi<<62, cchi>>2
	h0, c = bits.Add64(h0, cclo, 0)
	h1, c = bits.Add64(h1, cchi, c)
	h2 += c
	p.h0, p.h1, p.h2 = h0, h1, h2
}

func (p *poly1305) write(b []byte) {
	if p.n > 0 {
		k := copy(p.buf[p.n:], b)
		p.n += k
		b = b[k:]
		if p.n < 16 {
			return
		}
		p.blockInto(p.buf[:], 1)
		p.n = 0
	}
	for len(b) >= 16 {
		p.blockInto(b[:16], 1)
		b = b[16:]
	}
	p.n = copy(p.buf[:], b)
}

// pad16 completes the current 16-byte block with zeros, as the AEAD's
// MAC input pads aad and ciphertext.
func (p *poly1305) pad16() {
	if p.n > 0 {
		clear(p.buf[p.n:])
		p.blockInto(p.buf[:], 1)
		p.n = 0
	}
}

func (p *poly1305) sum() [16]byte {
	if p.n > 0 {
		clear(p.buf[p.n:])
		p.buf[p.n] = 1
		p.blockInto(p.buf[:], 0)
		p.n = 0
	}
	h0, h1, h2 := p.h0, p.h1, p.h2
	t0, b := bits.Sub64(h0, 0xFFFFFFFFFFFFFFFB, 0)
	t1, b := bits.Sub64(h1, 0xFFFFFFFFFFFFFFFF, b)
	_, b = bits.Sub64(h2, 3, b)
	// b == 0: h >= p, so use h - p.
	mask := b - 1
	h0 = h0&^mask | t0&mask
	h1 = h1&^mask | t1&mask
	var c uint64
	h0, c = bits.Add64(h0, p.s0, 0)
	h1, _ = bits.Add64(h1, p.s1, c)
	var out [16]byte
	binary.LittleEndian.PutUint64(out[0:], h0)
	binary.LittleEndian.PutUint64(out[8:], h1)
	return out
}

// polyTag is the AEAD's tag: Poly1305 over aad, the ciphertext and both
// lengths, each part padded to 16 bytes.
func polyTag(key, aad, ct []byte) [16]byte {
	p := newPoly1305(key)
	p.write(aad)
	p.pad16()
	p.write(ct)
	p.pad16()
	var lens [16]byte
	binary.LittleEndian.PutUint64(lens[0:], uint64(len(aad)))
	binary.LittleEndian.PutUint64(lens[8:], uint64(len(ct)))
	p.write(lens[:])
	return p.sum()
}

// Poly1305 returns the RFC 8439 one-time authenticator of msg under key.
func Poly1305(key *[32]byte, msg []byte) [16]byte {
	p := newPoly1305(key[:])
	p.write(msg)
	return p.sum()
}
