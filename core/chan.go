package core

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ericvh/purgatorio-go/styx"
)

// Member is one directory of a union, with the bind flags it was added
// with.
type Member struct {
	Chan  *Chan
	Flags uint32
	// ID orders binds and mounts as Inferno's mountid does; ns lists them
	// in this order. A forked name space keeps its parent's ids.
	ID uint64
}

// CanCreate reports whether create in the union may use this member.
func (m Member) CanCreate() bool { return m.Flags&mcreate != 0 }

// Key is a Chan's mount-table identity: Inferno's eqchan without the qid
// version.
type Key struct {
	DC    rune
	Devno uint32
	Path  uint64
}

// Chan is an open or walkable reference to a file, as Inferno's Chan.
//
// A Chan is made by attach or walk (unopened) and becomes open through
// Dev.Open. An open Chan is reference counted: each fd holding it owns a
// reference, and the last DecRef calls Dev.Close.
type Chan struct {
	Dev   Dev
	Devno uint32
	Qid   styx.Qid
	// Name is the name this Chan was reached by, for fd2path.
	Name string
	// Aux is device-private data set at attach or walk; clones of an
	// unopened Chan share it.
	Aux any
	// Mode is the open mode; meaningful only when Open is set.
	Mode int
	Open bool
	// Union holds the members of the union directory this Chan was
	// resolved to, in search order, when the name crossed a mount point.
	// The first member is the Chan itself.
	Union []Member

	mu     sync.Mutex
	offset int64
	// State is per-open device state, set by Dev.Open.
	State  any
	dirbuf []byte
	refs   atomic.Int32
}

// NewChan returns an unopened Chan with one reference.
func NewChan(d Dev, devno uint32, qid styx.Qid, name string, aux any) *Chan {
	c := &Chan{Dev: d, Devno: devno, Qid: qid, Name: name, Aux: aux}
	c.refs.Store(1)
	return c
}

// CloneUnopened returns a fresh unopened Chan for the same file.
func (c *Chan) CloneUnopened() *Chan {
	n := NewChan(c.Dev, c.Devno, c.Qid, c.Name, c.Aux)
	n.Union = c.Union
	return n
}

func (c *Chan) Key() Key { return Key{c.Dev.DC(), c.Devno, c.Qid.Path} }

func (c *Chan) IsDir() bool { return c.Qid.Type&styx.QTDIR != 0 }

// Offset and SetOffset give the fd's file offset.
func (c *Chan) Offset() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offset
}

func (c *Chan) SetOffset(o int64) {
	c.mu.Lock()
	c.offset = o
	c.mu.Unlock()
}

// addOffset adds n to the offset and returns the offset before.
func (c *Chan) addOffset(n int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	o := c.offset
	c.offset += n
	return o
}

// IncRef adds a reference.
func (c *Chan) IncRef() *Chan {
	c.refs.Add(1)
	return c
}

// DecRef drops a reference; the last one closes an open Chan.
func (c *Chan) DecRef() {
	if c.refs.Add(-1) == 0 && c.Open {
		c.Dev.Close(c)
	}
}

func (c *Chan) String() string {
	return fmt.Sprintf("Chan(#%c %s %v open=%v)", c.Dev.DC(), c.Name, c.Qid, c.Open)
}
