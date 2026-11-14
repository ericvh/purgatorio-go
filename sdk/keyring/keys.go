// Package keyring is Inferno's Keyring module for guests: keys and
// certificates for the elgamal, rsa and dsa signature algorithms, their
// text forms, signing, digests and ciphers with Inferno's state
// semantics, the message framing the authentication protocols use, and
// the station-to-station protocol of keyring-auth(2).
//
// Text forms, signatures and protocol messages are those of Inferno's
// libinterp/keyring.c and libkeyring, so keys, certificates and
// authentication work with a real Inferno system.
package keyring

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// SigAlg is a signature algorithm.
type SigAlg struct {
	Name string
	vec  *sigalg
}

// PK is a public key.
type PK struct {
	SA    *SigAlg
	Owner string
	key   []*big.Int
}

// SK is a secret key.
type SK struct {
	SA    *SigAlg
	Owner string
	key   []*big.Int
}

// Certificate is a signature: by Signer, of a hash made with HA, valid
// until Exp (seconds since the epoch; 0 never expires). Exp is a Limbo
// int: 32 bits.
type Certificate struct {
	SA     *SigAlg
	HA     string
	Signer string
	Exp    int
	sig    []*big.Int
}

// Authinfo is what a station needs to authenticate: its keys, the
// signer's certificate of its public key, the signer's public key, and
// the Diffie-Hellman parameters.
type Authinfo struct {
	MySK  *SK
	MyPK  *PK
	Cert  *Certificate
	SPK   *PK
	Alpha *IPint
	P     *IPint
}

func newalg(v *sigalg) *SigAlg { return &SigAlg{Name: v.name, vec: v} }

// strtoalg takes the algorithm name line.
func strtoalg(s string) (*SigAlg, string) {
	name, rest, _ := strings.Cut(s, "\n")
	if len(name) >= 20 {
		return nil, rest
	}
	v := findsigalg(name)
	if v == nil {
		return nil, rest
	}
	return newalg(v), rest
}

func strtostring(s string) (string, string) {
	line, rest, _ := strings.Cut(s, "\n")
	return line, rest
}

func bigs2str(v []*big.Int) string {
	var s strings.Builder
	for _, b := range v {
		s.WriteString(bigtobase64(b))
		s.WriteByte('\n')
	}
	return s.String()
}

func str2bigs(s string, n int) ([]*big.Int, string) {
	v := make([]*big.Int, n)
	ok := true
	for i := range v {
		v[i], s = base64tobig(s)
		if v[i] == nil {
			ok = false
		}
	}
	if !ok {
		return nil, s
	}
	return v, s
}

func copybigs(v []*big.Int) []*big.Int {
	c := make([]*big.Int, len(v))
	for i, b := range v {
		c[i] = new(big.Int).Set(b)
	}
	return c
}

// SKtoStr is Keyring's sktostr: algorithm, owner, then the numbers in
// base64, one per line.
func SKtoStr(sk *SK) string {
	return sk.SA.Name + "\n" + sk.Owner + "\n" + bigs2str(sk.key)
}

// PKtoStr is Keyring's pktostr.
func PKtoStr(pk *PK) string {
	return pk.SA.Name + "\n" + pk.Owner + "\n" + bigs2str(pk.key)
}

// CerttoStr is Keyring's certtostr.
func CerttoStr(c *Certificate) string {
	return fmt.Sprintf("%s\n%s\n%s\n%d\n", c.SA.Name, c.HA, c.Signer, int32(c.Exp)) + bigs2str(c.sig)
}

// StrtoSK parses sktostr's form; nil if it is malformed.
func StrtoSK(s string) *SK {
	sa, s := strtoalg(s)
	if sa == nil {
		return nil
	}
	owner, s := strtostring(s)
	key, _ := str2bigs(s, len(sa.vec.skattr))
	if key == nil {
		return nil
	}
	return &SK{SA: sa, Owner: owner, key: key}
}

// StrtoPK parses pktostr's form.
func StrtoPK(s string) *PK {
	sa, s := strtoalg(s)
	if sa == nil {
		return nil
	}
	owner, s := strtostring(s)
	key, _ := str2bigs(s, len(sa.vec.pkattr))
	if key == nil {
		return nil
	}
	return &PK{SA: sa, Owner: owner, key: key}
}

// StrtoCert parses certtostr's form.
func StrtoCert(s string) *Certificate {
	sa, s := strtoalg(s)
	if sa == nil {
		return nil
	}
	ha, s := strtostring(s)
	signer, s := strtostring(s)
	// strtoul then skip one character.
	t := strings.TrimLeft(s, " \t")
	n := 0
	for n < len(t) && t[n] >= '0' && t[n] <= '9' {
		n++
	}
	exp, _ := strconv.ParseUint(t[:n], 10, 64)
	s = t[n:]
	if s != "" {
		s = s[1:]
	}
	sig, _ := str2bigs(s, len(sa.vec.sigattr))
	if sig == nil {
		return nil
	}
	return &Certificate{SA: sa, HA: ha, Signer: signer, Exp: int(int32(uint32(exp))), sig: sig}
}

// quote is Inferno's %q.
func quote(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == '\'' }) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func bigs2attr(b *strings.Builder, v []*big.Int, names []string) string {
	for i, x := range v {
		if i >= len(names) {
			break
		}
		// The hex is of the base64 form's bytes, leading zero included.
		fmt.Fprintf(b, " %s=%s", names[i], strings.ToUpper(hex.EncodeToString(dec64(bigtobase64(x)))))
	}
	return b.String()
}

// SKtoAttr is Keyring's sktoattr: attribute=value pairs.
func SKtoAttr(sk *SK) string {
	var b strings.Builder
	fmt.Fprintf(&b, "alg=%s", quote(sk.SA.Name))
	if sk.Owner != "" {
		fmt.Fprintf(&b, " owner=%s", quote(sk.Owner))
	}
	return bigs2attr(&b, sk.key, sk.SA.vec.skattr)
}

// PKtoAttr is Keyring's pktoattr.
func PKtoAttr(pk *PK) string {
	var b strings.Builder
	fmt.Fprintf(&b, "alg=%s", quote(pk.SA.Name))
	if pk.Owner != "" {
		fmt.Fprintf(&b, " owner=%s", quote(pk.Owner))
	}
	return bigs2attr(&b, pk.key, pk.SA.vec.pkattr)
}

// CerttoAttr is Keyring's certtoattr.
func CerttoAttr(c *Certificate) string {
	ha := c.HA
	if ha == "sha" {
		ha = "sha1"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "sigalg=%s-%s signer=%s expires=%d", quote(c.SA.Name), quote(ha), quote(c.Signer), uint32(c.Exp))
	return bigs2attr(&b, c.sig, c.SA.vec.sigattr)
}

// GenSK makes a secret key with algorithm alg (elgamal, rsa or dsa) and
// a modulus of length bits; nil for an unknown algorithm. dsa keys are
// always 1024 bits, as in Inferno.
func GenSK(alg, owner string, length int) *SK {
	v := findsigalg(alg)
	if v == nil {
		return nil
	}
	return &SK{SA: newalg(v), Owner: owner, key: v.gensk(length)}
}

// GenSKfromPK makes a secret key with the same algorithm and parameters
// as pk.
func GenSKfromPK(pk *PK, owner string) *SK {
	return &SK{SA: pk.SA, Owner: owner, key: pk.SA.vec.genskfrompk(pk.key)}
}

// SKtoPK gives the public half of sk.
func SKtoPK(sk *SK) *PK {
	return &PK{SA: sk.SA, Owner: sk.Owner, key: copybigs(sk.key[:len(sk.SA.vec.pkattr)])}
}

// hashalg returns the digest function for a certificate hash name.
func hashfor(ha string) func([]byte, []byte, *DigestState) *DigestState {
	switch ha {
	case "sha", "sha1":
		return SHA1
	case "md5":
		return MD5
	case "md4":
		return MD4
	}
	return nil
}

// signhash finishes state with the signer and expiry, as Inferno adds
// them to every signed hash, and returns the digest as a number.
func signhash(ha string, state *DigestState, signer string, exp int) *big.Int {
	f := hashfor(ha)
	if f == nil || state == nil {
		return nil
	}
	digest := make([]byte, 20)
	f([]byte(fmt.Sprintf("%s %d", signer, int32(exp))), digest, state)
	if ha == "md5" || ha == "md4" {
		digest = digest[:16]
	}
	return new(big.Int).SetBytes(digest)
}

// Sign is Keyring's sign: it adds the signer's name and exp to state,
// a digest of the data made with ha, and signs the result.
func Sign(sk *SK, exp int, state *DigestState, ha string) *Certificate {
	m := signhash(ha, state.Copy(), sk.Owner, exp)
	if m == nil {
		return nil
	}
	return &Certificate{SA: sk.SA, HA: ha, Signer: sk.Owner, Exp: exp, sig: sk.SA.vec.sign(m, sk.key)}
}

// Verify is Keyring's verify.
func Verify(pk *PK, c *Certificate, state *DigestState) bool {
	if pk == nil || c == nil || c.SA.vec != pk.SA.vec {
		return false
	}
	m := signhash(c.HA, state.Copy(), c.Signer, c.Exp)
	if m == nil {
		return false
	}
	return pk.SA.vec.verify(m, c.sig, pk.key)
}

// signdata and verifydata hash data then sign or verify, as keyring.c's
// internal sign and verify.
func signdata(sk *SK, ha string, exp int, data []byte) *Certificate {
	f := hashfor(ha)
	if f == nil {
		return nil
	}
	return Sign(sk, exp, f(data, nil, nil), ha)
}

func verifydata(pk *PK, c *Certificate, data []byte) bool {
	f := hashfor(c.HA)
	if f == nil {
		return false
	}
	return Verify(pk, c, f(data, nil, nil))
}

// SignM signs the number m directly.
func SignM(sk *SK, m *IPint, ha string) *Certificate {
	if m == nil {
		return nil
	}
	return &Certificate{SA: sk.SA, HA: ha, Signer: sk.Owner, sig: sk.SA.vec.sign(m.v, sk.key)}
}

// VerifyM verifies a signature of the number m.
func VerifyM(pk *PK, c *Certificate, m *IPint) bool {
	if pk == nil || c == nil || m == nil || c.SA.vec != pk.SA.vec {
		return false
	}
	return pk.SA.vec.verify(m.v, c.sig, pk.key)
}
