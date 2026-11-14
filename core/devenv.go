package core

import (
	"context"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// maxenvlen is the longest value #e holds.
const maxenvlen = 16*1024 - 1

// DevEnv is #e, the calling prog's environment group (devenv.c).
type DevEnv struct {
	DevBase
	k *Kernel
}

func NewDevEnv(k *Kernel) *DevEnv { return &DevEnv{k: k} }

func (d *DevEnv) DC() rune     { return 'e' }
func (d *DevEnv) Name() string { return "env" }

func egrpOf(ctx context.Context) *Egrp {
	if p := ProgFrom(ctx); p != nil {
		return p.Egrp()
	}
	return NewEgrp()
}

func (d *DevEnv) Attach(context.Context, string) (Walked, error) {
	return Walked{Qid: styx.Qid{Type: styx.QTDIR}}, nil
}

func (d *DevEnv) Walk(ctx context.Context, c *Chan, name string) (Walked, error) {
	if !c.IsDir() {
		return Walked{}, Error(abi.Enotdir)
	}
	eg := egrpOf(ctx)
	eg.mu.Lock()
	defer eg.mu.Unlock()
	if v := eg.lookup(name); v != nil {
		return Walked{Qid: v.Qid}, nil
	}
	return Walked{}, Error(abi.Enonexist)
}

func (d *DevEnv) dir(v *Evalue) styx.Dir {
	return styx.Dir{Type: 'e', Qid: v.Qid, Mode: 0o666, Length: uint64(len(v.Val)),
		Name: v.Var, UID: d.k.Owner, GID: d.k.Owner}
}

func (d *DevEnv) Stat(ctx context.Context, c *Chan) (styx.Dir, error) {
	eg := egrpOf(ctx)
	eg.mu.Lock()
	defer eg.mu.Unlock()
	if c.IsDir() {
		return styx.Dir{Type: 'e', Qid: styx.Qid{Type: styx.QTDIR, Vers: eg.vers},
			Mode: styx.DMDIR | 0o775, Name: "#e", UID: d.k.Owner, GID: d.k.Owner}, nil
	}
	if v := eg.byPath(c.Qid.Path); v != nil {
		return d.dir(v), nil
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevEnv) DirRead(ctx context.Context, c *Chan) ([]styx.Dir, error) {
	eg := egrpOf(ctx)
	eg.mu.Lock()
	defer eg.mu.Unlock()
	out := make([]styx.Dir, 0, len(eg.entries))
	for _, v := range eg.entries {
		out = append(out, d.dir(v))
	}
	return out, nil
}

func (d *DevEnv) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, Error(abi.Eperm)
		}
		return c, nil
	}
	eg := egrpOf(ctx)
	eg.mu.Lock()
	defer eg.mu.Unlock()
	v := eg.byPath(c.Qid.Path)
	if v == nil {
		return nil, Error(abi.Enonexist)
	}
	if mode&abi.OTRUNC != 0 && v.Val != nil {
		v.Val = nil
		v.Qid.Vers++
	}
	return c, nil
}

func (d *DevEnv) Create(ctx context.Context, dir *Chan, name string, perm uint32) (Walked, error) {
	if !dir.IsDir() || perm&styx.DMDIR != 0 {
		return Walked{}, Error(abi.Eperm)
	}
	if len(name) >= 128 {
		return Walked{}, Error("name too long")
	}
	eg := egrpOf(ctx)
	eg.mu.Lock()
	defer eg.mu.Unlock()
	if eg.lookup(name) != nil {
		return Walked{}, Error(abi.Eexist)
	}
	eg.path++
	v := &Evalue{Var: name, Qid: styx.Qid{Path: eg.path}}
	eg.entries = append(eg.entries, v)
	eg.vers++
	return Walked{Qid: v.Qid}, nil
}

func (d *DevEnv) Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	eg := egrpOf(ctx)
	eg.mu.Lock()
	defer eg.mu.Unlock()
	v := eg.byPath(c.Qid.Path)
	if v == nil {
		return 0, Error(abi.Enonexist)
	}
	return ReadStr(v.Val, buf, off), nil
}

func (d *DevEnv) Write(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	ve := off + int64(len(buf))
	if ve > maxenvlen {
		return 0, Error(abi.Etoobig)
	}
	eg := egrpOf(ctx)
	eg.mu.Lock()
	defer eg.mu.Unlock()
	v := eg.byPath(c.Qid.Path)
	if v == nil {
		return 0, Error(abi.Enonexist)
	}
	if ve > int64(len(v.Val)) {
		v.Val = append(v.Val, make([]byte, ve-int64(len(v.Val)))...)
	}
	copy(v.Val[off:], buf)
	v.Qid.Vers++
	return len(buf), nil
}

func (d *DevEnv) Remove(ctx context.Context, c *Chan) error {
	if c.IsDir() {
		return Error(abi.Eperm)
	}
	eg := egrpOf(ctx)
	eg.mu.Lock()
	defer eg.mu.Unlock()
	for i, v := range eg.entries {
		if v.Qid.Path == c.Qid.Path {
			eg.entries = append(eg.entries[:i], eg.entries[i+1:]...)
			eg.vers++
			return nil
		}
	}
	return Error(abi.Enonexist)
}

func (d *DevEnv) Close(c *Chan) {
	// ORCLOSE needs the prog's egrp, which a close no longer has; Inferno
	// removes the variable from the closing prog's group. Rarely used.
}
