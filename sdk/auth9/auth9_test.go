package auth9

import (
	"bytes"
	"io"
	"math/bits"
	"net"
	"testing"
)

func TestCrypt(t *testing.T) {
	key := PassToKey("a password")
	for _, n := range []int{7, 8, 9, 13, 15, 72, 141} {
		plain := make([]byte, n)
		for i := range plain {
			plain[i] = byte(i * 7)
		}
		b := append([]byte(nil), plain...)
		Encrypt(key, b)
		if n >= 8 && bytes.Equal(b, plain) || n < 8 && !bytes.Equal(b, plain) {
			t.Errorf("%d bytes: encrypt gave %x", n, b)
		}
		Decrypt(key, b)
		if !bytes.Equal(b, plain) {
			t.Errorf("%d bytes: round trip gave %x", n, b)
		}
	}
}

func TestDes56to64(t *testing.T) {
	k := Des56to64([]byte{0xff, 0, 0xaa, 0x55, 0x12, 0x34, 0x56})
	for _, b := range k {
		if bits.OnesCount8(b)%2 != 1 {
			t.Errorf("%x: byte %02x has even parity", k, b)
		}
	}
	// The top seven bits of each byte are the key's 56 bits in order.
	var got uint64
	for _, b := range k {
		got = got<<7 | uint64(b>>1)
	}
	if got != 0xff00aa55123456 {
		t.Errorf("bits %x", got)
	}
}

func TestPassToKey(t *testing.T) {
	short, long := PassToKey("pw"), PassToKey("a much longer password than eight")
	if len(short) != DESKEYLEN || len(long) != DESKEYLEN || bytes.Equal(short, long) {
		t.Errorf("keys %x %x", short, long)
	}
	// A short password's bytes, seven bits each, after a NUL and blanks.
	if k := PassToKey("\x02"); k[0] != 0x02 || k[1] != 0 || k[2] != 0x20>>2|0x20<<5&0xff {
		t.Errorf("key %x", k)
	}
	if !bytes.Equal(PassToKey("same"), PassToKey("same")) {
		t.Error("not deterministic")
	}
	// Only the first ANAMELEN-1 bytes count.
	a := PassToKey("0123456789012345678901234567AAA")
	b := PassToKey("0123456789012345678901234567BBB")
	if !bytes.Equal(a, b) {
		t.Error("bytes past ANAMELEN-1 changed the key")
	}
}

func TestMessages(t *testing.T) {
	tr := &Ticketreq{Type: AuthTreq, AuthID: "glenda", AuthDom: "example.com", Chal: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}, HostID: "bootes", UID: "bootes"}
	b := tr.Pack()
	if len(b) != TICKREQLEN || b[0] != AuthTreq || string(b[1:7]) != "glenda" {
		t.Fatalf("ticketreq %x", b)
	}
	if got, _ := UnpackTicketreq(b); *got != *tr {
		t.Errorf("ticketreq %+v", got)
	}
	key := PassToKey("k")
	tk := &Ticket{Num: AuthTc, Chal: tr.Chal, CUID: "bootes", SUID: "bootes", Key: [7]byte{9, 8, 7, 6, 5, 4, 3}}
	if got := UnpackTicket(tk.Pack(key), key); *got != *tk {
		t.Errorf("ticket %+v", got)
	}
	if got := UnpackTicket(tk.Pack(key), PassToKey("wrong")); got.Num == AuthTc && got.Chal == tk.Chal {
		t.Error("wrong key opened the ticket")
	}
	a := &Authenticator{Num: AuthAc, Chal: tr.Chal, ID: 0x01020304}
	ab := a.Pack(tk.Key[:])
	if len(ab) != AUTHENTLEN {
		t.Fatal(len(ab))
	}
	if got := UnpackAuthenticator(ab, tk.Key[:]); *got != *a {
		t.Errorf("authenticator %+v", got)
	}
}

func TestServeTicket(t *testing.T) {
	keys := map[string][]byte{"glenda": PassToKey("server"), "bootes": PassToKey("client")}
	c, s := net.Pipe()
	go ServeTicket(s, keys)
	defer c.Close()
	tr := &Ticketreq{Type: AuthTreq, AuthID: "glenda", AuthDom: "d", Chal: [8]byte{1}, HostID: "bootes", UID: "bootes"}
	tc, sealed, err := GetTicket(c, tr, keys["bootes"])
	if err != nil {
		t.Fatal(err)
	}
	ts := UnpackTicket(sealed, keys["glenda"])
	if tc.Num != AuthTc || ts.Num != AuthTs || tc.Key != ts.Key || tc.Chal != tr.Chal || ts.CUID != "bootes" || ts.SUID != "bootes" {
		t.Errorf("tickets %+v %+v", tc, ts)
	}
	// Speaking for another user gets tickets no one can open.
	tr.UID = "glenda"
	tc, _, err = GetTicket(c, tr, keys["bootes"])
	if err != nil || tc.Num == AuthTc && tc.Chal == tr.Chal {
		t.Errorf("speaks for: %+v %v", tc, err)
	}
	tr.Type = 99
	if _, _, err := GetTicket(c, tr, keys["bootes"]); err == nil || err.Error() != "remote: unsupported request" {
		t.Errorf("bad type: %v", err)
	}
}

func TestParseKeyFile(t *testing.T) {
	keys, err := ParseKeyFile([]byte(`# the domain's users
key proto=p9sk1 dom=pg user=glenda '!password=server pw'

key proto=p9sk1 dom=pg user=bootes !hex=0102030405060a
key proto=ed25519 !seed=00
`))
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := keys("pg", "glenda"); !ok || !bytes.Equal(k, PassToKey("server pw")) {
		t.Errorf("glenda %x %v", k, ok)
	}
	if k, ok := keys("pg", "bootes"); !ok || !bytes.Equal(k, []byte{1, 2, 3, 4, 5, 6, 10}) {
		t.Errorf("bootes %x %v", k, ok)
	}
	if _, ok := keys("other", "glenda"); ok {
		t.Error("key found in another domain")
	}
	for text, want := range map[string]string{
		"key proto=p9sk1 dom=pg !password=x":         "line 1: need user and dom attributes",
		"\nkey proto=p9sk1 dom=pg user=u":            "line 2: need !password or !hex attribute",
		"key proto=p9sk1 dom=pg user=u !hex=01":      "line 1: malformed !hex key data",
		"delkey proto=p9sk1":                         "line 1: not a key line",
		"key proto=p9sk1 dom=pg user=u '!password=x": "line 1: not a key line",
		"key proto=p9sk1 dom=pg user=" + string(bytes.Repeat([]byte("a"), ANAMELEN)) + " !password=x": "line 1: user name too long",
	} {
		if _, err := ParseKeyFile([]byte(text)); err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %s", text, err, want)
		}
	}
}

func TestReadResp(t *testing.T) {
	if _, err := ReadResp(bytes.NewReader([]byte{42}), 1); err == nil || err.Error() != "AS protocol botch: resp 42" {
		t.Error(err)
	}
	if b, err := ReadResp(bytes.NewReader([]byte("\x0900003abc")), 1); err != nil || string(b) != "abc" {
		t.Error(b, err)
	}
	if _, err := ReadResp(bytes.NewReader([]byte{AuthOK, 1}), 2); err == nil || err.Error() != ErrBotch {
		t.Error(err)
	}
	if _, err := ReadResp(eof{}, 2); err == nil {
		t.Error("no error at EOF")
	}
}

type eof struct{}

func (eof) Read([]byte) (int, error) { return 0, io.EOF }
