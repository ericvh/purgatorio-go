package pgauth

import (
	"bytes"
	"crypto/mldsa"
	"crypto/mlkem"
	"crypto/mlkem/mlkemtest"
	"crypto/sha256"
	"strings"
	"testing"
)

func hybridKey(t testing.TB, b byte) *PrivateKey {
	k, err := NewPrivateKey(bytes.Repeat([]byte{b}, HybridSeedSize))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Known answers: the hybrid key and signature for a fixed seed. ML-DSA
// and ML-KEM are deterministic from their seeds in Go's standard library
// (SignDeterministic, NewDecapsulationKey768, mlkemtest.Encapsulate768),
// so these pin the encodings; a change in either library or in the
// formats shows here.
func TestHybridKnownAnswers(t *testing.T) {
	k := hybridKey(t, 5)
	if len(k.Public()) != HybridPublicSize || !k.Hybrid() {
		t.Fatal("hybrid public key size")
	}
	sig := k.Sign([]byte("message"))
	if len(sig) != HybridSignatureSize {
		t.Fatal("hybrid signature size")
	}
	if !bytes.Equal(sig, k.Sign([]byte("message"))) {
		t.Fatal("hybrid signing is not deterministic")
	}
	sum := func(b []byte) string { s := sha256.Sum256(b); return Hex(s[:8]) }
	if got := sum(k.Public()); got != kaPublic {
		t.Errorf("public key digest %s", got)
	}
	if got := sum(sig); got != kaSig {
		t.Errorf("signature digest %s", got)
	}
	if got := k.Public().ID().String(); got != kaID {
		t.Errorf("fingerprint %s", got)
	}
	dk, err := mlkem.NewDecapsulationKey768(bytes.Repeat([]byte{6}, mlkem.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	ss, ct, err := mlkemtest.Encapsulate768(dk.EncapsulationKey(), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	back, err := dk.Decapsulate(ct)
	if err != nil || !bytes.Equal(ss, back) {
		t.Fatal("ML-KEM round trip")
	}
	if got := sum(dk.EncapsulationKey().Bytes()) + " " + sum(ct) + " " + sum(ss); got != kaKEM {
		t.Errorf("ML-KEM digests %s", got)
	}
}

const (
	kaPublic = "7b4ad7f965fd8980"
	kaSig    = "11a8c05b01b2bc99"
	kaID     = "pq:8504fa3853a14293808ba49a08b7e8a08c2ca69e35d391240b2f10721d73bc0f"
	kaKEM    = "5fe09f109d4ccd04 52c493d43969c598 f6cc414deeef69e6"
)

func TestHybridSignatures(t *testing.T) {
	k := hybridKey(t, 1)
	msg := []byte("hello")
	sig := k.Sign(msg)
	if !k.Public().Verify(msg, sig) {
		t.Fatal("does not verify")
	}
	// Either half failing fails the whole.
	for _, i := range []int{0, 63, 64, HybridSignatureSize - 1} {
		bad := bytes.Clone(sig)
		bad[i] ^= 1
		if k.Public().Verify(msg, bad) {
			t.Fatalf("changed byte %d verifies", i)
		}
	}
	// The Ed25519 half alone is not a classical signature over msg: it
	// signs hybridContext || msg.
	if PublicKey(k.Public().Classical()).Verify(msg, sig[:SignatureSize]) {
		t.Fatal("Ed25519 half verifies alone")
	}
	// A signature of another length never verifies.
	if k.Public().Verify(msg, sig[:SignatureSize]) {
		t.Fatal("short signature verifies")
	}
	// The ML-DSA half is a plain FIPS 204 signature with an empty context.
	pk, _ := mldsa.NewPublicKey(mldsa.MLDSA65(), k.Public()[PublicSize:])
	if mldsa.Verify(pk, append([]byte(hybridContext), msg...), sig[SignatureSize:], nil) != nil {
		t.Fatal("ML-DSA half")
	}
	if _, err := NewPrivateKey(make([]byte, 33)); err == nil {
		t.Fatal("33-byte seed")
	}
	g, err := GenerateKey(true)
	if err != nil || !g.Hybrid() {
		t.Fatal(err)
	}
	k2, _ := NewPrivateKey(g.Seed())
	if !k2.Public().Equal(g.Public()) {
		t.Fatal("seed round trip")
	}
}

func TestCert2(t *testing.T) {
	user, signer, other := hybridKey(t, 1), hybridKey(t, 2), hybridKey(t, 3)
	c, err := NewCert("alice", user.Public(), 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Sign(key(t, 9)); err != EWrongAlg {
		t.Fatalf("classical signer on pgcert2: %v", err)
	}
	if err := c.Sign(signer); err != nil {
		t.Fatal(err)
	}
	line := c.Encode()
	if !strings.HasPrefix(line, "pgcert2 alice ") {
		t.Fatal(line[:20])
	}
	back, err := ParseCert([]byte(line + "\n"))
	if err != nil || back.Encode() != line {
		t.Fatalf("round trip: %v", err)
	}
	if !back.SignedBy(signer.Public().ID()) || back.SignedBy(other.Public().ID()) {
		t.Fatal("SignedBy")
	}
	if err := back.Sign(other); err != nil || !back.SignedBy(other.Public().ID()) {
		t.Fatal("countersign")
	}
	// Both halves of each signature must verify.
	sig := back.Sigs[0].Sig
	sig[10] ^= 1
	if back.SignedBy(signer.Public().ID()) {
		t.Fatal("broken Ed25519 half verifies")
	}
	sig[10] ^= 1
	sig[len(sig)-5] ^= 1
	if back.SignedBy(signer.Public().ID()) {
		t.Fatal("broken ML-DSA half verifies")
	}
	for _, bad := range []string{
		strings.Replace(line, "pgcert2", "pgcert1", 1),
		strings.Replace(line, " ", "  ", 1),
		line + " ",
		line[:len(line)-1] + "=",
	} {
		if _, err := ParseCert([]byte(bad)); err == nil {
			t.Errorf("parsed %.40q", bad)
		}
	}
	// Authinfo.
	info := &AuthInfo{Key: user, Signer: signer.Public().ID(), Cert: c}
	back2, err := ParseAuthInfo([]byte(info.Encode()))
	if err != nil || back2.Encode() != info.Encode() {
		t.Fatalf("authinfo: %v", err)
	}
	if !strings.Contains(info.Encode(), "\nsigner pq:") {
		t.Fatal("signer line")
	}
	t.Logf("pgcert2 with one signature: %d bytes", len(line))
}

func TestLists(t *testing.T) {
	a, h := key(t, 1).Public().ID(), hybridKey(t, 1).Public().ID()
	text := "# list\n\n" + ListLine(a, "alpha") + ListLine(h, "") + "  \n"
	l, ok := ParseList([]byte(text))
	if !ok || len(l) != 2 || l[0] != a || l[1] != h {
		t.Fatalf("%v %v", l, ok)
	}
	if _, ok := ParseList([]byte(text + "nothex\n")); ok {
		t.Fatal("bad line parsed")
	}
	if l := ParseListLenient([]byte(text + "nothex\n")); len(l) != 2 {
		t.Fatal("lenient")
	}
	if _, ok := ParseList([]byte(a.String() + "x\n")); ok {
		t.Fatal("glued comment")
	}
	named := ParseNamed([]byte(NamedLine("Host.example", a) + "* " + h.String() + " any\nnokey\n"))
	if len(named) != 2 {
		t.Fatal(named)
	}
	if got := KeysFor(named, "host.EXAMPLE", false); len(got) != 2 {
		t.Fatal(got)
	}
	if got := KeysFor(named, "other", false); len(got) != 1 || got[0] != h {
		t.Fatal(got)
	}
}

// A full version 4 exchange: the hybrid offer, the proof, the keys on
// both sides, records each way and the server's proof.
func TestHandshake4(t *testing.T) {
	user, signer, host := hybridKey(t, 1), hybridKey(t, 2), hybridKey(t, 3)
	cert, _ := NewCert("alice", user.Public(), 0)
	cert.Sign(signer)
	srv := NewServer(true)
	if len(srv.Offer()) != Offer4Size {
		t.Fatal("offer size")
	}
	cl, err := NewClient(srv.Offer(), true)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := MakeProof(user, cert, cl.Signed(), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if ProofLen(proof[:5]) != len(proof) {
		t.Fatal("ProofLen")
	}
	pol := &Policy{Signers: []KeyID{signer.Public().ID()}}
	if err := Check(pol, srv.Offer(), "alice", "", proof); err != nil {
		t.Fatalf("check: %v", err)
	}
	if err := Check(pol, srv.Offer(), "bob", "", proof); err != EFailed {
		t.Fatalf("other uname: %v", err)
	}
	ks, err := srv.Keys("alice", "", proof)
	if err != nil {
		t.Fatal(err)
	}
	kc, err := cl.Keys("alice", "", proof)
	if err != nil || *ks != *kc || ks.Version != 4 {
		t.Fatalf("keys differ: %v", err)
	}
	cs, co := kc.Client()
	ss, so := ks.Server()
	r, _ := cs.Seal(nil, []byte("Tattach"))
	if p, err := so.Open(r); err != nil || string(p) != "Tattach" {
		t.Fatal(err)
	}
	r, _ = ss.Seal(nil, []byte("Rattach"))
	if p, err := co.Open(r); err != nil || string(p) != "Rattach" {
		t.Fatal(err)
	}
	sp, err := MakeServerProof(host, nil, ks)
	if err != nil {
		t.Fatal(err)
	}
	spol := &Policy{Keys: []KeyID{host.Public().ID()}}
	if err := CheckServerHost(spol, kc, sp, "fs"); err != nil {
		t.Fatal(err)
	}
	// A server proof for another session does not verify.
	srv2 := NewServer(true)
	cl2, _ := NewClient(srv2.Offer(), true)
	p2, _ := MakeProof(user, cert, cl2.Signed(), "alice", "")
	k2, _ := cl2.Keys("alice", "", p2)
	if err := CheckServer(spol, k2, sp); err != EServerFailed {
		t.Fatalf("replayed server proof: %v", err)
	}
	// A changed ciphertext breaks the signature.
	bad := bytes.Clone(proof)
	bad[len(bad)-len(cert.Encode())-1] ^= 1
	if err := Check(pol, srv.Offer(), "alice", "", bad); err != EFailed {
		t.Fatalf("changed ciphertext: %v", err)
	}
	t.Logf("offer %d, signed %d, proof with pgcert2 %d, server proof %d bytes",
		len(srv.Offer()), len(cl.Signed()), len(proof), len(sp))
}

// Neither end falls back to a weaker handshake it did not choose.
func TestNoDowngrade(t *testing.T) {
	cuser, huser := key(t, 1), hybridKey(t, 1)
	pol := &Policy{Keys: []KeyID{cuser.Public().ID(), huser.Public().ID()}}

	// A hybrid client of a classical-only server refuses.
	csrv := NewServer(false)
	if _, err := NewClient(csrv.Offer(), true); err != ENoHybrid {
		t.Fatalf("hybrid client, classical offer: %v", err)
	}
	// A server offer cut to 64 bytes on the way is the same.
	hsrv := NewServer(true)
	if _, err := NewClient(hsrv.Offer()[:Offer3Size], true); err != ENoHybrid {
		t.Fatalf("stripped offer: %v", err)
	}
	// A hybrid-only server refuses every classical proof.
	cl, _ := NewClient(hsrv.Offer(), false)
	p3, _ := MakeProof(cuser, nil, cl.Signed(), "u", "")
	if err := Check(pol, hsrv.Offer(), "u", "", p3); err != EHybridRequired {
		t.Fatalf("v3 to hybrid server: %v", err)
	}
	p2, _ := MakeProof(cuser, nil, hsrv.Offer()[:ChallengeSize], "u", "")
	if err := Check(pol, hsrv.Offer(), "u", "", p2); err != EHybridRequired {
		t.Fatalf("v2 to hybrid server: %v", err)
	}
	p1 := MakeProofV1(cuser, hsrv.Offer()[:ChallengeSize])
	if err := Check(pol, hsrv.Offer(), "u", "", p1); err != EHybridRequired {
		t.Fatalf("v1 to hybrid server: %v", err)
	}
	// A server that takes both accepts each.
	both := *pol
	both.Accept = AcceptHybrid | AcceptClassical
	if err := Check(&both, hsrv.Offer(), "u", "", p3); err != nil {
		t.Fatalf("v3 to a server that takes both: %v", err)
	}
	// A classical-only server refuses a hybrid proof.
	hc, _ := NewClient(hsrv.Offer(), true)
	p4, _ := MakeProof(huser, nil, hc.Signed(), "u", "")
	classical := *pol
	classical.Accept = AcceptClassical
	if err := Check(&classical, hsrv.Offer(), "u", "", p4); err != EClassicalRequired {
		t.Fatalf("v4 to classical server: %v", err)
	}
	if err := Check(&both, hsrv.Offer(), "u", "", p4); err != nil {
		t.Fatalf("v4 to a server that takes both: %v", err)
	}
	// Keys and handshakes of the wrong family are refused outright.
	if _, err := MakeProof(cuser, nil, hc.Signed(), "u", ""); err != EWrongAlg {
		t.Fatal("classical key, hybrid handshake")
	}
	if _, err := MakeProof(huser, nil, cl.Signed(), "u", ""); err != EWrongAlg {
		t.Fatal("hybrid key, classical handshake")
	}
}

func TestCheckCertErrors(t *testing.T) {
	user, signer := hybridKey(t, 1), hybridKey(t, 2)
	c, _ := NewCert("alice", user.Public(), 1000)
	c.Sign(signer)
	pol := &Policy{Signers: []KeyID{signer.Public().ID()}, Now: 999}
	if err := CheckCert(pol, c, "alice", true); err != nil {
		t.Fatal(err)
	}
	if err := CheckCert(pol, c, "bob", true); err != ENameMismatch {
		t.Fatal(err)
	}
	pol.Now = 1000
	if err := CheckCert(pol, c, "alice", true); err != EExpired {
		t.Fatal(err)
	}
	pol.Now = 0
	pol.Revoked = []KeyID{signer.Public().ID()}
	if err := CheckCert(pol, c, "alice", true); err != ERevoked {
		t.Fatal(err)
	}
	// Revoking a hybrid key's Ed25519 half revokes the key.
	pol.Revoked = []KeyID{PublicKey(user.Public().Classical()).ID()}
	if err := CheckCert(pol, c, "alice", true); err != ERevoked {
		t.Fatal(err)
	}
	pol.Revoked, pol.Damaged = nil, true
	if err := CheckCert(pol, c, "alice", true); err != ERevoked {
		t.Fatal(err)
	}
	pol.Damaged, pol.Signers = false, nil
	if err := CheckCert(pol, c, "alice", true); err != EUntrusted {
		t.Fatal(err)
	}
}

func TestRecords(t *testing.T) {
	key := bytes.Repeat([]byte{4}, 32)
	s, o := NewSealer(key), NewOpener(key)
	r1, _ := s.Seal(nil, []byte("one"))
	r2, _ := s.Seal(nil, []byte("two"))
	if _, err := o.Open(bytes.Clone(r2)); err != EBadMAC {
		t.Fatal("reordered record opened")
	}
	if _, err := o.Open(bytes.Clone(r1)); err != EBadMAC {
		t.Fatal("an opener that failed opens again")
	}
	s, o = NewSealerAt(key, RecordLimit-1), NewOpenerAt(key, RecordLimit-1)
	r, err := s.Seal(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Seal(nil, nil); err != EExhausted {
		t.Fatal(err)
	}
	if _, err := o.Open(r); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordSize([]byte{19, 0, 0, 0}, 100); err != ETooShort {
		t.Fatal(err)
	}
	if _, err := RecordSize([]byte{121, 0, 0, 0}, 100); err != ETooLong {
		t.Fatal(err)
	}
}

func BenchmarkHandshake4(b *testing.B) {
	user, signer := hybridKey(b, 1), hybridKey(b, 2)
	cert, _ := NewCert("alice", user.Public(), 0)
	cert.Sign(signer)
	pol := &Policy{Signers: []KeyID{signer.Public().ID()}}
	for b.Loop() {
		srv := NewServer(true)
		cl, _ := NewClient(srv.Offer(), true)
		p, _ := MakeProof(user, cert, cl.Signed(), "alice", "")
		if err := Check(pol, srv.Offer(), "alice", "", p); err != nil {
			b.Fatal(err)
		}
		srv.Keys("alice", "", p)
		cl.Keys("alice", "", p)
	}
}

func BenchmarkHandshake3(b *testing.B) {
	user, signer := key(b, 1), key(b, 2)
	cert, _ := NewCert("alice", user.Public(), 0)
	cert.Sign(signer)
	pol := &Policy{Signers: []KeyID{signer.Public().ID()}, Accept: AcceptClassical}
	for b.Loop() {
		srv := NewServer(false)
		cl, _ := NewClient(srv.Offer(), false)
		p, _ := MakeProof(user, cert, cl.Signed(), "alice", "")
		if err := Check(pol, srv.Offer(), "alice", "", p); err != nil {
			b.Fatal(err)
		}
		srv.Keys("alice", "", p)
		cl.Keys("alice", "", p)
	}
}

func BenchmarkRecord8K(b *testing.B) {
	key := bytes.Repeat([]byte{4}, 32)
	s, o := NewSealer(key), NewOpener(key)
	msg := make([]byte, 8192)
	b.SetBytes(8192)
	var buf []byte
	for b.Loop() {
		buf, _ = s.Seal(buf[:0], msg)
		if _, err := o.Open(buf); err != nil {
			b.Fatal(err)
		}
	}
}
