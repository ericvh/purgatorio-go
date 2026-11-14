package core

import (
	"sync"
	"sync/atomic"

	"github.com/ericvh/purgatorio-go/abi"
)

// NFD is the most file descriptors an Fgrp holds.
const NFD = 1024

// Fgrp is a file descriptor table, Inferno's Fgrp. Each fd owns one
// reference to its Chan.
type Fgrp struct {
	mu  sync.Mutex
	fds []*Chan
	// refs counts the progs sharing this table; the last Release closes
	// every fd.
	refs atomic.Int32
}

func NewFgrp() *Fgrp {
	f := &Fgrp{}
	f.refs.Store(1)
	return f
}

// Share adds a prog sharing the table.
func (f *Fgrp) Share() *Fgrp {
	f.refs.Add(1)
	return f
}

// Release drops a sharer; the last one closes every fd.
func (f *Fgrp) Release() {
	if f.refs.Add(-1) == 0 {
		f.Clear()
	}
}

// Fork returns a copy sharing every open Chan (FORKFD).
func (f *Fgrp) Fork() *Fgrp {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := NewFgrp()
	n.fds = make([]*Chan, len(f.fds))
	for i, c := range f.fds {
		if c != nil {
			n.fds[i] = c.IncRef()
		}
	}
	return n
}

// Keep returns a table holding only the fds in keep, at the same numbers
// (NEWFD).
func (f *Fgrp) Keep(keep []int) *Fgrp {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := NewFgrp()
	for _, fd := range keep {
		if fd < 0 || fd >= len(f.fds) || f.fds[fd] == nil {
			continue
		}
		for len(n.fds) <= fd {
			n.fds = append(n.fds, nil)
		}
		if n.fds[fd] == nil {
			n.fds[fd] = f.fds[fd].IncRef()
		}
	}
	return n
}

// Alloc installs c, taking its reference, at the lowest free fd.
func (f *Fgrp) Alloc(c *Chan) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.fds {
		if x == nil {
			f.fds[i] = c
			return i, nil
		}
	}
	if len(f.fds) >= NFD {
		return -1, Error(abi.Enofd)
	}
	f.fds = append(f.fds, c)
	return len(f.fds) - 1, nil
}

// Install puts c, taking its reference, at fd, closing what was there.
func (f *Fgrp) Install(fd int, c *Chan) error {
	if fd < 0 || fd >= NFD {
		return Error(abi.Ebadfd)
	}
	f.mu.Lock()
	for len(f.fds) <= fd {
		f.fds = append(f.fds, nil)
	}
	old := f.fds[fd]
	f.fds[fd] = c
	f.mu.Unlock()
	if old != nil {
		old.DecRef()
	}
	return nil
}

// Get returns the Chan at fd without taking a reference.
func (f *Fgrp) Get(fd int) (*Chan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fd < 0 || fd >= len(f.fds) || f.fds[fd] == nil {
		return nil, Error(abi.Ebadfd)
	}
	return f.fds[fd], nil
}

// Close closes fd.
func (f *Fgrp) Close(fd int) error {
	f.mu.Lock()
	if fd < 0 || fd >= len(f.fds) || f.fds[fd] == nil {
		f.mu.Unlock()
		return Error(abi.Ebadfd)
	}
	old := f.fds[fd]
	f.fds[fd] = nil
	for len(f.fds) > 0 && f.fds[len(f.fds)-1] == nil {
		f.fds = f.fds[:len(f.fds)-1]
	}
	f.mu.Unlock()
	// Closing may take a device's own locks (a pipe end); do it outside.
	old.DecRef()
	return nil
}

// Fds lists the open fds, lowest first.
func (f *Fgrp) Fds() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for i, c := range f.fds {
		if c != nil {
			out = append(out, i)
		}
	}
	return out
}

// Clear closes every fd.
func (f *Fgrp) Clear() {
	f.mu.Lock()
	old := f.fds
	f.fds = nil
	f.mu.Unlock()
	for _, c := range old {
		if c != nil {
			c.DecRef()
		}
	}
}
