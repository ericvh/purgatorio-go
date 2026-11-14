package libsec

import (
	"encoding/binary"
	"hash"
	"math/bits"
)

// MD4Size is the length of an MD4 digest.
const MD4Size = 16

type md4 struct {
	s   [4]uint32
	x   [64]byte
	nx  int
	len uint64
}

// NewMD4 returns an MD4 hash (RFC 1320).
func NewMD4() hash.Hash {
	d := new(md4)
	d.Reset()
	return d
}

func (d *md4) Reset() {
	d.s = [4]uint32{0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476}
	d.nx, d.len = 0, 0
}

func (d *md4) Size() int      { return MD4Size }
func (d *md4) BlockSize() int { return 64 }

func (d *md4) Write(p []byte) (int, error) {
	n := len(p)
	d.len += uint64(n)
	if d.nx > 0 {
		k := copy(d.x[d.nx:], p)
		d.nx += k
		p = p[k:]
		if d.nx == 64 {
			d.block(d.x[:])
			d.nx = 0
		}
	}
	for len(p) >= 64 {
		d.block(p[:64])
		p = p[64:]
	}
	d.nx += copy(d.x[:], p)
	return n, nil
}

func (d *md4) Sum(in []byte) []byte {
	c := *d
	var pad [72]byte
	pad[0] = 0x80
	n := c.len
	if n%64 < 56 {
		c.Write(pad[:56-n%64])
	} else {
		c.Write(pad[:64+56-n%64])
	}
	var l [8]byte
	binary.LittleEndian.PutUint64(l[:], n<<3)
	c.Write(l[:])
	for _, v := range c.s {
		in = binary.LittleEndian.AppendUint32(in, v)
	}
	return in
}

var md4shift = [3][4]int{{3, 7, 11, 19}, {3, 5, 9, 13}, {3, 9, 11, 15}}
var md4x2 = [16]int{0, 4, 8, 12, 1, 5, 9, 13, 2, 6, 10, 14, 3, 7, 11, 15}
var md4x3 = [16]int{0, 8, 4, 12, 2, 10, 6, 14, 1, 9, 5, 13, 3, 11, 7, 15}

func (d *md4) block(p []byte) {
	var x [16]uint32
	for i := range x {
		x[i] = binary.LittleEndian.Uint32(p[4*i:])
	}
	a, b, c, e := d.s[0], d.s[1], d.s[2], d.s[3]
	for i := 0; i < 16; i++ {
		f := (b & c) | (^b & e)
		a, b, c, e = e, bits.RotateLeft32(a+f+x[i], md4shift[0][i%4]), b, c
	}
	for i := 0; i < 16; i++ {
		f := (b & c) | (b & e) | (c & e)
		a, b, c, e = e, bits.RotateLeft32(a+f+x[md4x2[i]]+0x5a827999, md4shift[1][i%4]), b, c
	}
	for i := 0; i < 16; i++ {
		f := b ^ c ^ e
		a, b, c, e = e, bits.RotateLeft32(a+f+x[md4x3[i]]+0x6ed9eba1, md4shift[2][i%4]), b, c
	}
	d.s[0] += a
	d.s[1] += b
	d.s[2] += c
	d.s[3] += e
}

// Clone makes MD4 a hash.Cloner.
func (d *md4) Clone() (hash.Cloner, error) {
	c := *d
	return &c, nil
}
