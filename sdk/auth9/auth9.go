// Package auth9 is the elements of Plan 9's authentication, as Inferno's
// Auth9 module (appl/lib/auth9.b): the ticket request, ticket and
// authenticator messages, their DES encryption under a user's key, the
// key made from a password, and the client's half of the ticket request
// to an authentication server. Factotum's p9sk1 uses it.
//
// ServeTicket is the other half, the ticket request as authsrv(6)
// describes the answer, with keys from a map: enough for a test, not an
// authentication server.
package auth9

import (
	"bytes"
	"crypto/cipher"
	"crypto/des"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"math/bits"
	"strconv"
)

// Sizes, as Plan 9's authsrv.h.
const (
	ANAMELEN  = 28 // a name in the ticket protocol, with its NUL
	AERRLEN   = 64 // an error from the authentication server
	DOMLEN    = 48 // an authentication domain
	DESKEYLEN = 7  // a DES key as the protocol carries it
	CHALLEN   = 8  // a p9sk1 challenge

	TICKREQLEN = 3*ANAMELEN + CHALLEN + DOMLEN + 1
	TICKETLEN  = CHALLEN + 2*ANAMELEN + DESKEYLEN + 1
	AUTHENTLEN = CHALLEN + 4 + 1
)

// Message types (anti-replay numbers).
const (
	AuthTreq  = 1 // ticket request
	AuthOK    = 4 // fixed length reply follows
	AuthErr   = 5 // error follows
	AuthOKvar = 9 // variable length reply follows

	AuthTs = 64 // ticket encrypted with the server's key
	AuthTc = 65 // ticket encrypted with the client's key
	AuthAs = 66 // server generated authenticator
	AuthAc = 67 // client generated authenticator
)

// Ticketreq is a ticket request.
type Ticketreq struct {
	Type    byte
	AuthID  string // the server's encryption id
	AuthDom string // the server's authentication domain
	Chal    [CHALLEN]byte
	HostID  string // the client host's encryption id
	UID     string // the user the client host asks for
}

// Ticket is a ticket: the server's challenge, the users on each side and
// the key they now share.
type Ticket struct {
	Num  byte
	Chal [CHALLEN]byte
	CUID string
	SUID string
	Key  [DESKEYLEN]byte
}

// Authenticator proves possession of a ticket's key.
type Authenticator struct {
	Num  byte
	Chal [CHALLEN]byte
	ID   uint32
}

func puts(b []byte, s string) {
	if len(s) > len(b)-1 {
		s = s[:len(b)-1]
	}
	n := copy(b, s)
	clear(b[n:])
}

func gets(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// Pack is the request on the wire.
func (t *Ticketreq) Pack() []byte {
	b := make([]byte, TICKREQLEN)
	b[0] = t.Type
	p := b[1:]
	puts(p[:ANAMELEN], t.AuthID)
	p = p[ANAMELEN:]
	puts(p[:DOMLEN], t.AuthDom)
	p = p[DOMLEN:]
	copy(p, t.Chal[:])
	p = p[CHALLEN:]
	puts(p[:ANAMELEN], t.HostID)
	puts(p[ANAMELEN:2*ANAMELEN], t.UID)
	return b
}

// UnpackTicketreq reads a request of TICKREQLEN bytes.
func UnpackTicketreq(b []byte) (*Ticketreq, error) {
	if len(b) < TICKREQLEN {
		return nil, errors.New("short ticket request")
	}
	t := &Ticketreq{Type: b[0]}
	p := b[1:]
	t.AuthID = gets(p[:ANAMELEN])
	p = p[ANAMELEN:]
	t.AuthDom = gets(p[:DOMLEN])
	p = p[DOMLEN:]
	copy(t.Chal[:], p)
	p = p[CHALLEN:]
	t.HostID = gets(p[:ANAMELEN])
	t.UID = gets(p[ANAMELEN : 2*ANAMELEN])
	return t, nil
}

// Pack is the ticket encrypted with key.
func (t *Ticket) Pack(key []byte) []byte {
	b := make([]byte, TICKETLEN)
	b[0] = t.Num
	copy(b[1:], t.Chal[:])
	puts(b[1+CHALLEN:1+CHALLEN+ANAMELEN], t.CUID)
	puts(b[1+CHALLEN+ANAMELEN:1+CHALLEN+2*ANAMELEN], t.SUID)
	copy(b[1+CHALLEN+2*ANAMELEN:], t.Key[:])
	Encrypt(key, b)
	return b
}

// UnpackTicket decrypts a ticket of TICKETLEN bytes with key. A wrong
// key gives a ticket whose Num and Chal are garbage.
func UnpackTicket(b, key []byte) *Ticket {
	b = append([]byte(nil), b[:TICKETLEN]...)
	Decrypt(key, b)
	t := &Ticket{Num: b[0]}
	copy(t.Chal[:], b[1:])
	t.CUID = gets(b[1+CHALLEN : 1+CHALLEN+ANAMELEN])
	t.SUID = gets(b[1+CHALLEN+ANAMELEN : 1+CHALLEN+2*ANAMELEN])
	copy(t.Key[:], b[1+CHALLEN+2*ANAMELEN:])
	return t
}

// Pack is the authenticator encrypted with key.
func (a *Authenticator) Pack(key []byte) []byte {
	b := make([]byte, AUTHENTLEN)
	b[0] = a.Num
	copy(b[1:], a.Chal[:])
	binary.LittleEndian.PutUint32(b[1+CHALLEN:], a.ID)
	Encrypt(key, b)
	return b
}

// UnpackAuthenticator decrypts an authenticator of AUTHENTLEN bytes.
func UnpackAuthenticator(b, key []byte) *Authenticator {
	b = append([]byte(nil), b[:AUTHENTLEN]...)
	Decrypt(key, b)
	a := &Authenticator{Num: b[0]}
	copy(a.Chal[:], b[1:])
	a.ID = binary.LittleEndian.Uint32(b[1+CHALLEN:])
	return a
}

// PassToKey is the DES key Plan 9 derives from a password. The password
// (at most ANAMELEN-1 bytes) is followed by a NUL and blanks. A window
// of 8 of those bytes gives a 7-byte key by dropping the top bit of each
// (pack7). While the window has not reached the password's end, it moves
// on by 8, or less so as to end there, its bytes are encrypted under the
// key so far, and the key is taken from it again.
func PassToKey(pw string) []byte {
	if len(pw) >= ANAMELEN {
		pw = pw[:ANAMELEN-1]
	}
	padded := bytes.Repeat([]byte{' '}, ANAMELEN)
	copy(padded, pw)
	padded[len(pw)] = 0
	end := len(pw)
	at := 0
	key := pack7(padded[0:8])
	for at+8 < end {
		at = min(at+8, end-8)
		Encrypt(key, padded[at:at+8])
		key = pack7(padded[at : at+8])
	}
	return key
}

// pack7 folds 8 bytes into 7: byte i of the key is byte i of w shifted
// down by i plus byte i+1 shifted up by 7-i, modulo 256. For ASCII that
// packs the 7 low bits of each byte, the first byte's lowest.
func pack7(w []byte) []byte {
	k := make([]byte, DESKEYLEN)
	for i := range k {
		k[i] = w[i]>>uint(i) + w[i+1]<<uint(7-i)
	}
	return k
}

// Des56to64 spreads a 7-byte key over 8 bytes with odd parity, the form
// DES takes and the shared secret factotum gives: the 56 bits, high bit
// first, 7 to a byte in its top bits.
func Des56to64(k []byte) []byte {
	var v uint64
	for _, b := range k[:DESKEYLEN] {
		v = v<<8 | uint64(b)
	}
	out := make([]byte, 8)
	for i := range out {
		b := byte(v>>(49-7*i)&0x7f) << 1
		if bits.OnesCount8(b)%2 == 0 {
			b |= 1
		}
		out[i] = b
	}
	return out
}

// Encrypt encrypts data in place under a 7-byte key as Plan 9 does:
// DES in ECB on 8-byte blocks that start every 7 bytes, and if they do
// not reach the end, one more block ending there. Data shorter than 8
// bytes is left alone.
func Encrypt(key, data []byte) {
	c := cipherFor(key)
	for _, o := range blocks(len(data)) {
		c.Encrypt(data[o:o+8], data[o:o+8])
	}
}

// Decrypt undoes Encrypt, block by block in reverse.
func Decrypt(key, data []byte) {
	c := cipherFor(key)
	b := blocks(len(data))
	for i := len(b) - 1; i >= 0; i-- {
		c.Decrypt(data[b[i]:b[i]+8], data[b[i]:b[i]+8])
	}
}

func cipherFor(key []byte) cipher.Block {
	c, err := des.NewCipher(Des56to64(key))
	if err != nil {
		panic(err)
	}
	return c
}

// blocks is where Encrypt's blocks start in n bytes.
func blocks(n int) []int {
	var at []int
	for o := 0; o+8 <= n; o += 7 {
		at = append(at, o)
	}
	if len(at) > 0 && at[len(at)-1]+8 < n {
		at = append(at, n-8)
	}
	return at
}

// ErrBotch is the error for a reply the protocol does not allow.
const ErrBotch = "AS protocol botch"

// GetTicket sends tr to the authentication server on rw and returns the
// client's ticket, decrypted with key, and the server's, still sealed.
func GetTicket(rw io.ReadWriter, tr *Ticketreq, key []byte) (*Ticket, []byte, error) {
	if _, err := rw.Write(tr.Pack()); err != nil {
		return nil, nil, errors.New(ErrBotch)
	}
	b, err := ReadResp(rw, 2*TICKETLEN)
	if err != nil {
		return nil, nil, err
	}
	return UnpackTicket(b, key), b[TICKETLEN:], nil
}

// ReadResp reads the authentication server's reply of n bytes, or of the
// length it gives, or its error.
func ReadResp(r io.Reader, n int) ([]byte, error) {
	var t [1]byte
	if _, err := io.ReadFull(r, t[:]); err != nil {
		return nil, errors.New(ErrBotch)
	}
	switch t[0] {
	case AuthOK:
	case AuthOKvar:
		var l [5]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return nil, errors.New(ErrBotch)
		}
		v, err := strconv.Atoi(string(l[:]))
		if err != nil || v <= 0 || v > 4096 {
			return nil, errors.New(ErrBotch)
		}
		n = v
	case AuthErr:
		e := make([]byte, AERRLEN)
		if _, err := io.ReadFull(r, e); err != nil {
			return nil, errors.New(ErrBotch)
		}
		return nil, errors.New("remote: " + gets(e))
	default:
		return nil, errors.New(ErrBotch + ": resp " + strconv.Itoa(int(t[0])))
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, errors.New(ErrBotch)
	}
	return b, nil
}

// Random fills b with random bytes.
func Random(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
}
