package keyring

import (
	"crypto/rand"
	"math/big"
	"strings"
)

// IPint is Keyring's infinite precision integer.
type IPint struct{ v *big.Int }

// NewIPint wraps b, which the IPint then owns.
func NewIPint(b *big.Int) *IPint { return &IPint{b} }

// Big returns the value as a big.Int, which must not be changed.
func (i *IPint) Big() *big.Int { return i.v }

func ip(b *big.Int) *IPint { return &IPint{b} }

// IPtoB64 is mptoa in base 64: a minus sign for a negative number, then
// base64 of the magnitude.
func (i *IPint) IPtoB64() string {
	s := enc64(mptobe(i.v))
	if i.v.Sign() < 0 {
		return "-" + s
	}
	return s
}

// IPtoB64z is as IPtoB64 but with a leading zero byte when the top bit of
// the magnitude is set, the form keys and certificates use.
func (i *IPint) IPtoB64z() string { return bigtobase64(i.v) }

// B64toIP parses base64, as strtomp: an optional sign, the digits, then
// only = padding. It returns nil for a malformed string.
func B64toIP(s string) *IPint { return StrtoIP(s, 64) }

// IPtoBytes is the magnitude big-endian, at least one byte.
func (i *IPint) IPtoBytes() []byte   { return mptobe(i.v) }
func (i *IPint) IPtoBEBytes() []byte { return mptobe(i.v) }

func BytestoIP(b []byte) *IPint   { return ip(new(big.Int).SetBytes(b)) }
func BEBytestoIP(b []byte) *IPint { return ip(new(big.Int).SetBytes(b)) }
func InttoIP(n int) *IPint        { return ip(big.NewInt(int64(n))) }

// IPtoInt gives the low 32 bits, as a Limbo int.
func (i *IPint) IPtoInt() int { return int(int32(i.v.Int64())) }

const b32alpha = "23456789abcdefghijkmnpqrstuvwxyz"

// IPtoStr formats in base 10, 16 (upper case), 32 or 64, as mptoa;
// other bases give base 16.
func (i *IPint) IPtoStr(base int) string {
	var s string
	a := new(big.Int).Abs(i.v)
	switch base {
	case 64:
		s = enc64(mptobe(a))
	case 10:
		s = a.Text(10)
	case 32:
		s = enc32(mptobe(a))
	default:
		s = strings.ToUpper(a.Text(16))
	}
	if i.v.Sign() < 0 {
		return "-" + s
	}
	return s
}

func enc32(in []byte) string {
	// Plan 9's enc32 takes a multiple of five bytes.
	if r := len(in) % 5; r != 0 {
		in = append(make([]byte, 0, len(in)+5-r), in...)
		in = append(in, make([]byte, 5-r)...)
	}
	var s strings.Builder
	for ; len(in) >= 5; in = in[5:] {
		v := uint64(in[0])<<32 | uint64(in[1])<<24 | uint64(in[2])<<16 | uint64(in[3])<<8 | uint64(in[4])
		for k := 35; k >= 0; k -= 5 {
			s.WriteByte(b32alpha[v>>uint(k)&31])
		}
	}
	return s.String()
}

// StrtoIP parses s in base 10, 16, 32 or 64, as strtomp; nil if s is not
// wholly a number (trailing = padding aside).
func StrtoIP(s string, base int) *IPint {
	t := strings.TrimLeft(s, " \t\n\r")
	neg := false
	for len(t) > 0 && (t[0] == '-' || t[0] == '+') {
		if t[0] == '-' {
			neg = !neg
		}
		t = t[1:]
	}
	var digits string
	var b *big.Int
	switch base {
	case 64:
		n := 0
		for n < len(t) && b64dec[t[n]] >= 0 {
			n++
		}
		digits = t[:n]
		b = new(big.Int).SetBytes(dec64(digits))
	case 32:
		n := 0
		for n < len(t) && strings.IndexByte(b32alpha, t[n]) >= 0 {
			n++
		}
		digits = t[:n]
		b = new(big.Int).SetBytes(dec32(digits))
	default:
		if base != 10 {
			base = 16
		}
		n := 0
		for n < len(t) && digitval(t[n]) < base {
			n++
		}
		digits = t[:n]
		b = new(big.Int)
		if digits != "" {
			b.SetString(digits, base)
		}
	}
	if digits == "" {
		return nil
	}
	if strings.TrimLeft(t[len(digits):], "=") != "" {
		return nil
	}
	if neg {
		b.Neg(b)
	}
	return ip(b)
}

func digitval(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return 99
}

func dec32(in string) []byte {
	var out []byte
	var v uint64
	n := 0
	for i := 0; i < len(in); i++ {
		v = v<<5 | uint64(strings.IndexByte(b32alpha, in[i]))
		n += 5
		if n >= 8 {
			n -= 8
			out = append(out, byte(v>>uint(n)))
		}
	}
	return out
}

// mprand returns a random number of at most bits bits, as libmp's mprand.
func mprand(bits int) *big.Int {
	if bits <= 0 {
		return new(big.Int)
	}
	b := make([]byte, (bits+7)/8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	if r := bits % 8; r != 0 {
		b[0] &= byte(1<<r - 1)
	}
	return new(big.Int).SetBytes(b)
}

// Random returns a random number below 2**maxbits; Inferno ignores
// minbits as well.
func Random(minbits, maxbits int) *IPint { return ip(mprand(maxbits)) }

func (i *IPint) Bits() int { return i.v.BitLen() }

// Expmod is base**exp mod mod.
func (i *IPint) Expmod(exp, mod *IPint) *IPint {
	if mod.v.Sign() == 0 {
		return ip(new(big.Int).Exp(i.v, exp.v, nil))
	}
	r := new(big.Int).Exp(i.v, new(big.Int).Abs(exp.v), mod.v)
	if exp.v.Sign() < 0 {
		r.ModInverse(r, mod.v)
	}
	return ip(r)
}

func (i *IPint) Invert(mod *IPint) *IPint {
	r := new(big.Int).ModInverse(i.v, mod.v)
	if r == nil {
		r = new(big.Int)
	}
	return ip(r)
}

func (i *IPint) Add(j *IPint) *IPint { return ip(new(big.Int).Add(i.v, j.v)) }
func (i *IPint) Sub(j *IPint) *IPint { return ip(new(big.Int).Sub(i.v, j.v)) }
func (i *IPint) Neg() *IPint         { return ip(new(big.Int).Neg(i.v)) }
func (i *IPint) Mul(j *IPint) *IPint { return ip(new(big.Int).Mul(i.v, j.v)) }

// Div gives quotient and remainder, truncated toward zero as libmp's
// mpdiv does.
func (i *IPint) Div(j *IPint) (*IPint, *IPint) {
	if j.v.Sign() == 0 {
		return ip(new(big.Int)), ip(new(big.Int))
	}
	q, r := new(big.Int).QuoRem(i.v, j.v, new(big.Int))
	return ip(q), ip(r)
}

// Mod is the non-negative remainder.
func (i *IPint) Mod(j *IPint) *IPint {
	if j.v.Sign() == 0 {
		return ip(new(big.Int))
	}
	return ip(new(big.Int).Mod(i.v, new(big.Int).Abs(j.v)))
}

func (i *IPint) Eq(j *IPint) bool    { return i.v.Cmp(j.v) == 0 }
func (i *IPint) Cmp(j *IPint) int    { return i.v.Cmp(j.v) }
func (i *IPint) Copy() *IPint        { return ip(new(big.Int).Set(i.v)) }
func (i *IPint) Shl(n int) *IPint    { return ip(new(big.Int).Lsh(i.v, uint(n))) }
func (i *IPint) Shr(n int) *IPint    { return ip(new(big.Int).Rsh(i.v, uint(n))) }
func (i *IPint) And(j *IPint) *IPint { return ip(new(big.Int).And(i.v, j.v)) }
func (i *IPint) Ori(j *IPint) *IPint { return ip(new(big.Int).Or(i.v, j.v)) }
func (i *IPint) Xor(j *IPint) *IPint { return ip(new(big.Int).Xor(i.v, j.v)) }
func (i *IPint) Not() *IPint         { return ip(new(big.Int).Not(i.v)) }
