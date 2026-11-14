package modsig

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/ericvh/purgatorio-go/pgauth"
)

// testdata/rust (in the parent package) was written by the Rust
// sibling's purg-auth: a small module, the same signed with a bare key
// and then countersigned through a certificate chain, and a signed
// image file.
func rust(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "rust", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ckey(t testing.TB, b byte) *pgauth.PrivateKey {
	k, err := pgauth.NewPrivateKey(bytes.Repeat([]byte{b}, pgauth.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func hkey(t testing.TB, b byte) *pgauth.PrivateKey {
	k, err := pgauth.NewPrivateKey(bytes.Repeat([]byte{b}, pgauth.HybridSeedSize))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRustModules(t *testing.T) {
	user, signer := ckey(t, 1), ckey(t, 2)
	wasm := rust(t, "module.wasm")
	d, err := Digest(wasm)
	if err != nil || !bytes.Equal(d[:], rust(t, "module.digest")) {
		t.Fatalf("digest: %v", err)
	}
	signed, err := Sign(wasm, signer, nil)
	if err != nil || !bytes.Equal(signed, rust(t, "module.key.wasm")) {
		t.Fatalf("signed module differs: %v", err)
	}
	cert, _ := pgauth.ParseCert(rust(t, "cert1.txt"))
	chain, err := Sign(signed, user, []*pgauth.Cert{cert})
	if err != nil || !bytes.Equal(chain, rust(t, "module.chain.wasm")) {
		t.Fatalf("countersigned module differs: %v", err)
	}
	info, _ := Inspect(rust(t, "module.chain.wasm"))
	if info.Digest != d || string(info.ABI) != "0.9" {
		t.Fatal("inspect")
	}
	tr := &Trust{Anchors: []pgauth.KeyID{signer.Public().ID()}, Now: 1700000000, Classical: true}
	keys, err := Check(info, tr)
	if err != nil || len(keys) != 2 {
		t.Fatalf("check: %v %d", err, len(keys))
	}
	// A hybrid-only policy refuses classical signatures.
	tr.Classical = false
	if _, err := Check(info, tr); err != EHybridRequired {
		t.Fatalf("hybrid only: %v", err)
	}
	stripped, _ := Strip(chain)
	if !bytes.Equal(stripped, wasm) {
		t.Fatal("strip")
	}
	// The image file.
	img, err := ParseImage(rust(t, "image.cwasm"))
	if err != nil {
		t.Fatal(err)
	}
	tr.Classical = true
	if _, err := img.Verify(0, d, tr); err != nil {
		t.Fatal(err)
	}
	mine, _ := BuildImage(0, d, []byte("not really machine code"), signer, nil)
	if !bytes.Equal(mine, rust(t, "image.cwasm")) {
		t.Fatal("image file differs")
	}
}

func module() []byte {
	w := []byte("\x00asm\x01\x00\x00\x00")
	name := "purgatorio.abi"
	body := append([]byte{byte(len(name))}, name...)
	body = append(body, "0.1"...)
	w = append(w, 0, byte(len(body)))
	w = append(w, body...)
	return append(w, 1, 4, 1, 0x60, 0, 0)
}

func TestHybridModules(t *testing.T) {
	wasm := module()
	anchor, dev := hkey(t, 1), hkey(t, 2)
	signed, err := Sign(wasm, anchor, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := Inspect(signed)
	if info.Sigs[0][0] != 2 {
		t.Fatal("a hybrid section must be version 2")
	}
	tr := &Trust{Anchors: []pgauth.KeyID{anchor.Public().ID()}}
	if _, err := Check(info, tr); err != nil {
		t.Fatal(err)
	}
	// A chain: dev's key certified by the anchor.
	c, _ := pgauth.NewCert("dev", dev.Public(), 0)
	c.Sign(anchor)
	byDev, err := Sign(wasm, dev, []*pgauth.Cert{c})
	if err != nil {
		t.Fatal(err)
	}
	info, _ = Inspect(byDev)
	if _, err := Check(info, tr); err != nil {
		t.Fatalf("chain: %v", err)
	}
	// Mixed: classical and hybrid entries in one section.
	mixed, err := Sign(byDev, ckey(t, 9), nil)
	if err != nil {
		t.Fatal(err)
	}
	info, _ = Inspect(mixed)
	if e, err := Entries(info); err != nil || len(e) != 2 {
		t.Fatal(err)
	}
	// Errors.
	if _, err := Check(&Info{}, tr); err != ENotSigned {
		t.Fatal(err)
	}
	info, _ = Inspect(signed)
	if _, err := Check(info, &Trust{Anchors: tr.Anchors, Revoked: []pgauth.KeyID{pgauth.DigestID(info.Digest)}}); err != EModuleRevoked {
		t.Fatal(err)
	}
	if _, err := Check(info, &Trust{Anchors: tr.Anchors, Revoked: []pgauth.KeyID{anchor.Public().ID()}}); err != ERevoked {
		t.Fatal(err)
	}
	if _, err := Check(info, &Trust{Anchors: tr.Anchors, Damaged: true}); err != ERevoked {
		t.Fatal(err)
	}
	if _, err := Check(info, &Trust{}); err != EUntrusted {
		t.Fatal(err)
	}
	changed := bytes.Clone(signed)
	changed[len(wasm)-1] = 1 // the type section, inside the digest
	info, _ = Inspect(changed)
	if _, err := Check(info, tr); err != EBadSignature {
		t.Fatal(err)
	}
	// Every truncation of a signed module either is not a module or
	// fails the check.
	for n := 8; n < len(signed); n++ {
		info, err := Inspect(signed[:n])
		if err != nil {
			continue
		}
		if _, err := Check(info, tr); err == nil {
			t.Fatalf("truncated at %d passes", n)
		}
	}
	t.Logf("hybrid module entry %d bytes, with one pgcert2 %d bytes", len(signed)-len(wasm), len(byDev)-len(wasm))
}

func TestHybridImages(t *testing.T) {
	k := hkey(t, 3)
	d, _ := Digest(module())
	file, err := BuildImage(1, d, []byte("code"), k, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := BuildImage(1, d, []byte("code"), k, nil)
	if !bytes.Equal(file, again) {
		t.Fatal("not deterministic")
	}
	img, err := ParseImage(file)
	if err != nil {
		t.Fatal(err)
	}
	tr := &Trust{Anchors: []pgauth.KeyID{k.Public().ID()}}
	if _, err := img.Verify(1, d, tr); err != nil {
		t.Fatal(err)
	}
	if _, err := img.Verify(0, d, tr); err != EBadImage {
		t.Fatal(err)
	}
	isum := sha256.Sum256([]byte("code"))
	if _, err := img.Verify(1, d, &Trust{Anchors: tr.Anchors, Revoked: []pgauth.KeyID{pgauth.DigestID(isum)}}); err != EModuleRevoked {
		t.Fatal(err)
	}
	if _, err := img.Verify(1, d, &Trust{}); err != EUntrustedImage {
		t.Fatal(err)
	}
	// A classical countersignature needs Classical to count.
	both, err := SignImage(file, ckey(t, 4), nil)
	if err != nil {
		t.Fatal(err)
	}
	img, _ = ParseImage(both)
	ctr := &Trust{Anchors: []pgauth.KeyID{ckey(t, 4).Public().ID()}}
	if _, err := img.Verify(1, d, ctr); err != EUntrustedImage {
		t.Fatal(err)
	}
	ctr.Classical = true
	if _, err := img.Verify(1, d, ctr); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(file); n += 97 {
		if _, err := ParseImage(file[:n]); err == nil {
			t.Fatalf("truncated image at %d parses", n)
		}
	}
}
