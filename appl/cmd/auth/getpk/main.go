// Command auth/getpk prints the public key in each file as a key list
// names it: 64 hex digits for an Ed25519 key, pq: and the fingerprint for
// a hybrid one (docs/auth.md). A file may be a seed (32 or 64 bytes), a
// public key file (named *.pub: 32 or 1984 bytes), an authinfo or a
// certificate. With more than one file each line ends with its name.
//
// -c prints instead the certificate line of each authinfo or certificate
// file, for auth/sign -c and the chains auth/mkauthinfo makes. It is an
// addition, as in the Rust sibling.
//
//	auth/getpk [-c] file ...
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

// cert is the certificate line in an authinfo or a certificate file.
func cert(path string) (string, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", authproto.FileErr(path, err)
	}
	if c, ok := pgauth.FindCert(b); ok {
		return c.Encode(), ""
	}
	return "", path + ": no certificate"
}

func main() {
	files := os.Args[1:]
	certs := len(files) > 0 && files[0] == "-c"
	if certs {
		files = files[1:]
	}
	if len(files) == 0 {
		os.Stderr.WriteString("usage: auth/getpk [-c] file ...\n")
		sys.Exits("fail:usage")
	}
	failed := false
	if certs {
		for _, path := range files {
			c, e := cert(path)
			if e != "" {
				os.Stderr.WriteString("auth/getpk: " + e + "\n")
				failed = true
				continue
			}
			os.Stdout.WriteString(c + "\n")
		}
		if failed {
			sys.Exits("fail:errors")
		}
		return
	}
	for _, path := range files {
		k, e := public(path)
		switch {
		case e != "":
			os.Stderr.WriteString("auth/getpk: " + e + "\n")
			failed = true
		case len(files) > 1:
			os.Stdout.WriteString(k.ID().String() + " " + path + "\n")
		default:
			os.Stdout.WriteString(k.ID().String() + "\n")
		}
	}
	if failed {
		sys.Exits("fail:errors")
	}
}
