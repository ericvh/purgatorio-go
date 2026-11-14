// Command auth/passwd changes user's password on the signer through
// auth/keysrv at server (a dial string; default $signer). It reads three
// lines on standard input: the old password, the new one and the new one
// again. The new verifier is made here, with the server's cost and a
// fresh salt, so neither password crosses the network. -p names the
// login version: hybrid (the default) or classical.
//
//	auth/passwd [-s server] [-u user] [-p proto]
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/passwd [-s server] [-u user] [-p proto]")
	server, user, proto := authproto.Signer(), authproto.User(), ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 's':
			server = a.EArg()
		case 'u':
			user = a.EArg()
		case 'p':
			proto = a.EArg()
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	hybrid, err := authproto.HybridFor(proto)
	if err != nil {
		authproto.Fail(err.Error())
	}
	if server == "" {
		authproto.Fail("no server: use -s or set $signer")
	}
	old, err := authproto.Password("old password")
	if err != nil {
		authproto.Fail(err.Error())
	}
	nw, err := authproto.Password("new password")
	if err != nil {
		authproto.Fail(err.Error())
	}
	again, err := authproto.Password("new password")
	if err != nil {
		authproto.Fail(err.Error())
	}
	if nw != again {
		authproto.Fail("passwords do not match")
	}
	if nw == "" {
		authproto.Fail("empty password")
	}
	addr := dial.Netmkaddr(server, "net", "keysrv")
	_, err = authproto.Login(addr, hybrid, "passwd", user, old, func(k pglogin.Kdf) ([]byte, error) {
		fresh, err := pglogin.NewKdf(k.M, k.T)
		if err != nil {
			return nil, err
		}
		v, err := pglogin.NewVerifier([]byte(nw), fresh)
		if err != nil {
			return nil, err
		}
		return []byte(v.Encode()), nil
	})
	if err != nil {
		authproto.Fail(err.Error())
	}
}
