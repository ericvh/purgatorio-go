package pgauth

import (
	"crypto/cipher"
	"encoding/binary"

	"github.com/ericvh/purgatorio-go/libsec"
)

// The record layer that protects a Styx conversation after a version 3
// or 4 handshake (docs/auth.md, "Record layer"); the Rust sibling's,
// byte for byte. Each direction has its own ChaCha20-Poly1305 key and
// counts its records from 0:
//
//	record = size[4] || ciphertext || tag[16]
//	nonce  = seq u64 LE || 0[4]
//	aad    = size[4]
//
// size (u32 LE) counts the whole record, header included. The sequence
// number is never sent, so a record replayed, dropped, reordered or
// changed fails to open, and so does every record after it.
const (
	RecordHeader   = 4
	RecordTag      = 16
	RecordOverhead = RecordHeader + RecordTag
	// RecordLimit is the most records one direction carries.
	RecordLimit = uint64(1) << 48

	EBadMAC    Err = "bad record mac"
	ETooLong   Err = "record too long"
	ETooShort  Err = "record too short"
	EExhausted Err = "record sequence exhausted"
)

func nonce(seq uint64) []byte {
	var n [12]byte
	binary.LittleEndian.PutUint64(n[:], seq)
	return n[:]
}

func newAEAD(key []byte) cipher.AEAD {
	a, err := libsec.NewChaCha20Poly1305(key)
	if err != nil {
		panic(err)
	}
	return a
}

// RecordSize is the size of the record whose header is header, checked
// against the largest plaintext the caller takes. The caller reads that
// many bytes, header included.
func RecordSize(header []byte, maxPlain int) (int, error) {
	n := int(binary.LittleEndian.Uint32(header))
	if n < RecordOverhead {
		return 0, ETooShort
	}
	if n-RecordOverhead > maxPlain {
		return 0, ETooLong
	}
	return n, nil
}

// Sealer is the sending end of one direction. It is not safe for
// concurrent use.
type Sealer struct {
	aead cipher.AEAD
	seq  uint64
}

// NewSealer is a sealer whose first record is number 0.
func NewSealer(key []byte) *Sealer { return &Sealer{aead: newAEAD(key)} }

// NewSealerAt is a sealer whose next record is number seq.
func NewSealerAt(key []byte, seq uint64) *Sealer { return &Sealer{aead: newAEAD(key), seq: seq} }

// Seal appends plain, sealed as the next record, to dst.
func (s *Sealer) Seal(dst, plain []byte) ([]byte, error) {
	if s.seq >= RecordLimit {
		return dst, EExhausted
	}
	n := len(plain) + RecordOverhead
	if uint64(n) > 0xffffffff {
		return dst, ETooLong
	}
	var hdr [RecordHeader]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(n))
	dst = append(dst, hdr[:]...)
	dst = s.aead.Seal(dst, nonce(s.seq), plain, hdr[:])
	s.seq++
	return dst, nil
}

// Opener is the receiving end of one direction.
type Opener struct {
	aead   cipher.AEAD
	seq    uint64
	failed bool
}

// NewOpener is an opener whose first record is number 0.
func NewOpener(key []byte) *Opener { return &Opener{aead: newAEAD(key)} }

// NewOpenerAt is an opener whose next record is number seq.
func NewOpenerAt(key []byte, seq uint64) *Opener { return &Opener{aead: newAEAD(key), seq: seq} }

// Open opens the next record, all of it, header included, and returns
// the plaintext (in rec's storage). After a failure every later record
// fails too.
func (o *Opener) Open(rec []byte) ([]byte, error) {
	if o.failed {
		return nil, EBadMAC
	}
	if len(rec) < RecordHeader {
		o.failed = true
		return nil, ETooShort
	}
	p, err := o.OpenBody(rec[:RecordHeader], rec[RecordHeader:])
	return p, err
}

// OpenBody opens the next record given as its header and the rest of
// it (ciphertext and tag), in place.
func (o *Opener) OpenBody(header, body []byte) ([]byte, error) {
	if o.failed {
		return nil, EBadMAC
	}
	p, err := o.open(header, body)
	if err != nil {
		o.failed = true
	}
	return p, err
}

func (o *Opener) open(header, body []byte) ([]byte, error) {
	n, err := RecordSize(header, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	if n != len(body)+RecordHeader {
		return nil, EBadMAC
	}
	if o.seq >= RecordLimit {
		return nil, EExhausted
	}
	p, err := o.aead.Open(body[:0], nonce(o.seq), body, header)
	if err != nil {
		return nil, EBadMAC
	}
	o.seq++
	return p, nil
}
