package main

// p9any and p9sk1 (Plan 9's authsrv(6)), which Inferno's factotum runs
// as one protocol, p9any. Inferno's is a client only; the server role
// here is written from the protocol, with the error strings Plan 9's
// factotum gives.
//
// p9any picks the protocol: the server offers "v.2" and a p9sk1@domain
// word for each domain it holds a key for, NUL-terminated; the client
// answers "p9sk1 domain" and a NUL; the server says "OK" and a NUL.
//
// p9sk1 then runs in three round trips, plus one to the authentication
// server. The client sends a nonce. The server sends a ticket request:
// its user and domain and a nonce of its own. The client completes the
// request with its user and gets from the authentication server two
// tickets holding a fresh key, one sealed with the client's key and one
// with the server's. It sends the server's ticket and an authenticator
// (the server's nonce under the fresh key); the server opens both and
// answers with an authenticator of the client's nonce.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/auth9"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// attrtext is the public attributes of a, as factotum shows them.
func attrtext(a []authproto.Attr) string {
	var s []string
	for _, x := range a {
		if !strings.HasPrefix(x.Name, "!") {
			s = append(s, x.Name+"="+authproto.Quote(x.Value))
		}
	}
	return strings.Join(s, " ")
}

// without is a with the named attributes left out.
func without(a []authproto.Attr, names ...string) []authproto.Attr {
	var out []authproto.Attr
next:
	for _, x := range a {
		for _, n := range names {
			if x.Name == n {
				continue next
			}
		}
		out = append(out, x)
	}
	return out
}

// findkey is the first key that matches want, or an error naming what
// is needed.
func (f *factotum) findkey(want []authproto.Attr) (*key, string) {
	for _, k := range f.keys {
		if k.matches(want) {
			return k, ""
		}
	}
	return nil, "needkey " + attrtext(want)
}

// p9sk1Key is the p9sk1 key for domain that also matches extra.
func (f *factotum) p9sk1Key(domain string, extra []authproto.Attr) (*key, string) {
	want := []authproto.Attr{{Name: "proto", Value: "p9sk1"}, {Name: "dom", Value: domain}}
	return f.findkey(append(want, without(extra, "proto", "role", "dom")...))
}

func (f *factotum) p9any(c *conv) string {
	if role, _ := authproto.Get(c.attrs, "role"); role == "server" {
		return f.p9anyServe(c)
	}
	return f.p9anyDial(c)
}

// untilNUL reads from the peer until a message ends in a NUL, and
// returns it without the NUL.
func untilNUL(c *conv) (string, bool) {
	for {
		msg := c.read()
		if i := bytes.IndexByte(msg, 0); i >= 0 {
			return string(msg[:i]), true
		}
		if len(msg) >= authproto.AuthRpcMax {
			return "", false
		}
		c.toosmall(len(msg) + 128)
	}
}

// p9anyDial is the client: as Inferno's, it takes the last p9sk1 domain
// offered, and knows only version 2 and the version-less form.
func (f *factotum) p9anyDial(c *conv) string {
	offer, ok := untilNUL(c)
	if !ok {
		return "p9any: unsupported protocol"
	}
	words := strings.Fields(offer)
	v2 := false
	if len(words) > 0 && strings.HasPrefix(words[0], "v.") {
		if words[0] != "v.2" {
			return "p9any: unknown version"
		}
		v2, words = true, words[1:]
	}
	domain := ""
	for _, w := range words {
		name, d, found := strings.Cut(w, "@")
		if found && name == "p9sk1" && d != "" && !strings.Contains(d, "@") {
			domain = d
		}
	}
	if domain == "" {
		return "p9any: unsupported protocol"
	}
	c.write([]byte("p9sk1 " + domain + "\x00"))
	if v2 && string(c.readn(3)[:3]) != "OK\x00" {
		return "p9any: AS protocol botch: not OK"
	}
	return f.p9sk1Dial(c, domain)
}

func (f *factotum) p9sk1Dial(c *conv, domain string) string {
	var myNonce [auth9.CHALLEN]byte
	auth9.Random(myNonce[:])
	c.write(myNonce[:])

	req, err := auth9.UnpackTicketreq(c.readn(auth9.TICKREQLEN))
	if err != nil {
		return "p9sk1: can't unpack ticket request"
	}
	k, need := f.p9sk1Key(domain, c.attrs)
	if k == nil {
		return "can't find key: " + need
	}
	// The client asks for tickets for its own user only.
	req.Type, req.HostID, req.UID = auth9.AuthTreq, k.user, k.user
	mine, theirs, err := f.tickets(req, k.des)
	if err != nil {
		return "p9sk1: getasticket failed: " + err.Error()
	}
	// Inferno looks only at the type; the server's nonce must be there
	// too, which makes a wrong key all but certain to show here.
	if mine.Num != auth9.AuthTc || mine.Chal != req.Chal {
		return "p9sk1: getasticket: failed: wrong key?"
	}
	proof := (&auth9.Authenticator{Num: auth9.AuthAc, Chal: req.Chal}).Pack(mine.Key[:])
	c.write(append(append([]byte(nil), theirs...), proof...))

	reply := auth9.UnpackAuthenticator(c.readn(auth9.AUTHENTLEN), mine.Key[:])
	if reply.Num != auth9.AuthAs || reply.Chal != myNonce || reply.ID != 0 {
		return "invalid authenticator from server"
	}
	c.finished(&authinfo{cuid: mine.CUID, suid: mine.SUID, secret: auth9.Des56to64(mine.Key[:])})
	return ""
}

// tickets asks the authentication server for req's tickets: the
// client's, opened with key, and the server's, sealed.
func (f *factotum) tickets(req *auth9.Ticketreq, key []byte) (*auth9.Ticket, []byte, error) {
	connect := f.dialAS
	if connect == nil {
		connect = f.authdial
	}
	as, err := connect(req.AuthDom)
	if err != nil {
		return nil, nil, err
	}
	defer as.Close()
	return auth9.GetTicket(as, req, key)
}

// authdial dials the authentication server for dom: the one -a named,
// else what /net/cs gives as auth for the authentication domain or the
// IP domain, else $auth, at the ticket service.
func (f *factotum) authdial(dom string) (io.ReadWriteCloser, error) {
	addr := f.authaddr
	if addr == "" && dom != "" {
		if addr = csValue("authdom", dom, "auth"); addr == "" {
			addr = csValue("dom", dom, "auth")
		}
	}
	if addr == "" {
		addr = "$auth"
	}
	conn, err := dial.Dial(dial.Netmkaddr(addr, "net", "ticket"), "")
	if err != nil {
		return nil, err
	}
	conn.Cfd.Close()
	return conn.Dfd, nil
}

// csValue asks /net/cs for attribute want of the entry with key=val.
func csValue(key, val, want string) string {
	fd, err := sys.Open("/net/cs", sys.ORDWR)
	if err != nil {
		return ""
	}
	cs := os.NewFile(uintptr(fd), "cs")
	defer cs.Close()
	if _, err := cs.Write([]byte("!" + key + "=" + val + " " + want + "=*")); err != nil {
		return ""
	}
	cs.Seek(0, 0)
	line := make([]byte, 1024)
	for {
		n, _ := cs.Read(line)
		if n <= 0 {
			return ""
		}
		a, _ := authproto.ParseAttrs(string(line[:n]))
		if v, ok := authproto.Get(a, want); ok {
			return v
		}
	}
}

// p9anyServe is the server: it offers every domain it holds a p9sk1
// key for, among the keys the start's attributes select.
func (f *factotum) p9anyServe(c *conv) string {
	sel := without(c.attrs, "proto")
	var domains []string
	for _, k := range f.keys {
		if k.proto == "p9sk1" && k.matches(sel) {
			domains = append(domains, "p9sk1@"+k.dom)
		}
	}
	if len(domains) == 0 {
		return "no valid keys"
	}
	c.write([]byte("v.2 " + strings.Join(domains, " ") + "\x00"))

	choice, ok := untilNUL(c)
	words, qok := authproto.Tokenize(choice)
	if !ok || !qok || len(words) != 2 {
		return "bad protocol message"
	}
	if words[0] != "p9sk1" {
		return "bad chosen protocol " + authproto.Quote(words[0])
	}
	c.write([]byte("OK\x00"))
	k, need := f.p9sk1Key(words[1], sel)
	if k == nil {
		return "p9sk1: can't find key: " + need
	}
	if e := p9sk1Serve(c, k); e != "" {
		return "p9sk1: " + e
	}
	return ""
}

// p9sk1Serve runs the server's side of p9sk1 with key k.
func p9sk1Serve(c *conv, k *key) string {
	var theirNonce [auth9.CHALLEN]byte
	copy(theirNonce[:], c.readn(auth9.CHALLEN))

	req := &auth9.Ticketreq{Type: auth9.AuthTreq, AuthID: k.user, AuthDom: k.dom}
	auth9.Random(req.Chal[:])
	c.write(req.Pack())

	tk, e := openClaim(c.readn(auth9.TICKETLEN+auth9.AUTHENTLEN), k.des, req.Chal)
	if e != "" {
		return e
	}
	c.write((&auth9.Authenticator{Num: auth9.AuthAs, Chal: theirNonce}).Pack(tk.Key[:]))
	c.finished(&authinfo{cuid: tk.CUID, suid: tk.SUID, secret: auth9.Des56to64(tk.Key[:])})
	return ""
}

// openClaim opens the ticket and authenticator a client sends the
// server, checking both carry the server's nonce.
func openClaim(msg, des []byte, nonce [auth9.CHALLEN]byte) (*auth9.Ticket, string) {
	tk := auth9.UnpackTicket(msg[:auth9.TICKETLEN], des)
	if tk.Num != auth9.AuthTs || tk.Chal != nonce {
		return nil, "key mismatch with auth server"
	}
	a := auth9.UnpackAuthenticator(msg[auth9.TICKETLEN:], tk.Key[:])
	if a.Num != auth9.AuthAc || a.Chal != nonce || a.ID != 0 {
		return nil, "client lies through his teeth"
	}
	return tk, ""
}

// parseP9sk1 makes a p9sk1 key: user and dom, and the DES key from
// !password or !hex (14 hex digits).
func parseP9sk1(a []authproto.Attr) (*key, error) {
	k := &key{proto: "p9sk1"}
	var ok1, ok2 bool
	k.user, ok1 = authproto.Get(a, "user")
	k.dom, ok2 = authproto.Get(a, "dom")
	switch {
	case !ok1 || !ok2:
		return nil, errors.New("need user and dom attributes")
	case len(k.user) >= auth9.ANAMELEN:
		return nil, errors.New("user name too long")
	case len(k.dom) >= auth9.DOMLEN:
		return nil, errors.New("auth dom name too long")
	}
	if h, ok := authproto.Get(a, "!hex"); ok {
		b, ok := pgauth.HexDecode(h)
		if !ok || len(b) != auth9.DESKEYLEN {
			return nil, errors.New("malformed !hex key data")
		}
		k.des, k.hex = b, true
		return k, nil
	}
	pw, ok := authproto.Get(a, "!password")
	if !ok {
		return nil, errors.New("need !password or !hex attribute")
	}
	k.des = auth9.PassToKey(pw)
	return k, nil
}
