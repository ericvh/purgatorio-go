package auth9

import (
	"errors"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
)

// ParseKeyFile reads an authentication server's keys (/lib/keys/p9sk1,
// docs/auth.md): one factotum key line a user, as written to factotum's
// ctl,
//
//	key proto=p9sk1 dom=<authdom> user=<name> !password=<password>
//	key proto=p9sk1 dom=<authdom> user=<name> !hex=<14 hex digits>
//
// Blank lines and lines starting with # are skipped, as are keys of
// other protocols. A line it cannot take is an error naming the line.
func ParseKeyFile(text []byte) (Keys, error) {
	keys := map[[2]string][]byte{}
	for i, line := range strings.Split(string(text), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		bad := func(s string) error { return errors.New("line " + strconv.Itoa(i+1) + ": " + s) }
		verb, rest, _ := strings.Cut(line, " ")
		a, ok := authproto.ParseAttrs(rest)
		if verb != "key" || !ok {
			return nil, bad("not a key line")
		}
		if p, _ := authproto.Get(a, "proto"); p != "p9sk1" {
			continue
		}
		user, ok1 := authproto.Get(a, "user")
		dom, ok2 := authproto.Get(a, "dom")
		switch {
		case !ok1 || !ok2:
			return nil, bad("need user and dom attributes")
		case len(user) >= ANAMELEN:
			return nil, bad("user name too long")
		case len(dom) >= DOMLEN:
			return nil, bad("auth dom name too long")
		}
		var k []byte
		if h, ok := authproto.Get(a, "!hex"); ok {
			if k, ok = pgauth.HexDecode(h); !ok || len(k) != DESKEYLEN {
				return nil, bad("malformed !hex key data")
			}
		} else if pw, ok := authproto.Get(a, "!password"); ok {
			k = PassToKey(pw)
		} else {
			return nil, bad("need !password or !hex attribute")
		}
		keys[[2]string{dom, user}] = k
	}
	return func(dom, user string) ([]byte, bool) {
		k, ok := keys[[2]string{dom, user}]
		return k, ok
	}, nil
}
