package modsig

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/ericvh/purgatorio-go/pgauth"
)

// A signed ahead-of-time image file (docs/signing.md, "Ahead-of-time
// images"), the Rust sibling's format:
//
//	magic   "PURGIMG1"
//	kind    u8          the image's target
//	zero    3 bytes
//	module  [32]        the module's digest
//	len     u64 LE
//	image   len bytes
//	sig     ...         to the end: a purgatorio.sig section body
//
// Each classical entry signs "pgaotimg1" 0x00 || kind u8 || module digest
// || SHA-256(image); a hybrid entry signs the same with "pgaotimg2". The
// kind byte is the engine's business: 0 and 1 are the Rust sibling's
// (native aarch64, Pulley); this package does not interpret it.
const (
	ImageMagic  = "PURGIMG1"
	imageHeader = 8 + 4 + DigestSize + 8
	imgContext1 = "pgaotimg1\x00"
	imgContext2 = "pgaotimg2\x00"

	// EUntrustedImage: no signature verifies against the trust lists.
	EUntrustedImage pgauth.Err = "untrusted AOT image"
	// EBadImage: the file does not parse, or is for another module or
	// kind.
	EBadImage pgauth.Err = "bad AOT image"
)

// ImageMessage is what an image entry signs.
func ImageMessage(kind byte, module, image [DigestSize]byte, hybrid bool) []byte {
	ctx := imgContext1
	if hybrid {
		ctx = imgContext2
	}
	m := make([]byte, 0, len(ctx)+1+2*DigestSize)
	m = append(m, ctx...)
	m = append(m, kind)
	m = append(m, module[:]...)
	return append(m, image[:]...)
}

// Image is a signed image file, parsed but not verified.
type Image struct {
	Kind    byte
	Module  [DigestSize]byte // the digest of the module it was compiled from
	Image   []byte
	Entries []*Entry
}

// ParseImage parses a signed image file strictly: the signature part is
// a whole purgatorio.sig body with at least one entry.
func ParseImage(file []byte) (*Image, error) {
	if len(file) < imageHeader || string(file[:8]) != ImageMagic {
		return nil, EBadImage
	}
	if file[9] != 0 || file[10] != 0 || file[11] != 0 {
		return nil, EBadImage
	}
	img := &Image{Kind: file[8]}
	copy(img.Module[:], file[12:12+DigestSize])
	n := binary.LittleEndian.Uint64(file[12+DigestSize:])
	if n > uint64(len(file)-imageHeader) {
		return nil, EBadImage
	}
	end := imageHeader + int(n)
	entries, ok := Parse(file[end:])
	if !ok {
		return nil, EBadImage
	}
	img.Image, img.Entries = file[imageHeader:end], entries
	return img, nil
}

// Verify returns the image if it is a compilation of the module whose
// digest is module, for kind, and one of its signatures is trusted.
// Neither the module's digest nor the image's may be revoked, and a
// damaged revocation list refuses everything.
func (img *Image) Verify(kind byte, module [DigestSize]byte, t *Trust) ([]byte, error) {
	if img.Kind != kind || img.Module != module {
		return nil, EBadImage
	}
	d := sha256.Sum256(img.Image)
	if t.Damaged || pgauth.Contains(t.Revoked, pgauth.DigestID(module)) || pgauth.Contains(t.Revoked, pgauth.DigestID(d)) {
		return nil, EModuleRevoked
	}
	for _, e := range img.Entries {
		if EntryStanding(e, ImageMessage(kind, module, d, e.Hybrid()), t) == Trusted {
			return img.Image, nil
		}
	}
	return nil, EUntrustedImage
}

// BuildImage writes a signed image file for image, compiled from the
// module whose digest is module, signed by k (with certs, its chain, or
// nil). Both signature algorithms are deterministic, so the same inputs
// give the same file on every host.
func BuildImage(kind byte, module [DigestSize]byte, image []byte, k *pgauth.PrivateKey, certs []*pgauth.Cert) ([]byte, error) {
	d := sha256.Sum256(image)
	e, err := NewEntry(k.Public(), k.Sign(ImageMessage(kind, module, d, k.Hybrid())), certs)
	if err != nil {
		return nil, err
	}
	return appendImage(kind, module, image, []*Entry{e}), nil
}

// SignImage adds k's signature to a signed image file (countersigning):
// an entry by the same key is replaced, the others are kept.
func SignImage(file []byte, k *pgauth.PrivateKey, certs []*pgauth.Cert) ([]byte, error) {
	img, err := ParseImage(file)
	if err != nil {
		return nil, err
	}
	d := sha256.Sum256(img.Image)
	e, err := NewEntry(k.Public(), k.Sign(ImageMessage(img.Kind, img.Module, d, k.Hybrid())), certs)
	if err != nil {
		return nil, err
	}
	var kept []*Entry
	for _, x := range img.Entries {
		if !x.Key.Equal(e.Key) {
			kept = append(kept, x)
		}
	}
	if len(kept) >= MaxEntries {
		return nil, ETooMany
	}
	return appendImage(img.Kind, img.Module, img.Image, append(kept, e)), nil
}

func appendImage(kind byte, module [DigestSize]byte, image []byte, entries []*Entry) []byte {
	out := make([]byte, 0, imageHeader+len(image)+4096)
	out = append(out, ImageMagic...)
	out = append(out, kind, 0, 0, 0)
	out = append(out, module[:]...)
	out = binary.LittleEndian.AppendUint64(out, uint64(len(image)))
	out = append(out, image...)
	return append(out, Encode(entries)...)
}
