// Command auth/keyfs serves the signer's key database at mntpt (default
// /mnt/keys), as the Rust sibling's does. The database is keyfile
// (default /keydb/keys), sealed with ChaCha20-Poly1305 under an Argon2id
// key from the master password, read as one line on standard input. A
// missing keyfile is created empty, with cost -C m,t.
//
// Each user is a directory holding secret (the password verifier that
// auth/changelogin writes), expire (seconds since 1970, or never) and
// status (ok or disabled). mkdir adds a user and rm -r removes one.
// Every change rewrites the keyfile. Inferno's keyfs, with its own
// database format, is /bin/keyfs.
//
//	auth/keyfs [-m mntpt] [-C m,t] [keyfile]
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
	"github.com/ericvh/purgatorio-go/sdk/synth"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var files = []string{"expire", "secret", "status"}

const eformat = "bad format"

type entry struct {
	secret   string // "" for none
	expire   uint64
	disabled bool
}

type db struct {
	users map[string]*entry
	path  string
	kdf   pglogin.Kdf
	key   [32]byte
}

func parse(text string) (map[string]*entry, error) {
	users := map[string]*entry{}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if line == "" {
			continue
		}
		w := strings.SplitN(line, " ", 4)
		if len(w) != 4 {
			return nil, errors.New(eformat)
		}
		e, ok := authproto.Expiry(w[1])
		if !ok {
			return nil, errors.New("bad expiry " + w[1])
		}
		u := &entry{expire: e, disabled: w[2] != "ok"}
		if w[3] != "-" {
			u.secret = w[3]
		}
		users[w[0]] = u
	}
	return users, nil
}

func (d *db) names() []string {
	var n []string
	for k := range d.users {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func (d *db) text() string {
	var b strings.Builder
	for _, n := range d.names() {
		e := d.users[n]
		st, sec := "ok", e.secret
		if e.disabled {
			st = "disabled"
		}
		if sec == "" {
			sec = "-"
		}
		b.WriteString(n + " " + authproto.ShowExpiry(e.expire) + " " + st + " " + sec + "\n")
	}
	return b.String()
}

func (d *db) save() error {
	blob, err := pglogin.SealWith(d.kdf, d.key, []byte(d.text()), rand.Reader)
	if err != nil {
		return err
	}
	fd, err := sys.Create(d.path, sys.OWRITE|sys.OTRUNC, 0o600)
	if err != nil {
		return errors.New(sys.Errstr())
	}
	f := os.NewFile(uintptr(fd), d.path)
	defer f.Close()
	_, err = f.Write(blob)
	return err
}

func (d *db) Lookup(p []string) (bool, bool) {
	switch len(p) {
	case 0:
		return true, true
	case 1:
		_, ok := d.users[p[0]]
		return ok, true
	case 2:
		_, ok := d.users[p[0]]
		for _, f := range files {
			if ok && f == p[1] {
				return true, false
			}
		}
	}
	return false, false
}

func (d *db) List(p []string) []string {
	switch len(p) {
	case 0:
		return d.names()
	case 1:
		return files
	}
	return nil
}

func (d *db) value(p []string) (string, bool) {
	if len(p) != 2 {
		return "", false
	}
	e, ok := d.users[p[0]]
	if !ok {
		return "", false
	}
	switch p[1] {
	case "secret":
		if e.secret == "" {
			return "", true
		}
		return e.secret + "\n", true
	case "expire":
		return authproto.ShowExpiry(e.expire) + "\n", true
	case "status":
		if e.disabled {
			return "disabled\n", true
		}
		return "ok\n", true
	}
	return "", false
}

func (d *db) Read(p []string, _ uint32, off uint64, count uint32) ([]byte, error) {
	v, ok := d.value(p)
	if !ok {
		return nil, errors.New(synth.Enonexist)
	}
	if off >= uint64(len(v)) {
		return nil, nil
	}
	return []byte(v[off:min(uint64(len(v)), off+uint64(count))]), nil
}

func (d *db) Write(p []string, _ uint32, _ uint64, data []byte) (uint32, error) {
	if len(p) != 2 {
		return 0, errors.New(synth.Eperm)
	}
	e, ok := d.users[p[0]]
	if !ok {
		return 0, errors.New(synth.Enonexist)
	}
	n := *e
	text := strings.TrimSpace(string(data))
	switch p[1] {
	case "secret":
		if _, err := pglogin.ParseVerifier([]byte(text)); err != nil {
			return 0, err
		}
		n.secret = text
	case "expire":
		x, ok := authproto.Expiry(text)
		if !ok {
			return 0, errors.New("bad expiry " + text)
		}
		n.expire = x
	case "status":
		switch text {
		case "ok":
			n.disabled = false
		case "disabled":
			n.disabled = true
		default:
			return 0, errors.New(eformat)
		}
	default:
		return 0, errors.New(synth.Eperm)
	}
	d.users[p[0]] = &n
	if err := d.save(); err != nil {
		return 0, err
	}
	return uint32(len(data)), nil
}

func (d *db) Create(dir []string, name string, perm uint32) error {
	if len(dir) != 0 || perm&sys.DMDIR == 0 {
		return errors.New(synth.Eperm)
	}
	if !pgauth.ValidName(name) {
		return errors.New(eformat)
	}
	d.users[name] = &entry{}
	return d.save()
}

func (d *db) Remove(p []string) error {
	switch len(p) {
	case 1:
		delete(d.users, p[0])
		return d.save()
	case 2:
		return nil // rm -r removes the files first; the directory goes with them
	}
	return errors.New(synth.Eperm)
}

func (d *db) Clunk([]string, uint32) {}

// serve is the server prog: the secret is the kdf, the key and the path.
func serve() {
	s := strings.SplitN(string(synth.Secret()), "\n", 3)
	if len(s) != 3 {
		authproto.Fail(eformat)
	}
	kdf, err := pglogin.ParseKdf([]byte(s[0]))
	key, err2 := hex.DecodeString(s[1])
	if err != nil || err2 != nil || len(key) != 32 {
		authproto.Fail(eformat)
	}
	d := &db{path: s[2], kdf: kdf}
	copy(d.key[:], key)
	blob, err := os.ReadFile(d.path)
	if err != nil {
		authproto.Fail(authproto.FileErr(d.path, err))
	}
	data, err := pglogin.OpenWith(d.key, blob)
	if err != nil {
		authproto.Fail(err.Error())
	}
	if d.users, err = parse(string(data)); err != nil {
		authproto.Fail(err.Error())
	}
	synth.Serve(os.Stdin, d, synth.Owner())
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--serve" {
		serve()
		return
	}
	a := arg.New(os.Args)
	a.SetUsage("auth/keyfs [-m mntpt] [-C m,t] [keyfile]")
	mtpt := authproto.Keys
	m, t := uint32(pglogin.DefaultM), uint32(pglogin.DefaultT)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'm':
			mtpt = a.EArg()
		case 'C':
			var err error
			if m, t, err = authproto.Cost(a.EArg()); err != nil {
				authproto.Fail(err.Error())
			}
		default:
			a.Usage()
		}
	}
	rest := a.Argv()
	if len(rest) > 1 {
		a.Usage()
	}
	path := authproto.KeyDB
	if len(rest) == 1 {
		path = rest[0]
	}
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "#") {
		wd, _ := os.Getwd()
		path = strings.TrimSuffix(wd, "/") + "/" + path
	}
	pw, err := authproto.Password("master password")
	if err != nil {
		authproto.Fail(err.Error())
	}
	var kdf pglogin.Kdf
	var key [32]byte
	if blob, err := os.ReadFile(path); err == nil {
		if _, kdf, key, err = pglogin.Open([]byte(pw), blob); err != nil {
			authproto.Fail(path + ": " + err.Error())
		}
	} else {
		if kdf, err = pglogin.NewKdf(m, t); err != nil {
			authproto.Fail(err.Error())
		}
		if key, err = kdf.Derive([]byte(pw)); err != nil {
			authproto.Fail(err.Error())
		}
		authproto.MkdirsFor(path)
		d := &db{users: map[string]*entry{}, path: path, kdf: kdf, key: key}
		if err := d.save(); err != nil {
			authproto.Fail(path + ": " + err.Error())
		}
	}
	if _, err := sys.Stat(mtpt); err != nil {
		authproto.MkdirsFor(mtpt + "/x")
	}
	secret := kdf.Encode() + "\n" + hex.EncodeToString(key[:]) + "\n" + path
	if err := synth.Start("/bin/auth/keyfs.wasm", []string{"auth/keyfs", "--serve"}, mtpt, []byte(secret)); err != nil {
		authproto.Fail(err.Error())
	}
}
