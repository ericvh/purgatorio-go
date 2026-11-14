// Command lethed serves a host directory read only over Lethe
// (docs/lethe.md):
//
//	lethed [-v] [-A] [-p proto] [-a addr] [-u owner] [-t ticketkey] [-e lifetime] [-w window]
//	       [-k keyfile ...] [-U userdir] [-s signers] [-r revoked] root
//
// -A answers requests with no authentication and no encryption, as the
// user none. Without it, and without a key to trust, lethed does not
// start. -p names the ticket exchanges taken: hybrid (X25519 with
// ML-KEM-768 and hybrid signatures, the default), classical (the Rust
// exchange), or both; without it, $authproto, then hybrid.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
)

func main() { os.Exit(host.RunLethed(os.Args[1:])) }
