// Command auth/countersigner adds the signature of key (a seed file,
// default /keydb/signerkey) to the certificate in file (a certificate or
// an authinfo), and prints the file with the new certificate in place. A
// server that trusts either signer then accepts it. The key must be of
// the certificate's family: hybrid for pgcert2, Ed25519 for pgcert1.
//
//	auth/countersigner [-f key] file
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
	a.SetUsage("auth/countersigner [-f key] file")
	keyfile := authproto.SignerKey
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'f':
			keyfile = a.EArg()
		default:
			a.Usage()
		}
	}
	rest := a.Argv()
	if len(rest) != 1 {
		a.Usage()
	}
	file := rest[0]
	kb, err := os.ReadFile(keyfile)
	if err != nil {
		authproto.Fail(authproto.FileErr(keyfile, err))
	}
	key, err := pgauth.NewPrivateKey(kb)
	if err != nil {
		authproto.Fail(keyfile + ": not a key")
	}
	text, err := os.ReadFile(file)
	if err != nil {
		authproto.Fail(authproto.FileErr(file, err))
	}
	c, ok := pgauth.FindCert(text)
	if !ok {
		authproto.Fail(file + ": bad certificate")
	}
	old := c.Encode()
	if err := c.Sign(key); err != nil {
		authproto.Fail(err.Error())
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(string(text), "\n"), "\n") {
		if line == old {
			line = c.Encode()
		}
		out.WriteString(line + "\n")
	}
	os.Stdout.WriteString(out.String())
}
