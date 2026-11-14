package pgauth

import (
	"encoding/base64"
	"encoding/binary"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Certificate formats. Both are one line of text:
//
//	pgcert1 <name> <key> <expires> <signer>:<signature> ...
//	pgcert2 <name> <key> <expires> <signer>:<signature> ...
//
// pgcert1 is the Rust sibling's: Ed25519 keys and signatures, in hex.
// pgcert2 is hybrid: the key and every signer's key are hybrid public
// keys and every signature is a hybrid signature, all in unpadded
// base64url (RFC 4648, Section 5) to keep the line shorter. Each signer
// signs Body:
//
//	"pgcert1" 0x00 || len(name) u8 || name || key[32]   || expires u64 LE
//	"pgcert2" 0x00 || len(name) u8 || name || key[1984] || expires u64 LE
const (
	Cert1Magic   = "pgcert1"
	Cert2Magic   = "pgcert2"
	MaxCertSigs  = 8
	MaxNameBytes = 255
)

var b64 = base64.RawURLEncoding.Strict()

// CertSig is one signer's signature.
type CertSig struct {
	Signer PublicKey
	Sig    []byte
}

// Cert is a pgcert1 or pgcert2 certificate.
type Cert struct {
	Name    string
	Key     PublicKey
	Expires uint64 // seconds since 1970 UTC; 0 never expires
	Sigs    []CertSig
}

// ValidName reports whether name can be a certificate's (and a
// /usr/<name>) user name.
func ValidName(name string) bool {
	if name == "" || len(name) > MaxNameBytes || name == "." || name == ".." || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == ':' {
			return false
		}
	}
	return true
}

// NewCert is an unsigned certificate for key; a hybrid key makes a
// pgcert2 certificate.
func NewCert(name string, key PublicKey, expires uint64) (*Cert, error) {
	if !ValidName(name) {
		return nil, EBadName
	}
	if !key.Valid() {
		return nil, EBadPublic
	}
	return &Cert{Name: name, Key: clone(key), Expires: expires}, nil
}

// Hybrid reports whether c is a pgcert2 certificate.
func (c *Cert) Hybrid() bool { return c.Key.Hybrid() }

func (c *Cert) magic() string {
	if c.Hybrid() {
		return Cert2Magic
	}
	return Cert1Magic
}

// Body is what each signer signs.
func (c *Cert) Body() []byte {
	b := make([]byte, 0, len(c.magic())+2+len(c.Name)+len(c.Key)+8)
	b = append(b, c.magic()...)
	b = append(b, 0, byte(len(c.Name)))
	b = append(b, c.Name...)
	b = append(b, c.Key...)
	return binary.LittleEndian.AppendUint64(b, c.Expires)
}

// Sign adds k's signature. The signer must be of the certificate's
// family: classical for pgcert1, hybrid for pgcert2.
func (c *Cert) Sign(k *PrivateKey) error {
	if k.Hybrid() != c.Hybrid() {
		return EWrongAlg
	}
	signer := k.Public()
	for _, s := range c.Sigs {
		if s.Signer.Equal(signer) {
			return EDupSig
		}
	}
	if len(c.Sigs) >= MaxCertSigs {
		return ETooMany
	}
	c.Sigs = append(c.Sigs, CertSig{Signer: signer, Sig: k.Sign(c.Body())})
	return nil
}

// SignedBy reports whether the key named id has a signature here that
// verifies.
func (c *Cert) SignedBy(id KeyID) bool {
	var body []byte
	for _, s := range c.Sigs {
		if s.Signer.ID() == id {
			if body == nil {
				body = c.Body()
			}
			if s.Signer.Verify(body, s.Sig) {
				return true
			}
		}
	}
	return false
}

// Expired reports whether the certificate has expired at now.
func (c *Cert) Expired(now uint64) bool { return c.Expires != 0 && now >= c.Expires }

func (c *Cert) enc(b []byte) string {
	if c.Hybrid() {
		return b64.EncodeToString(b)
	}
	return Hex(b)
}

// Encode is the certificate as one line, without a newline.
func (c *Cert) Encode() string {
	var s strings.Builder
	s.WriteString(c.magic())
	s.WriteByte(' ')
	s.WriteString(c.Name)
	s.WriteByte(' ')
	s.WriteString(c.enc(c.Key))
	s.WriteByte(' ')
	s.WriteString(strconv.FormatUint(c.Expires, 10))
	for _, sig := range c.Sigs {
		s.WriteByte(' ')
		s.WriteString(c.enc(sig.Signer))
		s.WriteByte(':')
		s.WriteString(c.enc(sig.Sig))
	}
	return s.String()
}

// ParseCert parses one certificate line, strictly: the line must be
// exactly what Encode writes. One trailing newline is allowed.
func ParseCert(text []byte) (*Cert, error) {
	if n := len(text); n > 0 && text[n-1] == '\n' {
		text = text[:n-1]
	}
	if !utf8.Valid(text) {
		return nil, EBadCert
	}
	line := string(text)
	w := strings.Split(line, " ")
	if len(w) < 5 {
		return nil, EBadCert
	}
	var hybrid bool
	switch w[0] {
	case Cert1Magic:
	case Cert2Magic:
		hybrid = true
	default:
		return nil, EBadCert
	}
	dec := func(s string, n int) ([]byte, bool) {
		if hybrid {
			b, err := b64.DecodeString(s)
			return b, err == nil && len(b) == n
		}
		return hexArray(s, n)
	}
	pubLen, sigLen := PublicSize, SignatureSize
	if hybrid {
		pubLen, sigLen = HybridPublicSize, HybridSignatureSize
	}
	key, ok := dec(w[2], pubLen)
	if !ok {
		return nil, EBadCert
	}
	expires, ok := parseU64(w[3])
	if !ok {
		return nil, EBadCert
	}
	pk, err := ParsePublicKey(key)
	if err != nil {
		return nil, EBadCert
	}
	c, err := NewCert(w[1], pk, expires)
	if err != nil {
		return nil, EBadCert
	}
	for _, f := range w[4:] {
		signer, sig, ok := strings.Cut(f, ":")
		if !ok {
			return nil, EBadCert
		}
		sk, ok1 := dec(signer, pubLen)
		sb, ok2 := dec(sig, sigLen)
		if !ok1 || !ok2 || len(c.Sigs) >= MaxCertSigs {
			return nil, EBadCert
		}
		spk, err := ParsePublicKey(sk)
		if err != nil {
			return nil, EBadCert
		}
		for _, s := range c.Sigs {
			if s.Signer.Equal(spk) {
				return nil, EBadCert
			}
		}
		c.Sigs = append(c.Sigs, CertSig{Signer: spk, Sig: sb})
	}
	if c.Encode() != line {
		return nil, EBadCert
	}
	return c, nil
}

// parseU64 is a decimal with no sign, no leading zeros (except "0") and
// no overflow.
func parseU64(s string) (uint64, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}
