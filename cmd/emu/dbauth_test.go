package main

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/keyring"
)

// dmHostDir makes root/tmp/name holding files, removed when the test
// ends, for tests whose input is made on the host.
func dmHostDir(t *testing.T, name string, files map[string][]byte) {
	t.Helper()
	dir := filepath.Join(root, "tmp", name)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// aescbcSeal encrypts data under password as Plan 9's aescbc -e does,
// with a fixed IV and initial block, independently of auth/aescbc.
func aescbcSeal(password string, data []byte) []byte {
	k := sha1.Sum([]byte("aescbc file" + password))
	key := k[:16]
	mk := md5.Sum(key)
	b, _ := aes.NewCipher(key)
	iv := []byte("0123456789abcdef")
	ivec := append([]byte(nil), iv...)
	plain := append([]byte("random  plaintxt"), data...)
	ct := append([]byte(nil), plain...)
	p := ct
	for ; len(p) >= 16; p = p[16:] {
		for i := range 16 {
			p[i] ^= ivec[i]
		}
		b.Encrypt(p[:16], p[:16])
		copy(ivec, p[:16])
	}
	if len(p) > 0 {
		b.Encrypt(ivec, ivec)
		for i := range p {
			p[i] ^= ivec[i]
		}
	}
	h := hmac.New(sha1.New, mk[:])
	h.Write(ct)
	out := append([]byte("AES CBC SHA1  2\n"), iv...)
	out = append(out, ct...)
	return h.Sum(out)
}

func TestAescbc(t *testing.T) {
	text := strings.Repeat("a secret message that is longer than a block; ", 200) + "end\n"
	dmHostDir(t, "aescbctest", map[string][]byte{
		"msg.aes": aescbcSeal("sesame", []byte(text)),
		"msg":     []byte(text),
		"keyfile": []byte("sesame\n\n"),
	})
	dmRun(t, `load std
d=/tmp/aescbctest
cd $d
auth/aescbc -d -k sesame < msg.aes > out
cmp msg out && echo decrypted
auth/aescbc -d -f keyfile < msg.aes > out
cmp msg out && echo decrypted with keyfile
auth/aescbc -e -k sesame < msg > new.aes
auth/aescbc -d -k sesame < new.aes > out
cmp msg out && echo round trip
echo -n short | auth/aescbc -e -k k | auth/aescbc -d -k k
echo
cat /dev/null | auth/aescbc -e -k k | auth/aescbc -d -k k
echo empty
echo errors
auth/aescbc -d -k wrong < msg.aes > /dev/null
echo $status
echo -n AES | auth/aescbc -d -k k
echo $status
auth/aescbc -d -f nosuch < msg.aes
echo $status
auth/aescbc -k x
echo $status
`, `decrypted
decrypted with keyfile
round trip
short
empty
errors
aescbc: decrypted file failed to authenticate
failed to authenticate
aescbc: unexpectedly short read
|read error
aescbc: can't open nosuch: file does not exist
keyfile
usage: auth/aescbc -d [-k key] [-f keyfile] <file.aes >clear.txt
  or: auth/aescbc -e [-k key] [-f keyfile] <clear.txt >file.aes
usage
`)
}

func TestAi2key(t *testing.T) {
	alpha, p := keyring.DHparams(128)
	signer := keyring.GenSK("elgamal", "signer", 128)
	mk := func(owner string, exp int) (*keyring.Authinfo, []byte) {
		sk := keyring.GenSKfromPK(keyring.SKtoPK(signer), owner)
		pk := keyring.SKtoPK(sk)
		cert := keyring.Sign(signer, exp, keyring.SHA1([]byte(keyring.PKtoStr(pk)), nil, nil), "sha1")
		ai := &keyring.Authinfo{MySK: sk, MyPK: pk, Cert: cert, SPK: keyring.SKtoPK(signer), Alpha: alpha, P: p}
		return ai, keyring.MarshalAuthinfo(ai)
	}
	exp := int(time.Now().Unix()) + 365*24*3600
	alice, ab := mk("alice", 0)
	bob, bb := mk("bob smith", exp)
	_, eb := mk("old", 1000)
	dmHostDir(t, "ai2keytest", map[string][]byte{
		"default": ab, "tcp!example.com!styx": bb, "bob": bb, "net!host": ab, "expired": eb,
	})
	dnl := func(s string) string { return strings.TrimRight(strings.ReplaceAll(s, "\n", "^"), "^") }
	line := func(ai *keyring.Authinfo, tag, loc, user, expires string) string {
		return fmt.Sprintf("key proto=infauth%s %s sigalg=elgamal-sha1 user=%s signer=signer pk=%s !sk=%s spk=%s cert=%s dh-alpha=%s dh-p=%s%s\n",
			tag, loc, user, dnl(keyring.PKtoStr(ai.MyPK)), dnl(keyring.SKtoStr(ai.MySK)), dnl(keyring.PKtoStr(ai.SPK)),
			dnl(keyring.CerttoStr(ai.Cert)), alpha.IPtoStr(16), p.IPtoStr(16), expires)
	}
	bobexp := fmt.Sprintf(" expires=%d", exp)
	want := line(alice, "", "dom=* server=*", "alice", "") +
		line(bob, "", "dom=example.com server=example.com service=styx", "'bob smith'", bobexp) +
		line(bob, "", "server=bob", "'bob smith'", bobexp) +
		"ai2key: /tmp/ai2keytest/expired: certificate expired -- key ignored\n" +
		line(alice, " role=client", "server=host", "alice", "") +
		line(alice, " role=client", "dom=* server=*", "alice", "") +
		"ai2key: cannot read nosuch: file does not exist\n" +
		"usage: ai2key [-t 'attr=value attr=value ...'] keyfile ...\n" +
		"usage\n"
	dmRun(t, `d=/tmp/ai2keytest
auth/ai2key $d/default $d/'tcp!example.com!styx' $d/bob $d/expired
auth/ai2key -t 'role=client' $d/'net!host' $d/default
auth/ai2key nosuch
auth/ai2key
echo $status
`, want)
}
