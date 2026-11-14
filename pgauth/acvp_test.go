package pgauth

import (
	"bytes"
	"crypto/mldsa"
	"crypto/mlkem"
	"crypto/mlkem/mlkemtest"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// A subset of NIST's ACVP vectors for ML-KEM-768 and ML-DSA-65
// (testdata/acvp/subset.json, from the ACVP-Server repository's
// gen-val/json-files): key generation from seeds, deterministic
// encapsulation, encapsulation key checks, and signature verification
// with a context. Go's standard library runs the full sets in its own
// tests; these pin the parameter sets pgauth uses, through the API it
// uses.

type hexb []byte

func (h *hexb) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := hex.DecodeString(s)
	*h = v
	return err
}

func TestACVP(t *testing.T) {
	b, err := os.ReadFile("testdata/acvp/subset.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		KeyGen []struct {
			TcID     int
			D, Z, Ek hexb
		} `json:"mlkem768_keygen"`
		Encap []struct {
			TcID        int
			Ek, M, C, K hexb
		} `json:"mlkem768_encap"`
		EkCheck []struct {
			TcID       int
			Ek         hexb
			TestPassed bool
		} `json:"mlkem768_ekcheck"`
		DsaKeyGen []struct {
			TcID     int
			Seed, Pk hexb
		} `json:"mldsa65_keygen"`
		SigVer []struct {
			TcID                   int
			Pk, Message, Signature hexb
			Context                hexb
			TestPassed             bool
		} `json:"mldsa65_sigver"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.KeyGen) == 0 || len(v.Encap) == 0 || len(v.EkCheck) == 0 || len(v.DsaKeyGen) == 0 || len(v.SigVer) == 0 {
		t.Fatal("empty vector set")
	}
	for _, c := range v.KeyGen {
		dk, err := mlkem.NewDecapsulationKey768(append(append([]byte(nil), c.D...), c.Z...))
		if err != nil || !bytes.Equal(dk.EncapsulationKey().Bytes(), c.Ek) {
			t.Errorf("ML-KEM keyGen %d: %v", c.TcID, err)
		}
	}
	for _, c := range v.Encap {
		ek, err := mlkem.NewEncapsulationKey768(c.Ek)
		if err != nil {
			t.Fatalf("encap %d: %v", c.TcID, err)
		}
		k, ct, err := mlkemtest.Encapsulate768(ek, c.M)
		if err != nil || !bytes.Equal(k, c.K) || !bytes.Equal(ct, c.C) {
			t.Errorf("ML-KEM encapsulation %d: %v", c.TcID, err)
		}
	}
	for _, c := range v.EkCheck {
		_, err := mlkem.NewEncapsulationKey768(c.Ek)
		if (err == nil) != c.TestPassed {
			t.Errorf("ML-KEM encapsulation key check %d: %v, want passed %v", c.TcID, err, c.TestPassed)
		}
	}
	for _, c := range v.DsaKeyGen {
		k, err := mldsa.NewPrivateKey(mldsa.MLDSA65(), c.Seed)
		if err != nil || !bytes.Equal(k.PublicKey().Bytes(), c.Pk) {
			t.Errorf("ML-DSA keyGen %d: %v", c.TcID, err)
		}
	}
	for _, c := range v.SigVer {
		pk, err := mldsa.NewPublicKey(mldsa.MLDSA65(), c.Pk)
		ok := err == nil && mldsa.Verify(pk, c.Message, c.Signature, &mldsa.Options{Context: string(c.Context)}) == nil
		if ok != c.TestPassed {
			t.Errorf("ML-DSA sigVer %d: verified %v, want %v", c.TcID, ok, c.TestPassed)
		}
	}
}
