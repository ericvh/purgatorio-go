package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericvh/purgatorio-go/lethe"
	"github.com/ericvh/purgatorio-go/pgauth"
)

// ParseProto reads an authentication protocol setting (docs/auth.md,
// "Choosing algorithms"): hybrid, classical, or both separated by a space
// or a comma. Keyring is Inferno's and means nothing to Lethe.
func ParseProto(s string) (pgauth.Accept, error) {
	var a pgauth.Accept
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' }) {
		switch f {
		case "hybrid":
			a |= pgauth.AcceptHybrid
		case "classical":
			a |= pgauth.AcceptClassical
		default:
			return 0, fmt.Errorf("lethed: unknown protocol %q: hybrid or classical", f)
		}
	}
	if a == 0 {
		a = pgauth.AcceptHybrid
	}
	return a, nil
}

// setupTickets gives the server its ticket layer: a ticket key from -t
// or a random one, and the exchanges -p names (by default $authproto,
// then hybrid).
func setupTickets(o *LetheOptions, s *lethe.Server) error {
	proto := o.Proto
	if proto == "" {
		proto = os.Getenv("authproto")
	}
	accept, err := ParseProto(proto)
	if err != nil {
		return err
	}
	var key [32]byte
	if o.TicketKey != "" {
		k, ok := readKey(o.TicketKey)
		if !ok {
			return fmt.Errorf("%s: not a 32-byte key", o.TicketKey)
		}
		key = k
	} else {
		(&Host{}).Entropy(key[:])
	}
	ts := lethe.NewTicketServer(key, accept)
	// Hold the tickets requests open under, so clients send a ticket's
	// reference in its place (Treqref).
	ts.Refs = lethe.NewRefCache(maxRefs)
	s.Tickets = ts
	return nil
}

// readPublic reads a public key file: 32 bytes (Ed25519) or a hybrid
// key.
func readPublic(p string) (pgauth.PublicKey, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	k, err := pgauth.ParsePublicKey(b)
	return k, err == nil
}

// letheTrust is the trust for uname, read now from the files the options
// name: an edit takes effect on the next ticket.
func letheTrust(o *LetheOptions, uname string) pgauth.Policy {
	var t pgauth.Policy
	for _, p := range o.Keys {
		if k, ok := readPublic(p); ok {
			t.Keys = append(t.Keys, k.ID())
		}
	}
	if o.UserKeys != "" && pgauth.ValidName(uname) {
		if k, ok := readPublic(filepath.Join(o.UserKeys, uname, "key.pub")); ok {
			t.Keys = append(t.Keys, k.ID())
		}
	}
	if o.Signers != "" {
		if b, err := os.ReadFile(o.Signers); err == nil {
			t.Signers = pgauth.ParseListLenient(b)
		}
	}
	// A list that is missing revokes nothing; one that is there but does
	// not read or parse fails closed.
	if o.Revoked != "" {
		b, err := os.ReadFile(o.Revoked)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			t.Damaged = true
		default:
			l, ok := pgauth.ParseList(b)
			t.Revoked, t.Damaged = l, !ok
		}
	}
	return t
}

// maxRefs is how many tickets lethed holds for references.
const maxRefs = 4096
