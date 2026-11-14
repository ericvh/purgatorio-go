package pgauth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The files in testdata/rust were written by the Rust sibling's purg-auth
// (testdata/rust/gen.rs.txt is the program, built against
// crates/purg-auth). Ed25519 and the derivations are deterministic, so
// Go must parse each file and also make the same bytes from the same
// seeds and randomness.

func rust(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "rust", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func seed(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func key(t testing.TB, b byte) *PrivateKey {
	k, err := NewPrivateKey(seed(b))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRustKeysAndCerts(t *testing.T) {
	user, signer, counter := key(t, 1), key(t, 2), key(t, 3)
	if !bytes.Equal(user.Public(), rust(t, "user.pub")) {
		t.Fatal("public key differs from Rust's")
	}
	k, err := NewPrivateKey(rust(t, "user.seed"))
	if err != nil || !k.Public().Equal(user.Public()) {
		t.Fatal("seed file")
	}
	c, err := ParseCert(rust(t, "cert1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "alice" || c.Expires != 1800000000 || !c.SignedBy(signer.Public().ID()) {
		t.Fatalf("cert1: %+v", c)
	}
	mine, _ := NewCert("alice", user.Public(), 1800000000)
	if err := mine.Sign(signer); err != nil {
		t.Fatal(err)
	}
	if mine.Encode() != string(rust(t, "cert1.txt")) {
		t.Fatalf("Go cert differs:\n%s\n%s", mine.Encode(), rust(t, "cert1.txt"))
	}
	if err := mine.Sign(counter); err != nil {
		t.Fatal(err)
	}
	if mine.Encode() != string(rust(t, "cert2.txt")) {
		t.Fatal("countersigned cert differs")
	}
	info, err := ParseAuthInfo(rust(t, "authinfo"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Encode() != string(rust(t, "authinfo")) || info.Signer != signer.Public().ID() {
		t.Fatal("authinfo round trip")
	}
	list, ok := ParseList(rust(t, "signers"))
	if !ok || len(list) != 1 || list[0] != signer.Public().ID() {
		t.Fatalf("signers list: %v %v", list, ok)
	}
	if "# signers\n"+ListLine(signer.Public().ID(), "mysigner") != string(rust(t, "signers")) {
		t.Fatal("list line differs")
	}
}

func TestRustProofs(t *testing.T) {
	user, signer := key(t, 1), key(t, 2)
	cert, _ := ParseCert(rust(t, "cert1.txt"))
	chal := bytes.Repeat([]byte{7}, 32)
	p2, err := MakeProof(user, cert, chal, "alice", "")
	if err != nil || !bytes.Equal(p2, rust(t, "proof2.bin")) {
		t.Fatalf("v2 proof differs: %v", err)
	}
	p2k, _ := MakeProof(user, nil, chal, "alice", "x")
	if !bytes.Equal(p2k, rust(t, "proof2k.bin")) {
		t.Fatal("v2 key proof differs")
	}
	pol := &Policy{Signers: []KeyID{signer.Public().ID()}, Now: 1700000000, Accept: AcceptClassical}
	if err := Check(pol, chal, "alice", "", rust(t, "proof2.bin")); err != nil {
		t.Fatalf("v2 check: %v", err)
	}

	srv, err := NewServerFrom(chal, seed(8), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srv.Offer(), rust(t, "offer3.bin")) {
		t.Fatal("offer differs")
	}
	cl, err := NewClientFrom(srv.Offer(), false, seed(9))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cl.Signed(), rust(t, "signed3.bin")) {
		t.Fatal("signed bytes differ")
	}
	p3, _ := MakeProof(user, cert, cl.Signed(), "alice", "aname")
	if !bytes.Equal(p3, rust(t, "proof3.bin")) {
		t.Fatal("v3 proof differs")
	}
	if err := Check(pol, srv.Offer(), "alice", "aname", p3); err != nil {
		t.Fatalf("v3 check: %v", err)
	}
	ks, err := srv.Keys("alice", "aname", p3)
	if err != nil {
		t.Fatal(err)
	}
	kc, _ := cl.Keys("alice", "aname", p3)
	want := rust(t, "keys3.bin")
	got := append(append(append(ks.C2S[:], ks.S2C[:]...), ks.Transcript[:]...), ks.Bind[:]...)
	if !bytes.Equal(got, want) || *kc != *ks {
		t.Fatal("session keys differ from Rust's")
	}

	// Rust's records open here, and Go seals the same bytes.
	_, op := ks.Server()
	recs := rust(t, "records.bin")
	var msgs [][]byte
	for len(recs) > 0 {
		n, err := RecordSize(recs[:4], 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		p, err := op.Open(bytes.Clone(recs[:n]))
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, p)
		recs = recs[n:]
	}
	if len(msgs) != 3 || string(msgs[0]) != "hello" || len(msgs[1]) != 0 || len(msgs[2]) != 300 {
		t.Fatalf("records: %q", msgs)
	}
	se, _ := kc.Client()
	var mine []byte
	for _, m := range msgs {
		mine, _ = se.Seal(mine, m)
	}
	if !bytes.Equal(mine, rust(t, "records.bin")) {
		t.Fatal("sealed records differ")
	}

	sp, _ := MakeServerProof(signer, nil, ks)
	if !bytes.Equal(sp, rust(t, "srvproof.bin")) {
		t.Fatal("server proof differs")
	}
	sp2, _ := MakeServerProof(user, cert, ks)
	if !bytes.Equal(sp2, rust(t, "srvproof2.bin")) {
		t.Fatal("certified server proof differs")
	}
	spol := &Policy{Keys: []KeyID{signer.Public().ID()}, Signers: []KeyID{signer.Public().ID()}}
	if err := CheckServerHost(spol, kc, rust(t, "srvproof.bin"), "fs"); err != nil {
		t.Fatal(err)
	}
	if err := CheckServerHost(spol, kc, rust(t, "srvproof2.bin"), "alice"); err != nil {
		t.Fatal(err)
	}
	if err := CheckServerHost(spol, kc, rust(t, "srvproof2.bin"), "fs"); err != ENameMismatch {
		t.Fatalf("other host: %v", err)
	}
}
