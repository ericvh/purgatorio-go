// Package modsig is module digests and signatures (docs/signing.md), and
// the signed ahead-of-time image file.
//
// A module's digest is SHA-256 over its bytes with every purgatorio.sig
// custom section cut out. That section holds entries, each a signature
// over Message with the signing key or a chain of certificates:
//
//	version u8   1 (classical entries only, the Rust sibling's) or 2
//	count   u8   1 to 8
//	entry   ...
//
//	entry  = kind u8 || signature || body
//	kind 0: Ed25519 signature[64],   body = key[32]
//	kind 1: Ed25519 signature[64],   body = ncerts u8 (1-4), then per
//	        certificate len u16 LE (1-2048) || pgcert1 line
//	kind 2: hybrid signature[3373],  body = hybrid key[1984]
//	kind 3: hybrid signature[3373],  body = ncerts u8 (1-4), then per
//	        certificate len u16 LE (1-16384) || pgcert2 line
//
// Kinds 0 and 1 sign "pgmodsig1" 0x00 || digest || len(abi) u8 || abi,
// as the Rust sibling does; kinds 2 and 3 sign the same with "pgmodsig2".
// A section with any hybrid entry is version 2, which the Rust sibling's
// loader refuses as a version it does not know.
package modsig

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/pgauth"
)

const (
	// Section is the custom section that holds a module's signatures.
	Section = "purgatorio.sig"
	// ABISection is the custom section holding the ABI version.
	ABISection = abi.Section

	MaxEntries     = 8
	MaxCerts       = 4
	MaxCertLen     = 2048
	MaxHybridCert  = 16384
	DigestSize     = 32
	context1       = "pgmodsig1\x00"
	context2       = "pgmodsig2\x00"
	kindKey        = 0
	kindCerts      = 1
	kindHybridKey  = 2
	kindHybridCert = 3
)

// The loader's errors.
const (
	ENotSigned     pgauth.Err = "module not signed"
	EBadSignature  pgauth.Err = "bad signature"
	EModuleRevoked pgauth.Err = "module revoked"
	ETooMany       pgauth.Err = "too many signatures"
	EBadChain      pgauth.Err = "bad certificate chain"
	// A policy that takes only hybrid signatures found a module whose
	// only trusted entries are classical.
	EHybridRequired pgauth.Err = "hybrid signature required"
	ERevoked                   = pgauth.ERevoked
	EExpired                   = pgauth.EExpired
	EUntrusted                 = pgauth.EUntrusted
)

// EBadExec is the error for bytes that are not a module.
const EBadExec pgauth.Err = abi.Ebadexec

var header = []byte("\x00asm\x01\x00\x00\x00")

func leb(buf []byte, pos *int) (uint32, bool) {
	var v uint32
	for i := 0; i < 5; i++ {
		if *pos >= len(buf) {
			return 0, false
		}
		b := buf[*pos]
		*pos++
		if i == 4 && b > 0x0f {
			return 0, false
		}
		v |= uint32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return v, true
		}
	}
	return 0, false
}

func putLeb(out []byte, v uint32) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

type section struct {
	start, end int
	custom     bool
	name, body []byte
}

func sections(wasm []byte) ([]section, bool) {
	if len(wasm) < len(header) || string(wasm[:len(header)]) != string(header) {
		return nil, false
	}
	var out []section
	pos := len(header)
	for pos < len(wasm) {
		s := section{start: pos}
		id := wasm[pos]
		pos++
		size, ok := leb(wasm, &pos)
		if !ok || uint64(pos)+uint64(size) > uint64(len(wasm)) {
			return nil, false
		}
		s.end = pos + int(size)
		if id == 0 {
			body := wasm[pos:s.end]
			p := 0
			n, ok := leb(body, &p)
			if !ok || uint64(p)+uint64(n) > uint64(len(body)) {
				return nil, false
			}
			s.custom, s.name, s.body = true, body[p:p+int(n)], body[p+int(n):]
		}
		out = append(out, s)
		pos = s.end
	}
	return out, true
}

// Info is what the loader needs to know about a module's bytes.
type Info struct {
	Digest [DigestSize]byte
	ABI    []byte   // the first purgatorio.abi section's contents, or empty
	Sigs   [][]byte // each purgatorio.sig section's contents
}

// Inspect computes the digest of wasm and finds its ABI and signature
// sections. It fails (EBadExec) for bytes that are not a section list.
func Inspect(wasm []byte) (*Info, error) {
	secs, ok := sections(wasm)
	if !ok {
		return nil, EBadExec
	}
	h := sha256.New()
	h.Write(header)
	info := &Info{}
	abiSeen := false
	for _, s := range secs {
		if s.custom && string(s.name) == Section {
			info.Sigs = append(info.Sigs, s.body)
			continue
		}
		if s.custom && string(s.name) == ABISection && !abiSeen {
			info.ABI, abiSeen = s.body, true
		}
		h.Write(wasm[s.start:s.end])
	}
	h.Sum(info.Digest[:0])
	return info, nil
}

// Digest is SHA-256 of wasm without its purgatorio.sig sections.
func Digest(wasm []byte) ([DigestSize]byte, error) {
	i, err := Inspect(wasm)
	if err != nil {
		return [DigestSize]byte{}, err
	}
	return i.Digest, nil
}

// Strip returns wasm without its purgatorio.sig sections.
func Strip(wasm []byte) ([]byte, error) {
	secs, ok := sections(wasm)
	if !ok {
		return nil, EBadExec
	}
	out := append(make([]byte, 0, len(wasm)), header...)
	for _, s := range secs {
		if !(s.custom && string(s.name) == Section) {
			out = append(out, wasm[s.start:s.end]...)
		}
	}
	return out, nil
}

// Message is what a signer signs for a module with this digest and ABI
// version: the classical context, or the hybrid one.
func Message(digest [DigestSize]byte, abiVersion []byte, hybrid bool) ([]byte, bool) {
	if len(abiVersion) > 255 {
		return nil, false
	}
	ctx := context1
	if hybrid {
		ctx = context2
	}
	m := make([]byte, 0, len(ctx)+DigestSize+1+len(abiVersion))
	m = append(m, ctx...)
	m = append(m, digest[:]...)
	m = append(m, byte(len(abiVersion)))
	return append(m, abiVersion...), true
}

// Entry is one signature in a purgatorio.sig section (or a signed image
// file).
type Entry struct {
	Sig   []byte
	Key   pgauth.PublicKey // the signing key
	Certs []*pgauth.Cert   // the chain, first the signing key's; empty for a bare key
}

// NewEntry wraps a signature made elsewhere (by an agent). certs is the
// chain for key, or nil for a bare key.
func NewEntry(key pgauth.PublicKey, sig []byte, certs []*pgauth.Cert) (*Entry, error) {
	if !key.Valid() || len(sig) != key.SignatureLen() {
		return nil, EBadChain
	}
	if len(certs) > 0 {
		max := MaxCertLen
		if key.Hybrid() {
			max = MaxHybridCert
		}
		if len(certs) > MaxCerts || !certs[0].Key.Equal(key) {
			return nil, EBadChain
		}
		for _, c := range certs {
			if len(c.Encode()) > max || len(c.Sigs) == 0 || c.Hybrid() != key.Hybrid() {
				return nil, EBadChain
			}
		}
	}
	return &Entry{Sig: append([]byte(nil), sig...), Key: key, Certs: certs}, nil
}

// SignEntry signs a module's digest and ABI version with k.
func SignEntry(k *pgauth.PrivateKey, certs []*pgauth.Cert, digest [DigestSize]byte, abiVersion []byte) (*Entry, error) {
	msg, ok := Message(digest, abiVersion, k.Hybrid())
	if !ok {
		return nil, EBadSignature
	}
	return NewEntry(k.Public(), k.Sign(msg), certs)
}

// Hybrid reports whether the entry is a hybrid signature.
func (e *Entry) Hybrid() bool { return e.Key.Hybrid() }

func (e *Entry) kind() byte {
	k := byte(kindKey)
	if len(e.Certs) > 0 {
		k = kindCerts
	}
	if e.Hybrid() {
		k += 2
	}
	return k
}

// Encode is the entry as it is stored.
func (e *Entry) Encode() []byte {
	out := []byte{e.kind()}
	out = append(out, e.Sig...)
	if len(e.Certs) == 0 {
		return append(out, e.Key...)
	}
	out = append(out, byte(len(e.Certs)))
	for _, c := range e.Certs {
		line := c.Encode()
		out = binary.LittleEndian.AppendUint16(out, uint16(len(line)))
		out = append(out, line...)
	}
	return out
}

func take(buf []byte) (*Entry, []byte, bool) {
	if len(buf) < 1 {
		return nil, nil, false
	}
	kind := buf[0]
	if kind > kindHybridCert {
		return nil, nil, false
	}
	hybrid := kind >= kindHybridKey
	slen, plen, max := pgauth.SignatureSize, pgauth.PublicSize, MaxCertLen
	if hybrid {
		slen, plen, max = pgauth.HybridSignatureSize, pgauth.HybridPublicSize, MaxHybridCert
	}
	rest := buf[1:]
	if len(rest) < slen {
		return nil, nil, false
	}
	e := &Entry{Sig: rest[:slen]}
	rest = rest[slen:]
	if kind == kindKey || kind == kindHybridKey {
		if len(rest) < plen {
			return nil, nil, false
		}
		k, err := pgauth.ParsePublicKey(rest[:plen])
		if err != nil {
			return nil, nil, false
		}
		e.Key = k
		return e, rest[plen:], true
	}
	if len(rest) < 1 || rest[0] == 0 || int(rest[0]) > MaxCerts {
		return nil, nil, false
	}
	n := int(rest[0])
	rest = rest[1:]
	for range n {
		if len(rest) < 2 {
			return nil, nil, false
		}
		l := int(binary.LittleEndian.Uint16(rest))
		rest = rest[2:]
		if l == 0 || l > max || l > len(rest) {
			return nil, nil, false
		}
		line := rest[:l]
		rest = rest[l:]
		if line[l-1] == '\n' {
			return nil, nil, false
		}
		c, err := pgauth.ParseCert(line)
		if err != nil || c.Hybrid() != hybrid {
			return nil, nil, false
		}
		e.Certs = append(e.Certs, c)
	}
	e.Key = e.Certs[0].Key
	return e, rest, true
}

// ParseEntry parses exactly one entry (an agent's reply).
func ParseEntry(buf []byte) (*Entry, bool) {
	e, rest, ok := take(buf)
	return e, ok && len(rest) == 0
}

// Encode is the contents of a purgatorio.sig section: version 1 when
// every entry is classical, else version 2.
func Encode(entries []*Entry) []byte {
	v := byte(1)
	for _, e := range entries {
		if e.Hybrid() {
			v = 2
		}
	}
	out := []byte{v, byte(len(entries))}
	for _, e := range entries {
		out = append(out, e.Encode()...)
	}
	return out
}

// Parse parses the contents of a purgatorio.sig section, strictly.
func Parse(body []byte) ([]*Entry, bool) {
	if len(body) < 2 {
		return nil, false
	}
	version, count := body[0], int(body[1])
	if (version != 1 && version != 2) || count == 0 || count > MaxEntries {
		return nil, false
	}
	rest := body[2:]
	var entries []*Entry
	hybrid := false
	for range count {
		e, r, ok := take(rest)
		if !ok {
			return nil, false
		}
		for _, x := range entries {
			if x.Key.Equal(e.Key) {
				return nil, false
			}
		}
		hybrid = hybrid || e.Hybrid()
		entries = append(entries, e)
		rest = r
	}
	// Version 1 holds classical entries only, and version 2 is written
	// only when there is a hybrid one: one encoding per section.
	if len(rest) != 0 || hybrid != (version == 2) {
		return nil, false
	}
	return entries, true
}

// Entries are a module's entries: none when it is unsigned, an error
// when it has more than one signature section or a malformed one.
func Entries(info *Info) ([]*Entry, error) {
	switch len(info.Sigs) {
	case 0:
		return nil, nil
	case 1:
		e, ok := Parse(info.Sigs[0])
		if !ok {
			return nil, EBadSignature
		}
		return e, nil
	}
	return nil, EBadSignature
}

// Attach returns wasm carrying entry: its other entries are kept, an
// entry by the same key is replaced, and the one signature section is
// written last.
func Attach(wasm []byte, entry *Entry) ([]byte, error) {
	info, err := Inspect(wasm)
	if err != nil {
		return nil, err
	}
	all, err := Entries(info)
	if err != nil {
		return nil, err
	}
	kept := all[:0]
	for _, e := range all {
		if !e.Key.Equal(entry.Key) {
			kept = append(kept, e)
		}
	}
	if len(kept) >= MaxEntries {
		return nil, ETooMany
	}
	body := Encode(append(kept, entry))
	out, err := Strip(wasm)
	if err != nil {
		return nil, err
	}
	name := putLeb(nil, uint32(len(Section)))
	name = append(name, Section...)
	out = append(out, 0)
	out = putLeb(out, uint32(len(name)+len(body)))
	out = append(out, name...)
	return append(out, body...), nil
}

// Sign signs wasm with k (and certs, its chain, or nil) and returns the
// signed module.
func Sign(wasm []byte, k *pgauth.PrivateKey, certs []*pgauth.Cert) ([]byte, error) {
	info, err := Inspect(wasm)
	if err != nil {
		return nil, err
	}
	e, err := SignEntry(k, certs, info.Digest, info.ABI)
	if err != nil {
		return nil, err
	}
	return Attach(wasm, e)
}

// Trust is what a module policy trusts.
type Trust struct {
	Anchors []pgauth.KeyID // /lib/keys/modules (or /lib/keys/aot for images)
	Revoked []pgauth.KeyID // keys and digests
	Damaged bool           // the revocation list is damaged: nothing passes
	Now     uint64         // seconds since 1970 UTC
	// Classical lets classical (Ed25519-only) entries count. Hybrid
	// entries always do.
	Classical bool
}

func (t *Trust) revoked(k pgauth.PublicKey) bool {
	if t.Damaged || pgauth.Contains(t.Revoked, k.ID()) {
		return true
	}
	return k.Hybrid() && pgauth.Contains(t.Revoked, k.Classical().ID())
}

func (t *Trust) anchor(k pgauth.PublicKey) bool { return pgauth.Contains(t.Anchors, k.ID()) }

// Standing is how one entry stands, from worst to best.
type Standing int

const (
	Bad Standing = iota // the signature does not verify
	Untrusted
	Expired
	Revoked
	// Classical is an entry that would be trusted but is classical, under
	// a policy that takes only hybrid signatures.
	Classical
	Trusted
)

func (s Standing) String() string {
	return [...]string{"bad", "untrusted", "expired", "revoked", "classical", "trusted"}[s]
}

func (s Standing) err() error {
	switch s {
	case Untrusted:
		return EUntrusted
	case Expired:
		return EExpired
	case Revoked:
		return ERevoked
	case Classical:
		return EHybridRequired
	}
	return EBadSignature
}

// EntryStanding judges one entry whose signed message is msg.
func EntryStanding(e *Entry, msg []byte, t *Trust) Standing {
	s := standing(e, msg, t)
	if s == Trusted && !e.Hybrid() && !t.Classical {
		return Classical
	}
	return s
}

func standing(e *Entry, msg []byte, t *Trust) Standing {
	if !e.Key.Verify(msg, e.Sig) {
		return Bad
	}
	if len(e.Certs) == 0 {
		switch {
		case t.revoked(e.Key):
			return Revoked
		case t.anchor(e.Key):
			return Trusted
		}
		return Untrusted
	}
	for _, c := range e.Certs {
		if t.revoked(c.Key) {
			return Revoked
		}
	}
	if t.anchor(e.Certs[0].Key) {
		return Trusted
	}
	for _, c := range e.Certs {
		if c.Expired(t.Now) {
			return Expired
		}
	}
	revokedAnchor := false
	for i, c := range e.Certs {
		for _, s := range c.Sigs {
			id := s.Signer.ID()
			if t.anchor(s.Signer) && c.SignedBy(id) {
				if !t.revoked(s.Signer) {
					return Trusted
				}
				revokedAnchor = true
			}
		}
		if i+1 >= len(e.Certs) || !c.SignedBy(e.Certs[i+1].Key.ID()) {
			break
		}
	}
	if revokedAnchor {
		return Revoked
	}
	return Untrusted
}

// Check checks a module against t. It returns the signing keys of its
// trusted entries, or the error a require policy refuses it with.
func Check(info *Info, t *Trust) ([]pgauth.PublicKey, error) {
	if len(info.Sigs) == 0 {
		return nil, ENotSigned
	}
	if !t.Damaged && pgauth.Contains(t.Revoked, pgauth.DigestID(info.Digest)) {
		return nil, EModuleRevoked
	}
	entries, err := Entries(info)
	if err != nil {
		return nil, err
	}
	return checkEntries(entries, t, func(hybrid bool) ([]byte, bool) {
		return Message(info.Digest, info.ABI, hybrid)
	})
}

func checkEntries(entries []*Entry, t *Trust, msg func(hybrid bool) ([]byte, bool)) ([]pgauth.PublicKey, error) {
	best := Bad
	var signers []pgauth.PublicKey
	for _, e := range entries {
		m, ok := msg(e.Hybrid())
		if !ok {
			return nil, EBadSignature
		}
		s := EntryStanding(e, m, t)
		if s == Trusted {
			signers = append(signers, e.Key)
		}
		best = max(best, s)
	}
	if len(signers) == 0 {
		return nil, best.err()
	}
	return signers, nil
}
