package libsec

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func xunhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func xseal(key *[32]byte, nonce *[24]byte, plain, aad []byte) []byte {
	a, _ := NewXChaCha20Poly1305(key[:])
	return a.Seal(nil, nonce[:], plain, aad)
}

func xopen(key *[32]byte, nonce *[24]byte, sealed, aad []byte) ([]byte, error) {
	a, _ := NewXChaCha20Poly1305(key[:])
	return a.Open(nil, nonce[:], sealed, aad)
}

// draft-irtf-cfrg-xchacha-03, A.3.1, and HChaCha20's 2.2.1.
func TestXChaCha20Poly1305(t *testing.T) {
	plain := []byte("Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.")
	aad := xunhex(t, "50515253c0c1c2c3c4c5c6c7")
	key := [32]byte(xunhex(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f"))
	nonce := [24]byte(xunhex(t, "404142434445464748494a4b4c4d4e4f5051525354555657"))
	want := xunhex(t, "bd6d179d3e83d43b9576579493c0e939572a1700252bfaccbed2902c21396cbb731c7f1b0b4aa6440bf3a82f4eda7e39ae64c6708c54c216cb96b72e1213b4522f8c9ba40db5d945b11b69b982c1bb9e3f3fac2bc369488f76b2383565d3fff921f9664c97637da9768812f615c68b13b52e"+
		"c0875924c1c7987947deafd8780acf49")
	got := xseal(&key, &nonce, plain, aad)
	if !bytes.Equal(got, want) {
		t.Fatalf("seal\n got %x\nwant %x", got, want)
	}
	if back, err := xopen(&key, &nonce, got, aad); err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("open: %v", err)
	}
	if _, err := xopen(&key, &nonce, got, append(aad, 0)); err == nil {
		t.Errorf("longer aad: %v", err)
	}
	hk := [32]byte(xunhex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"))
	in := [16]byte(xunhex(t, "000000090000004a0000000031415927"))
	if sub := HChaCha20(&hk, &in); hex.EncodeToString(sub[:]) != "82413b4227b27bfed30e42508a877d73a0f9e4d58a74a853c12ec41326d3ecdc" {
		t.Errorf("hchacha20 %x", sub)
	}
}

// Every length around the block and pad boundaries round-trips, and a
// changed byte anywhere fails.
func TestXChaCha20Poly1305Lengths(t *testing.T) {
	var key [32]byte
	var nonce [24]byte
	for i := range key {
		key[i] = byte(i * 7)
	}
	for _, n := range []int{0, 1, 15, 16, 17, 63, 64, 65, 127, 128, 255, 256, 257, 1000, 4096, 32768, 32799} {
		msg := make([]byte, n)
		for i := range msg {
			msg[i] = byte(i * 13)
		}
		aad := msg[:n%40]
		s := xseal(&key, &nonce, msg, aad)
		if back, err := xopen(&key, &nonce, s, aad); err != nil || !bytes.Equal(back, msg) {
			t.Errorf("%d: %v", n, err)
		}
		for _, at := range []int{0, len(s) / 2, len(s) - 1} {
			bad := bytes.Clone(s)
			bad[at] ^= 0x80
			if _, err := xopen(&key, &nonce, bad, aad); err == nil {
				t.Errorf("%d: byte %d changed: %v", n, at, err)
			}
		}
	}
}
