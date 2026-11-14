// Command auth/signer is the certificate server, for one conversation on
// fd 0 (run it under listen). It checks the client's password against
// auth/keyfs (default /mnt/keys) with the login protocol, then signs the
// public key the client sent with the signer key (default
// /keydb/signerkey): a pgcert2 for a hybrid signer, a pgcert1 for an
// Ed25519 one. The certificate expires with the account, and at most
// days (default 365) from now. It takes the login versions -p names
// (docs/auth.md): hybrid (pglogin2) by default, classical (pglogin1, the
// Rust sibling's), or both. Inferno's signer is /bin/signer.
//
//	auth/signer [-f signerkey] [-m mntpt] [-e days] [-p proto]
package main

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
)

const day = 24 * 60 * 60

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/signer [-f signerkey] [-m mntpt] [-e days] [-p proto]")
	signerkey, mtpt, proto := authproto.SignerKey, authproto.Keys, ""
	days := uint64(365)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'f':
			signerkey = a.EArg()
		case 'm':
			mtpt = a.EArg()
		case 'e':
			n, err := strconv.ParseUint(a.EArg(), 10, 32)
			if err != nil {
				authproto.Fail("bad days")
			}
			days = n
		case 'p':
			proto = a.EArg()
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	protos, err := authproto.Resolve(proto)
	if err != nil {
		authproto.Fail(err.Error())
	}
	accept, err := authproto.Accept(protos)
	if err != nil {
		authproto.Fail(err.Error())
	}
	b, err := os.ReadFile(signerkey)
	if err != nil {
		authproto.Fail(authproto.FileErr(signerkey, err))
	}
	key, err := pgauth.NewPrivateKey(b)
	if err != nil {
		authproto.Fail(signerkey + ": not a key")
	}
	err = authproto.ServeLogin([]string{"cert"}, mtpt, b, accept, func(h *pglogin.Hello, acct *authproto.Account, req []byte) ([]byte, error) {
		pub, err := parsePublic(strings.TrimSpace(string(req)))
		if err != nil {
			return nil, err
		}
		if pub.Hybrid() != key.Hybrid() {
			return nil, pgauth.EWrongAlg
		}
		limit := authproto.Now() + days*day
		expires := limit
		if acct.Expire != 0 {
			expires = min(acct.Expire, limit)
		}
		c, err := pgauth.NewCert(h.Name, pub, expires)
		if err != nil {
			return nil, err
		}
		if err := c.Sign(key); err != nil {
			return nil, err
		}
		return []byte("ok\n" + key.Public().ID().String() + "\n" + c.Encode() + "\n"), nil
	})
	if err != nil {
		authproto.Fail(err.Error())
	}
}

// parsePublic reads a public key as the client sends it: hex, of an
// Ed25519 or a hybrid key.
func parsePublic(s string) (pgauth.PublicKey, error) {
	b, ok := pgauth.HexDecode(s)
	if !ok {
		return nil, errors.New("bad public key")
	}
	k, err := pgauth.ParsePublicKey(b)
	if err != nil {
		return nil, errors.New("bad public key")
	}
	return k, nil
}
