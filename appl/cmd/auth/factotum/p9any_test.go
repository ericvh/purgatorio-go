package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/auth9"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
)

// rpcFile is an open of a factotum's rpc file.
type rpcFile struct {
	f   *factotum
	fid uint32
}

func (r rpcFile) Write(b []byte) (int, error) {
	n, err := r.f.Write([]string{"rpc"}, r.fid, 0, b)
	return int(n), err
}

func (r rpcFile) Read(b []byte) (int, error) {
	d, err := r.f.Read([]string{"rpc"}, r.fid, 0, uint32(len(b)))
	return copy(b, d), err
}

// asKeys is the test authentication server's database.
var asKeys = map[string][]byte{"glenda": auth9.PassToKey("server pw"), "bootes": auth9.PassToKey("client pw")}

func newP9(t *testing.T, ctl string) *factotum {
	f := newF(t, ctl)
	f.dialAS = func(dom string) (io.ReadWriteCloser, error) {
		if dom != "example" {
			return nil, errors.New("no auth server for " + dom)
		}
		c, s := net.Pipe()
		go func() { auth9.ServeTicket(s, asKeys); s.Close() }()
		return c, nil
	}
	return f
}

// p9any runs a client and a server factotum against each other.
func p9any(t *testing.T, cli, srv *factotum, keyspec string) (*authproto.Authinfo, error, *authproto.Authinfo, error) {
	t.Helper()
	c, s := net.Pipe()
	type res struct {
		ai  *authproto.Authinfo
		err error
	}
	done := make(chan res)
	go func() {
		ai, err := authproto.Proxy(s, rpcFile{srv, 1}, "proto=p9any role=server")
		s.Close()
		done <- res{ai, err}
	}()
	cai, cerr := authproto.Proxy(c, rpcFile{cli, 1}, "proto=p9any role=client "+keyspec)
	c.Close()
	r := <-done
	cli.Clunk([]string{"rpc"}, 1)
	srv.Clunk([]string{"rpc"}, 1)
	return cai, cerr, r.ai, r.err
}

func TestP9any(t *testing.T) {
	srv := newP9(t, "key proto=p9sk1 dom=example user=glenda '!password=server pw'\n")
	cli := newP9(t, "key proto=p9sk1 dom=example user=bootes '!password=client pw'\n")
	ctl, _ := cli.Read([]string{"ctl"}, 0, 0, 1<<20)
	if string(ctl) != "key proto=p9sk1 dom=example user=bootes !password?\n" {
		t.Errorf("ctl %q", ctl)
	}
	cai, cerr, sai, serr := p9any(t, cli, srv, "")
	if cerr != nil || serr != nil {
		t.Fatalf("client %v, server %v", cerr, serr)
	}
	if cai.Cuid != "bootes" || cai.Suid != "bootes" || sai.Cuid != "bootes" || len(cai.Secret) != 8 || !bytes.Equal(cai.Secret, sai.Secret) {
		t.Errorf("authinfo %+v %+v", cai, sai)
	}

	// A key given in hex is the same key.
	hex := "key proto=p9sk1 dom=example user=bootes !hex="
	for _, b := range asKeys["bootes"] {
		hex += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&15])
	}
	if _, cerr, _, serr = p9any(t, newP9(t, hex+"\n"), srv, ""); cerr != nil || serr != nil {
		t.Errorf("hex key: client %v, server %v", cerr, serr)
	}

	// Wrong keys, Inferno's error for the client and Plan 9's for the
	// server; the other side sees the conversation end.
	bad := newP9(t, "key proto=p9sk1 dom=example user=bootes '!password=wrong'\n")
	_, cerr, _, serr = p9any(t, bad, srv, "")
	if cerr == nil || cerr.Error() != "proxy rpc write: error p9sk1: getasticket: failed: wrong key?" ||
		serr == nil || serr.Error() != "proxy short read" {
		t.Errorf("wrong client key: client %v, server %v", cerr, serr)
	}
	badsrv := newP9(t, "key proto=p9sk1 dom=example user=glenda '!password=wrong'\n")
	_, cerr, _, serr = p9any(t, cli, badsrv, "")
	if cerr == nil || cerr.Error() != "proxy short read" ||
		serr == nil || serr.Error() != "proxy rpc write: error p9sk1: key mismatch with auth server" {
		t.Errorf("wrong server key: client %v, server %v", cerr, serr)
	}
	// A user the auth server does not know is a wrong key too.
	nobody := newP9(t, "key proto=p9sk1 dom=example user=nobody '!password=client pw'\n")
	if _, cerr, _, _ = p9any(t, nobody, srv, ""); cerr == nil || !strings.HasSuffix(cerr.Error(), "wrong key?") {
		t.Errorf("unknown user: %v", cerr)
	}
	// No key for the domain offered.
	other := newP9(t, "key proto=p9sk1 dom=other user=bootes '!password=client pw'\n")
	if _, cerr, _, _ = p9any(t, other, srv, ""); cerr == nil || cerr.Error() != "proxy rpc write: error can't find key: needkey proto=p9sk1 dom=example" {
		t.Errorf("no key: %v", cerr)
	}
	// A server with no p9sk1 key offers nothing.
	if _, _, _, serr = p9any(t, cli, newF(t, ""), ""); serr == nil || serr.Error() != "proxy rpc: no valid keys" {
		t.Errorf("no server key: %v", serr)
	}
	// No authentication server.
	f := newF(t, "key proto=p9sk1 dom=example user=bootes '!password=client pw'\n")
	f.dialAS = func(string) (io.ReadWriteCloser, error) { return nil, errors.New("connection refused") }
	if _, cerr, _, _ = p9any(t, f, srv, ""); cerr == nil || cerr.Error() != "proxy rpc write: error p9sk1: getasticket failed: connection refused" {
		t.Errorf("no auth server: %v", cerr)
	}
}

func TestP9sk1Keys(t *testing.T) {
	f := &factotum{rpcs: map[uint32]*rpc{}}
	for line, want := range map[string]string{
		"key proto=p9sk1 user=u !password=p":                                     "need user and dom attributes",
		"key proto=p9sk1 dom=d user=u":                                           "need !password or !hex attribute",
		"key proto=p9sk1 dom=d user=u !hex=0011":                                 "malformed !hex key data",
		"key proto=p9sk1 dom=d user=" + strings.Repeat("u", 28) + " !password=p": "user name too long",
	} {
		if err := f.ctl(line); err == nil || err.Error() != want {
			t.Errorf("%s: %v", line, err)
		}
	}
	if err := f.ctl("key proto=p9sk1 dom=d user=u !hex=00112233445566"); err != nil {
		t.Fatal(err)
	}
	ctl, _ := f.Read([]string{"ctl"}, 0, 0, 1<<20)
	if string(ctl) != "key proto=p9sk1 dom=d user=u !hex?\n" {
		t.Errorf("ctl %q", ctl)
	}
	// Plan 9's rpc verbs out of turn.
	f.request(1, []byte("start proto=p9any role=client"))
	if r := f.request(1, []byte("authinfo")); string(r) != "error authentication unfinished" {
		t.Errorf("authinfo: %q", r)
	}
	if r := f.request(1, []byte("attr")); string(r) != "ok proto=p9any role=client" {
		t.Errorf("attr: %q", r)
	}
	if r := f.request(1, []byte("read")); string(r) != "phase protocol phase error" {
		t.Errorf("read: %q", r)
	}
	f.Clunk([]string{"rpc"}, 1)
}
