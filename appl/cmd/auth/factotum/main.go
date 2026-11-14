// Command auth/factotum is the authentication agent, with Plan 9's ctl
// and rpc files at mntpt (default /mnt/factotum), as the Rust sibling's
// (docs/auth.md, "Factotum"). Each authinfo named is loaded as an
// ed25519 key; -s loads one as the key for server proofs (role=server).
//
// ctl takes lines of attributes; a name starting with ! is secret and is
// never read back:
//
//	key proto=ed25519 [user=<name>] [role=<role>] !seed=<hex> [cert='<certificate>']
//	key proto=pass server=<addr> user=<name> !password=<password>
//	delkey proto=<proto> [attr=value ...]
//
// A seed of 32 bytes is an Ed25519 key, of 64 a hybrid one; ctl shows
// which as alg=classical or alg=hybrid, and a start may name alg to pick
// one. rpc is a conversation per open: each write is a request (start
// <attrs>, write <data>, read) and the next read is its reply (ok
// [data], needkey <attrs>, error <message>). Factotum builds every
// message it signs itself, so it signs nothing but mount proofs
// (role=client, the default: write the 32, 96 or 2368 bytes read from
// an auth fd, read the proof), server proofs (role=server: write the
// session's transcript and bound value, read the proof) and module
// signatures (role=modsign: write the digest and the ABI version, read
// a section entry).
//
//	auth/factotum [-m mntpt] [-s authinfo] [authinfo ...]
package main

import (
	"errors"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/pgauth/modsig"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/synth"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var roles = []string{"client", "server", "modsign"}

type key struct {
	proto    string // ed25519 or pass
	user     string
	role     string // "" serves every role
	k        *pgauth.PrivateKey
	cert     *pgauth.Cert
	server   string
	password string
}

func (k *key) alg() string {
	if k.k.Hybrid() {
		return "hybrid"
	}
	return "classical"
}

func (k *key) public() []authproto.Attr {
	a := []authproto.Attr{{Name: "proto", Value: k.proto}}
	if k.proto == "pass" {
		return append(a, authproto.Attr{Name: "server", Value: k.server}, authproto.Attr{Name: "user", Value: k.user})
	}
	a = append(a, authproto.Attr{Name: "alg", Value: k.alg()})
	if k.user != "" {
		a = append(a, authproto.Attr{Name: "user", Value: k.user})
	}
	if k.role != "" {
		a = append(a, authproto.Attr{Name: "role", Value: k.role})
	}
	if k.cert != nil {
		a = append(a, authproto.Attr{Name: "cert", Value: k.cert.Encode()})
	}
	return a
}

func (k *key) line() string {
	s := "key"
	for _, a := range k.public() {
		s += " " + a.Name + "=" + authproto.Quote(a.Value)
	}
	if k.proto == "pass" {
		return s + " !password?\n"
	}
	return s + " !seed?\n"
}

// matches reports whether every attribute in want (but aname and
// secrets) matches the key. A key with no role matches any role, and one
// with no user any user.
func (k *key) matches(want []authproto.Attr) bool {
	have := k.public()
	for _, w := range want {
		switch {
		case w.Name == "role" && (k.role == "" || k.role == w.Value):
		case w.Name == "aname" || strings.HasPrefix(w.Name, "!"):
		case w.Name == "user" && k.proto == "ed25519" && k.user == "":
		default:
			if v, ok := authproto.Get(have, w.Name); !ok || v != w.Value {
				return false
			}
		}
	}
	return true
}

func parseKey(a []authproto.Attr) (*key, error) {
	proto, _ := authproto.Get(a, "proto")
	switch proto {
	case "ed25519":
		s, _ := authproto.Get(a, "!seed")
		seed, ok := pgauth.HexDecode(s)
		if !ok {
			return nil, errors.New("missing or bad !seed")
		}
		pk, err := pgauth.NewPrivateKey(seed)
		if err != nil {
			return nil, errors.New("missing or bad !seed")
		}
		k := &key{proto: proto, k: pk}
		if c, ok := authproto.Get(a, "cert"); ok {
			if k.cert, err = pgauth.ParseCert([]byte(c)); err != nil {
				return nil, err
			}
			if !k.cert.Key.Equal(pk.Public()) {
				return nil, errors.New("certificate is for another key")
			}
			k.user = k.cert.Name
		}
		if u, ok := authproto.Get(a, "user"); ok {
			k.user = u
		}
		if r, ok := authproto.Get(a, "role"); ok {
			found := false
			for _, x := range roles {
				found = found || x == r
			}
			if !found {
				return nil, errors.New("unknown role " + r)
			}
			k.role = r
		}
		return k, nil
	case "pass":
		k := &key{proto: proto}
		var ok1, ok2, ok3 bool
		k.server, ok1 = authproto.Get(a, "server")
		k.user, ok2 = authproto.Get(a, "user")
		k.password, ok3 = authproto.Get(a, "!password")
		if !ok1 || !ok2 || !ok3 {
			return nil, errors.New("missing server, user or !password")
		}
		return k, nil
	case "":
		return nil, errors.New("missing proto")
	}
	return nil, errors.New("unknown proto " + proto)
}

type rpc struct {
	key       *key
	attrs     []authproto.Attr
	challenge []byte
	reply     []byte
}

type factotum struct {
	keys []*key
	rpcs map[uint32]*rpc
}

func (f *factotum) ctl(text string) error {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		verb, rest, _ := strings.Cut(line, " ")
		a, ok := authproto.ParseAttrs(rest)
		if !ok {
			return errors.New("bad quoting")
		}
		switch verb {
		case "key":
			k, err := parseKey(a)
			if err != nil {
				return err
			}
			f.keys = append(f.keys, k)
		case "delkey":
			_, hasProto := authproto.Get(a, "proto")
			role, hasRole := authproto.Get(a, "role")
			var kept []*key
			for _, k := range f.keys {
				if hasProto && k.matches(a) && (!hasRole || k.role == role) {
					continue
				}
				kept = append(kept, k)
			}
			if len(kept) == len(f.keys) {
				return errors.New("no key matches")
			}
			f.keys = kept
		default:
			return errors.New("unknown verb " + verb)
		}
	}
	return nil
}

func (f *factotum) request(fid uint32, req []byte) []byte {
	verb, arg := string(req), []byte(nil)
	if i := strings.IndexByte(verb, ' '); i >= 0 {
		verb, arg = verb[:i], req[i+1:]
	}
	r := f.rpcs[fid]
	if r == nil {
		r = &rpc{}
		f.rpcs[fid] = r
	}
	switch verb {
	case "start":
		a, ok := authproto.ParseAttrs(string(arg))
		if !ok {
			return []byte("error bad start")
		}
		proto, ok := authproto.Get(a, "proto")
		if !ok {
			return []byte("error missing proto")
		}
		role, ok := authproto.Get(a, "role")
		if !ok {
			role = "client"
		}
		want := []authproto.Attr{{Name: "role", Value: role}}
		for _, x := range a {
			if x.Name != "role" {
				want = append(want, x)
			}
		}
		*r = rpc{}
		var found *key
		for _, pass := range []bool{true, false} {
			for _, k := range f.keys {
				if k.proto == proto && k.matches(want) && (!pass || k.role == role) {
					found = k
					break
				}
			}
			if found != nil {
				break
			}
		}
		if found == nil {
			s := "needkey"
			for _, x := range a {
				if x.Name != "role" && x.Name != "aname" {
					s += " " + x.Name + "=" + authproto.Quote(x.Value)
				}
			}
			return []byte(s)
		}
		r.key, r.attrs = found, want
		return []byte("ok")
	case "write":
		if r.key == nil || r.key.proto != "ed25519" {
			return []byte("error phase error")
		}
		r.challenge = append([]byte(nil), arg...)
		return []byte("ok")
	case "read":
		if r.key == nil {
			return []byte("error phase error")
		}
		k := r.key
		if k.proto == "pass" {
			return []byte("ok " + authproto.Quote(k.user) + " " + authproto.Quote(k.password))
		}
		if r.challenge == nil {
			return []byte("error phase error")
		}
		out, err := sign(k, r.attrs, r.challenge)
		if err != nil {
			return []byte("error " + err.Error())
		}
		return append([]byte("ok "), out...)
	}
	return []byte("error unknown verb")
}

// sign builds and signs what the role asks for.
func sign(k *key, attrs []authproto.Attr, ch []byte) ([]byte, error) {
	role, _ := authproto.Get(attrs, "role")
	switch role {
	case "modsign":
		if len(ch) < modsig.DigestSize {
			return nil, errors.New("bad digest")
		}
		var d [modsig.DigestSize]byte
		copy(d[:], ch)
		var certs []*pgauth.Cert
		if k.cert != nil {
			certs = append(certs, k.cert)
		}
		e, err := modsig.SignEntry(k.k, certs, d, ch[modsig.DigestSize:])
		if err != nil {
			return nil, err
		}
		return e.Encode(), nil
	case "server":
		if len(ch) != pgauth.SessionSize {
			return nil, errors.New("bad session")
		}
		kind, body := pgauth.ProofBody(k.cert, k.k.Public())
		sig := k.k.Sign(pgauth.ServerMessage(ch, k.k.Hybrid(), kind, body))
		return pgauth.AssembleServerProof(kind, sig, body), nil
	}
	uname, ok := authproto.Get(attrs, "user")
	if !ok {
		uname = k.user
	}
	if uname == "" {
		uname = authproto.User()
	}
	aname, _ := authproto.Get(attrs, "aname")
	return pgauth.MakeProof(k.k, k.cert, ch, uname, aname)
}

func (f *factotum) Lookup(p []string) (bool, bool) {
	switch {
	case len(p) == 0:
		return true, true
	case len(p) == 1 && (p[0] == "ctl" || p[0] == "rpc"):
		return true, false
	}
	return false, false
}

func (f *factotum) List(p []string) []string {
	if len(p) == 0 {
		return []string{"ctl", "rpc"}
	}
	return nil
}

func (f *factotum) Read(p []string, fid uint32, off uint64, count uint32) ([]byte, error) {
	switch p[0] {
	case "ctl":
		var b strings.Builder
		for _, k := range f.keys {
			b.WriteString(k.line())
		}
		s := b.String()
		if off >= uint64(len(s)) {
			return nil, nil
		}
		return []byte(s[off:min(uint64(len(s)), off+uint64(count))]), nil
	case "rpc":
		r := f.rpcs[fid]
		if r == nil || r.reply == nil {
			return nil, errors.New("no request")
		}
		// A long reply (a hybrid proof) comes in as many reads as it
		// takes; the kernel's mount keeps reading while reads are full.
		out := r.reply[:min(len(r.reply), int(count))]
		if r.reply = r.reply[len(out):]; len(r.reply) == 0 {
			r.reply = nil
		}
		return out, nil
	}
	return nil, errors.New(synth.Enonexist)
}

func (f *factotum) Write(p []string, fid uint32, _ uint64, data []byte) (uint32, error) {
	switch p[0] {
	case "ctl":
		if err := f.ctl(string(data)); err != nil {
			return 0, err
		}
	case "rpc":
		reply := f.request(fid, data)
		f.rpcs[fid].reply = reply
	default:
		return 0, errors.New(synth.Eperm)
	}
	return uint32(len(data)), nil
}

func (f *factotum) Create([]string, string, uint32) error { return errors.New(synth.Eperm) }
func (f *factotum) Remove([]string) error                 { return errors.New(synth.Eperm) }
func (f *factotum) Clunk(_ []string, fid uint32)          { delete(f.rpcs, fid) }

func authinfoLine(path, role string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		authproto.Fail(authproto.FileErr(path, err))
	}
	a, err := pgauth.ParseAuthInfo(b)
	if err != nil {
		authproto.Fail(path + ": " + err.Error())
	}
	r := ""
	if role != "" {
		r = " role=" + role
	}
	return "key proto=ed25519 user=" + authproto.Quote(a.Cert.Name) + r + " !seed=" + pgauth.Hex(a.Key.Seed()) +
		" cert=" + authproto.Quote(a.Cert.Encode()) + "\n"
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--serve" {
		f := &factotum{rpcs: map[uint32]*rpc{}}
		if err := f.ctl(string(synth.Secret())); err != nil {
			authproto.Fail(err.Error())
		}
		synth.Serve(os.Stdin, f, synth.Owner())
		return
	}
	a := arg.New(os.Args)
	a.SetUsage("auth/factotum [-m mntpt] [-s authinfo] [authinfo ...]")
	mtpt, server := authproto.Factotum, ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'm':
			mtpt = a.EArg()
		case 's':
			server += authinfoLine(a.EArg(), "server")
		default:
			a.Usage()
		}
	}
	keys := ""
	for _, p := range a.Argv() {
		keys += authinfoLine(p, "")
	}
	keys += server
	if _, err := sys.Stat(mtpt); err != nil {
		authproto.MkdirsFor(mtpt + "/x")
	}
	if err := synth.Start("/bin/auth/factotum.wasm", []string{"auth/factotum", "--serve"}, mtpt, []byte(keys)); err != nil {
		authproto.Fail(err.Error())
	}
}
