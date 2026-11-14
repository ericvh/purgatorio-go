package image

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func section(id byte, body []byte) []byte {
	return append([]byte{id, byte(len(body))}, body...)
}

func custom(name string, data []byte) []byte {
	return section(0, append(append([]byte{byte(len(name))}, name...), data...))
}

func TestDigestSkipsSignatures(t *testing.T) {
	hdr := []byte("\x00asm\x01\x00\x00\x00")
	typ := section(1, []byte{0})
	plain := append(append([]byte{}, hdr...), typ...)
	signed := append(append(append([]byte{}, hdr...), custom("purgatorio.sig", []byte{1, 2, 3})...), typ...)
	other := append(append(append([]byte{}, hdr...), custom("purgatorio.abi", []byte{1})...), typ...)
	d1, err := Digest(plain)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != sha256.Sum256(plain) {
		t.Error("digest of an unsigned module is not its SHA-256")
	}
	if d2, _ := Digest(signed); d2 != d1 {
		t.Error("a signature changed the digest")
	}
	if d3, _ := Digest(other); d3 == d1 {
		t.Error("another custom section did not change the digest")
	}
	for _, bad := range [][]byte{hdr[:7], append(append([]byte{}, hdr...), 1, 5, 0), append(append([]byte{}, hdr...), 1, 0x80, 0x80, 0x80, 0x80, 0x10)} {
		if _, err := Digest(bad); err == nil {
			t.Errorf("Digest(%x) did not fail", bad)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	im := &Image{Kind: KindWazeroArm64, Code: []byte("WAZEVO code"), Sig: []byte{1, 2}}
	im.Module[0] = 7
	b := Encode(im)
	got, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != im.Kind || got.Module != im.Module || !bytes.Equal(got.Code, im.Code) || !bytes.Equal(got.Sig, im.Sig) {
		t.Errorf("Parse(Encode(im)) = %+v", got)
	}
	if !bytes.Equal(got.Message(), im.Message()) {
		t.Error("messages differ")
	}
	for _, bad := range [][]byte{b[:20], append([]byte("PURGIMG2"), b[8:]...), b[:headerSize]} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse of a bad image did not fail")
		}
	}
	b[9] = 1
	if _, err := Parse(b); err == nil {
		t.Error("nonzero padding parsed")
	}
}
