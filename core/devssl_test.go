package core

import (
	"bytes"
	"context"
	"crypto/rc4"
	"crypto/sha1"
	"fmt"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
)

func newSSLProg(t *testing.T) *Prog {
	t.Helper()
	k := &Kernel{Platform: &testPlat{}, Progs: NewProgs(), Owner: "inferno"}
	k.DevTab = NewDevTab(NewRoot("inferno"), NewDevPipe(k), NewDevDup(k), NewDevSSL(k))
	rc, err := k.DevTab.Attach(context.Background(), '/', "")
	if err != nil {
		t.Fatal(err)
	}
	return NewProg(k, NewPgrp(k.DevTab, rc), "inferno")
}

// sslconn pushes ssl onto fd and returns the ctl and data fds.
func sslconn(t *testing.T, p *Prog, fd int) (int, int) {
	t.Helper()
	cfd, err := p.Open("#D/clone", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, err := p.Read(cfd, buf)
	if err != nil {
		t.Fatal(err)
	}
	dfd, err := p.Open("#D/"+string(buf[:n])+"/data", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	ctl(t, p, cfd, fmt.Sprintf("fd %d", fd))
	return cfd, dfd
}

func ctl(t *testing.T, p *Prog, fd int, s string) {
	t.Helper()
	if _, err := p.Write(fd, []byte(s)); err != nil {
		t.Fatalf("ctl %q: %v", s, err)
	}
}

func readn(t *testing.T, p *Prog, fd, n int) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, 65536)
	for len(out) < n {
		k, err := p.Read(fd, buf[:n-len(out)])
		if err != nil || k == 0 {
			t.Fatalf("read: %d %v", k, err)
		}
		out = append(out, buf[:k]...)
	}
	return out
}

func TestSSLFiles(t *testing.T) {
	p := newSSLProg(t)
	cfd, _ := sslconn(t, p, 0)
	_ = cfd
	if got := names(t, p, "#D"); got != "0 clone" {
		t.Errorf("#D: %q", got)
	}
	if got := names(t, p, "#D/0"); got != "ctl data secretin secretout encalgs hashalgs" {
		t.Errorf("#D/0: %q", got)
	}
	fd, err := p.Open("#D/0/encalgs", abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, _ := p.Read(fd, buf)
	if got := string(buf[:n]); got != "descbc desecb des_56_cbc des_56_ecb des_40_cbc des_40_ecb rc4 rc4_256 rc4_128 rc4_40 ideacbc ideaecb" {
		t.Errorf("encalgs %q", got)
	}
	fd, _ = p.Open("#D/0/hashalgs", abi.OREAD)
	n, _ = p.Read(fd, buf)
	if got := string(buf[:n]); got != "md4 md5 sha1 sha" {
		t.Errorf("hashalgs %q", got)
	}
	if _, err := p.Write(cfd, []byte("alg rc4")); err == nil || err.Error() != "algorithm but no secret" {
		t.Errorf("alg without secret: %v", err)
	}
	if _, err := p.Write(cfd, []byte("bogus")); err == nil || err.Error() != abi.Ebadarg {
		t.Errorf("bad ctl: %v", err)
	}
}

// TestSSLRecords checks the bytes on the wire against the SSLv2 record
// format devssl.c writes.
func TestSSLRecords(t *testing.T) {
	p := newSSLProg(t)
	fds, err := p.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cfd, dfd := sslconn(t, p, fds[0])

	// Clear: a two-byte count with the top bit set.
	p.Write(dfd, []byte("hello"))
	if got := readn(t, p, fds[1], 7); !bytes.Equal(got, []byte("\x80\x05hello")) {
		t.Fatalf("clear record %q", got)
	}

	// Digesting: sha1 of secret, data and message id, then the data.
	secret := []byte("secret")
	p.Write(cfd, append([]byte("secretout "), []byte("c2VjcmV0")...)) // base64 of "secret"
	ctl(t, p, cfd, "alg sha1")
	p.Write(dfd, []byte("hello"))
	h := sha1.New()
	h.Write(secret)
	h.Write([]byte("hello"))
	h.Write([]byte{0, 0, 0, 1}) // the clear record was message 0
	want := append([]byte{0x80, 25}, h.Sum(nil)...)
	want = append(want, "hello"...)
	if got := readn(t, p, fds[1], len(want)); !bytes.Equal(got, want) {
		t.Fatalf("sha1 record\n got %x\nwant %x", got, want)
	}

	// rc4_128 and sha1: the digest, then encryption of digest and data.
	ctl(t, p, cfd, "alg sha1 rc4_128")
	p.Write(dfd, []byte("again"))
	h = sha1.New()
	h.Write(secret)
	h.Write([]byte("again"))
	h.Write([]byte{0, 0, 0, 2})
	body := append(h.Sum(nil), "again"...)
	c, _ := rc4.NewCipher(secret)
	c.XORKeyStream(body, body)
	want = append([]byte{0x80, 25}, body...)
	if got := readn(t, p, fds[1], len(want)); !bytes.Equal(got, want) {
		t.Fatalf("rc4 record\n got %x\nwant %x", got, want)
	}

	// A block cipher pads to its block size with a three-byte header.
	for _, f := range []string{"secretin", "secretout"} {
		fd, err := p.Open("#D/0/"+f, abi.OWRITE)
		if err != nil {
			t.Fatal(err)
		}
		p.Write(fd, []byte("0123456789abcdef"))
		p.Close(fd)
	}
	ctl(t, p, cfd, "alg des_56_cbc")
	p.Write(dfd, []byte("abc"))
	got := readn(t, p, fds[1], 11)
	if got[0] != 0 || got[1] != 8 || got[2] != 5 {
		t.Fatalf("padded header %x", got[:3])
	}
}

func TestSSLBothWays(t *testing.T) {
	for _, alg := range []string{"clear", "sha1", "md5", "md4", "rc4", "rc4_128 sha1", "rc4_256/md5",
		"descbc sha1", "des_40_ecb", "ideacbc md5", "ideaecb"} {
		p := newSSLProg(t)
		fds, err := p.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		ca, da := sslconn(t, p, fds[0])
		cb, db := sslconn(t, p, fds[1])
		secret := strings.Repeat("0123456789abcdef", 2)
		for _, c := range []int{ca, cb} {
			fd, err := p.Open(fmt.Sprintf("#D/%d/secretin", map[int]int{ca: 0, cb: 1}[c]), abi.ORDWR)
			if err != nil {
				t.Fatal(err)
			}
			p.Write(fd, []byte(secret))
			p.Close(fd)
			ctl(t, p, c, "alg "+alg)
		}
		for i, msg := range []string{"first", strings.Repeat("x", 40000), "a", "third message"} {
			from, to := da, db
			if i%2 == 1 {
				from, to = db, da
			}
			if i == 1 {
				// Larger than a record: arrives in pieces.
				go p.Write(from, []byte(msg))
			} else if _, err := p.Write(from, []byte(msg)); err != nil {
				t.Fatalf("%s: write: %v", alg, err)
			}
			if got := string(readn(t, p, to, len(msg))); got != msg {
				t.Fatalf("%s: message %d: got %d bytes", alg, i, len(got))
			}
		}
	}
}

func TestSSLBadDigest(t *testing.T) {
	p := newSSLProg(t)
	fds, _ := p.Pipe()
	ca, da := sslconn(t, p, fds[0])
	cb, db := sslconn(t, p, fds[1])
	ctl(t, p, ca, "secretin YWFhYQ==")
	ctl(t, p, cb, "secretin YmJiYg==")
	ctl(t, p, ca, "alg sha1")
	ctl(t, p, cb, "alg sha1")
	p.Write(da, []byte("hi"))
	buf := make([]byte, 10)
	if _, err := p.Read(db, buf); err == nil || err.Error() != "bad digest" {
		t.Fatalf("read with the wrong secret: %v", err)
	}
	if _, err := p.Read(db, buf); err == nil || err.Error() != abi.Ebadusefd {
		t.Fatalf("read after a bad digest: %v", err)
	}
}
