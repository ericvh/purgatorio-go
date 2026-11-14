// Command auth/sign signs a module (docs/signing.md). The signature goes
// into the module's purgatorio.sig section: an entry by the same key is
// replaced, and other entries stay, so signing a signed module
// countersigns it.
//
// The key is keyfile, a seed file or an authinfo (whose certificate then
// goes with the signature), or without -k the user's own key of the
// family -p names (hybrid by default): /usr/<user>/keyring/default, then
// /usr/<user>/key. A hybrid key makes a hybrid signature, an Ed25519 key
// a classical one, byte-identical to the Rust sibling's. Each -c adds a
// certificate to the chain: one for the key that signed the certificate
// before it. The module is rewritten in place unless -o names another
// file.
//
//	auth/sign [-k keyfile] [-p proto] [-c certfile] ... [-o out] module
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/pgauth/modsig"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
)

func read(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		authproto.Fail(authproto.FileErr(path, err))
	}
	return b
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/sign [-k keyfile] [-p proto] [-c certfile] ... [-o out] module")
	keyfile, proto, out := "", "", ""
	var certfiles []string
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'k':
			keyfile = a.EArg()
		case 'p':
			proto = a.EArg()
		case 'c':
			certfiles = append(certfiles, a.EArg())
		case 'o':
			out = a.EArg()
		default:
			a.Usage()
		}
	}
	rest := a.Argv()
	if len(rest) != 1 {
		a.Usage()
	}
	module := rest[0]
	wasm := read(module)
	var k *pgauth.PrivateKey
	var certs []*pgauth.Cert
	if keyfile != "" {
		var c *pgauth.Cert
		var err error
		if k, c, err = authproto.ReadKey(keyfile); err != nil {
			authproto.Fail(keyfile + ": not a key")
		}
		if c != nil {
			certs = append(certs, c)
		}
	} else {
		hybrid, err := authproto.HybridFor(proto)
		if err != nil {
			authproto.Fail(err.Error())
		}
		u := authproto.User()
		for _, f := range []string{"/usr/" + u + "/keyring/default", "/usr/" + u + "/key"} {
			if kk, c, err := authproto.ReadKey(f); err == nil && kk.Hybrid() == hybrid {
				k = kk
				if c != nil {
					certs = append(certs, c)
				}
				break
			}
		}
		if k == nil {
			authproto.Fail(pgauth.ENoKey.Error())
		}
	}
	for _, path := range certfiles {
		c, ok := pgauth.FindCert(read(path))
		if !ok {
			authproto.Fail(path + ": bad certificate")
		}
		certs = append(certs, c)
	}
	signed, err := modsig.Sign(wasm, k, certs)
	if err != nil {
		authproto.Fail(module + ": " + err.Error())
	}
	if out == "" {
		out = module
	}
	if err := os.WriteFile(out, signed, 0o666); err != nil {
		authproto.Fail(authproto.FileErr(out, err))
	}
}
