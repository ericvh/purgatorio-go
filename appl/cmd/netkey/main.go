// Command netkey reads a password, then challenges, one per line, and
// prints the response to each: the first 8 hex digits of HMAC-SHA256
// keyed with SHA-256 of the password, as the Rust sibling's does. It
// takes the place of Inferno's SecureNet calculator, which used DES.
//
//	netkey
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) != 1 {
		os.Stderr.WriteString("usage: netkey\n")
		sys.Exits("fail:usage")
	}
	pw, err := authproto.Password("password")
	if err != nil {
		authproto.Fail(err.Error())
	}
	for {
		c, err := authproto.Password("challenge")
		if err != nil {
			return
		}
		if c = strings.TrimSpace(c); c != "" {
			os.Stdout.WriteString(pglogin.NetkeyResponse([]byte(pw), []byte(c)) + "\n")
		}
	}
}
