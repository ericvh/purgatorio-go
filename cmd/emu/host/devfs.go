package host

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/styx"
)

// DevFS is #U, a host directory tree (Inferno's devfs-posix.c). Every
// file is accessed as the host user who runs the emu.
type DevFS struct {
	core.DevBase
	root  string
	devno atomic.Uint32
	// caseFold is set on hosts whose default volumes are case-insensitive
	// (macOS): a walk then checks the exact name bytes exist, as Inferno's
	// names are case- and byte-exact.
	caseFold bool
}

// NewDevFS serves root as #U.
func NewDevFS(root string) (*DevFS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &DevFS{root: abs, caseFold: runtime.GOOS == "darwin"}, nil
}

func (d *DevFS) DC() rune     { return 'U' }
func (d *DevFS) Name() string { return "fs" }

// fsinfo is a Chan's host state: its host path. Open state is in State.
type fsinfo struct {
	path string
}

type fsopen struct {
	mu   sync.Mutex
	f    *os.File
	ents []styx.Dir // directory snapshot
}

func (d *DevFS) Devno(string) uint32 { return d.devno.Load() }

func (d *DevFS) Attach(_ context.Context, spec string) (core.Walked, error) {
	if spec != "" && spec != "*" {
		return core.Walked{}, core.Error(abi.Ebadspec)
	}
	fi, err := os.Stat(d.root)
	if err != nil {
		return core.Walked{}, oserror(err)
	}
	if !fi.IsDir() {
		return core.Walked{}, core.Error(abi.Enotdir)
	}
	return core.Walked{Qid: fsqid(fi), Aux: &fsinfo{path: d.root}}, nil
}

func info(c *core.Chan) *fsinfo { return c.Aux.(*fsinfo) }

func (d *DevFS) Walk(_ context.Context, c *core.Chan, name string) (core.Walked, error) {
	if !c.IsDir() {
		return core.Walked{}, core.Error(abi.Enotdir)
	}
	next := filepath.Join(info(c).path, name)
	fi, err := os.Stat(next)
	if err != nil {
		return core.Walked{}, core.Error(abi.Enonexist)
	}
	if d.caseFold && !exactName(info(c).path, name) {
		return core.Walked{}, core.Error(abi.Enonexist)
	}
	return core.Walked{Qid: fsqid(fi), Aux: &fsinfo{path: next}}, nil
}

// exactName reports whether dir holds an entry whose name is exactly name.
func exactName(dir, name string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.Name() == name {
			return true
		}
	}
	return false
}

func fsqid(fi fs.FileInfo) styx.Qid {
	q := styx.Qid{Vers: uint32(fi.ModTime().Unix())}
	if fi.IsDir() {
		q.Type = styx.QTDIR
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		dev := uint16(st.Dev)
		if dev&0x8000 != 0 {
			dev ^= 0x8080
		}
		q.Path = uint64(dev)<<48 ^ uint64(st.Ino)
	}
	return q
}

var (
	idmu    sync.Mutex
	idnames = map[string]string{}
)

func idname(id uint32, group bool) string {
	key := strconv.FormatUint(uint64(id), 10)
	k := key
	if group {
		k = "g" + key
	}
	idmu.Lock()
	defer idmu.Unlock()
	if n, ok := idnames[k]; ok {
		return n
	}
	n := "#" + key
	if group {
		if g, err := user.LookupGroupId(key); err == nil {
			n = g.Name
		}
	} else if u, err := user.LookupId(key); err == nil {
		n = u.Username
	}
	idnames[k] = n
	return n
}

func (d *DevFS) dirconv(name string, fi fs.FileInfo, devno uint32) styx.Dir {
	q := fsqid(fi)
	dir := styx.Dir{Type: 'U', Dev: devno, Qid: q, Name: name,
		Mode: uint32(q.Type)<<24 | uint32(fi.Mode().Perm()), Mtime: uint32(fi.ModTime().Unix()),
		Length: uint64(fi.Size())}
	dir.Atime = dir.Mtime
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		dir.UID = idname(st.Uid, false)
		dir.GID = idname(st.Gid, true)
		dir.Atime = uint32(atime(st).Unix())
	}
	if fi.IsDir() {
		dir.Length = 0
	}
	return dir
}

func (d *DevFS) Stat(_ context.Context, c *core.Chan) (styx.Dir, error) {
	fi, err := os.Stat(info(c).path)
	if err != nil {
		return styx.Dir{}, oserror(err)
	}
	return d.dirconv(filepath.Base(info(c).path), fi, c.Devno), nil
}

// fsperm checks the host permission bits for the emu's user, as Inferno's
// fsperm does.
func fsperm(fi fs.FileInfo, mask uint32) error {
	m := uint32(fi.Mode().Perm())
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		switch {
		case int(st.Uid) == os.Getuid():
			m >>= 6
		case int(st.Gid) == os.Getgid() || ingroup(int(st.Gid)):
			m >>= 3
		}
	}
	if m&mask == 0 {
		return core.Error(abi.Eperm)
	}
	return nil
}

func ingroup(gid int) bool {
	gs, _ := os.Getgroups()
	for _, g := range gs {
		if g == gid {
			return true
		}
	}
	return false
}

func (d *DevFS) Open(_ context.Context, c *core.Chan, mode int) (*core.Chan, error) {
	fi, err := os.Stat(info(c).path)
	if err != nil {
		return nil, oserror(err)
	}
	if os.Getuid() != 0 {
		var masks []uint32
		switch mode & (abi.OTRUNC | 3) {
		case 0:
			masks = []uint32{4}
		case 1, 1 | 16:
			masks = []uint32{2}
		case 2, 0 | 16, 2 | 16:
			masks = []uint32{4, 2}
		case 3:
			masks = []uint32{1}
		default:
			return nil, core.Error(abi.Ebadarg)
		}
		for _, m := range masks {
			if err := fsperm(fi, m); err != nil {
				return nil, err
			}
		}
	}
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, core.Error(abi.Eperm)
		}
		c.State = &fsopen{}
		return c, nil
	}
	flag := []int{os.O_RDONLY, os.O_WRONLY, os.O_RDWR, os.O_RDONLY}[mode&3]
	if mode&abi.OTRUNC != 0 {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(info(c).path, flag, 0)
	if err != nil {
		return nil, oserror(err)
	}
	c.State = &fsopen{f: f}
	return c, nil
}

func (d *DevFS) Create(_ context.Context, dir *core.Chan, name string, perm uint32) (core.Walked, error) {
	dfi, err := os.Stat(info(dir).path)
	if err != nil {
		return core.Walked{}, oserror(err)
	}
	if os.Getuid() != 0 {
		if err := fsperm(dfi, 2); err != nil {
			return core.Walked{}, err
		}
	}
	if name == "." || name == ".." {
		return core.Walked{}, core.Error(abi.Efilename)
	}
	p := filepath.Join(info(dir).path, name)
	dmode := uint32(dfi.Mode().Perm())
	if perm&styx.DMDIR != 0 {
		perm &= ^uint32(0o777) | dmode&0o777
		if err := os.Mkdir(p, fs.FileMode(perm&0o777)); err != nil {
			return core.Walked{}, oserror(err)
		}
		_ = os.Chmod(p, fs.FileMode(perm&0o777))
	} else {
		perm &= ^uint32(0o666) | dmode&0o666
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(perm&0o777))
		if err != nil {
			return core.Walked{}, oserror(err)
		}
		_ = f.Chmod(fs.FileMode(perm & 0o777))
		f.Close()
	}
	fi, err := os.Stat(p)
	if err != nil {
		return core.Walked{}, oserror(err)
	}
	return core.Walked{Qid: fsqid(fi), Aux: &fsinfo{path: p}}, nil
}

func (d *DevFS) Close(c *core.Chan) {
	if o, ok := c.State.(*fsopen); ok && o.f != nil {
		o.f.Close()
	}
	if c.Mode&abi.ORCLOSE != 0 {
		_ = d.Remove(context.Background(), c)
	}
}

func (d *DevFS) Read(_ context.Context, c *core.Chan, buf []byte, off int64) (int, error) {
	o := c.State.(*fsopen)
	n, err := o.f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		var pe *fs.PathError
		if errors.As(err, &pe) && errors.Is(pe.Err, syscall.ESPIPE) {
			return o.f.Read(buf)
		}
		if n == 0 {
			return 0, oserror(err)
		}
	}
	return n, nil
}

func (d *DevFS) Write(_ context.Context, c *core.Chan, buf []byte, off int64) (int, error) {
	o := c.State.(*fsopen)
	n, err := o.f.WriteAt(buf, off)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) && errors.Is(pe.Err, syscall.ESPIPE) {
			return o.f.Write(buf)
		}
		// O_APPEND files refuse WriteAt; write at the end.
		if n == 0 && c.Mode&3 != abi.OREAD {
			if n, err = o.f.Write(buf); err == nil {
				return n, nil
			}
		}
		return n, oserror(err)
	}
	return n, nil
}

func (d *DevFS) DirRead(_ context.Context, c *core.Chan) ([]styx.Dir, error) {
	ents, err := os.ReadDir(info(c).path)
	if err != nil {
		return nil, oserror(err)
	}
	out := make([]styx.Dir, 0, len(ents))
	for _, e := range ents {
		if runtime.GOOS == "darwin" && (e.Name() == ".DS_Store" || len(e.Name()) > 2 && e.Name()[:2] == "._") {
			continue
		}
		fi, err := os.Stat(filepath.Join(info(c).path, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, d.dirconv(e.Name(), fi, c.Devno))
	}
	return out, nil
}

// fswchk checks the emu's user may write directory p.
func fswchk(p string) error {
	if os.Getuid() == 0 {
		return nil
	}
	fi, err := os.Stat(p)
	if err != nil {
		return oserror(err)
	}
	return fsperm(fi, 2)
}

func (d *DevFS) Remove(_ context.Context, c *core.Chan) error {
	p := info(c).path
	if err := fswchk(filepath.Dir(p)); err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		return oserror(err)
	}
	return nil
}

func (d *DevFS) Wstat(_ context.Context, c *core.Chan, dir *styx.Dir) error {
	p := info(c).path
	fi, err := os.Stat(p)
	if err != nil {
		return oserror(err)
	}
	if dir.Name != "" && dir.Name != filepath.Base(p) {
		if err := fswchk(filepath.Dir(p)); err != nil {
			return err
		}
		np := filepath.Join(filepath.Dir(p), dir.Name)
		if _, err := os.Lstat(np); err == nil {
			return core.Error(abi.Eexist)
		}
		if err := os.Rename(p, np); err != nil {
			return oserror(err)
		}
		info(c).path = np
		p = np
	}
	if !core.NullMode(dir) && dir.Mode&0o777 != uint32(fi.Mode().Perm()) {
		if err := os.Chmod(p, fs.FileMode(dir.Mode&0o777)); err != nil {
			return oserror(err)
		}
	}
	if !core.NullLength(dir) && !fi.IsDir() && dir.Length != uint64(fi.Size()) {
		if err := os.Truncate(p, int64(dir.Length)); err != nil {
			return oserror(err)
		}
	}
	if !core.NullMtime(dir) {
		t := time.Unix(int64(dir.Mtime), 0)
		at := t
		if dir.Atime != ^uint32(0) {
			at = time.Unix(int64(dir.Atime), 0)
		}
		if err := os.Chtimes(p, at, t); err != nil {
			return oserror(err)
		}
	}
	return nil
}

// oserror turns a host error into an Inferno error string. The common
// cases get Inferno's wording; others keep the host's text, as Inferno's
// oserror does.
func oserror(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return core.Error(abi.Enonexist)
	case errors.Is(err, fs.ErrPermission):
		return core.Error(abi.Eperm)
	case errors.Is(err, fs.ErrExist):
		return core.Error(abi.Eexist)
	case errors.Is(err, syscall.ENOTDIR):
		return core.Error(abi.Enotdir)
	case errors.Is(err, syscall.EISDIR):
		return core.Error(abi.Eisdir)
	case errors.Is(err, syscall.ENOTEMPTY):
		return core.Error(abi.Enotempty)
	case errors.Is(err, syscall.ENOSPC):
		return core.Error(abi.Enospace)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return core.Error(pe.Err.Error())
	}
	return core.Error(err.Error())
}
