package main

// Plan 9's p9any and p9sk1, which Inferno's factotum keeps in one module
// (appl/cmd/auth/factotum/proto/p9any.b, "currently includes p9sk1").
// Inferno's p9any is only a client; the server role here is Plan 9's,
// with its error strings.
//
//	p9any:
//	S->C:	v.2 p9sk1@dom ... NUL
//	C->S:	p9sk1 dom NUL
//	S->C:	OK NUL
//
//	p9sk1:
//	C->S:	nonce-C
//	S->C:	nonce-S, uid-S, domain-S
//	C->A:	nonce-S, uid-S, domain-S, uid-C, factotum-C
//	A->C:	Kc{nonce-S, uid-C, uid-S, Kn}, Ks{nonce-S, uid-C, uid-S, Kn}
//	C->S:	Ks{nonce-S, uid-C, uid-S, Kn}, Kn{nonce-S, counter}
//	S->C:	Kn{nonce-C, counter}

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

func (f *factotum) p9any(c *conv) string {
	if role, _ := authproto.Get(c.attrs, "role"); role == "server" {
		return f.p9anyserver(c)
	}
	return f.p9anyclient(c)
}

func (f *factotum) p9anyclient(c *conv) string {
	var buf []byte
	for {
		buf = c.read()
		if n := len(buf); n > 0 && buf[n-1] == 0 {
			break
		}
		c.toosmall(2048)
	}
	flds := strings.Fields(string(buf[:len(buf)-1]))
	version := 1
	if len(flds) > 0 && strings.HasPrefix(flds[0], "v.") {
		if flds[0] != "v.2" {
			return "p9any: unknown version"
		}
		version = 2
		flds = flds[1:]
	}
	var dom string
	for _, fl := range flds {
		if p, d, ok := strings.Cut(fl, "@"); ok && p == "p9sk1" && d != "" && !strings.Contains(d, "@") {
			dom = d // the last offered, as Inferno's
		}
	}
	if dom == "" {
		return "p9any: unsupported protocol"
	}
	c.write(append([]byte("p9sk1 "+dom), 0))
	if version == 2 {
		b := c.readn(3)
		if string(b[:3]) != "OK\x00" {
			return "p9any: AS protocol botch: not OK"
		}
	}
	return f.p9sk1client(c, dom)
}

func (f *factotum) p9sk1client(c *conv, dom string) string {
	var cchal [auth9.CHALLEN]byte
	auth9.Random(cchal[:])
	c.write(cchal[:])

	tr, err := auth9.UnpackTicketreq(c.readn(auth9.TICKREQLEN))
	if err != nil {
		return "p9sk1: can't unpack ticket request"
	}
	want := append([]authproto.Attr{{Name: "proto", Value: "p9sk1"}, {Name: "dom", Value: dom}},
		without(c.attrs, "proto", "role", "dom")...)
	k, diag := f.findkey(want)
	if k == nil {
		return "can't find key: " + diag
	}
	tr.Type = auth9.AuthTreq
	tr.HostID = k.user
	tr.UID = tr.HostID // not speaking for anyone else
	tick, serverbits, err := f.getastickets(tr, k.des)
	if err != nil {
		return "p9sk1: getasticket failed: " + err.Error()
	}
	// Inferno checks only the number; the challenge too makes a wrong
	// key all but certain to show here.
	if tick.Num != auth9.AuthTc || tick.Chal != tr.Chal {
		return "p9sk1: getasticket: failed: wrong key?"
	}
	ar := &auth9.Authenticator{Num: auth9.AuthAc, Chal: tick.Chal}
	c.write(append(append([]byte(nil), serverbits...), ar.Pack(tick.Key[:])...))

	ar = auth9.UnpackAuthenticator(c.readn(auth9.AUTHENTLEN), tick.Key[:])
	if ar.Num != auth9.AuthAs || ar.Chal != cchal || ar.ID != 0 {
		return "invalid authenticator from server"
	}
	c.finished(&authinfo{cuid: tick.CUID, suid: tick.SUID, secret: auth9.Des56to64(tick.Key[:])})
	return ""
}

func (f *factotum) getastickets(tr *auth9.Ticketreq, key []byte) (*auth9.Ticket, []byte, error) {
	dialer := f.dialAS
	if dialer == nil {
		dialer = f.authdial
	}
	afd, err := dialer(tr.AuthDom)
	if err != nil {
		return nil, nil, err
	}
	defer afd.Close()
	return auth9.GetTicket(afd, tr, key)
}

// authdial dials the authentication server for dom: the one -a named,
// else what /net/cs gives as auth for the authentication domain or the
// IP domain, else $auth, at the ticket service.
func (f *factotum) authdial(dom string) (io.ReadWriteCloser, error) {
	p := f.authaddr
	if p == "" && dom != "" {
		p = csgetvalue("authdom", dom, "auth")
		if p == "" {
			p = csgetvalue("dom", dom, "auth")
		}
	}
	if p == "" {
		p = "$auth"
	}
	conn, err := dial.Dial(dial.Netmkaddr(p, "net", "ticket"), "")
	if err != nil {
		return nil, err
	}
	conn.Cfd.Close()
	return conn.Dfd, nil
}

// csgetvalue asks /net/cs for the value of needtag in the entry where
// keytag is keyval.
func csgetvalue(keytag, keyval, needtag string) string {
	fd, err := sys.Open("/net/cs", sys.ORDWR)
	if err != nil {
		return ""
	}
	cs := os.NewFile(uintptr(fd), "cs")
	defer cs.Close()
	if _, err := cs.Write([]byte("!" + keytag + "=" + keyval + " " + needtag + "=*")); err != nil {
		return ""
	}
	cs.Seek(0, 0)
	buf := make([]byte, 1024)
	for {
		n, _ := cs.Read(buf)
		if n <= 0 {
			return ""
		}
		a, _ := authproto.ParseAttrs(string(buf[:n]))
		if v, ok := authproto.Get(a, needtag); ok {
			return v
		}
	}
}

func (f *factotum) p9anyserver(c *conv) string {
	want := without(c.attrs, "proto")
	offer := "v.2"
	n := 0
	for _, k := range f.keys {
		if k.proto == "p9sk1" && k.matches(want) {
			offer += " p9sk1@" + k.dom
			n++
		}
	}
	if n == 0 {
		return "no valid keys"
	}
	c.write(append([]byte(offer), 0))

	var s string
	for {
		b := c.read()
		if i := bytes.IndexByte(b, 0); i >= 0 {
			s = string(b[:i])
			break
		}
		if len(b) >= 4096 {
			return "bad protocol message"
		}
		c.toosmall(len(b) + 128)
	}
	tok, ok := authproto.Tokenize(s)
	if !ok || len(tok) != 2 {
		return "bad protocol message"
	}
	if tok[0] != "p9sk1" {
		return "bad chosen protocol " + authproto.Quote(tok[0])
	}
	c.write([]byte("OK\x00"))
	if err := f.p9sk1server(c, append(without(want, "dom"), authproto.Attr{Name: "dom", Value: tok[1]})); err != "" {
		return tok[0] + ": " + err
	}
	return ""
}

func (f *factotum) p9sk1server(c *conv, attrs []authproto.Attr) string {
	k, diag := f.findkey(append([]authproto.Attr{{Name: "proto", Value: "p9sk1"}}, attrs...))
	if k == nil {
		return "can't find key: " + diag
	}
	var cchal [auth9.CHALLEN]byte
	copy(cchal[:], c.readn(auth9.CHALLEN))

	tr := &auth9.Ticketreq{Type: auth9.AuthTreq, AuthID: k.user, AuthDom: k.dom}
	auth9.Random(tr.Chal[:])
	c.write(tr.Pack())

	buf := c.readn(auth9.TICKETLEN + auth9.AUTHENTLEN)
	t := auth9.UnpackTicket(buf, k.des)
	if t.Num != auth9.AuthTs || t.Chal != tr.Chal {
		return "key mismatch with auth server"
	}
	au := auth9.UnpackAuthenticator(buf[auth9.TICKETLEN:], t.Key[:])
	if au.Num != auth9.AuthAc || au.Chal != tr.Chal || au.ID != 0 {
		return "client lies through his teeth"
	}
	au = &auth9.Authenticator{Num: auth9.AuthAs, Chal: cchal}
	c.write(au.Pack(t.Key[:]))
	c.finished(&authinfo{cuid: t.CUID, suid: t.SUID, secret: auth9.Des56to64(t.Key[:])})
	return ""
}

// parseP9sk1 makes a p9sk1 key: user and dom, and the DES key from
// !password or !hex (14 hex digits), as Plan 9's p9sk1 checks it.
func parseP9sk1(a []authproto.Attr) (*key, error) {
	k := &key{proto: "p9sk1"}
	var ok1, ok2 bool
	k.user, ok1 = authproto.Get(a, "user")
	k.dom, ok2 = authproto.Get(a, "dom")
	if !ok1 || !ok2 {
		return nil, errors.New("need user and dom attributes")
	}
	if len(k.user) >= auth9.ANAMELEN {
		return nil, errors.New("user name too long")
	}
	if len(k.dom) >= auth9.DOMLEN {
		return nil, errors.New("auth dom name too long")
	}
	if h, ok := authproto.Get(a, "!hex"); ok {
		b, ok := pgauth.HexDecode(h)
		if !ok || len(b) != auth9.DESKEYLEN {
			return nil, errors.New("malformed !hex key data")
		}
		k.des, k.hex = b, true
	} else if pw, ok := authproto.Get(a, "!password"); ok {
		k.des = auth9.PassToKey(pw)
	} else {
		return nil, errors.New("need !password or !hex attribute")
	}
	return k, nil
}
