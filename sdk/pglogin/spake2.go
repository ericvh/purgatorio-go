package pglogin

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"filippo.io/edwards25519"
)

// SPAKE2 over the Ed25519 group, as the Rust spake2 crate (0.4, which
// follows python-spake2) does it, byte for byte:
//
//	pw      = HKDF-SHA256(salt "", ikm password, info "SPAKE2 pw", 48 bytes)
//	          read big-endian, reduced mod the group order
//	A:  X = x*B + pw*M,   message 'A' || X
//	B:  Y = y*B + pw*N,   message 'B' || Y
//	A:  K = x*(Y - pw*N)  B: K = y*(X - pw*M)
//	key = SHA-256(SHA-256(password) || SHA-256(idA) || SHA-256(idB) || X || Y || K)
//
// x and y are 64 random bytes reduced mod the order. M and N are
// python-spake2's points for Ed25519.

// SpakeLen is the length of a SPAKE2 message.
const SpakeLen = 33

var (
	pointM = mustPoint("15cfd18e385952982b6a8f8c7854963b58e34388c8e6dae891db756481a02312")
	pointN = mustPoint("f04f2e7eb734b2a8f8b472eaf9c3c632576ac64aea650b496a8a20ff00e583c3")

	errSpake = errors.New("bad spake2 message")
)

func mustPoint(h string) *edwards25519.Point {
	b, _ := hex.DecodeString(h)
	p, err := new(edwards25519.Point).SetBytes(b)
	if err != nil {
		panic(err)
	}
	return p
}

func passwordScalar(pw []byte) *edwards25519.Scalar {
	okm, err := hkdf.Key(sha256.New, pw, []byte{}, "SPAKE2 pw", 48)
	if err != nil {
		panic(err)
	}
	var wide [64]byte
	for i, b := range okm {
		wide[47-i] = b
	}
	s, _ := edwards25519.NewScalar().SetUniformBytes(wide[:])
	return s
}

// Spake2 is one side of an exchange.
type Spake2 struct {
	sideA    bool
	x        *edwards25519.Scalar
	pw       *edwards25519.Scalar
	password []byte
	idA, idB []byte
	msg      []byte // our element, without the side byte
}

// StartSpake2 starts side A (the client) or B (the server) with password
// and the identities, taking 64 bytes from rand for the secret scalar.
// It returns the message to send.
func StartSpake2(sideA bool, password, idA, idB []byte, rand io.Reader) (*Spake2, []byte, error) {
	var wide [64]byte
	if _, err := io.ReadFull(rand, wide[:]); err != nil {
		return nil, nil, err
	}
	x, _ := edwards25519.NewScalar().SetUniformBytes(wide[:])
	s := &Spake2{sideA: sideA, x: x, pw: passwordScalar(password), password: append([]byte(nil), password...),
		idA: append([]byte(nil), idA...), idB: append([]byte(nil), idB...)}
	blind, side := pointM, byte('A')
	if !sideA {
		blind, side = pointN, 'B'
	}
	p := new(edwards25519.Point).ScalarBaseMult(x)
	p.Add(p, new(edwards25519.Point).ScalarMult(s.pw, blind))
	s.msg = p.Bytes()
	return s, append([]byte{side}, s.msg...), nil
}

// Finish takes the other side's message and returns the shared key.
func (s *Spake2) Finish(msg []byte) ([]byte, error) {
	want, unblind := byte('B'), pointN
	if !s.sideA {
		want, unblind = 'A', pointM
	}
	if len(msg) != SpakeLen || msg[0] != want {
		return nil, errSpake
	}
	q, err := new(edwards25519.Point).SetBytes(msg[1:])
	if err != nil {
		return nil, errSpake
	}
	neg := edwards25519.NewScalar().Negate(s.pw)
	t := new(edwards25519.Point).ScalarMult(neg, unblind)
	t.Add(q, t)
	k := new(edwards25519.Point).ScalarMult(s.x, t).Bytes()
	x, y := s.msg, msg[1:]
	if !s.sideA {
		x, y = msg[1:], s.msg
	}
	h := sha256.New()
	for _, b := range [][]byte{s.password, s.idA, s.idB} {
		d := sha256.Sum256(b)
		h.Write(d[:])
	}
	h.Write(x)
	h.Write(y)
	h.Write(k)
	return h.Sum(nil), nil
}
