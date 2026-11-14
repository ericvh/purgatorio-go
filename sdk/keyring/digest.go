package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"hash"

	"github.com/ericvh/purgatorio-go/libsec"
)

// Digest lengths.
const (
	SHA1dlen   = 20
	SHA224dlen = 28
	SHA256dlen = 32
	SHA384dlen = 48
	SHA512dlen = 64
	MD5dlen    = 16
	MD4dlen    = 16
)

// DigestState is the state of a digest in progress. A digest call with
// a nil state starts one; with a digest buffer it also writes the digest
// of everything so far.
type DigestState struct {
	h hash.Hash
}

// Copy returns an independent copy of the state.
func (d *DigestState) Copy() *DigestState {
	if d == nil {
		return nil
	}
	n, err := d.h.(hash.Cloner).Clone()
	if err != nil {
		panic("keyring: digest state cannot be copied: " + err.Error())
	}
	return &DigestState{n.(hash.Hash)}
}

func digestx(buf, digest []byte, state *DigestState, mk func() hash.Hash) *DigestState {
	if state == nil {
		state = &DigestState{mk()}
	}
	state.h.Write(buf)
	if digest != nil {
		copy(digest, state.h.Sum(nil))
	}
	return state
}

// SHA1 adds buf to state; see DigestState.
func SHA1(buf, digest []byte, state *DigestState) *DigestState {
	return digestx(buf, digest, state, sha1.New)
}

func SHA224(buf, digest []byte, state *DigestState) *DigestState {
	return digestx(buf, digest, state, sha256.New224)
}

func SHA256(buf, digest []byte, state *DigestState) *DigestState {
	return digestx(buf, digest, state, sha256.New)
}

func SHA384(buf, digest []byte, state *DigestState) *DigestState {
	return digestx(buf, digest, state, sha512.New384)
}

func SHA512(buf, digest []byte, state *DigestState) *DigestState {
	return digestx(buf, digest, state, sha512.New)
}

func MD5(buf, digest []byte, state *DigestState) *DigestState {
	return digestx(buf, digest, state, md5.New)
}

func MD4(buf, digest []byte, state *DigestState) *DigestState {
	return digestx(buf, digest, state, libsec.NewMD4)
}

// HMAC_SHA1 and HMAC_MD5 are keyed digests; key is used when state is
// nil.
func HMAC_SHA1(data, key, digest []byte, state *DigestState) *DigestState {
	return digestx(data, digest, state, func() hash.Hash { return hmac.New(sha1.New, key) })
}

func HMAC_MD5(data, key, digest []byte, state *DigestState) *DigestState {
	return digestx(data, digest, state, func() hash.Hash { return hmac.New(md5.New, key) })
}

// Cipher directions.
const (
	Encrypt = 0
	Decrypt = 1
)

// Block sizes.
const (
	AESbsize  = 16
	DESbsize  = 8
	IDEAbsize = 8
)

// BlockState is an expanded block cipher key and the chaining vector,
// Keyring's AESstate, DESstate and IDEAstate.
type BlockState struct {
	b    cipher.Block
	ivec []byte
}

type (
	AESstate  = BlockState
	DESstate  = BlockState
	IDEAstate = BlockState
)

func blockstate(b cipher.Block, ivec []byte) *BlockState {
	s := &BlockState{b: b, ivec: make([]byte, b.BlockSize())}
	copy(s.ivec, ivec)
	return s
}

// AESsetup expands a 16, 24 or 32 byte key; nil for another length.
func AESsetup(key, ivec []byte) *AESstate {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	return blockstate(b, ivec)
}

// DESsetup takes an 8-byte key.
func DESsetup(key, ivec []byte) *DESstate {
	if len(key) < 8 {
		return nil
	}
	b, _ := des.NewCipher(key[:8])
	return blockstate(b, ivec)
}

// IDEAsetup takes a 16-byte key.
func IDEAsetup(key, ivec []byte) *IDEAstate {
	if len(key) < 16 {
		return nil
	}
	return blockstate(libsec.NewIDEA(key[:16]), ivec)
}

// ECB encrypts or decrypts whole blocks of buf in place.
func ECB(s *BlockState, buf []byte, direction int) {
	bs := s.b.BlockSize()
	for ; len(buf) >= bs; buf = buf[bs:] {
		if direction == Encrypt {
			s.b.Encrypt(buf, buf)
		} else {
			s.b.Decrypt(buf, buf)
		}
	}
}

// CBC encrypts or decrypts buf in place, chaining through the state. As
// libsec's aesCBCencrypt, a final partial block is XORed with the
// encrypted chaining vector, so the decryptor must be given buffers of
// the same sizes.
func CBC(s *BlockState, buf []byte, direction int) {
	bs := s.b.BlockSize()
	tmp := make([]byte, bs)
	for ; len(buf) >= bs; buf = buf[bs:] {
		if direction == Encrypt {
			subtleXOR(buf[:bs], s.ivec)
			s.b.Encrypt(buf, buf)
			copy(s.ivec, buf[:bs])
		} else {
			copy(tmp, buf[:bs])
			s.b.Decrypt(buf, buf)
			subtleXOR(buf[:bs], s.ivec)
			copy(s.ivec, tmp)
		}
	}
	if len(buf) > 0 {
		s.b.Encrypt(s.ivec, s.ivec)
		subtleXOR(buf, s.ivec[:len(buf)])
	}
}

func subtleXOR(dst, src []byte) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}

// AEScbc and the others are Keyring's names for the modes.
func AEScbc(s *AESstate, buf []byte, direction int)   { CBC(s, buf, direction) }
func DESecb(s *DESstate, buf []byte, direction int)   { ECB(s, buf, direction) }
func DEScbc(s *DESstate, buf []byte, direction int)   { CBC(s, buf, direction) }
func IDEAecb(s *IDEAstate, buf []byte, direction int) { ECB(s, buf, direction) }
func IDEAcbc(s *IDEAstate, buf []byte, direction int) { CBC(s, buf, direction) }

// RC4state is an RC4 key stream.
type RC4state = libsec.RC4

func RC4setup(seed []byte) *RC4state { return libsec.NewRC4(seed) }
func RC4(s *RC4state, buf []byte)    { s.XOR(buf) }
func RC4skip(s *RC4state, n int)     { s.Skip(n) }
func RC4back(s *RC4state, n int)     { s.Back(n) }
