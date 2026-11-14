package host

import (
	"errors"
	"path/filepath"

	"github.com/ericvh/purgatorio-go/lethe"
)

// setupTickets gives the server its ticket layer when the options trust
// a key. Tickets come in a later change; until then lethed serves in the
// clear only.
func setupTickets(o *LetheOptions, s *lethe.Server) error {
	if len(o.Keys) > 0 || o.UserKeys != "" || o.Signers != "" {
		return errors.New("lethed: tickets are not implemented yet: use -A")
	}
	return nil
}

// letheTrust is the trust for uname, read now from the files the options
// name: an edit takes effect on the next ticket.
func letheTrust(o *LetheOptions, uname string) lethe.Trust {
	var t lethe.Trust
	for _, p := range o.Keys {
		if k, ok := readKey(p); ok {
			t.Keys = append(t.Keys, k)
		}
	}
	if o.UserKeys != "" && lethe.ValidElem(uname) {
		if k, ok := readKey(filepath.Join(o.UserKeys, uname, "key.pub")); ok {
			t.Keys = append(t.Keys, k)
		}
	}
	return t
}
