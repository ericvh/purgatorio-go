package keyring

import (
	"crypto/dsa"
	"crypto/rand"
	"math/big"
)

// A signature algorithm, as libkeyring's SigAlgVec. Keys and signatures
// are lists of numbers: a secret key starts with its public key's
// numbers, so sk2pk takes a prefix.
type sigalg struct {
	name                    string
	pkattr, skattr, sigattr []string
	gensk                   func(bits int) []*big.Int
	genskfrompk             func(pk []*big.Int) []*big.Int
	sign                    func(m *big.Int, sk []*big.Int) []*big.Int
	verify                  func(m *big.Int, sig, pk []*big.Int) bool
}

var (
	big1 = big.NewInt(1)
	big2 = big.NewInt(2)
)

// The algorithms in the order Inferno registers them.
var sigalgs = []*sigalg{
	{
		name:    "elgamal",
		pkattr:  []string{"p", "alpha", "key"},
		skattr:  []string{"p", "alpha", "key", "!secret"},
		sigattr: []string{"r", "s"},
		gensk: func(bits int) []*big.Int {
			p, alpha := gensafeprime(bits)
			return egkey(p, alpha)
		},
		genskfrompk: func(pk []*big.Int) []*big.Int { return egkey(pk[0], pk[1]) },
		sign:        egsign,
		verify:      egverify,
	},
	{
		name:        "rsa",
		pkattr:      []string{"n", "ek"},
		skattr:      []string{"n", "ek", "!dk", "!p", "!q", "!kp", "!kq", "!c2"},
		sigattr:     []string{"val"},
		gensk:       func(bits int) []*big.Int { return rsagen(bits, 17) },
		genskfrompk: func(pk []*big.Int) []*big.Int { return rsagen(pk[0].BitLen(), pk[1].BitLen()) },
		sign:        func(m *big.Int, sk []*big.Int) []*big.Int { return []*big.Int{rsadecrypt(sk, m)} },
		verify: func(m *big.Int, sig, pk []*big.Int) bool {
			return new(big.Int).Exp(sig[0], pk[1], pk[0]).Cmp(m) == 0
		},
	},
	{
		name:        "dsa",
		pkattr:      []string{"p", "q", "alpha", "key"},
		skattr:      []string{"p", "q", "alpha", "key", "!secret"},
		sigattr:     []string{"r", "s"},
		gensk:       func(int) []*big.Int { return dsagen(nil) },
		genskfrompk: dsagen,
		sign:        dsasign,
		verify:      dsaverify,
	},
}

func findsigalg(name string) *sigalg {
	for _, a := range sigalgs {
		if a.name == name {
			return a
		}
	}
	return nil
}

// genprime returns a random prime of n bits.
func genprime(n int) *big.Int {
	if n < 2 {
		n = 2
	}
	p, err := rand.Prime(rand.Reader, n)
	if err != nil {
		panic(err)
	}
	return p
}

// gensafeprime finds a prime p = 2q+1 of n bits and a generator of Z*p,
// as libsec's gensafeprime.
func gensafeprime(n int) (p, alpha *big.Int) {
	var q *big.Int
	p = new(big.Int)
	for {
		q = genprime(n - 1)
		p.Lsh(q, 1).Add(p, big1)
		if p.ProbablyPrime(20) {
			break
		}
	}
	b := new(big.Int)
	for {
		alpha = new(big.Int).Mod(mprand(n), p)
		b.Mul(alpha, alpha).Mod(b, p)
		if b.Cmp(big1) == 0 {
			continue
		}
		b.Exp(alpha, q, p)
		if b.Cmp(big1) != 0 {
			return p, alpha
		}
	}
}

func egkey(p, alpha *big.Int) []*big.Int {
	secret := mprand(p.BitLen() - 1)
	key := new(big.Int).Exp(alpha, secret, p)
	return []*big.Int{new(big.Int).Set(p), new(big.Int).Set(alpha), key, secret}
}

func egsign(m *big.Int, sk []*big.Int) []*big.Int {
	p, alpha, secret := sk[0], sk[1], sk[3]
	pm1 := new(big.Int).Sub(p, big1)
	var k, kinv *big.Int
	for {
		k = mprand(p.BitLen())
		if k.Cmp(big1) < 0 || k.Cmp(pm1) >= 0 {
			continue
		}
		kinv = new(big.Int).ModInverse(k, pm1)
		if kinv != nil {
			break
		}
	}
	r := new(big.Int).Exp(alpha, k, p)
	s := new(big.Int).Mul(secret, r)
	s.Mod(s, pm1)
	s.Sub(m, s)
	s.Mul(kinv, s)
	s.Mod(s, pm1)
	return []*big.Int{r, s}
}

func egverify(m *big.Int, sig, pk []*big.Int) bool {
	p, alpha, key := pk[0], pk[1], pk[2]
	r, s := sig[0], sig[1]
	if r.Cmp(big1) < 0 || r.Cmp(p) >= 0 {
		return false
	}
	v1 := new(big.Int).Exp(key, r, p)
	rs := new(big.Int).Exp(r, s, p)
	v1.Mul(v1, rs).Mod(v1, p)
	v2 := new(big.Int).Exp(alpha, m, p)
	return v1.Cmp(v2) == 0
}

// rsagen makes an RSA key of nlen bits as libsec's rsagen, but starting
// the public exponent at a fixed value of elen bits.
func rsagen(nlen, elen int) []*big.Int {
	if nlen < 8 {
		nlen = 8
	}
	for {
		p := genprime(nlen / 2)
		// rand.Prime sets the top two bits, so the product has exactly
		// the bits of its factors.
		q := genprime(nlen - p.BitLen())
		if p.Cmp(q) == 0 {
			continue
		}
		n := new(big.Int).Mul(p, q)
		if n.BitLen() != nlen {
			continue
		}
		pm1 := new(big.Int).Sub(p, big1)
		qm1 := new(big.Int).Sub(q, big1)
		phi := new(big.Int).Mul(pm1, qm1)
		e := big.NewInt(65537)
		if elen < 17 {
			e = big.NewInt(3)
		}
		var d *big.Int
		for {
			d = new(big.Int).ModInverse(e, phi)
			if d != nil {
				break
			}
			e.Add(e, big2)
		}
		c2 := new(big.Int).ModInverse(p, q)
		kp := new(big.Int).Mod(d, pm1)
		kq := new(big.Int).Mod(d, qm1)
		return []*big.Int{n, e, d, p, q, kp, kq, c2}
	}
}

// rsadecrypt is Garner's algorithm, as libsec's rsadecrypt.
func rsadecrypt(sk []*big.Int, in *big.Int) *big.Int {
	p, q, kp, kq, c2 := sk[3], sk[4], sk[5], sk[6], sk[7]
	v1 := new(big.Int).Mod(in, p)
	v2 := new(big.Int).Mod(in, q)
	v1.Exp(v1, kp, p)
	v2.Exp(v2, kq, q)
	v2.Sub(v2, v1)
	v2.Mul(v2, c2)
	v2.Mod(v2, q)
	out := new(big.Int).Mul(v2, p)
	return out.Add(out, v1)
}

// dsagen makes a DSA key, with new 1024-bit parameters or those of opk,
// as libsec's dsagen.
func dsagen(opk []*big.Int) []*big.Int {
	var p, q *big.Int
	if opk != nil {
		p, q = new(big.Int).Set(opk[0]), new(big.Int).Set(opk[1])
	} else {
		var params dsa.Parameters
		if err := dsa.GenerateParameters(&params, rand.Reader, dsa.L1024N160); err != nil {
			panic(err)
		}
		p, q = params.P, params.Q
	}
	bits := p.BitLen()
	exp := new(big.Int).Sub(p, big1)
	exp.Quo(exp, q)
	alpha := new(big.Int)
	for {
		g := new(big.Int).Mod(mprand(bits), p)
		alpha.Exp(g, exp, p)
		if alpha.Cmp(big1) != 0 {
			break
		}
	}
	secret := new(big.Int).Mod(mprand(bits), p)
	key := new(big.Int).Exp(alpha, secret, p)
	return []*big.Int{p, q, alpha, key, secret}
}

func dsasign(m *big.Int, sk []*big.Int) []*big.Int {
	p, q, alpha, secret := sk[0], sk[1], sk[2], sk[4]
	var k, kinv *big.Int
	for {
		k = mprand(q.BitLen())
		if k.Cmp(big1) < 0 || k.Cmp(q) >= 0 {
			continue
		}
		if kinv = new(big.Int).ModInverse(k, q); kinv != nil {
			break
		}
	}
	r := new(big.Int).Exp(alpha, k, p)
	r.Mod(r, q)
	s := new(big.Int).Mul(r, secret)
	s.Add(s, m)
	s.Mul(s, kinv)
	s.Mod(s, q)
	return []*big.Int{r, s}
}

func dsaverify(m *big.Int, sig, pk []*big.Int) bool {
	p, q, alpha, key := pk[0], pk[1], pk[2], pk[3]
	r, s := sig[0], sig[1]
	if r.Cmp(big1) < 0 || r.Cmp(q) >= 0 || s.Cmp(big1) < 0 || s.Cmp(q) >= 0 {
		return false
	}
	sinv := new(big.Int).ModInverse(s, q)
	if sinv == nil {
		return false
	}
	u1 := new(big.Int).Mul(sinv, m)
	u1.Mod(u1, q)
	u2 := new(big.Int).Mul(r, sinv)
	u2.Mod(u2, q)
	v := new(big.Int).Exp(alpha, u1, p)
	t := new(big.Int).Exp(key, u2, p)
	v.Mul(v, t).Mod(v, p).Mod(v, q)
	return v.Cmp(r) == 0
}

// DHparams makes Diffie-Hellman parameters (alpha, p) of nbits. As in
// Inferno, 1024 bits uses the DSA primes, with alpha the 160-bit prime
// q; other sizes use a safe prime and a generator.
func DHparams(nbits int) (alpha, p *IPint) {
	if nbits == 1024 {
		var params dsa.Parameters
		if err := dsa.GenerateParameters(&params, rand.Reader, dsa.L1024N160); err != nil {
			panic(err)
		}
		return ip(params.Q), ip(params.P)
	}
	pp, a := gensafeprime(nbits)
	return ip(a), ip(pp)
}
