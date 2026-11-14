// Package pglogin is the password exchange of the signer and the
// password server (docs/auth.md, "Password scheme"), and password-sealed
// data: keyfs's file, secstore's files and crypt's stream.
//
// The password is hardened with Argon2id into a 32-byte verifier w,
// which the server keeps (in keyfs). SPAKE2 over the Ed25519 group, with
// w as its password, gives both sides a key only when they used the same
// w. Two versions:
//
//	pglogin1 (classical, the Rust sibling's, byte for byte)
//	C -> S  "pglogin1 <op> <name>"
//	S -> C  "argon2id <m> <t> <p> <salt>"
//	S -> C  SPAKE2 message B
//	C -> S  SPAKE2 message A
//	C -> S  seal(c2s, request)
//	S -> C  seal(s2c, reply) | "error <reason>"
//
//	pglogin2 (hybrid, the default)
//	C -> S  "pglogin2 <op> <name>"
//	S -> C  "argon2id <m> <t> <p> <salt>"
//	S -> C  SPAKE2 message B
//	S -> C  ML-KEM-768 encapsulation key (ephemeral)
//	C -> S  SPAKE2 message A
//	C -> S  ML-KEM-768 ciphertext
//	C -> S  seal(c2s, request)
//	S -> C  seal(s2c, reply) | "error <reason>"
//
// pglogin1 derives the keys with HKDF-SHA256 from SPAKE2's key, salted
// with a hash of the first two frames; pglogin2 from SPAKE2's key followed
// by the ML-KEM secret, salted with a hash of the first two frames, the
// encapsulation key and the ciphertext. Frames are a u32 LE length (at
// most 16 KiB) and that many bytes.
package pglogin

import (
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/ericvh/purgatorio-go/libsec"
	"github.com/ericvh/purgatorio-go/pgauth"
)

// Errors, worded as the Rust sibling's.
var (
	ErrFailed   = errors.New("authentication failed")
	ErrProto    = errors.New("authentication protocol botch")
	ErrVerifier = errors.New("bad verifier")
	ErrKDF      = errors.New("bad key derivation parameters")
	// The other end speaks the other version, which this end was not
	// told to take.
	ErrHybridRequired    = pgauth.EHybridRequired
	ErrClassicalRequired = pgauth.EClassicalRequired
)

const (
	Magic1    = "pglogin1"
	Magic2    = "pglogin2"
	ServerID  = "pgsigner"
	MaxFrame  = 16 * 1024
	SaltSize  = 16
	DefaultM  = 19456
	DefaultT  = 2
	MaxM      = 256 * 1024
	MaxT      = 16
	MaxP      = 4
	minMemory = 8 // Argon2 needs 8 KiB per lane
)

// WriteFrame sends one frame.
func WriteFrame(w io.Writer, data []byte) error {
	if len(data) > MaxFrame {
		return ErrProto
	}
	b := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(data)), uint32(len(data)))
	_, err := w.Write(append(b, data...))
	return err
}

// ReadFrame reads one frame.
func ReadFrame(r io.Reader) ([]byte, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	l := binary.LittleEndian.Uint32(n[:])
	if l > MaxFrame {
		return nil, ErrProto
	}
	b := make([]byte, l)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// Kdf is Argon2id's parameters and salt.
type Kdf struct {
	M, T, P uint32 // memory in KiB, passes, lanes
	Salt    [SaltSize]byte
}

// NewKdf is fresh parameters at cost m, t.
func NewKdf(m, t uint32) (Kdf, error) {
	k := Kdf{M: m, T: t, P: 1}
	rand.Read(k.Salt[:])
	return k, k.check()
}

func (k Kdf) check() error {
	if k.M > MaxM || k.T > MaxT || k.P > MaxP || k.T < 1 || k.P < 1 || k.M < minMemory*k.P {
		return ErrKDF
	}
	return nil
}

// Derive is Argon2id (version 0x13) of password, 32 bytes.
func (k Kdf) Derive(password []byte) ([32]byte, error) {
	var out [32]byte
	if err := k.check(); err != nil {
		return out, err
	}
	copy(out[:], argon2.IDKey(password, k.Salt[:], k.T, k.M, uint8(k.P), 32))
	return out, nil
}

// Encode is "argon2id <m> <t> <p> <salt>".
func (k Kdf) Encode() string {
	return "argon2id " + strconv.FormatUint(uint64(k.M), 10) + " " + strconv.FormatUint(uint64(k.T), 10) + " " +
		strconv.FormatUint(uint64(k.P), 10) + " " + hex.EncodeToString(k.Salt[:])
}

func num(s string) (uint32, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 32)
	return uint32(v), err == nil
}

func kdfWords(w []string) (Kdf, []string, error) {
	var k Kdf
	if len(w) < 5 || w[0] != "argon2id" {
		return k, nil, ErrKDF
	}
	var ok1, ok2, ok3 bool
	k.M, ok1 = num(w[1])
	k.T, ok2 = num(w[2])
	k.P, ok3 = num(w[3])
	salt, err := hex.DecodeString(w[4])
	if !ok1 || !ok2 || !ok3 || err != nil || len(salt) != SaltSize || strings.ToLower(w[4]) != w[4] {
		return k, nil, ErrKDF
	}
	copy(k.Salt[:], salt)
	return k, w[5:], k.check()
}

// ParseKdf reads Encode's form.
func ParseKdf(text []byte) (Kdf, error) {
	k, rest, err := kdfWords(strings.Split(strings.TrimRight(string(text), " \t\r\n"), " "))
	if err == nil && len(rest) != 0 {
		err = ErrKDF
	}
	return k, err
}

func (k Kdf) bytes() []byte {
	b := make([]byte, 0, 12+SaltSize)
	b = binary.LittleEndian.AppendUint32(b, k.M)
	b = binary.LittleEndian.AppendUint32(b, k.T)
	b = binary.LittleEndian.AppendUint32(b, k.P)
	return append(b, k.Salt[:]...)
}

func kdfFrom(b []byte) (Kdf, error) {
	var k Kdf
	if len(b) != 12+SaltSize {
		return k, ErrKDF
	}
	k.M, k.T, k.P = binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint32(b[8:])
	copy(k.Salt[:], b[12:])
	return k, k.check()
}

// Verifier is what the server keeps for a user.
type Verifier struct {
	Kdf Kdf
	W   [32]byte
}

// NewVerifier hardens password under kdf.
func NewVerifier(password []byte, kdf Kdf) (Verifier, error) {
	w, err := kdf.Derive(password)
	return Verifier{Kdf: kdf, W: w}, err
}

// Encode is "argon2id <m> <t> <p> <salt> <w>".
func (v Verifier) Encode() string { return v.Kdf.Encode() + " " + hex.EncodeToString(v.W[:]) }

// ParseVerifier reads Encode's form.
func ParseVerifier(text []byte) (Verifier, error) {
	var v Verifier
	k, rest, err := kdfWords(strings.Split(strings.TrimRight(string(text), " \t\r\n"), " "))
	if err != nil || len(rest) != 1 {
		return v, ErrVerifier
	}
	w, err := hex.DecodeString(rest[0])
	if err != nil || len(w) != 32 || strings.ToLower(rest[0]) != rest[0] {
		return v, ErrVerifier
	}
	v.Kdf = k
	copy(v.W[:], w)
	return v, nil
}

// FakeVerifier stands in for a user that does not exist: its salt comes
// from the server's secret and the name, so it is the same every time,
// and w is random, so the exchange fails at the sealed request.
func FakeVerifier(secret []byte, name string, rnd io.Reader) Verifier {
	h := sha256.New()
	h.Write([]byte("pglogin1 fake\x00"))
	h.Write(secret)
	h.Write([]byte(name))
	d := h.Sum(nil)
	v := Verifier{Kdf: Kdf{M: DefaultM, T: DefaultT, P: 1}}
	copy(v.Kdf.Salt[:], d)
	io.ReadFull(rnd, v.W[:])
	return v
}

// Session is the two directions' keys after an exchange.
type Session struct {
	send, recv     cipher.AEAD
	sent, received uint64
}

func nonce(n uint64) []byte {
	var b [12]byte
	binary.LittleEndian.PutUint64(b[:], n)
	return b[:]
}

func newSession(version int, ikm, salt []byte, client bool) *Session {
	label := Magic1
	if version == 2 {
		label = Magic2
	}
	key := func(dir string) cipher.AEAD {
		k, err := hkdf.Expand(sha256.New, mustExtract(ikm, salt), label+" "+dir, 32)
		if err != nil {
			panic(err)
		}
		a, _ := libsec.NewChaCha20Poly1305(k)
		return a
	}
	c2s, s2c := key("c2s"), key("s2c")
	if client {
		return &Session{send: c2s, recv: s2c}
	}
	return &Session{send: s2c, recv: c2s}
}

func mustExtract(ikm, salt []byte) []byte {
	p, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		panic(err)
	}
	return p
}

// Seal seals the next message.
func (s *Session) Seal(plain []byte) []byte {
	n := nonce(s.sent)
	s.sent++
	return s.send.Seal(nil, n, plain, nil)
}

// Open opens the next message.
func (s *Session) Open(sealed []byte) ([]byte, bool) {
	p, err := s.recv.Open(nil, nonce(s.received), sealed, nil)
	if err != nil {
		return nil, false
	}
	s.received++
	return p, true
}

// Send sends one sealed frame.
func (s *Session) Send(w io.Writer, plain []byte) error { return WriteFrame(w, s.Seal(plain)) }

// Recv reads one sealed frame. One that does not open is an "error
// <reason>" in the clear, or a failure.
func (s *Session) Recv(r io.Reader) ([]byte, error) {
	f, err := ReadFrame(r)
	if err != nil {
		return nil, err
	}
	if p, ok := s.Open(f); ok {
		return p, nil
	}
	if e, ok := clearError(f); ok {
		return nil, errors.New(e)
	}
	return nil, ErrFailed
}

func clearError(f []byte) (string, bool) {
	s, ok := strings.CutPrefix(string(f), "error ")
	return s, ok
}

func transcript(frames ...[]byte) []byte {
	h := sha256.New()
	for _, f := range frames {
		h.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(f))))
		h.Write(f)
	}
	return h.Sum(nil)
}

// Client runs the client's side over rw: version 2 (hybrid) or 1
// (classical); neither falls back to the other. request makes the sealed
// request from the server's parameters (passwd's needs them for the new
// verifier). It returns the session and the server's reply.
func Client(rw io.ReadWriter, hybrid bool, op, name string, password []byte, rnd io.Reader, request func(Kdf) ([]byte, error)) (*Session, []byte, error) {
	if !pgauth.ValidName(name) || strings.Contains(op, " ") {
		return nil, nil, pgauth.EBadName
	}
	magic, version := Magic1, 1
	if hybrid {
		magic, version = Magic2, 2
	}
	hello := []byte(magic + " " + op + " " + name)
	if err := WriteFrame(rw, hello); err != nil {
		return nil, nil, err
	}
	params, err := ReadFrame(rw)
	if err != nil {
		return nil, nil, err
	}
	if e, ok := clearError(params); ok {
		return nil, nil, errors.New(e)
	}
	kdf, err := ParseKdf(params)
	if err != nil {
		return nil, nil, err
	}
	b, err := ReadFrame(rw)
	if err != nil {
		return nil, nil, err
	}
	var ekb []byte
	if hybrid {
		if ekb, err = ReadFrame(rw); err != nil {
			return nil, nil, err
		}
	}
	w, err := kdf.Derive(password)
	if err != nil {
		return nil, nil, err
	}
	st, a, err := StartSpake2(true, w[:], []byte(name), []byte(ServerID), rnd)
	if err != nil {
		return nil, nil, err
	}
	key, err := st.Finish(b)
	if err != nil {
		return nil, nil, ErrProto
	}
	if err := WriteFrame(rw, a); err != nil {
		return nil, nil, err
	}
	salt := transcript(hello, params)
	if hybrid {
		ek, err := mlkem.NewEncapsulationKey768(ekb)
		if err != nil {
			return nil, nil, ErrProto
		}
		ss, ct := ek.Encapsulate()
		if err := WriteFrame(rw, ct); err != nil {
			return nil, nil, err
		}
		key = append(key, ss...)
		salt = transcript(hello, params, ekb, ct)
	}
	s := newSession(version, key, salt, true)
	req, err := request(kdf)
	if err != nil {
		return nil, nil, err
	}
	if err := s.Send(rw, req); err != nil {
		return nil, nil, err
	}
	reply, err := s.Recv(rw)
	return s, reply, err
}

// Hello is the client's first frame, as the server reads it.
type Hello struct {
	Version  int
	Op, Name string
	raw      []byte
}

// ServerHello reads the client's first frame.
func ServerHello(r io.Reader) (*Hello, error) {
	raw, err := ReadFrame(r)
	if err != nil {
		return nil, err
	}
	w := strings.Split(string(raw), " ")
	if len(w) != 3 || !pgauth.ValidName(w[2]) {
		return nil, ErrProto
	}
	h := &Hello{Op: w[1], Name: w[2], raw: raw}
	switch w[0] {
	case Magic1:
		h.Version = 1
	case Magic2:
		h.Version = 2
	default:
		return nil, ErrProto
	}
	return h, nil
}

// Server runs the server's side after ServerHello, against v. accept is
// the versions it takes; a hello of another version gets "error <why>"
// in the clear. A wrong password sends "error authentication failed".
// It returns the session and the client's opened request.
func Server(rw io.ReadWriter, h *Hello, v Verifier, accept pgauth.Accept, rnd io.Reader) (*Session, []byte, error) {
	if accept == 0 {
		accept = pgauth.AcceptHybrid
	}
	if h.Version == 2 && accept&pgauth.AcceptHybrid == 0 {
		SendError(rw, ErrClassicalRequired.Error())
		return nil, nil, ErrClassicalRequired
	}
	if h.Version == 1 && accept&pgauth.AcceptClassical == 0 {
		SendError(rw, ErrHybridRequired.Error())
		return nil, nil, ErrHybridRequired
	}
	params := []byte(v.Kdf.Encode())
	if err := WriteFrame(rw, params); err != nil {
		return nil, nil, err
	}
	st, b, err := StartSpake2(false, v.W[:], []byte(h.Name), []byte(ServerID), rnd)
	if err != nil {
		return nil, nil, err
	}
	if err := WriteFrame(rw, b); err != nil {
		return nil, nil, err
	}
	var dk *mlkem.DecapsulationKey768
	var ekb []byte
	if h.Version == 2 {
		if dk, err = mlkem.GenerateKey768(); err != nil {
			return nil, nil, err
		}
		ekb = dk.EncapsulationKey().Bytes()
		if err := WriteFrame(rw, ekb); err != nil {
			return nil, nil, err
		}
	}
	a, err := ReadFrame(rw)
	if err != nil {
		return nil, nil, err
	}
	key, err := st.Finish(a)
	if err != nil {
		return nil, nil, ErrProto
	}
	salt := transcript(h.raw, params)
	if h.Version == 2 {
		ct, err := ReadFrame(rw)
		if err != nil {
			return nil, nil, err
		}
		ss, err := dk.Decapsulate(ct)
		if err != nil {
			return nil, nil, ErrProto
		}
		key = append(key, ss...)
		salt = transcript(h.raw, params, ekb, ct)
	}
	s := newSession(h.Version, key, salt, false)
	req, err := ReadFrame(rw)
	if err != nil {
		return nil, nil, err
	}
	p, ok := s.Open(req)
	if !ok {
		SendError(rw, ErrFailed.Error())
		return nil, nil, ErrFailed
	}
	return s, p, nil
}

// SendError sends "error <reason>" in the clear.
func SendError(w io.Writer, reason string) error { return WriteFrame(w, []byte("error "+reason)) }

// NetkeyResponse is netkey's response to challenge: the first 8 hex
// digits of HMAC-SHA256 keyed with SHA-256 of the password.
func NetkeyResponse(password, challenge []byte) string {
	k := sha256.Sum256(password)
	m := hmac.New(sha256.New, k[:])
	m.Write(challenge)
	return hex.EncodeToString(m.Sum(nil)[:4])
}
