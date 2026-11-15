package main

import (
	"archive/tar"
	"bytes"
	"crypto/aes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"fmt"
	"io"
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

// TestTar runs tar against itself, gettar, puttar and gunzip, reads an
// archive Go's archive/tar wrote with GNU and pax long names, and has
// archive/tar read one tar wrote.
func TestTar(t *testing.T) {
	var gnu, pax bytes.Buffer
	long := strings.Repeat("long-directory-name/", 7) + "file"
	for _, c := range []struct {
		buf    *bytes.Buffer
		format tar.Format
	}{{&gnu, tar.FormatGNU}, {&pax, tar.FormatPAX}} {
		tw := tar.NewWriter(c.buf)
		tw.WriteHeader(&tar.Header{Name: long, Mode: 0o644, Size: 5, ModTime: time.Unix(1000, 0), Format: c.format})
		tw.Write([]byte("long\n"))
		tw.WriteHeader(&tar.Header{Name: "link", Linkname: "file", Typeflag: tar.TypeSymlink, ModTime: time.Unix(1000, 0), Format: c.format})
		tw.Close()
	}
	dmHostDir(t, "tartest", map[string][]byte{"gnu.tar": gnu.Bytes(), "pax.tar": pax.Bytes()})
	dmRun(t, `load std
d=/tmp/tartest
cd $d
rm -rf src x y
mkdir -p src/sub
echo hello > src/a
echo world > src/sub/b
chmod 600 src/a
touch -t 1000 src/a src/sub/b src/sub src
tar cf t.tar src
tar t < t.tar
tar tvf t.tar
mkdir x
cd x
tar xvf ../t.tar
cat src/a src/sub/b
ls -l src/a | sed 's/ [A-Z] [0-9].*//'
cd ..
tar czf t.tgz src
gunzip < t.tgz | tar t src/sub
tar tf t.tgz src/a
mkdir y
cd y
tar xzf ../t.tgz src/sub
ls -p src
echo changed > src/sub/b
tar xkf ../t.tar
cat src/sub/b src/a
cd ..
gettar -v < t.tar >[2=1] | sort
puttar src | tar t
tar tvf gnu.tar
tar xf pax.tar
cat long-directory-name/*/*/*/*/*/*/file
tar cf out.tar src/a
echo errors
tar xf nosuch.tar
echo $status
tar cf /dev/null nosuch
echo $status
tar q
echo $status
`, `src
src/a
src/sub
src/sub/b
d-rwxr-xr-x        0 Jan 01  1970 src
--rw-------        6 Jan 01  1970 src/a
d-rwxr-xr-x        0 Jan 01  1970 src/sub
--rw-r--r--        6 Jan 01  1970 src/sub/b
src
src/a
src/sub
src/sub/b
hello
world
--rw-------
src/sub
src/sub/b
src/a
sub
changed
hello
src/a
src/sub/b
src
src/a
src/sub
src/sub/b
--rw-r--r--        5 Jan 01  1970 `+long+`
-----------        0 Jan 01  1970 link -> file
tar: skipping symlink link
long
errors
tar: nosuch.tar: file does not exist
nosuch.tar: file does not exist
tar: nosuch: file does not exist
errors
usage: tar {ctx}[fkmpRTvz] [archive] [file ...]
usage
`)
	f, err := os.Open(filepath.Join(root, "tmp", "tartest", "out.tar"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	h, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(tr)
	if h.Name != "src/a" || h.Mode != 0o600 || h.ModTime.Unix() != 1000 || string(b) != "hello\n" {
		t.Errorf("archive/tar read %+v %q", h, b)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Errorf("after the member: %v", err)
	}
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
