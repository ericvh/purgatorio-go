// Command auth/createsignerkey makes a signer's key in keyfile (default
// /keydb/signerkey) and prints its public key as a key-list line, ready
// to append to /lib/keys/signers (docs/auth.md). The key is hybrid
// (Ed25519 and ML-DSA-65; the line names its fingerprint, pq:...) unless
// -p classical asks for an Ed25519 key, the Rust sibling's.
//
//	auth/createsignerkey [-f keyfile] [-p proto] [name]
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/createsignerkey [-f keyfile] [-p proto] [name]")
	keyfile, proto := authproto.SignerKey, ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'f':
			keyfile = a.EArg()
		case 'p':
			proto = a.EArg()
		default:
			a.Usage()
		}
	}
	rest := a.Argv()
	if len(rest) > 1 {
		a.Usage()
	}
	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}
	if strings.ContainsAny(name, "\r\n") {
		authproto.Fail("bad name")
	}
	hybrid, err := authproto.HybridFor(proto)
	if err != nil {
		authproto.Fail(err.Error())
	}
	k, err := pgauth.GenerateKey(hybrid)
	if err != nil {
		authproto.Fail(err.Error())
	}
	authproto.MkdirsFor(keyfile)
	if err := authproto.WriteSecret(keyfile, k.Seed()); err != nil {
		authproto.Fail(err.Error())
	}
	os.Stdout.WriteString(pgauth.ListLine(k.Public().ID(), name))
}
