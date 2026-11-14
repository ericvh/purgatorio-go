package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"strings"
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// Capabilities expire after Inferno's Captimeout: 15 ticks of its
// 30-second watcher.
const captimeout = 15 * 30 * time.Second

// DevCap is #¤, the capability device (devcap.c): the host owner writes a
// hash to caphash; a prog that writes the matching
// "fromuser@touser@key" to capuse becomes touser.
type DevCap struct {
	DevBase
	k        *Kernel
	mu       sync.Mutex
	caps     map[[sha1.Size]byte]time.Time
	nohash   bool
	clockNow func() time.Time
}

func NewDevCap(k *Kernel) *DevCap {
	return &DevCap{k: k, caps: map[[sha1.Size]byte]time.Time{}, clockNow: time.Now}
}

func (d *DevCap) DC() rune     { return '¤' }
func (d *DevCap) Name() string { return "cap" }

const (
	capQdir = iota
	capQhash
	capQuse
)

func (d *DevCap) tab() []Tab {
	t := []Tab{{"capuse", capQuse, 0o222, 0}}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.nohash {
		t = append(t, Tab{"caphash", capQhash, 0o200, 0})
	}
	return t
}

func (d *DevCap) Attach(context.Context, string) (Walked, error) {
	return Walked{Qid: styx.Qid{Type: styx.QTDIR}}, nil
}

func (d *DevCap) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	if !c.IsDir() {
		return Walked{}, Error(abi.Enotdir)
	}
	return TabWalk(d.tab(), name)
}

func (d *DevCap) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	if c.IsDir() {
		return TabDir('¤', Tab{".", capQdir, styx.DMDIR | 0o555, 0}, d.k.Owner), nil
	}
	for _, t := range d.tab() {
		if t.Path == c.Qid.Path {
			return TabDir('¤', t, d.k.Owner), nil
		}
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevCap) DirRead(context.Context, *Chan) ([]styx.Dir, error) {
	var out []styx.Dir
	for _, t := range d.tab() {
		out = append(out, TabDir('¤', t, d.k.Owner))
	}
	return out, nil
}

func (d *DevCap) iseve(ctx context.Context) bool {
	p := ProgFrom(ctx)
	return p == nil || p.User() == d.k.Owner
}

func (d *DevCap) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, Error(abi.Eisdir)
		}
		return c, nil
	}
	if c.Qid.Path == capQhash && !d.iseve(ctx) {
		return nil, Error(abi.Eperm)
	}
	return c, nil
}

func (d *DevCap) Read(context.Context, *Chan, []byte, int64) (int, error) {
	return 0, Error(abi.Eperm)
}

func (d *DevCap) expire() {
	now := d.clockNow()
	for h, t := range d.caps {
		if now.Sub(t) > captimeout {
			delete(d.caps, h)
		}
	}
}

func (d *DevCap) Write(ctx context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	switch c.Qid.Path {
	case capQhash:
		if len(buf) != sha1.Size {
			return 0, Error(abi.Ebadarg)
		}
		var h [sha1.Size]byte
		copy(h[:], buf)
		d.mu.Lock()
		d.expire()
		d.caps[h] = d.clockNow()
		d.mu.Unlock()
		return len(buf), nil
	case capQuse:
		if err := d.use(ctx, buf); err != nil {
			return 0, err
		}
		return len(buf), nil
	}
	return 0, Error(abi.Ebadarg)
}

// use checks "users@key" against a hash, as capwriteuse: the hash is
// HMAC-SHA1 of the users part keyed by the key; users is "from@to" or
// "to".
func (d *DevCap) use(ctx context.Context, buf []byte) error {
	bad := Error("invalid capability")
	if len(buf) >= 127 {
		return bad
	}
	i := bytes.LastIndexByte(buf, '@')
	if i <= 0 || i == len(buf)-1 {
		return bad
	}
	usersPart, key := buf[:i], buf[i+1:]
	m := hmac.New(sha1.New, key)
	m.Write(usersPart)
	var digest [sha1.Size]byte
	copy(digest[:], m.Sum(nil))
	users := strings.Split(string(usersPart), "@")
	if len(users) == 1 {
		users = append(users, users[0])
	} else if len(users) != 2 {
		return bad
	}
	if users[0] == "" || users[1] == "" {
		return bad
	}
	d.mu.Lock()
	d.expire()
	_, ok := d.caps[digest]
	delete(d.caps, digest)
	d.mu.Unlock()
	if !ok {
		return bad
	}
	p := ProgFrom(ctx)
	if p == nil {
		return bad
	}
	if len(strings.Split(string(usersPart), "@")) == 2 && p.User() != users[0] {
		return bad
	}
	p.SetUser(users[1])
	return nil
}

func (d *DevCap) Remove(ctx context.Context, c *Chan) error {
	if c.Qid.Path != capQhash || !d.iseve(ctx) {
		return Error(abi.Eperm)
	}
	d.mu.Lock()
	d.nohash = true
	d.mu.Unlock()
	return nil
}
