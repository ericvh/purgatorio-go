// Command auth/getpk prints the public key in each file as a key list
// names it: 64 hex digits for an Ed25519 key, pq: and the fingerprint for
// a hybrid one (docs/auth.md). A file may be a seed (32 or 64 bytes), a
// public key file (named *.pub: 32 or 1984 bytes), an authinfo or a
// certificate. With more than one file each line ends with its name.
//
//	auth/getpk file ...
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func public(path string) (pgauth.PublicKey, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, authproto.FileErr(path, err)
	}
	if a, err := pgauth.ParseAuthInfo(b); err == nil {
		return a.Key.Public(), ""
	}
	if c, err := pgauth.ParseCert(b); err == nil {
		return c.Key, ""
	}
	if strings.HasSuffix(path, ".pub") {
		if k, err := pgauth.ParsePublicKey(b); err == nil {
			return k, ""
		}
	} else if k, err := pgauth.NewPrivateKey(b); err == nil {
		return k.Public(), ""
	}
	return nil, path + ": not a key"
}

func main() {
	if len(os.Args) < 2 {
		os.Stderr.WriteString("usage: auth/getpk file ...\n")
		sys.Exits("fail:usage")
	}
	failed := false
	for _, path := range os.Args[1:] {
		k, e := public(path)
		switch {
		case e != "":
			os.Stderr.WriteString("auth/getpk: " + e + "\n")
			failed = true
		case len(os.Args) > 2:
			os.Stdout.WriteString(k.ID().String() + " " + path + "\n")
		default:
			os.Stdout.WriteString(k.ID().String() + "\n")
		}
	}
	if failed {
		sys.Exits("fail:errors")
	}
}
