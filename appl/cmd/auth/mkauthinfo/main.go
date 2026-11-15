// Command auth/mkauthinfo, on the signer's machine, makes a key for user,
// certifies it with the signer key (default /keydb/signerkey) and writes
// the authinfo to keyfile (a name without a slash is under
// /usr/<user>/keyring). The key is of the signer's family: hybrid, with a
// pgcert2 certificate, for a hybrid signer; Ed25519 and pgcert1 for an
// Ed25519 one (docs/auth.md). expires is seconds since 1970 or never; the
// default is a year.
//
// signerkey may also be an authinfo: its key, certified by another
// signer, then certifies this one, and auth/getpk -c gives its
// certificate to pass on beside the new one, a chain (docs/signing.md).
// That is an addition, as in the Rust sibling.
//
//	auth/mkauthinfo [-f signerkey] [-e expires] user keyfile
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
)

const year = 365 * 24 * 60 * 60

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/mkauthinfo [-f signerkey] [-e expires] user keyfile")
	signerkey := authproto.SignerKey
	expires := authproto.Now() + year
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'f':
			signerkey = a.EArg()
		case 'e':
			s := a.EArg()
			e, ok := authproto.Expiry(s)
			if !ok {
				authproto.Fail("bad expiry " + s)
			}
			expires = e
		default:
			a.Usage()
		}
	}
	rest := a.Argv()
	if len(rest) != 2 {
		a.Usage()
	}
	user, keyfile := rest[0], rest[1]
	b, err := os.ReadFile(signerkey)
	if err != nil {
		authproto.Fail(authproto.FileErr(signerkey, err))
	}
	var signer *pgauth.PrivateKey
	if info, err := pgauth.ParseAuthInfo(b); err == nil {
		signer = info.Key
	} else if signer, err = pgauth.NewPrivateKey(b); err != nil {
		authproto.Fail(signerkey + ": not a key")
	}
	k, err := pgauth.GenerateKey(signer.Hybrid())
	if err != nil {
		authproto.Fail(err.Error())
	}
	c, err := pgauth.NewCert(user, k.Public(), expires)
	if err != nil {
		authproto.Fail(err.Error())
	}
	if err := c.Sign(signer); err != nil {
		authproto.Fail(err.Error())
	}
	info := &pgauth.AuthInfo{Key: k, Signer: signer.Public().ID(), Cert: c}
	path := authproto.KeyfilePath(keyfile, user)
	authproto.MkdirsFor(path)
	if err := authproto.WriteSecret(path, []byte(info.Encode())); err != nil {
		authproto.Fail(err.Error())
	}
}
