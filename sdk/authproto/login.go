package authproto

import (
	"bufio"
	"crypto/rand"
	"errors"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
)

// What the password tools share: keyfs accounts, the login server and
// client, and reading passwords.

const (
	KeyDB = "/keydb/keys"
	Keys  = "/mnt/keys" // where auth/keyfs serves
)

// Account is a keyfs account.
type Account struct {
	Verifier *pglogin.Verifier
	Expire   uint64 // 0 never
	Disabled bool
}

// Expired reports whether the account has expired at now.
func (a *Account) Expired(now uint64) bool { return a.Expire != 0 && now >= a.Expire }

// ReadAccount reads <mnt>/<name>; nil when there is no such user.
func ReadAccount(mnt, name string) *Account {
	if !pgauth.ValidName(name) {
		return nil
	}
	dir := mnt + "/" + name
	st, err := os.ReadFile(dir + "/status")
	if err != nil {
		return nil
	}
	a := &Account{Disabled: strings.TrimSpace(string(st)) != "ok"}
	if b, err := os.ReadFile(dir + "/secret"); err == nil {
		if v, err := pglogin.ParseVerifier(b); err == nil {
			a.Verifier = &v
		}
	}
	if b, err := os.ReadFile(dir + "/expire"); err == nil {
		a.Expire, _ = Expiry(strings.TrimSpace(string(b)))
	}
	return a
}

// FakeSecret salts the stand-in verifier of unknown users: the signer's
// key when it can be read.
func FakeSecret(keyfile string) []byte {
	if b, err := os.ReadFile(keyfile); err == nil {
		return b
	}
	return []byte(pglogin.Magic1)
}

// Accept is the login versions a server told protos takes.
func Accept(protos []string) (pgauth.Accept, error) {
	var a pgauth.Accept
	for _, p := range protos {
		switch p {
		case Hybrid:
			a |= pgauth.AcceptHybrid
		case Classical:
			a |= pgauth.AcceptClassical
		default:
			return 0, EBadProto
		}
	}
	return a, nil
}

// ServeLogin serves one login conversation on fd 0 for the operations
// in ops, taking the versions in accept. handler gets the hello, the
// account and the opened request, and returns the reply to seal. Errors
// after the password check go sealed, so only the password's holder
// sees them.
func ServeLogin(ops []string, mnt string, fake []byte, accept pgauth.Accept, handler func(*pglogin.Hello, *Account, []byte) ([]byte, error)) error {
	rw := os.Stdin // the connection, read and written, as listen gives it
	h, err := pglogin.ServerHello(rw)
	if err != nil {
		return err
	}
	known := false
	for _, o := range ops {
		known = known || o == h.Op
	}
	if !known {
		pglogin.SendError(rw, "unknown request")
		return errors.New("unknown request " + h.Op)
	}
	acct := ReadAccount(mnt, h.Name)
	var v pglogin.Verifier
	if acct != nil && acct.Verifier != nil {
		v = *acct.Verifier
	} else {
		v = pglogin.FakeVerifier(fake, h.Name, rand.Reader)
	}
	sess, req, err := pglogin.Server(rw, h, v, accept, rand.Reader)
	if err != nil {
		return err
	}
	if acct == nil {
		return pglogin.ErrFailed
	}
	var reply []byte
	switch {
	case acct.Disabled:
		err = errors.New("account disabled")
	case acct.Expired(Now()):
		err = errors.New("account expired")
	default:
		reply, err = handler(h, acct, req)
	}
	if err != nil {
		sess.Send(rw, []byte("error "+err.Error()))
		return errors.New(h.Name + ": " + err.Error())
	}
	return sess.Send(rw, reply)
}

// Login dials addr and runs op as name. A reply of "error <reason>"
// becomes that error; otherwise it returns the reply after "ok".
func Login(addr string, hybrid bool, op, name, password string, request func(pglogin.Kdf) ([]byte, error)) ([]byte, error) {
	c, err := dial.Dial(addr, "")
	if err != nil {
		return nil, errors.New("can't dial " + addr + ": " + err.Error())
	}
	defer c.Dfd.Close()
	_, reply, err := pglogin.Client(c.Dfd, hybrid, op, name, []byte(password), rand.Reader, request)
	if err != nil {
		return nil, err
	}
	if e, ok := strings.CutPrefix(string(reply), "error "); ok {
		return nil, errors.New(e)
	}
	rest, ok := strings.CutPrefix(string(reply), "ok")
	if !ok {
		return nil, pglogin.ErrProto
	}
	return []byte(strings.TrimPrefix(rest, "\n")), nil
}

var stdin = bufio.NewReader(os.Stdin)

// Password reads one line from standard input, without its newline.
func Password(what string) (string, error) {
	l, err := stdin.ReadString('\n')
	if err != nil && l == "" {
		return "", errors.New("no " + what)
	}
	return strings.TrimRight(l, "\r\n"), nil
}

// Signer is $signer, the default server of the login tools.
func Signer() string {
	b, _ := os.ReadFile("/env/signer")
	return strings.TrimSpace(string(b))
}

// Cost parses -C m,t: Argon2id memory in KiB and passes.
func Cost(s string) (uint32, uint32, error) {
	m, t, ok := strings.Cut(s, ",")
	mv, ok1 := Expiry(m)
	tv, ok2 := Expiry(t)
	if !ok || !ok1 || !ok2 || mv < 8 || tv < 1 || mv > pglogin.MaxM || tv > pglogin.MaxT {
		return 0, 0, errors.New("bad cost " + s + ": want m,t")
	}
	return uint32(mv), uint32(tv), nil
}
