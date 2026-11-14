package pglogin

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/ericvh/purgatorio-go/pgauth"
)

// testdata was written by the Rust sibling's purg-auth (gen.rs.txt) with
// a deterministic byte source: byte i is seed+i.
type det byte

func (d *det) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(*d)
		*d++
	}
	return len(b), nil
}

func detR(seed byte) *det { d := det(seed); return &d }

func rust(t *testing.T, name string) []byte {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSpake2Rust(t *testing.T) {
	pw := []byte("0123456789abcdef0123456789abcdef")
	a, ma, _ := StartSpake2(true, pw, []byte("alice"), []byte(ServerID), detR(1))
	b, mb, _ := StartSpake2(false, pw, []byte("alice"), []byte(ServerID), detR(100))
	ka, err := a.Finish(mb)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := b.Finish(ma)
	want := rust(t, "spake2.bin")
	got := append(append(append([]byte(nil), ma...), mb...), ka...)
	if !bytes.Equal(got, want) || !bytes.Equal(ka, kb) {
		t.Fatalf("spake2 differs from Rust's:\n%x\n%x", got, want)
	}
	// A wrong password gives another key; a message from the wrong side fails.
	c, mc, _ := StartSpake2(true, []byte("other"), []byte("alice"), []byte(ServerID), detR(1))
	b2, _, _ := StartSpake2(false, pw, []byte("alice"), []byte(ServerID), detR(100))
	k2, _ := b2.Finish(mc)
	kc, _ := c.Finish(mb)
	if bytes.Equal(k2, kc) {
		t.Fatal("different passwords agree")
	}
	if _, err := a.Finish(ma); err == nil {
		t.Fatal("A took A's message")
	}
}

func TestKdfRust(t *testing.T) {
	kdf := Kdf{M: 64, T: 1, P: 1}
	for i := range kdf.Salt {
		kdf.Salt[i] = 5
	}
	v, err := NewVerifier([]byte("password"), kdf)
	if err != nil || v.Encode() != string(rust(t, "verifier.txt")) {
		t.Fatalf("verifier: %v\n%s\n%s", err, v.Encode(), rust(t, "verifier.txt"))
	}
	back, err := ParseVerifier(rust(t, "verifier.txt"))
	if err != nil || back != v {
		t.Fatal("parse")
	}
	if f := FakeVerifier([]byte("secret"), "nobody", detR(7)); f.Encode() != string(rust(t, "fake.txt")) {
		t.Fatalf("fake verifier: %s", f.Encode())
	}
	key, _ := kdf.Derive([]byte("master"))
	blob, _ := SealWith(kdf, key, []byte("keyfs contents"), detR(3))
	if !bytes.Equal(blob, rust(t, "sealed.bin")) {
		t.Fatal("sealed blob differs")
	}
	data, _, _, err := Open([]byte("master"), rust(t, "sealed.bin"))
	if err != nil || string(data) != "keyfs contents" {
		t.Fatal(err)
	}
	if _, _, _, err := Open([]byte("wrong"), blob); err != ErrWrongSeal {
		t.Fatal(err)
	}
	c, out, err := EncryptStream([]byte("secret"), 64, 1, detR(11))
	if err != nil {
		t.Fatal(err)
	}
	x, _ := c.SealChunk([]byte("first chunk"), false)
	y, _ := c.SealChunk([]byte("last"), true)
	out = append(append(out, x...), y...)
	if !bytes.Equal(out, rust(t, "crypt.bin")) {
		t.Fatal("crypt stream differs")
	}
	d, err := DecryptStream([]byte("secret"), out[:CryptHeader])
	if err != nil {
		t.Fatal(err)
	}
	if p, err := d.OpenChunk(x[4:], false); err != nil || string(p) != "first chunk" {
		t.Fatal(err)
	}
	if _, err := d.OpenChunk(y[4:], false); err != ErrWrongCrypt {
		t.Fatal("a last chunk opened as not last")
	}
	if NetkeyResponse([]byte("password"), []byte("12345")) != string(rust(t, "netkey.txt")) {
		t.Fatal("netkey")
	}
}

// recorder logs what one end writes.
type recorder struct {
	io.ReadWriter
	log bytes.Buffer
}

func (r *recorder) Write(b []byte) (int, error) { r.log.Write(b); return r.ReadWriter.Write(b) }

// The Rust exchange, replayed: Go's client and server, with the same
// randomness, write exactly the frames the Rust ones did.
func TestLogin1Rust(t *testing.T) {
	kdf := Kdf{M: 64, T: 1, P: 1}
	for i := range kdf.Salt {
		kdf.Salt[i] = 5
	}
	v, _ := NewVerifier([]byte("password"), kdf)
	c, s := net.Pipe()
	cr, sr := &recorder{ReadWriter: c}, &recorder{ReadWriter: s}
	done := make(chan error, 1)
	go func() {
		h, err := ServerHello(sr)
		if err != nil {
			done <- err
			return
		}
		sess, req, err := Server(sr, h, v, pgauth.AcceptClassical, detR(50))
		if err == nil && string(req) != "the request" {
			err = ErrProto
		}
		if err == nil {
			err = sess.Send(sr, []byte("ok reply"))
		}
		done <- err
	}()
	_, reply, err := Client(cr, false, "cert", "alice", []byte("password"), detR(9), func(Kdf) ([]byte, error) {
		return []byte("the request"), nil
	})
	if err != nil || string(reply) != "ok reply" {
		t.Fatalf("client: %v %q", err, reply)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cr.log.Bytes(), rust(t, "login.c2s")) || !bytes.Equal(sr.log.Bytes(), rust(t, "login.s2c")) {
		t.Fatal("frames differ from the Rust exchange")
	}
}

func exchange(t *testing.T, clientHybrid bool, accept pgauth.Accept, password string) (string, error, error) {
	kdf := Kdf{M: 64, T: 1, P: 1}
	v, _ := NewVerifier([]byte("password"), kdf)
	c, s := net.Pipe()
	done := make(chan error, 1)
	go func() {
		defer s.Close()
		h, err := ServerHello(s)
		if err != nil {
			done <- err
			return
		}
		sess, req, err := Server(s, h, v, accept, detR(50))
		if err == nil {
			err = sess.Send(s, append([]byte("reply to "), req...))
		}
		done <- err
	}()
	_, reply, cerr := Client(c, clientHybrid, "cert", "alice", []byte(password), detR(9), func(Kdf) ([]byte, error) {
		return []byte("req"), nil
	})
	c.Close()
	return string(reply), cerr, <-done
}

func TestLogin2(t *testing.T) {
	if r, cerr, serr := exchange(t, true, 0, "password"); cerr != nil || serr != nil || r != "reply to req" {
		t.Fatalf("hybrid: %q %v %v", r, cerr, serr)
	}
	if _, cerr, serr := exchange(t, true, 0, "wrong"); cerr == nil || cerr.Error() != "authentication failed" || serr != ErrFailed {
		t.Fatalf("wrong password: %v %v", cerr, serr)
	}
	// No fallback either way.
	if _, cerr, _ := exchange(t, false, pgauth.AcceptHybrid, "password"); cerr == nil || cerr.Error() != "hybrid authentication required" {
		t.Fatalf("classical client, hybrid server: %v", cerr)
	}
	if _, cerr, _ := exchange(t, true, pgauth.AcceptClassical, "password"); cerr == nil || cerr.Error() != "classical authentication required" {
		t.Fatalf("hybrid client, classical server: %v", cerr)
	}
	if r, cerr, _ := exchange(t, false, pgauth.AcceptHybrid|pgauth.AcceptClassical, "password"); cerr != nil || r != "reply to req" {
		t.Fatalf("server taking both: %v", cerr)
	}
}
