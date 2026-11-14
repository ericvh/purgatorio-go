package core

import (
	"sync"

	"github.com/ericvh/purgatorio-go/styx"
)

// Evalue is one environment variable.
type Evalue struct {
	Var string
	Val []byte
	Qid styx.Qid
}

// Egrp is a group of environment variables, Inferno's Egrp, served by #e.
type Egrp struct {
	mu      sync.Mutex
	path    uint64
	vers    uint32
	entries []*Evalue
}

func NewEgrp() *Egrp { return &Egrp{} }

// Fork returns a copy (FORKENV), as egrpcpy.
func (e *Egrp) Fork() *Egrp {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := &Egrp{path: e.path, vers: e.vers}
	for _, v := range e.entries {
		n.entries = append(n.entries, &Evalue{Var: v.Var, Val: append([]byte(nil), v.Val...), Qid: v.Qid})
	}
	return n
}

func (e *Egrp) lookup(name string) *Evalue {
	for _, v := range e.entries {
		if v.Var == name {
			return v
		}
	}
	return nil
}

func (e *Egrp) byPath(path uint64) *Evalue {
	for _, v := range e.entries {
		if v.Qid.Path == path {
			return v
		}
	}
	return nil
}

// Get returns name's value.
func (e *Egrp) Get(name string) ([]byte, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v := e.lookup(name); v != nil {
		return append([]byte(nil), v.Val...), true
	}
	return nil, false
}

// Set sets name, creating it at the end when needed.
func (e *Egrp) Set(name string, val []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := e.lookup(name)
	if v == nil {
		e.path++
		v = &Evalue{Var: name, Qid: styx.Qid{Path: e.path}}
		e.entries = append(e.entries, v)
		e.vers++
	}
	v.Val = append([]byte(nil), val...)
	v.Qid.Vers++
}

// Remove deletes name.
func (e *Egrp) Remove(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, v := range e.entries {
		if v.Var == name {
			e.entries = append(e.entries[:i], e.entries[i+1:]...)
			e.vers++
			return true
		}
	}
	return false
}

// Entries returns a snapshot of the variables, in creation order.
func (e *Egrp) Entries() []Evalue {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Evalue, len(e.entries))
	for i, v := range e.entries {
		out[i] = Evalue{Var: v.Var, Val: append([]byte(nil), v.Val...), Qid: v.Qid}
	}
	return out
}

// Strings returns the environment as name=value strings, for WASI.
func (e *Egrp) Strings() []string {
	var out []string
	for _, v := range e.Entries() {
		out = append(out, v.Var+"="+string(v.Val))
	}
	return out
}
