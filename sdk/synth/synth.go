// Package synth is a small synthetic file server: auth/keyfs and
// auth/factotum describe their files with Files, and Serve speaks Styx
// for them. Start runs the server as a second prog (a guest cannot serve
// the mount it is waiting on) and mounts it, passing the server a secret
// on a pipe rather than in its arguments.
package synth

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

// The errors the server itself gives, as Inferno words them.
const (
	Enonexist = "file does not exist"
	Eperm     = "permission denied"
	Ebadfid   = "bad fid"
	Einuse    = "fid in use"
	Eopen     = "fid already opened"
	Enotdir   = "not a directory"
)

// Files is a tree of synthetic files. A path is the names below the
// root: nil is the root, ["alice", "secret"] a file. fid lets a file
// keep per-open state (factotum's rpc conversation).
type Files interface {
	// Lookup reports whether there is something at path and whether it
	// is a directory.
	Lookup(path []string) (exists, dir bool)
	List(dir []string) []string
	Read(path []string, fid uint32, off uint64, count uint32) ([]byte, error)
	Write(path []string, fid uint32, off uint64, data []byte) (uint32, error)
	Create(dir []string, name string, perm uint32) error
	Remove(path []string) error
	Clunk(path []string, fid uint32)
}

// Perms is implemented by Files that give other permissions than 0700
// for directories and 0600 for files.
type Perms interface {
	Perm(path []string) uint32
}

type fid struct {
	path []string
	open bool
	snap []byte // packed directory entries, rebuilt at offset 0
}

type server struct {
	f     Files
	rw    io.ReadWriter
	owner string
	fids  map[uint32]*fid
	msize uint32
	buf   []byte
}

func qpath(path []string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for _, p := range path {
		for _, b := range append([]byte(p), 0) {
			h ^= uint64(b)
			h *= 0x100000001b3
		}
	}
	return h
}

func (s *server) dir(path []string) (styx.Dir, bool) {
	ok, isdir := s.f.Lookup(path)
	if !ok {
		return styx.Dir{}, false
	}
	name := "/"
	if len(path) > 0 {
		name = path[len(path)-1]
	}
	perm := uint32(0o600)
	if isdir {
		perm = 0o700
	}
	if p, ok := s.f.(Perms); ok {
		perm = p.Perm(path)
	}
	d := styx.Dir{Name: name, UID: s.owner, GID: s.owner, MUID: s.owner, Mode: perm, Qid: styx.Qid{Path: qpath(path)}}
	if isdir {
		d.Mode |= sys.DMDIR
		d.Qid.Type = styx.QTDIR
	}
	return d, true
}

// Serve speaks Styx for f on rw until the client hangs up.
func Serve(rw io.ReadWriter, f Files, owner string) {
	s := &server{f: f, rw: rw, owner: owner, fids: map[uint32]*fid{}, msize: 8192}
	for {
		t, err := s.read()
		if err != nil {
			return
		}
		r := s.handle(t)
		if b, err := styx.Marshal(nil, r); err == nil {
			rw.Write(b)
		}
	}
}

func (s *server) read() (*styx.Fcall, error) {
	tmp := make([]byte, 8192)
	for {
		if n := styx.Size(s.buf); n != 0 && int(n) <= len(s.buf) {
			msg := s.buf[:n]
			s.buf = append([]byte(nil), s.buf[n:]...)
			return styx.Unmarshal(msg, 0)
		}
		k, err := s.rw.Read(tmp)
		if k == 0 || err != nil {
			return nil, io.EOF
		}
		s.buf = append(s.buf, tmp[:k]...)
	}
}

func rerr(t *styx.Fcall, e string) *styx.Fcall {
	return &styx.Fcall{Type: styx.Rerror, Tag: t.Tag, Ename: e}
}

func (s *server) handle(t *styx.Fcall) *styx.Fcall {
	r := &styx.Fcall{Type: t.Type + 1, Tag: t.Tag}
	c := s.fids[t.Fid]
	switch t.Type {
	case styx.Tversion:
		s.msize = min(t.Msize, 64*1024)
		r.Msize, r.Version = s.msize, styx.Version
		s.fids = map[uint32]*fid{}
	case styx.Tauth:
		return rerr(t, "authentication not required")
	case styx.Tflush:
	case styx.Tattach:
		if c != nil {
			return rerr(t, Einuse)
		}
		s.fids[t.Fid] = &fid{}
		r.Qid = styx.Qid{Type: styx.QTDIR, Path: qpath(nil)}
	case styx.Twalk:
		if c == nil {
			return rerr(t, Ebadfid)
		}
		if c.open {
			return rerr(t, Eopen)
		}
		if t.Newfid != t.Fid && s.fids[t.Newfid] != nil {
			return rerr(t, Einuse)
		}
		path := append([]string(nil), c.path...)
		for i, n := range t.Wname {
			if n == ".." {
				if len(path) > 0 {
					path = path[:len(path)-1]
				}
			} else {
				if ok, isdir := s.f.Lookup(path); !ok || !isdir {
					if i == 0 {
						return rerr(t, Enotdir)
					}
					return r
				}
				path = append(path, n)
			}
			d, ok := s.dir(path)
			if !ok {
				if i == 0 {
					return rerr(t, Enonexist)
				}
				return r
			}
			r.Wqid = append(r.Wqid, d.Qid)
		}
		s.fids[t.Newfid] = &fid{path: path}
	case styx.Topen, styx.Tcreate:
		if c == nil {
			return rerr(t, Ebadfid)
		}
		if c.open {
			return rerr(t, Eopen)
		}
		if t.Type == styx.Tcreate {
			if err := s.f.Create(c.path, t.Name, t.Perm); err != nil {
				return rerr(t, err.Error())
			}
			c.path = append(append([]string(nil), c.path...), t.Name)
		}
		d, ok := s.dir(c.path)
		if !ok {
			return rerr(t, Enonexist)
		}
		c.open, c.snap = true, nil
		r.Qid, r.Iounit = d.Qid, s.msize-styx.IOHDRSZ
	case styx.Tread:
		if c == nil || !c.open {
			return rerr(t, Ebadfid)
		}
		if _, isdir := s.f.Lookup(c.path); isdir {
			if t.Offset == 0 || c.snap == nil {
				c.snap = nil
				for _, n := range s.f.List(c.path) {
					if d, ok := s.dir(append(append([]string(nil), c.path...), n)); ok {
						c.snap = styx.MarshalDir(c.snap, &d)
					}
				}
			}
			r.Data = dirSlice(c.snap, t.Offset, t.Count)
			return r
		}
		b, err := s.f.Read(c.path, t.Fid, t.Offset, t.Count)
		if err != nil {
			return rerr(t, err.Error())
		}
		r.Data = b
	case styx.Twrite:
		if c == nil || !c.open {
			return rerr(t, Ebadfid)
		}
		n, err := s.f.Write(c.path, t.Fid, t.Offset, t.Data)
		if err != nil {
			return rerr(t, err.Error())
		}
		r.Count = n
	case styx.Tclunk, styx.Tremove:
		if c == nil {
			return rerr(t, Ebadfid)
		}
		delete(s.fids, t.Fid)
		if c.open {
			s.f.Clunk(c.path, t.Fid)
		}
		if t.Type == styx.Tremove {
			if len(c.path) == 0 {
				return rerr(t, Eperm)
			}
			if err := s.f.Remove(c.path); err != nil {
				return rerr(t, err.Error())
			}
		}
	case styx.Tstat:
		if c == nil {
			return rerr(t, Ebadfid)
		}
		d, ok := s.dir(c.path)
		if !ok {
			return rerr(t, Enonexist)
		}
		r.Stat = styx.MarshalDir(nil, &d)
	case styx.Twstat:
		// Truncation and the like are accepted and ignored.
	default:
		return rerr(t, "invalid T-message type")
	}
	return r
}

// dirSlice is the whole entries of packed dirs from off, at most count
// bytes.
func dirSlice(b []byte, off uint64, count uint32) []byte {
	if off >= uint64(len(b)) {
		return nil
	}
	b = b[off:]
	n := 0
	for n+2 <= len(b) {
		sz := int(b[n]) | int(b[n+1])<<8 + 2
		if n+sz > int(count) || n+sz > len(b) {
			break
		}
		n += sz
	}
	return b[:n]
}

// Start runs prog with args as the server, gives it secret on its fd 1
// (a pipe), and mounts it at mtpt. The server reads the secret with
// Secret and calls Serve on fd 0.
func Start(prog string, args []string, mtpt string, secret []byte) error {
	p, err := sys.Pipe()
	if err != nil {
		return err
	}
	k, err := sys.Pipe()
	if err != nil {
		return err
	}
	if _, err := sys.Spawn(prog, args, sys.NEWFD|sys.NEWPGRP, [][2]int{{0, p[0]}, {1, k[0]}, {2, 2}}); err != nil {
		return errors.New("can't start server: " + sys.Errstr())
	}
	kf := os.NewFile(uintptr(k[1]), "secret")
	kf.Write(secret)
	kf.Close()
	os.NewFile(uintptr(k[0]), "secret").Close()
	os.NewFile(uintptr(p[0]), "styx").Close()
	if _, err := sys.Mount(p[1], -1, mtpt, sys.MREPL|sys.MCREATE, ""); err != nil {
		return errors.New("mount on " + mtpt + " failed: " + sys.Errstr())
	}
	return nil
}

// Secret reads what Start passed on fd 1, in a server prog.
func Secret() []byte {
	f := os.NewFile(1, "secret")
	b, _ := io.ReadAll(f)
	f.Close()
	return b
}

// Owner is the user files are shown as owned by.
func Owner() string {
	b, _ := os.ReadFile("/dev/user")
	if s := strings.TrimSpace(string(b)); s != "" {
		return s
	}
	return "none"
}
