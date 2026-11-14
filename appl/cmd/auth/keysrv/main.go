// Command auth/keysrv is the password-change server, for one
// conversation on fd 0 (run it under listen). The client proves it knows
// the old password with the login protocol and sends a new verifier,
// which replaces the user's secret in auth/keyfs (default /mnt/keys).
// The signer key only salts the answers for unknown users. -p names the
// login versions it takes, as for auth/signer.
//
//	auth/keysrv [-f signerkey] [-m mntpt] [-p proto]
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/keysrv [-f signerkey] [-m mntpt] [-p proto]")
	signerkey, mtpt, proto := authproto.SignerKey, authproto.Keys, ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'f':
			signerkey = a.EArg()
		case 'm':
			mtpt = a.EArg()
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
	err = authproto.ServeLogin([]string{"passwd"}, mtpt, authproto.FakeSecret(signerkey), accept, func(h *pglogin.Hello, _ *authproto.Account, req []byte) ([]byte, error) {
		v, err := pglogin.ParseVerifier(req)
		if err != nil {
			return nil, err
		}
		path := mtpt + "/" + h.Name + "/secret"
		fd, err := sys.Open(path, sys.OWRITE)
		if err != nil {
			return nil, sys.Error(sys.Errstr())
		}
		f := os.NewFile(uintptr(fd), path)
		defer f.Close()
		if _, err := f.Write([]byte(v.Encode())); err != nil {
			return nil, err
		}
		return []byte("ok"), nil
	})
	if err != nil {
		authproto.Fail(err.Error())
	}
}
