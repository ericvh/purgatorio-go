package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// Walked is the result of attaching to a device or walking one step.
type Walked struct {
	Qid styx.Qid
	Aux any
}

// Dev is a device driver, mirroring Inferno's Dev table entry.
//
// Name resolution is lexical, so Walk never sees "." or "..". Directory
// reads return whole Dirs; the core packs them and keeps the offsets. Every
// call that may block takes the calling prog's context, so a kill can end
// it (see Park).
type Dev interface {
	// DC is the device character, as in #c.
	DC() rune
	// Name is the driver name listed in #c/drivers.
	Name() string
	Attach(ctx context.Context, spec string) (Walked, error)
	// Walk walks from directory c to its child name.
	Walk(ctx context.Context, c *Chan, name string) (Walked, error)
	Stat(ctx context.Context, c *Chan) (styx.Dir, error)
	// Open prepares c for I/O in mode and returns the Chan to use, which
	// is c unless the device redirects the open (#d). It may store
	// per-open state in c.State. The core sets Open and Mode on c.
	Open(ctx context.Context, c *Chan, mode int) (*Chan, error)
	// Read reads a file (never a directory) at off.
	Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error)
	// DirRead lists directory c, which need not be open.
	DirRead(ctx context.Context, c *Chan) ([]styx.Dir, error)
	Write(ctx context.Context, c *Chan, buf []byte, off int64) (int, error)
	// Close is called when the last reference to an open Chan goes.
	Close(c *Chan)
	// Create makes name in dir with Inferno permission bits perm (DMDIR
	// for a directory). The core opens the result.
	Create(ctx context.Context, dir *Chan, name string, perm uint32) (Walked, error)
	Remove(ctx context.Context, c *Chan) error
	// Wstat applies the non-null fields of d (see NullDir).
	Wstat(ctx context.Context, c *Chan, d *styx.Dir) error
}

// Optional device interfaces.
type (
	// Initer is called once when the device table is built.
	Initer interface{ Init(tab *DevTab) }
	// Devnoer gives the device instance an attach to spec makes, so two
	// instances never share a mount-table key.
	Devnoer interface{ Devno(spec string) uint32 }
	// MultiWalker walks several names in one step, as a 9P Twalk does.
	MultiWalker interface {
		MaxWalk() int
		WalkNames(ctx context.Context, c *Chan, names []string) (WalkedNames, error)
	}
	// Iouniter gives the largest atomic I/O, 0 for no limit.
	Iouniter interface{ Iounit(c *Chan) uint32 }
	// Mounted describes a #M root for ns: its attach name and the file
	// the conversation is on.
	Mounted interface {
		Aname(c *Chan) string
		MountSource(c *Chan) (string, bool)
	}
	// Waiter tells purgatorio.wait whether I/O would block, and wakes it.
	Waiter interface {
		Ready(c *Chan, write bool) bool
		Watch(c *Chan, write bool, r *Rendez)
		Unwatch(c *Chan, r *Rendez)
	}
)

// WalkedNames is the result of MultiWalker.WalkNames.
type WalkedNames struct {
	// Qids of the names walked, in order; at least one.
	Qids []styx.Qid
	// Aux is the device state for the last name, when Reached.
	Aux any
	// Reached means every name in Qids was walked and the walk can go on
	// from the last. Otherwise the name after the last could not be
	// walked, for Why, or when Why is nil for the reason Inferno gives a
	// short walk.
	Reached bool
	Why     error
}

// DevBase gives the defaults for the calls most devices refuse.
type DevBase struct{}

func (DevBase) Close(*Chan) {}
func (DevBase) Create(context.Context, *Chan, string, uint32) (Walked, error) {
	return Walked{}, Error(abi.Eperm)
}
func (DevBase) Remove(context.Context, *Chan) error           { return Error(abi.Eperm) }
func (DevBase) Wstat(context.Context, *Chan, *styx.Dir) error { return Error(abi.Eperm) }

// DevTab is Inferno's devtab.
type DevTab struct {
	devs []Dev
}

// NewDevTab builds the device table and calls each device's Init.
func NewDevTab(devs ...Dev) *DevTab {
	t := &DevTab{devs: devs}
	for _, d := range devs {
		if i, ok := d.(Initer); ok {
			i.Init(t)
		}
	}
	return t
}

func (t *DevTab) Lookup(dc rune) Dev {
	for _, d := range t.devs {
		if d.DC() == dc {
			return d
		}
	}
	return nil
}

func (t *DevTab) Devs() []Dev { return t.devs }

// Drivers is the text of #c/drivers.
func (t *DevTab) Drivers() string {
	var s strings.Builder
	for _, d := range t.devs {
		fmt.Fprintf(&s, "#%c %s\n", d.DC(), d.Name())
	}
	return s.String()
}

// Attach attaches to #<dc><spec> and returns the device's root Chan.
func (t *DevTab) Attach(ctx context.Context, dc rune, spec string) (*Chan, error) {
	d := t.Lookup(dc)
	if d == nil {
		return nil, Error(abi.Ebadsharp)
	}
	w, err := d.Attach(ctx, spec)
	if err != nil {
		return nil, err
	}
	var devno uint32
	if dn, ok := d.(Devnoer); ok {
		devno = dn.Devno(spec)
	}
	return NewChan(d, devno, w.Qid, "#"+string(dc)+spec, w.Aux), nil
}

// ReadStr serves a read of s at off.
func ReadStr(s []byte, buf []byte, off int64) int {
	if off < 0 || off >= int64(len(s)) {
		return 0
	}
	return copy(buf, s[off:])
}

// NullDir returns a Dir whose every field means "don't change", for wstat.
func NullDir() styx.Dir {
	return styx.Dir{
		Type: ^uint16(0), Dev: ^uint32(0),
		Qid:  styx.Qid{Type: ^uint8(0), Vers: ^uint32(0), Path: ^uint64(0)},
		Mode: ^uint32(0), Atime: ^uint32(0), Mtime: ^uint32(0), Length: ^uint64(0),
	}
}

// DirIsNull reports which fields of d are null.
func NullMode(d *styx.Dir) bool   { return d.Mode == ^uint32(0) }
func NullMtime(d *styx.Dir) bool  { return d.Mtime == ^uint32(0) }
func NullLength(d *styx.Dir) bool { return d.Length == ^uint64(0) }

// Tab is one entry of a static directory table, Inferno's Dirtab.
type Tab struct {
	Name   string
	Path   uint64
	Perm   uint32
	Length uint64
}

// TabDir returns the Dir for t in a device dc owned by user.
func TabDir(dc rune, t Tab, user string) styx.Dir {
	qt := uint8(styx.QTFILE)
	if t.Perm&styx.DMDIR != 0 {
		qt = styx.QTDIR
	}
	return styx.Dir{Type: uint16(dc), Qid: styx.Qid{Type: qt, Path: t.Path},
		Mode: t.Perm, Length: t.Length, Name: t.Name, UID: user, GID: user}
}

// TabWalk finds name in tab.
func TabWalk(tab []Tab, name string) (Walked, error) {
	for _, t := range tab {
		if t.Name == name {
			qt := uint8(styx.QTFILE)
			if t.Perm&styx.DMDIR != 0 {
				qt = styx.QTDIR
			}
			return Walked{Qid: styx.Qid{Type: qt, Path: t.Path}}, nil
		}
	}
	return Walked{}, Error(abi.Enonexist)
}

// OpenMode checks an open of a file with permission bits perm, for the
// owner, as devopen does for the device's files.
func OpenPerm(perm uint32, mode int) error {
	var need uint32
	switch mode & abi.OACCESS {
	case abi.OREAD:
		need = 0o400
	case abi.OWRITE:
		need = 0o200
	case abi.ORDWR:
		need = 0o600
	case abi.OEXEC:
		need = 0o100
	}
	if mode&abi.OTRUNC != 0 {
		need |= 0o200
	}
	if perm&need != need {
		return Error(abi.Eperm)
	}
	return nil
}
