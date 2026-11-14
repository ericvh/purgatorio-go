package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// DevDup is #d: for each open fd n, a file "n" that opens a copy of the
// fd and a file "nctl" that describes it (devdup.c).
type DevDup struct {
	DevBase
	k *Kernel
}

func NewDevDup(k *Kernel) *DevDup { return &DevDup{k: k} }

func (d *DevDup) DC() rune     { return 'd' }
func (d *DevDup) Name() string { return "dup" }

// Qid paths are 2*fd+1 for "n" and 2*fd+2 for "nctl"; 0 is the directory.
func (d *DevDup) Attach(context.Context, string) (Walked, error) {
	return Walked{Qid: styx.Qid{Type: styx.QTDIR}}, nil
}

func fgrpOf(ctx context.Context) *Fgrp {
	if p := ProgFrom(ctx); p != nil {
		return p.Fgrp()
	}
	return NewFgrp()
}

var dupperm = [4]uint32{0o400, 0o200, 0o600, 0}

func (d *DevDup) gen(ctx context.Context, fd int, ctl bool) (styx.Dir, bool) {
	f, err := fgrpOf(ctx).Get(fd)
	if err != nil {
		return styx.Dir{}, false
	}
	name, perm, path := strconv.Itoa(fd), dupperm[f.Mode&3], uint64(2*fd+1)
	if ctl {
		name, perm, path = name+"ctl", 0o400, path+1
	}
	return styx.Dir{Type: 'd', Qid: styx.Qid{Path: path}, Mode: perm, Name: name,
		UID: d.k.Owner, GID: d.k.Owner}, true
}

func (d *DevDup) Walk(ctx context.Context, c *Chan, name string) (Walked, error) {
	if !c.IsDir() {
		return Walked{}, Error(abi.Enotdir)
	}
	s, ctl := strings.CutSuffix(name, "ctl")
	fd, err := strconv.Atoi(s)
	if err != nil || fd < 0 || strconv.Itoa(fd) != s {
		return Walked{}, Error(abi.Enonexist)
	}
	dir, ok := d.gen(ctx, fd, ctl)
	if !ok {
		return Walked{}, Error(abi.Enonexist)
	}
	return Walked{Qid: dir.Qid}, nil
}

func (d *DevDup) Stat(ctx context.Context, c *Chan) (styx.Dir, error) {
	if c.IsDir() {
		return styx.Dir{Type: 'd', Qid: c.Qid, Mode: styx.DMDIR | 0o555, Name: ".",
			UID: d.k.Owner, GID: d.k.Owner}, nil
	}
	t := int(c.Qid.Path) - 1
	if dir, ok := d.gen(ctx, t/2, t&1 == 1); ok {
		return dir, nil
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevDup) DirRead(ctx context.Context, _ *Chan) ([]styx.Dir, error) {
	var out []styx.Dir
	for _, fd := range fgrpOf(ctx).Fds() {
		for _, ctl := range []bool{false, true} {
			if dir, ok := d.gen(ctx, fd, ctl); ok {
				out = append(out, dir)
			}
		}
	}
	return out, nil
}

// fdtochan checks an fd's Chan against a requested mode, as Inferno's
// fdtochan does.
func fdtochan(f *Fgrp, fd, mode int) (*Chan, error) {
	c, err := f.Get(fd)
	if err != nil {
		return nil, err
	}
	if mode < 0 || c.Mode&3 == abi.ORDWR {
		return c, nil
	}
	if mode&abi.OTRUNC != 0 && c.Mode&3 == abi.OREAD {
		return nil, Error(abi.Ebadusefd)
	}
	if mode&^abi.OTRUNC != c.Mode&3 {
		return nil, Error(abi.Ebadusefd)
	}
	return c, nil
}

func (d *DevDup) Open(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, Error(abi.Eisdir)
		}
		return c, nil
	}
	t := int(c.Qid.Path) - 1
	if t&1 == 1 {
		if mode&abi.OACCESS != abi.OREAD {
			return nil, Error(abi.Eperm)
		}
		return c, nil
	}
	f, err := fdtochan(fgrpOf(ctx), t/2, mode&(abi.OACCESS|abi.OTRUNC))
	if err != nil {
		return nil, err
	}
	return f.IncRef(), nil
}

func (d *DevDup) Read(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	t := int(c.Qid.Path) - 1
	f, err := fgrpOf(ctx).Get(t / 2)
	if err != nil {
		return 0, err
	}
	return ReadStr([]byte(FdLine(f, t/2)), buf, off), nil
}

func (d *DevDup) Write(context.Context, *Chan, []byte, int64) (int, error) {
	return 0, Error(abi.Eperm)
}

// FdLine describes an open fd as Inferno's progfdprint does, for #d and
// /prog/n/fd.
func FdLine(c *Chan, fd int) string {
	modes := [4]string{"r ", "w ", "rw", "  "}
	var iounit uint32
	if iu, ok := c.Dev.(Iouniter); ok {
		iounit = iu.Iounit(c)
	}
	return fmt.Sprintf("%3d %s %c %4d (%.16x %d %.2x) %5d %8d %s\n",
		fd, modes[c.Mode&3], c.Dev.DC(), c.Devno, c.Qid.Path, c.Qid.Vers, c.Qid.Type,
		iounit, c.Offset(), c.Name)
}
