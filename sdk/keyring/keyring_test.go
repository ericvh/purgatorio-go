package keyring

import (
	"bytes"
	"io"
	"math/big"
	"strings"
	"sync"
	"testing"
)

func TestBase64Forms(t *testing.T) {
	cases := []struct {
		v       int64
		z, b64  string
		hexform string
	}{
		{0, "AA==", "AA==", "0"},
		{0x80, "AIA=", "gA==", "80"},
		{0x7f, "fw==", "fw==", "7F"},
		{0x123456, "EjRW", "EjRW", "123456"},
	}
	for _, c := range cases {
		i := InttoIP(int(c.v))
		if got := i.IPtoB64z(); got != c.z {
			t.Errorf("iptob64z(%#x) = %q, want %q", c.v, got, c.z)
		}
		if got := i.IPtoB64(); got != c.b64 {
			t.Errorf("iptob64(%#x) = %q, want %q", c.v, got, c.b64)
		}
		if got := i.IPtoStr(16); got != c.hexform {
			t.Errorf("iptostr(%#x, 16) = %q", c.v, got)
		}
		if b := B64toIP(c.z); b == nil || b.v.Int64() != c.v {
			t.Errorf("b64toip(%q) = %v", c.z, b)
		}
	}
	if InttoIP(-5).IPtoB64() != "-BQ==" {
		t.Errorf("negative: %q", InttoIP(-5).IPtoB64())
	}
	if B64toIP("AB!") != nil {
		t.Errorf("b64toip accepted junk")
	}
}

var (
	keysOnce sync.Once
	keys     map[string]*SK
)

func testkeys(t *testing.T) map[string]*SK {
	keysOnce.Do(func() {
		keys = map[string]*SK{
			"elgamal": GenSK("elgamal", "eve", 256),
			"rsa":     GenSK("rsa", "eve", 512),
			"dsa":     GenSK("dsa", "eve", 1024),
		}
	})
	return keys
}

func TestTextRoundTrip(t *testing.T) {
	for alg, sk := range testkeys(t) {
		if sk.SA.Name != alg {
			t.Fatalf("%s: alg %q", alg, sk.SA.Name)
		}
		s := SKtoStr(sk)
		if !strings.HasPrefix(s, alg+"\neve\n") || !strings.HasSuffix(s, "\n") {
			t.Errorf("%s: sktostr %q", alg, s)
		}
		sk2 := StrtoSK(s)
		if sk2 == nil || SKtoStr(sk2) != s {
			t.Errorf("%s: strtosk round trip failed", alg)
		}
		pk := SKtoPK(sk)
		ps := PKtoStr(pk)
		if pk2 := StrtoPK(ps); pk2 == nil || PKtoStr(pk2) != ps {
			t.Errorf("%s: strtopk round trip failed", alg)
		}
		if n := strings.Count(ps, "\n"); n != 2+len(sk.SA.vec.pkattr) {
			t.Errorf("%s: pk has %d lines", alg, n)
		}
		st := SHA1([]byte(ps), nil, nil)
		c := Sign(sk, 12345, st, "sha1")
		cs := CerttoStr(c)
		if !strings.HasPrefix(cs, alg+"\nsha1\neve\n12345\n") {
			t.Errorf("%s: certtostr %q", alg, cs)
		}
		c2 := StrtoCert(cs)
		if c2 == nil || CerttoStr(c2) != cs {
			t.Fatalf("%s: strtocert round trip failed", alg)
		}
		if !Verify(pk, c2, SHA1([]byte(ps), nil, nil)) {
			t.Errorf("%s: verify failed", alg)
		}
		if Verify(pk, c2, SHA1([]byte(ps+"x"), nil, nil)) {
			t.Errorf("%s: verify accepted other data", alg)
		}
		c2.Exp++
		if Verify(pk, c2, SHA1([]byte(ps), nil, nil)) {
			t.Errorf("%s: verify accepted other expiry", alg)
		}
		md := Sign(sk, 0, MD5([]byte("x"), nil, nil), "md5")
		if !Verify(pk, md, MD5([]byte("x"), nil, nil)) {
			t.Errorf("%s: md5 verify failed", alg)
		}
		if !strings.HasPrefix(PKtoAttr(pk), "alg="+alg+" owner=eve ") {
			t.Errorf("%s: pktoattr %q", alg, PKtoAttr(pk))
		}
		if !strings.HasPrefix(CerttoAttr(c), "sigalg="+alg+"-sha1 signer=eve expires=12345 ") {
			t.Errorf("%s: certtoattr %q", alg, CerttoAttr(c))
		}
	}
}

func TestSignM(t *testing.T) {
	sk := testkeys(t)["elgamal"]
	m := InttoIP(424242)
	c := SignM(sk, m, "sha1")
	if !VerifyM(SKtoPK(sk), c, m) || VerifyM(SKtoPK(sk), c, InttoIP(1)) {
		t.Fatal("signm/verifym")
	}
}

func TestDigestState(t *testing.T) {
	d1 := make([]byte, SHA1dlen)
	st := SHA1([]byte("hello "), nil, nil)
	c := st.Copy()
	SHA1([]byte("world"), d1, st)
	d2 := make([]byte, SHA1dlen)
	SHA1([]byte("hello world"), d2, nil)
	if !bytes.Equal(d1, d2) {
		t.Fatal("sha1 state")
	}
	SHA1([]byte("there"), d2, c)
	d3 := make([]byte, SHA1dlen)
	SHA1([]byte("hello there"), d3, nil)
	if !bytes.Equal(d2, d3) {
		t.Fatal("copied state")
	}
}

func TestCBC(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := []byte("fedcba9876543210")
	msg := []byte("a message that is not a whole number of blocks")
	buf := append([]byte(nil), msg...)
	AEScbc(AESsetup(key, iv), buf, Encrypt)
	AEScbc(AESsetup(key, iv), buf, Decrypt)
	if !bytes.Equal(buf, msg) {
		t.Fatalf("aescbc round trip: %q", buf)
	}
}

func TestMsg(t *testing.T) {
	var b bytes.Buffer
	SendMsg(&b, []byte("hello"))
	SendErrMsg(&b, "bad thing")
	if b.String() != "0005\nhello!009\nbad thing" {
		t.Fatalf("framing %q", b.String())
	}
	m, err := GetMsg(&b)
	if err != nil || string(m) != "hello" {
		t.Fatalf("getmsg %q %v", m, err)
	}
	if _, err := GetMsg(&b); err == nil || err.Error() != "bad thing" {
		t.Fatalf("getmsg error %v", err)
	}
	if _, err := GetMsg(&b); err == nil || err.Error() != "hungup" {
		t.Fatalf("getmsg at end %v", err)
	}
}

// duplex is one end of a buffered pipe pair.
type duplex struct {
	r *bufpipe
	w *bufpipe
}

func (d duplex) Read(p []byte) (int, error)  { return d.r.Read(p) }
func (d duplex) Write(p []byte) (int, error) { return d.w.Write(p) }

type bufpipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool
}

func newbufpipe() *bufpipe {
	p := &bufpipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *bufpipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.buf.Len() == 0 && !p.closed {
		p.cond.Wait()
	}
	if p.buf.Len() == 0 {
		return 0, io.EOF
	}
	return p.buf.Read(b)
}

func (p *bufpipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf.Write(b)
	p.cond.Broadcast()
	return len(b), nil
}

func (p *bufpipe) Close() {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

func pipepair() (duplex, duplex, func()) {
	a, b := newbufpipe(), newbufpipe()
	return duplex{a, b}, duplex{b, a}, func() { a.Close(); b.Close() }
}

var (
	dhOnce       sync.Once
	dhalpha, dhp *IPint
)

// certify makes an Authinfo for owner signed by signer.
func certify(t *testing.T, signer *SK, owner string, exp int) *Authinfo {
	dhOnce.Do(func() { dhalpha, dhp = DHparams(256) })
	sk := GenSKfromPK(SKtoPK(signer), owner)
	pk := SKtoPK(sk)
	pkbuf := []byte(PKtoStr(pk))
	cert := Sign(signer, exp, SHA1(pkbuf, nil, nil), "sha1")
	return &Authinfo{MySK: sk, MyPK: pk, Cert: cert, SPK: SKtoPK(signer), Alpha: dhalpha, P: dhp}
}

type result struct {
	id     string
	secret []byte
	err    error
}

func authpair(a, b *Authinfo) (result, result) {
	x, y, done := pipepair()
	defer done()
	c := make(chan result)
	go func() {
		id, s, err := Auth(y, b, false)
		c <- result{id, s, err}
	}()
	id, s, err := Auth(x, a, false)
	return result{id, s, err}, <-c
}

func TestAuth(t *testing.T) {
	for _, alg := range []string{"elgamal", "rsa"} {
		signer := testkeys(t)[alg]
		alice := certify(t, signer, "alice", 0)
		bob := certify(t, signer, "bob", 0)
		ra, rb := authpair(alice, bob)
		if ra.err != nil || rb.err != nil {
			t.Fatalf("%s: auth: %v / %v", alg, ra.err, rb.err)
		}
		if ra.id != "bob" || rb.id != "alice" {
			t.Errorf("%s: ids %q %q", alg, ra.id, rb.id)
		}
		if !bytes.Equal(ra.secret, rb.secret) || len(ra.secret) == 0 {
			t.Errorf("%s: secrets differ", alg)
		}

		// Round trip through the file form.
		a2 := ReadAuthinfoFrom(bytes.NewReader(MarshalAuthinfo(alice)))
		if a2 == nil || !bytes.Equal(MarshalAuthinfo(a2), MarshalAuthinfo(alice)) {
			t.Fatalf("%s: authinfo round trip", alg)
		}
	}
}

func TestAuthFailures(t *testing.T) {
	signer := testkeys(t)["elgamal"]
	other := GenSK("elgamal", "mallory", 256)
	alice := certify(t, signer, "alice", 0)
	mallory := certify(t, other, "mallory", 0)
	mallory.Alpha, mallory.P = alice.Alpha, alice.P
	ra, rb := authpair(alice, mallory)
	if ra.err == nil || rb.err == nil {
		t.Fatalf("auth with a foreign signer succeeded")
	}
	if ra.err.Error() != "pk doesn't match certificate" && ra.err.Error() != "remote: pk doesn't match certificate" {
		t.Errorf("alice: %v", ra.err)
	}
	if !strings.Contains(rb.err.Error(), "pk doesn't match certificate") {
		t.Errorf("mallory: %v", rb.err)
	}

	old := certify(t, signer, "old", 1)
	ra, rb = authpair(alice, old)
	if ra.err == nil || ra.err.Error() != "certificate expired" {
		t.Errorf("expired: %v", ra.err)
	}
	if rb.err == nil || rb.err.Error() != "remote: certificate expired" {
		t.Errorf("expired, other end: %v", rb.err)
	}

	ra, rb = authpair(nil, alice)
	if ra.err == nil || ra.err.Error() != "no authentication information" {
		t.Errorf("nil info: %v", ra.err)
	}
	if rb.err == nil || rb.err.Error() != "remote: no authentication information" {
		t.Errorf("nil info, other end: %v", rb.err)
	}
	_ = big.NewInt
}
