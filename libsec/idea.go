package libsec

import "encoding/binary"

// IDEA block size and key length in bytes.
const (
	IDEABlockSize = 8
	IDEAKeySize   = 16
)

const ideaKeylen = 52

// IDEA is an expanded IDEA key, encryption and decryption schedules, as
// libsec's IDEAstate keys.
type IDEA struct {
	ek [2 * ideaKeylen]uint16
}

func ideamul(x, y uint16) uint16 {
	if x == 0 {
		return 1 - y
	}
	if y == 0 {
		return 1 - x
	}
	z := uint32(x) * uint32(y)
	q, r := uint16(z>>16), uint16(z)
	if r < q {
		return r - q + 1
	}
	return r - q
}

func ideainv(x uint16) uint16 {
	if x <= 1 {
		return x
	}
	r0, r1 := int32(0x10001), int32(x)
	v0, v1 := int32(0), int32(1)
	for r1 != 0 {
		q := r0 / r1
		r0, r1 = r1, r0-q*r1
		v0, v1 = v1, v0-q*v1
	}
	if v0 < 0 {
		v0 += 0x10001
	}
	return uint16(v0)
}

// NewIDEA expands a 16-byte key.
func NewIDEA(key []byte) *IDEA {
	c := new(IDEA)
	ek := c.ek[:ideaKeylen]
	for i := 0; i < 8; i++ {
		ek[i] = binary.BigEndian.Uint16(key[2*i:])
	}
	e := 0
	for i, j := 8, 1; i < ideaKeylen; i, j = i+1, j+1 {
		ek[i] = ek[e+j&7]<<9 | ek[e+(j+1)&7]>>7
		if (i+1)&7 == 0 {
			e += 8
		}
	}
	ek[49], ek[50] = ek[50], ek[49]
	dk := c.ek[ideaKeylen:]
	for i := 0; i < 54; i += 6 {
		dk[i] = ideainv(ek[48-i])
		dk[i+1] = -ek[50-i]
		dk[i+2] = -ek[49-i]
		dk[i+3] = ideainv(ek[51-i])
		if i < 48 {
			dk[i+4] = ek[46-i]
			dk[i+5] = ek[47-i]
		}
	}
	return c
}

func (c *IDEA) BlockSize() int { return IDEABlockSize }

// Encrypt and Decrypt make IDEA a cipher.Block.
func (c *IDEA) Encrypt(dst, src []byte) { c.crypt(dst, src, c.ek[:ideaKeylen]) }
func (c *IDEA) Decrypt(dst, src []byte) { c.crypt(dst, src, c.ek[ideaKeylen:]) }

func (c *IDEA) crypt(dst, src []byte, k []uint16) {
	var x [4]uint16
	for i := range x {
		x[i] = binary.BigEndian.Uint16(src[2*i:])
	}
	for i := 0; i < 17; i++ {
		if i&1 == 0 {
			x[0] = ideamul(x[0], k[3*i])
			t := x[2] + k[3*i+2]
			x[2] = x[1] + k[3*i+1]
			x[3] = ideamul(x[3], k[3*i+3])
			x[1] = t
		} else {
			t := ideamul(k[3*i+1], x[0]^x[1])
			y := ideamul(t+(x[2]^x[3]), k[3*i+2])
			z := t + y
			x[0] ^= y
			x[1] ^= y
			x[2] ^= z
			x[3] ^= z
		}
	}
	for i := range x {
		binary.BigEndian.PutUint16(dst[2*i:], x[i])
	}
}
