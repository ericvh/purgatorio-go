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
