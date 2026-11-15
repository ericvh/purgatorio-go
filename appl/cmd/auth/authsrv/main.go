// Command auth/authsrv is Plan 9's ticket service for p9sk1 (authsrv(6)),
// for one conversation on fd 0. Run it under listen on the ticket port:
//
//	listen -A 'tcp!*!ticket' auth/authsrv
//
// keyfile (default /lib/keys/p9sk1) holds the users' keys as factotum
// key lines (docs/auth.md). Each ticket request is answered with a
// ticket for its host, sealed with the host's key, and one for its
// server, sealed with the server's, from the keys of the request's
// domain. A host gets tickets for itself only: Plan 9's speaksfor is not
// kept. Only ticket requests are served; Plan 9's password changes and
// challenge boxes are not.
//
//	auth/authsrv [-k keyfile]
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/auth9"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
)

type conn struct{}

func (conn) Read(b []byte) (int, error)  { return os.Stdin.Read(b) }
func (conn) Write(b []byte) (int, error) { return os.Stdout.Write(b) }

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/authsrv [-k keyfile]")
	keyfile := "/lib/keys/p9sk1"
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'k':
			keyfile = a.EArg()
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	b, err := os.ReadFile(keyfile)
	if err != nil {
		authproto.Fail(authproto.FileErr(keyfile, err))
	}
	keys, err := auth9.ParseKeyFile(b)
	if err != nil {
		authproto.Fail(keyfile + ": " + err.Error())
	}
	if err := auth9.ServeTickets(conn{}, keys); err != nil {
		authproto.Fail(err.Error())
	}
}
