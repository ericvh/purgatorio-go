// Package image is the file format of modules compiled ahead of time: an
// image beside a module (/bin/cmd.aot next to /bin/cmd.wasm) holds the
// native code wazero's compiler made from it on the build host, which
// the kernel loads instead of compiling the module or interpreting it.
//
// The format is the Rust sibling's (its docs/signing.md, "Ahead-of-time
// images") with a kind of its own for wazero:
//
//	magic   "PURGIMG1"
//	kind    u8          2: wazero's compiled module for arm64
//	zero    3 bytes
//	module  [32]        the module's digest (Digest)
//	len     u64 LE
//	code    len bytes   wazero's compilation cache entry for the module
//	sig     ...         to the end: the signatures, empty when unsigned
//
// A signature is over Message: the kind, the module's digest and the
// SHA-256 of the code, so it says "this code is a compilation of that
// module for this target".
package image

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	Magic = "PURGIMG1"
	// KindWazeroArm64 is wazero's compiled module (its compilation
	// cache entry, as wazero.NewCompilationCacheWithDir writes it) for
	// arm64. The Rust sibling uses 0 (wasmtime, aarch64) and 1 (Pulley).
	KindWazeroArm64 = 2

	headerSize = 8 + 4 + 32 + 8
	// Context starts every signed message, so an image signature never
	// verifies as a module's, a certificate's or an authentication's.
	Context = "pgaotimg1"
)

// ErrFormat is any image that does not parse.
var ErrFormat = errors.New("bad image")

// Image is a parsed image file.
type Image struct {
	Kind   byte
	Module [32]byte // the module's digest
	Code   []byte   // wazero's compiled module
	Sig    []byte   // the signature part, empty when unsigned
}

// Encode returns the image file of im.
func Encode(im *Image) []byte {
	b := make([]byte, 0, headerSize+len(im.Code)+len(im.Sig))
	b = append(b, Magic...)
	b = append(b, im.Kind, 0, 0, 0)
	b = append(b, im.Module[:]...)
	b = binary.LittleEndian.AppendUint64(b, uint64(len(im.Code)))
	b = append(b, im.Code...)
	return append(b, im.Sig...)
}

// Parse parses an image file strictly. Code and Sig share b.
func Parse(b []byte) (*Image, error) {
	if len(b) < headerSize || string(b[:8]) != Magic || b[9] != 0 || b[10] != 0 || b[11] != 0 {
		return nil, ErrFormat
	}
	im := &Image{Kind: b[8]}
	copy(im.Module[:], b[12:44])
	n := binary.LittleEndian.Uint64(b[44:52])
	if n == 0 || n > uint64(len(b)-headerSize) {
		return nil, ErrFormat
	}
	im.Code = b[headerSize : headerSize+int(n) : headerSize+int(n)]
	im.Sig = b[headerSize+int(n):]
	return im, nil
}

// Message is what an image's signatures sign:
//
//	"pgaotimg1" 0x00 || kind u8 || module digest[32] || SHA-256(code)[32]
func (im *Image) Message() []byte {
	h := sha256.Sum256(im.Code)
	m := make([]byte, 0, len(Context)+1+1+32+32)
	m = append(m, Context...)
	m = append(m, 0, im.Kind)
	m = append(m, im.Module[:]...)
	return append(m, h[:]...)
}

// Digest is a module's digest: SHA-256 over the module with every
// custom section named purgatorio.sig left out, so a module's
// signatures do not change it (the Rust sibling's docs/signing.md,
// "Digest"). It fails on bytes that are not a module.
func Digest(module []byte) ([32]byte, error) {
	var zero [32]byte
	if len(module) < 8 || string(module[:8]) != "\x00asm\x01\x00\x00\x00" {
		return zero, ErrFormat
	}
	h := sha256.New()
	h.Write(module[:8])
	for off := 8; off < len(module); {
		start := off
		id := module[off]
		size, n, ok := uleb(module[off+1:])
		if !ok {
			return zero, ErrFormat
		}
		body := off + 1 + n
		end := body + int(size)
		if end > len(module) || end < body {
			return zero, ErrFormat
		}
		skip := false
		if id == 0 {
			nl, m, ok := uleb(module[body:end])
			if !ok || uint64(end-body-m) < uint64(nl) {
				return zero, ErrFormat
			}
			name := module[body+m : body+m+int(nl)]
			skip = string(name) == "purgatorio.sig"
		}
		if !skip {
			h.Write(module[start:end])
		}
		off = end
	}
	var d [32]byte
	h.Sum(d[:0])
	return d, nil
}

// uleb reads an unsigned LEB128 of at most 5 bytes whose fifth byte is
// at most 0x0f.
func uleb(b []byte) (uint32, int, bool) {
	var v uint32
	for i := 0; i < 5 && i < len(b); i++ {
		c := b[i]
		if i == 4 && c > 0x0f {
			return 0, 0, false
		}
		v |= uint32(c&0x7f) << (7 * i)
		if c&0x80 == 0 {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}
