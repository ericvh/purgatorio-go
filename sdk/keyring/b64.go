package keyring

import (
	"math/big"
	"strings"
)

const b64alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var b64dec [256]int8

func init() {
	for i := range b64dec {
		b64dec[i] = -1
	}
	for i := 0; i < len(b64alpha); i++ {
		b64dec[b64alpha[i]] = int8(i)
	}
}

// enc64 is lib9's enc64: standard base64 with = padding.
func enc64(in []byte) string {
	var s strings.Builder
	for len(in) >= 3 {
		v := uint32(in[0])<<16 | uint32(in[1])<<8 | uint32(in[2])
		s.WriteByte(b64alpha[v>>18])
		s.WriteByte(b64alpha[v>>12&0x3f])
		s.WriteByte(b64alpha[v>>6&0x3f])
		s.WriteByte(b64alpha[v&0x3f])
		in = in[3:]
	}
	switch len(in) {
	case 2:
		v := uint32(in[0])<<16 | uint32(in[1])<<8
		s.WriteByte(b64alpha[v>>18])
		s.WriteByte(b64alpha[v>>12&0x3f])
		s.WriteByte(b64alpha[v>>6&0x3f])
		s.WriteByte('=')
	case 1:
		v := uint32(in[0]) << 16
		s.WriteByte(b64alpha[v>>18])
		s.WriteByte(b64alpha[v>>12&0x3f])
		s.WriteString("==")
	}
	return s.String()
}

// dec64 is lib9's dec64: characters outside the alphabet, padding
// included, are skipped, and a trailing partial group gives what bytes
// it can.
func dec64(in string) []byte {
	var out []byte
	var v uint32
	i := 0
	for k := 0; k < len(in); k++ {
		c := b64dec[in[k]]
		if c < 0 {
			continue
		}
		v = v<<6 | uint32(c)
		i++
		if i == 4 {
			out = append(out, byte(v>>16), byte(v>>8), byte(v))
			v, i = 0, 0
		}
	}
	switch i {
	case 2:
		out = append(out, byte(v>>4))
	case 3:
		out = append(out, byte(v>>10), byte(v>>2))
	}
	return out
}

// mptobe is libmp's mptobe: the magnitude big-endian, at least one byte.
func mptobe(b *big.Int) []byte {
	m := b.Bytes()
	if len(m) == 0 {
		return []byte{0}
	}
	return m
}

// bigtobase64 is keyring.c's form of a number in keys and certificates:
// base64 of the magnitude, with a leading zero byte when the top bit is
// set, for compatibility with an older signed representation.
func bigtobase64(b *big.Int) string {
	m := b.Bytes()
	if len(m) == 0 {
		m = []byte{0}
	} else if m[0]&0x80 != 0 {
		m = append([]byte{0}, m...)
	}
	return enc64(m)
}

// base64tobig parses one newline-terminated number and returns the rest
// after the newline; nil for an empty line.
func base64tobig(s string) (*big.Int, string) {
	line, rest, _ := strings.Cut(s, "\n")
	if line == "" {
		return nil, rest
	}
	return new(big.Int).SetBytes(dec64(line)), rest
}
