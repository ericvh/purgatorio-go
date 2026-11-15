// Package auth9 is the elements of Plan 9's authentication, as Inferno's
// Auth9 module (appl/lib/auth9.b): the ticket request, ticket and
// authenticator messages, their DES encryption under a user's key, the
// key made from a password, and the client's half of the ticket request
// to an authentication server. Factotum's p9sk1 uses it.
//
// ServeTicket is the other half, the ticket request as Plan 9's authsrv
// answers it, with keys from a map: enough for a test, not an
// authentication server.
package auth9

import (
	"crypto/des"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
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

// PassToKey is the DES key made from a password, as Plan 9's passtokey:
// the password, blank padded, folded seven bits a byte, and each further
// 8 bytes encrypted under the key so far.
func PassToKey(pw string) []byte {
	n := len(pw)
	if n >= ANAMELEN {
		n = ANAMELEN - 1
	}
	buf := make([]byte, ANAMELEN)
	for i := range buf {
		buf[i] = ' '
	}
	copy(buf, pw[:n])
	buf[n] = 0
	key := make([]byte, DESKEYLEN)
	t := 0
	for {
		for i := 0; i < DESKEYLEN; i++ {
			key[i] = byte(int(buf[t+i])>>i + int(buf[t+i+1])<<(8-(i+1)))
		}
		if n <= 8 {
			return key
		}
		n -= 8
		t += 8
		if n < 8 {
			t -= 8 - n
			n = 8
		}
		Encrypt(key, buf[t:t+8])
	}
}

// Des56to64 spreads a 7-byte key over 8 bytes with odd parity, the form
// DES takes and the shared secret factotum gives.
func Des56to64(k []byte) []byte {
	hi := uint32(k[0])<<24 | uint32(k[1])<<16 | uint32(k[2])<<8 | uint32(k[3])
	lo := uint32(k[4])<<24 | uint32(k[5])<<16 | uint32(k[6])<<8
	seven := [8]uint32{hi >> 25, hi >> 18, hi >> 11, hi >> 4, hi<<3 | lo>>29, lo >> 22, lo >> 15, lo >> 8}
	out := make([]byte, 8)
	for i, v := range seven {
		out[i] = oddParity(byte(v&0x7f) << 1)
	}
	return out
}

// oddParity sets the low bit of b so that b has an odd number of ones.
func oddParity(b byte) byte {
	ones := 0
	for x := b; x != 0; x >>= 1 {
		ones += int(x & 1)
	}
	if ones%2 == 0 {
		b |= 1
	}
	return b
}

// Encrypt encrypts data in place under a 7-byte key as Plan 9's encrypt:
// DES in ECB on 8-byte blocks 7 bytes apart, the last one ending at the
// end of data. Data shorter than 8 bytes is left alone.
func Encrypt(key, data []byte) { crypt(key, data, true) }

// Decrypt undoes Encrypt.
func Decrypt(key, data []byte) { crypt(key, data, false) }

func crypt(key, data []byte, enc bool) {
	if len(data) < 8 {
		return
	}
	c, err := des.NewCipher(Des56to64(key))
	if err != nil {
		panic(err)
	}
	n := len(data) - 1
	r, nb := n%7, n/7
	var offs []int
	for i := 0; i < nb; i++ {
		offs = append(offs, i*7)
	}
	if r != 0 {
		offs = append(offs, nb*7-7+r)
	}
	if enc {
		for _, o := range offs {
			c.Encrypt(data[o:o+8], data[o:o+8])
		}
		return
	}
	for i := len(offs) - 1; i >= 0; i-- {
		o := offs[i]
		c.Decrypt(data[o:o+8], data[o:o+8])
	}
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

// ServeTicket answers ticket requests on rw until it closes, as Plan 9's
// authsrv answers AuthTreq: the client's ticket is sealed with the key
// of the requesting host (HostID), the server's with the key of the
// server (AuthID). A user with no key gets a made-up one, so a caller
// learns nothing from the reply, and a host speaks only for itself.
// keys maps a user to its DES key (PassToKey).
func ServeTicket(rw io.ReadWriter, keys map[string][]byte) error {
	buf := make([]byte, TICKREQLEN)
	madeup := func() []byte {
		k := make([]byte, DESKEYLEN)
		Random(k)
		return k
	}
	key := func(u string) []byte {
		if k, ok := keys[u]; ok {
			return k
		}
		return madeup()
	}
	for {
		if _, err := io.ReadFull(rw, buf); err != nil {
			return nil
		}
		tr, _ := UnpackTicketreq(buf)
		if tr.Type != AuthTreq {
			e := make([]byte, 1+AERRLEN)
			e[0] = AuthErr
			puts(e[1:], "unsupported request")
			rw.Write(e)
			return errors.New("unsupported request type " + strconv.Itoa(int(tr.Type)))
		}
		akey, hkey := key(tr.AuthID), key(tr.HostID)
		t := &Ticket{Chal: tr.Chal, CUID: tr.UID}
		if tr.HostID == tr.UID {
			t.SUID = tr.UID
		} else {
			akey, hkey = madeup(), madeup()
		}
		Random(t.Key[:])
		out := []byte{AuthOK}
		t.Num = AuthTc
		out = append(out, t.Pack(hkey)...)
		t.Num = AuthTs
		out = append(out, t.Pack(akey)...)
		if _, err := rw.Write(out); err != nil {
			return err
		}
	}
}
