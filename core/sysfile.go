package core

import (
	"context"
	"encoding/binary"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// The Sys file operations (Inferno's sysfile.c), as methods on Prog. They
// return Inferno error strings as Error values.

// Namec resolves path in the prog's name space without opening it.
func (p *Prog) Namec(path string) (*Chan, error) {
	return p.Pgrp().Namec(p.Ctx, path, p.Dot())
}

// OpenChan resolves and opens path.
func (p *Prog) OpenChan(path string, mode int) (*Chan, error) {
	c, err := p.Namec(path)
	if err != nil {
		return nil, err
	}
	return OpenC(p.Ctx, c, mode)
}

// Open is Sys->open: the lowest free fd.
func (p *Prog) Open(path string, mode int) (int, error) {
	c, err := p.OpenChan(path, mode)
	if err != nil {
		return -1, err
	}
	return p.allocFd(c)
}

func (p *Prog) allocFd(c *Chan) (int, error) {
	fd, err := p.Fgrp().Alloc(c)
	if err != nil {
		c.DecRef()
	}
	return fd, err
}

// CreateChan creates path, or truncates it when it exists, and opens it.
func (p *Prog) CreateChan(path string, mode int, perm uint32) (*Chan, error) {
	// As in Inferno, an existing name is opened and truncated, so >
	// works on device files such as /dev/null.
	if c, err := p.Namec(path); err == nil {
		if perm&styx.DMDIR != 0 {
			return nil, Error(abi.Eexist)
		}
		return OpenC(p.Ctx, c, mode|abi.OTRUNC)
	}
	dir, name, err := p.Pgrp().CreateTarget(p.Ctx, path, p.Dot())
	if err != nil {
		return nil, err
	}
	if perm&styx.DMDIR != 0 && mode&abi.OACCESS != abi.OREAD {
		return nil, Error(abi.Eisdir)
	}
	w, err := dir.Dev.Create(p.Ctx, dir, name, perm)
	if err != nil {
		return nil, err
	}
	c := NewChan(dir.Dev, dir.Devno, w.Qid, Absolute(path, p.Dot()), w.Aux)
	return OpenC(p.Ctx, c, mode&^abi.OTRUNC)
}

// Create is Sys->create.
func (p *Prog) Create(path string, mode int, perm uint32) (int, error) {
	c, err := p.CreateChan(path, mode, perm)
	if err != nil {
		return -1, err
	}
	return p.allocFd(c)
}

func (p *Prog) Remove(path string) error {
	c, err := p.Namec(path)
	if err != nil {
		return err
	}
	return c.Dev.Remove(p.Ctx, c)
}

func (p *Prog) Wstat(path string, d *styx.Dir) error {
	c, err := p.Namec(path)
	if err != nil {
		return err
	}
	return c.Dev.Wstat(p.Ctx, c, d)
}

func (p *Prog) Fwstat(fd int, d *styx.Dir) error {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return err
	}
	return c.Dev.Wstat(p.Ctx, c, d)
}

func (p *Prog) Close(fd int) error { return p.Fgrp().Close(fd) }

// Chan returns the Chan open on fd.
func (p *Prog) Chan(fd int) (*Chan, error) { return p.Fgrp().Get(fd) }

// Read is Sys->read, advancing the fd's offset. Reading a directory gives
// whole packed Dirs.
func (p *Prog) Read(fd int, buf []byte) (int, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return 0, err
	}
	if err := checkAccess(c, abi.OREAD); err != nil {
		return 0, err
	}
	// The offset is not locked across the device read, which may block.
	off := c.Offset()
	var n int
	if c.IsDir() {
		n, err = dirRead(p.Ctx, c, buf, off)
	} else {
		n, err = c.Dev.Read(p.Ctx, c, buf, off)
	}
	if err != nil {
		return 0, err
	}
	c.addOffset(int64(n))
	return n, nil
}

// Pread reads at off without moving the fd's offset.
func (p *Prog) Pread(fd int, buf []byte, off int64) (int, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return 0, err
	}
	if err := checkAccess(c, abi.OREAD); err != nil {
		return 0, err
	}
	if c.IsDir() {
		return 0, Error(abi.Eisdir)
	}
	if off < 0 {
		return 0, Error(abi.Enegoff)
	}
	return c.Dev.Read(p.Ctx, c, buf, off)
}

func (p *Prog) Write(fd int, buf []byte) (int, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return 0, err
	}
	if err := checkAccess(c, abi.OWRITE); err != nil {
		return 0, err
	}
	if c.IsDir() {
		return 0, Error(abi.Eisdir)
	}
	n, err := c.Dev.Write(p.Ctx, c, buf, c.Offset())
	if err != nil {
		return 0, err
	}
	c.addOffset(int64(n))
	return n, nil
}

// Pwrite writes at off without moving the fd's offset.
func (p *Prog) Pwrite(fd int, buf []byte, off int64) (int, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return 0, err
	}
	if err := checkAccess(c, abi.OWRITE); err != nil {
		return 0, err
	}
	if c.IsDir() {
		return 0, Error(abi.Eisdir)
	}
	if off < 0 {
		return 0, Error(abi.Enegoff)
	}
	return c.Dev.Write(p.Ctx, c, buf, off)
}

// Seek is Sys->seek, as Inferno's kseek.
func (p *Prog) Seek(fd int, off int64, whence int) (int64, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return 0, err
	}
	if c.Dev.DC() == '|' {
		return 0, Error(abi.Enotseek)
	}
	switch whence {
	case 0:
		if c.IsDir() && off != 0 {
			return 0, Error(abi.Eisdir)
		}
	case 1:
		if c.IsDir() {
			return 0, Error(abi.Eisdir)
		}
		off += c.Offset()
	case 2:
		if c.IsDir() {
			return 0, Error(abi.Eisdir)
		}
		d, err := c.Dev.Stat(p.Ctx, c)
		if err != nil {
			return 0, err
		}
		off += int64(d.Length)
	default:
		return 0, Error(abi.Ebadarg)
	}
	if off < 0 {
		return 0, Error(abi.Enegoff)
	}
	c.SetOffset(off)
	return off, nil
}

func (p *Prog) Stat(path string) (styx.Dir, error) {
	c, err := p.Namec(path)
	if err != nil {
		return styx.Dir{}, err
	}
	return statNamed(p.Ctx, c)
}

func (p *Prog) Fstat(fd int) (styx.Dir, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return styx.Dir{}, err
	}
	return statNamed(p.Ctx, c)
}

// Dirread returns the next whole packed entries of the directory on fd.
func (p *Prog) Dirread(fd int, buf []byte) (int, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return 0, err
	}
	if !c.IsDir() {
		return 0, Error(abi.Enotdir)
	}
	return p.Read(fd, buf)
}

// Dirents returns every entry of the directory open on fd.
func (p *Prog) Dirents(fd int) ([]styx.Dir, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return nil, err
	}
	if !c.IsDir() {
		return nil, Error(abi.Enotdir)
	}
	return unionDirents(p.Ctx, c)
}

// Bind is Sys->bind.
func (p *Prog) Bind(new, old string, flags uint32) error {
	d := p.Dot()
	return p.Pgrp().Bind(p.Ctx, Absolute(new, d), Absolute(old, d), flags)
}

// Unmount is Sys->unmount; an empty name removes everything on old.
func (p *Prog) Unmount(name, old string) error {
	d := p.Dot()
	if name != "" {
		name = Absolute(name, d)
	}
	return p.Pgrp().Unmount(p.Ctx, name, Absolute(old, d))
}

func (p *Prog) Chdir(path string) error { return p.Pgrp().Chdir(p.Ctx, path) }

// Fd2path is the name the fd's file was opened by.
func (p *Prog) Fd2path(fd int) (string, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return "", err
	}
	return c.Name, nil
}

// Dup is Sys->dup; new = -1 takes the lowest free fd.
func (p *Prog) Dup(old, new int) (int, error) {
	f := p.Fgrp()
	c, err := f.Get(old)
	if err != nil {
		return -1, err
	}
	if c.Qid.Type&styx.QTAUTH != 0 {
		return -1, Error(abi.Eperm)
	}
	if new < 0 {
		return p.allocFd(c.IncRef())
	}
	if err := f.Install(new, c.IncRef()); err != nil {
		c.DecRef()
		return -1, err
	}
	return new, nil
}

// Iounit is Sys->iounit.
func (p *Prog) Iounit(fd int) (uint32, error) {
	c, err := p.Fgrp().Get(fd)
	if err != nil {
		return 0, err
	}
	if iu, ok := c.Dev.(Iouniter); ok {
		return iu.Iounit(c), nil
	}
	return 0, nil
}

// Pipe is Sys->pipe: two fds on the ends of a new #| pipe.
func (p *Prog) Pipe() ([2]int, error) {
	dir, err := p.Namec("#|")
	if err != nil {
		return [2]int{}, err
	}
	end := func(name string) (*Chan, error) {
		w, err := dir.Dev.Walk(p.Ctx, dir, name)
		if err != nil {
			return nil, err
		}
		return OpenC(p.Ctx, NewChan(dir.Dev, dir.Devno, w.Qid, "#|/"+name, w.Aux), abi.ORDWR)
	}
	d0, err := end("data")
	if err != nil {
		return [2]int{}, err
	}
	d1, err := end("data1")
	if err != nil {
		d0.DecRef()
		return [2]int{}, err
	}
	fd0, err := p.allocFd(d0)
	if err != nil {
		d1.DecRef()
		return [2]int{}, err
	}
	fd1, err := p.allocFd(d1)
	if err != nil {
		_ = p.Close(fd0)
		return [2]int{}, err
	}
	return [2]int{fd0, fd1}, nil
}

// streamMax is the largest buffer Stream allocates.
const streamMax = 1 << 20

// Stream is Sys->stream: copy src to dst until end of file, an error or a
// kill, in bufsiz pieces. It returns the bytes dst took and the error that
// ended the copy, if any, for the errstr.
func (p *Prog) Stream(src, dst, bufsiz int) (int64, error, error) {
	if bufsiz < 0 {
		return 0, nil, Error(abi.Ebadarg)
	}
	if _, err := p.Fgrp().Get(src); err != nil {
		return 0, nil, err
	}
	if _, err := p.Fgrp().Get(dst); err != nil {
		return 0, nil, err
	}
	buf := make([]byte, min(bufsiz, streamMax))
	var total int64
	for len(buf) > 0 {
		if Interrupted(p.Ctx) {
			return total, Error(abi.Eintr), nil
		}
		n, err := p.Read(src, buf)
		if err != nil {
			return total, err, nil
		}
		if n == 0 {
			break
		}
		w, err := p.Write(dst, buf[:n])
		total += int64(w)
		if err != nil {
			return total, err, nil
		}
		if w != n {
			break
		}
	}
	return total, nil, nil
}

// OpenC opens an unopened Chan in mode.
func OpenC(ctx context.Context, c *Chan, mode int) (*Chan, error) {
	access := mode & abi.OACCESS
	if c.IsDir() && (access != abi.OREAD || mode&abi.OTRUNC != 0) {
		return nil, Error(abi.Eisdir)
	}
	nc, err := c.Dev.Open(ctx, c, mode)
	if err != nil {
		return nil, err
	}
	if nc == c {
		c.Mode = mode
		c.Open = true
	}
	return nc, nil
}

func checkAccess(c *Chan, want int) error {
	have := c.Mode & abi.OACCESS
	ok := false
	if want == abi.OREAD {
		ok = have == abi.OREAD || have == abi.ORDWR || have == abi.OEXEC
	} else {
		ok = have == abi.OWRITE || have == abi.ORDWR
	}
	if c.Open && ok {
		return nil
	}
	return Error(abi.Ebadusefd)
}

func statNamed(ctx context.Context, c *Chan) (styx.Dir, error) {
	d, err := c.Dev.Stat(ctx, c)
	if err != nil {
		return d, err
	}
	if c.Name == "/" {
		d.Name = "/"
	}
	return d, nil
}

func unionDirents(ctx context.Context, c *Chan) ([]styx.Dir, error) {
	if c.Union == nil {
		return c.Dev.DirRead(ctx, c)
	}
	var all []styx.Dir
	for _, m := range c.Union {
		ds, err := m.Chan.Dev.DirRead(ctx, m.Chan)
		if err != nil {
			return nil, err
		}
		all = append(all, ds...)
	}
	return all, nil
}

// dirRead serves a directory read at off from a snapshot taken when the
// read starts at 0. Only whole entries are returned.
func dirRead(ctx context.Context, c *Chan, buf []byte, off int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if off == 0 {
		c.dirbuf = c.dirbuf[:0]
		c.mu.Unlock()
		ds, err := unionDirents(ctx, c)
		c.mu.Lock()
		if err != nil {
			return 0, err
		}
		for i := range ds {
			c.dirbuf = styx.MarshalDir(c.dirbuf, &ds[i])
		}
	}
	if off > int64(len(c.dirbuf)) {
		return 0, nil
	}
	rest := c.dirbuf[off:]
	n := 0
	for n+2 <= len(rest) {
		l := int(binary.LittleEndian.Uint16(rest[n:])) + 2
		if n+l > len(buf) {
			break
		}
		n += l
	}
	if n == 0 && len(rest) > 0 {
		return 0, Error(abi.Eshortstat)
	}
	copy(buf, rest[:n])
	return n, nil
}
