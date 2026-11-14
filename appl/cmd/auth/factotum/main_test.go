package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/pgauth/modsig"
)

func newF(t *testing.T, ctl string) *factotum {
	f := &factotum{rpcs: map[uint32]*rpc{}}
	if err := f.ctl(ctl); err != nil {
		t.Fatal(err)
	}
	return f
}

func rpcs(f *factotum, fid uint32, reqs ...[]byte) []byte {
	var r []byte
	for _, q := range reqs {
		r = f.request(fid, q)
		if !bytes.HasPrefix(r, []byte("ok")) {
			return r
		}
	}
	return r
}

func TestFactotum(t *testing.T) {
	hk, _ := pgauth.NewPrivateKey(bytes.Repeat([]byte{1}, 64))
	ck, _ := pgauth.NewPrivateKey(bytes.Repeat([]byte{2}, 32))
	srv, _ := pgauth.NewPrivateKey(bytes.Repeat([]byte{3}, 64))
	f := newF(t, "key proto=ed25519 user=alice !seed="+pgauth.Hex(hk.Seed())+"\n"+
		"key proto=ed25519 user=alice !seed="+pgauth.Hex(ck.Seed())+"\n"+
		"key proto=ed25519 role=server !seed="+pgauth.Hex(srv.Seed())+"\n"+
		"key proto=pass server=s user=bob '!password=it''s'\n")
	ctl, _ := f.Read([]string{"ctl"}, 0, 0, 1<<20)
	if strings.Contains(string(ctl), pgauth.Hex(hk.Seed())) || !strings.Contains(string(ctl), "alg=hybrid") ||
		!strings.Contains(string(ctl), "!seed?") {
		t.Fatalf("ctl shows: %s", ctl)
	}

	// A version 4 client proof, from the hybrid key.
	s := pgauth.NewServer(true)
	cl, _ := pgauth.NewClient(s.Offer(), true)
	r := rpcs(f, 1, []byte("start proto=ed25519 role=client alg=hybrid user=alice aname=x"),
		append([]byte("write "), cl.Signed()...), []byte("read"))
	if !bytes.HasPrefix(r, []byte("ok ")) {
		t.Fatalf("v4: %s", r)
	}
	pol := &pgauth.Policy{Keys: []pgauth.KeyID{hk.Public().ID()}}
	if err := pgauth.Check(pol, s.Offer(), "alice", "x", r[3:]); err != nil {
		t.Fatal(err)
	}
	// And a version 3 one from the classical key.
	s3 := pgauth.NewServer(false)
	c3, _ := pgauth.NewClient(s3.Offer(), false)
	r = rpcs(f, 1, []byte("start proto=ed25519 alg=classical"), append([]byte("write "), c3.Signed()...), []byte("read"))
	pol3 := &pgauth.Policy{Keys: []pgauth.KeyID{ck.Public().ID()}, Accept: pgauth.AcceptClassical}
	if err := pgauth.Check(pol3, s3.Offer(), "alice", "", r[3:]); err != nil {
		t.Fatalf("v3: %v (%.40q)", err, r)
	}

	// A server proof comes from the key kept for role=server.
	p4, _ := pgauth.MakeProof(hk, nil, cl.Signed(), "alice", "x")
	keys, _ := cl.Keys("alice", "x", p4)
	r = rpcs(f, 2, []byte("start proto=ed25519 role=server alg=hybrid"), append([]byte("write "), keys.Session()...), []byte("read"))
	spol := &pgauth.Policy{Keys: []pgauth.KeyID{srv.Public().ID()}}
	if err := pgauth.CheckServer(spol, keys, r[3:]); err != nil {
		t.Fatalf("server proof: %v %.40q", err, r)
	}

	// A module signature entry.
	d := [32]byte{9}
	r = rpcs(f, 3, []byte("start proto=ed25519 role=modsign alg=hybrid"), append(append([]byte("write "), d[:]...), "0.2"...), []byte("read"))
	e, ok := modsig.ParseEntry(r[3:])
	if !ok || !e.Key.Equal(hk.Public()) {
		t.Fatalf("modsign: %.40q", r)
	}
	msg, _ := modsig.Message(d, []byte("0.2"), true)
	if modsig.EntryStanding(e, msg, &modsig.Trust{Anchors: []pgauth.KeyID{hk.Public().ID()}}) != modsig.Trusted {
		t.Fatal("modsign entry does not verify")
	}

	// pass, needkey and phase errors.
	if r := rpcs(f, 4, []byte("start proto=pass server=s"), []byte("read")); string(r) != "ok bob 'it''s'" {
		t.Fatalf("pass: %q", r)
	}
	if r := f.request(5, []byte("start proto=ed25519 user=carol")); !bytes.HasPrefix(r, []byte("needkey")) {
		t.Fatalf("needkey: %q", r)
	}
	if r := f.request(6, []byte("read")); string(r) != "error phase error" {
		t.Fatalf("phase: %q", r)
	}
	if err := f.ctl("delkey proto=ed25519 role=server"); err != nil {
		t.Fatal(err)
	}
	if r := rpcs(f, 7, []byte("start proto=ed25519 role=server alg=hybrid")); string(r) != "ok" {
		t.Fatalf("after delkey a key with no role serves: %q", r)
	}
}
