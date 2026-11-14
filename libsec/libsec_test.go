package libsec

import (
	"bytes"
	"crypto/rc4"
	"encoding/hex"
	"testing"
)

func TestMD4(t *testing.T) {
	// RFC 1320 test suite.
	cases := map[string]string{
		"":                           "31d6cfe0d16ae931b73c59d7e0c089c0",
		"a":                          "bde52cb31de33e46245e05fbdbd6fb24",
		"abc":                        "a448017aaf21d8525fc10ae87aa6729d",
		"message digest":             "d9130a8164549fe818874806e1c7014b",
		"abcdefghijklmnopqrstuvwxyz": "d79e1c308aa5bbcdeea8ed63df412da9",
		"12345678901234567890123456789012345678901234567890123456789012345678901234567890": "e33b4ddc9c38f2199c3e7b164fcc0536",
	}
	for in, want := range cases {
		h := NewMD4()
		h.Write([]byte(in))
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			t.Errorf("md4(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestIDEA(t *testing.T) {
	key, _ := hex.DecodeString("00010002000300040005000600070008")
	pt, _ := hex.DecodeString("0000000100020003")
	c := NewIDEA(key)
	ct := make([]byte, 8)
	c.Encrypt(ct, pt)
	if got := hex.EncodeToString(ct); got != "11fbed2b01986de5" {
		t.Fatalf("idea = %s", got)
	}
	back := make([]byte, 8)
	c.Decrypt(back, ct)
	if !bytes.Equal(back, pt) {
		t.Fatalf("idea decrypt = %x", back)
	}
}

func TestRC4(t *testing.T) {
	key := []byte("Secret key")
	msg := []byte("some text to be encrypted, and more of it")
	want := make([]byte, len(msg))
	s, _ := rc4.NewCipher(key)
	s.XORKeyStream(want, msg)
	r := NewRC4(key)
	got := append([]byte(nil), msg...)
	r.XOR(got[:10])
	r.Skip(5)
	r.Back(5)
	r.XOR(got[10:])
	if !bytes.Equal(got, want) {
		t.Fatalf("rc4 differs")
	}
}

func TestPoly1305RFC8439(t *testing.T) {
	var key [32]byte
	k, _ := hex.DecodeString("85d6be7857556d337f4452fe42d506a80103808afb0db2fd4abff6af4149f51b")
	copy(key[:], k)
	tag := Poly1305(&key, []byte("Cryptographic Forum Research Group"))
	if got := hex.EncodeToString(tag[:]); got != "a8061dc1305136c6c22b8baf0c0127a9" {
		t.Fatalf("poly1305 = %s", got)
	}
}

func TestChaCha20Poly1305RFC8439(t *testing.T) {
	key, _ := hex.DecodeString("808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	nonce, _ := hex.DecodeString("070000004041424344454647")
	aad, _ := hex.DecodeString("50515253c0c1c2c3c4c5c6c7")
	plain := []byte("Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.")
	want := "d31a8d34648e60db7b86afbc53ef7ec2a4aded51296e08fea9e2b5a736ee62d63dbea45e8ca9671282fafb69da92728b1a71de0a9e060b2905d6a5b67ecd3b3692ddbd7f2d778b8c9803aee328091b58fab324e4fad675945585808b4831d7bc3ff4def08e4b7a9de576d26586cec64b6116" +
		"1ae10b594f09e26a7e902ecbd0600691"
	a, err := NewChaCha20Poly1305(key)
	if err != nil {
		t.Fatal(err)
	}
	ct := a.Seal(nil, nonce, plain, aad)
	if got := hex.EncodeToString(ct); got != want {
		t.Fatalf("seal = %s", got)
	}
	back, err := a.Open(nil, nonce, ct, aad)
	if err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("open = %q, %v", back, err)
	}
	for i := range ct {
		bad := bytes.Clone(ct)
		bad[i] ^= 0x10
		if _, err := a.Open(nil, nonce, bad, aad); err == nil {
			t.Fatalf("changed byte %d opened", i)
		}
	}
	// Every length around the block sizes round-trips.
	for n := 0; n < 200; n++ {
		m := bytes.Repeat([]byte{byte(n)}, n)
		c := a.Seal(nil, nonce, m, aad[:n%len(aad)])
		if back, err := a.Open(nil, nonce, c, aad[:n%len(aad)]); err != nil || !bytes.Equal(back, m) {
			t.Fatalf("length %d: %v", n, err)
		}
	}
}
