// Command auth/getauthinfo makes a new key, gets it certified by the
// signer (a dial string; default $signer) after a password check, and
// writes the authinfo keyname: a path, or a name under
// /usr/<user>/keyring (default is what mounts use). The password is one
// line on standard input; it never crosses the network. With -p hybrid
// (the default) the key is hybrid and the login is pglogin2; with -p
// classical both are the Rust sibling's (an Ed25519 key, pglogin1). It
// checks that the certificate is for its key and name and is signed by
// the key the signer named. Inferno's getauthinfo is /bin/getauthinfo.
//
//	auth/getauthinfo [-s signer] [-u user] [-p proto] keyname
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/getauthinfo [-s signer] [-u user] [-p proto] keyname")
	signer, user, proto := authproto.Signer(), authproto.User(), ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 's':
			signer = a.EArg()
		case 'u':
			user = a.EArg()
		case 'p':
			proto = a.EArg()
		default:
			a.Usage()
		}
	}
	rest := a.Argv()
	if len(rest) != 1 {
		a.Usage()
	}
	hybrid, err := authproto.HybridFor(proto)
	if err != nil {
		authproto.Fail(err.Error())
	}
	if signer == "" {
		authproto.Fail("no signer: use -s or set $signer")
	}
	addr := dial.Netmkaddr(signer, "net", "infsigner")
	pw, err := authproto.Password("password")
	if err != nil {
		authproto.Fail(err.Error())
	}
	k, err := pgauth.GenerateKey(hybrid)
	if err != nil {
		authproto.Fail(err.Error())
	}
	reply, err := authproto.Login(addr, hybrid, "cert", user, pw, func(pglogin.Kdf) ([]byte, error) {
		return []byte(pgauth.Hex(k.Public())), nil
	})
	if err != nil {
		authproto.Fail(err.Error())
	}
	lines := strings.Split(string(reply), "\n")
	if len(lines) < 2 {
		authproto.Fail("bad reply from signer")
	}
	sid, ok := pgauth.ParseKeyID(lines[0])
	if !ok {
		authproto.Fail("bad reply from signer")
	}
	c, err := pgauth.ParseCert([]byte(lines[1]))
	if err != nil || !c.Key.Equal(k.Public()) || c.Name != user || !c.SignedBy(sid) {
		authproto.Fail("bad certificate from signer")
	}
	info := &pgauth.AuthInfo{Key: k, Signer: sid, Cert: c}
	path := authproto.KeyfilePath(rest[0], user)
	authproto.MkdirsFor(path)
	if err := authproto.WriteSecret(path, []byte(info.Encode())); err != nil {
		authproto.Fail(err.Error())
	}
}
