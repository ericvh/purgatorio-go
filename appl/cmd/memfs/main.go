// Command memfs is a file system held in memory (Inferno memfs(4)). It
// serves Styx on a pipe and mounts the other end on mountpoint (/tmp by
// default); with -s it serves its standard input instead.
//
// A guest is single-threaded, and mount must have answers from the
// server before it returns, so memfs cannot serve and mount at once as
// memfs.b's spawned server does. It runs the server as a second prog,
// memfs -s with the pipe as its standard input, then mounts and exits.
package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	blksz = 512
	efull = "filesystem full"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("memfs [-s] [-rab] [-m size] [mountpoint]")
	amode := sys.MREPL
	maxsz := 0x7fffffff
	srv := false
	mntpt := "/tmp"
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 's':
			srv = true
		case 'r':
			amode = sys.MREPL
		case 'a':
			amode = sys.MAFTER
		case 'b':
			amode = sys.MBEFORE
		case 'm':
			maxsz, _ = strconv.Atoi(a.EArg())
		default:
			a.Usage()
		}
	}
	if args := a.Argv(); len(args) > 0 {
		mntpt = args[0]
	}
	if srv {
		serve(os.NewFile(0, "styx"), maxsz)
		return
	}
	p, err := sys.Pipe()
	if err != nil {
		fatal("cannot create pipe: " + sys.Errstr())
	}
	if _, err := sys.Spawn("/bin/memfs.wasm", []string{"memfs", "-s", "-m", strconv.Itoa(maxsz)},
		sys.NEWFD, [][2]int{{0, p[1]}, {2, 2}}); err != nil {
		fatal("cannot start server: " + sys.Errstr())
	}
	os.NewFile(uintptr(p[1]), "pipe").Close()
	if err := sys.Mount(p[0], -1, mntpt, amode|sys.MCREATE, ""); err != nil {
		fatal("failed to mount onto " + mntpt + ": " + sys.Errstr())
	}
}

func fatal(e string) {
	os.Stderr.WriteString("memfs: " + e + "\n")
	sys.Exits("fail:error")
}

type memfile struct {
	name   string
	owner  string
	qid    styx.Qid
	perm   uint32
	atime  uint32
	mtime  uint32
	nopen  int
	data   []byte
	nblks  int // blocks charged to the file system
	parent *memfile
	kids   []*memfile // newest first, as memfs.b links them
}

type memfs struct {
	freeblks int
	nextqid  uint64
	files    map[uint64]*memfile
	root     *memfile
}

func now() uint32 { return uint32(time.Now().Unix()) }

func (fs *memfs) newmf(parent *memfile, name, owner string, perm uint32) *memfile {
	t := now()
	mf := &memfile{name: name, owner: owner, perm: perm, atime: t, mtime: t, parent: parent,
		qid: styx.Qid{Path: fs.nextqid}}
	fs.nextqid++
	if perm&styx.DMDIR != 0 {
		mf.qid.Type = styx.QTDIR
	}
	fs.files[mf.qid.Path] = mf
	return mf
}

func (mf *memfile) info() *styx.Dir {
	return &styx.Dir{Name: mf.name, UID: mf.owner, GID: "memfs", Qid: mf.qid, Mode: mf.perm,
		Atime: mf.atime, Mtime: mf.mtime, Length: uint64(len(mf.data))}
}

// The Navigator.

func (fs *memfs) Stat(q uint64) (*styx.Dir, error) {
	mf := fs.files[q]
	if mf == nil {
		return nil, styxservers.Enotfound
	}
	return mf.info(), nil
}

func (fs *memfs) Walk(q uint64, name string) (*styx.Dir, error) {
	mf := fs.files[q]
	if mf == nil {
		return nil, styxservers.Enotfound
	}
	if k := dirlookup(mf, name); k != nil {
		return k.info(), nil
	}
	return nil, styxservers.Enotfound
}

func (fs *memfs) Readdir(q uint64, off, n int) []*styx.Dir {
	mf := fs.files[q]
	if mf == nil || off >= len(mf.kids) {
		return nil
	}
	var ds []*styx.Dir
	for _, k := range mf.kids[off:min(off+n, len(mf.kids))] {
		ds = append(ds, k.info())
	}
	return ds
}

func dirlookup(dir *memfile, name string) *memfile {
	switch name {
	case ".":
		return dir
	case "..":
		return dir.parent
	}
	for _, k := range dir.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

var access = [4]uint32{0o400, 0o200, 0o600, 0o100}

// modeok is memfs.b's permission check: owner or other, no groups.
func modeok(mode int, perm uint32, user, owner string) bool {
	if mode >= sys.OTRUNC|sys.ORCLOSE|sys.OREAD|sys.OWRITE {
		return false
	}
	if user != owner {
		perm <<= 6
	}
	if mode&sys.OTRUNC != 0 && perm&0o200 == 0 {
		return false
	}
	a := access[mode&3]
	return a&perm == a
}

func user() string {
	b, err := os.ReadFile("/dev/user")
	if err != nil {
		return "none"
	}
	return strings.TrimSpace(string(b))
}

func serve(conn *os.File, maxsz int) {
	fs := &memfs{freeblks: maxsz / blksz, files: map[uint64]*memfile{}}
	fs.root = fs.newmf(nil, "memfs", user(), 0o755|styx.DMDIR)
	sys.Pctl(sys.NEWNS, nil)
	fs.root.parent = fs.root
	srv := styxservers.New(conn, fs, fs.root.qid.Path)
	for {
		t, err := srv.Next()
		if err != nil {
			return
		}
		if e := fs.handle(srv, t); e != "" {
			srv.Error(t, e)
		}
	}
}

func (fs *memfs) fidtomf(srv *styxservers.Server, fid uint32) (*styxservers.Fid, *memfile, string) {
	c := srv.GetFid(fid)
	if c == nil {
		return nil, nil, string(styxservers.Ebadfid)
	}
	mf := fs.files[c.Path]
	if mf == nil {
		return c, nil, string(styxservers.Enotfound)
	}
	return c, mf, ""
}

// handle serves one message and returns an error to send, or "" when it
// has replied.
func (fs *memfs) handle(srv *styxservers.Server, t *styx.Fcall) string {
	switch t.Type {
	case styx.Topen:
		c, mf, err := fs.fidtomf(srv, t.Fid)
		mode := int(t.Mode)
		switch {
		case err != "":
			return err
		case c.IsOpen:
			return string(styxservers.Eopen)
		case !modeok(mode, mf.perm, c.Uname, mf.owner):
			return string(styxservers.Eperm)
		case mf.perm&styx.DMDIR != 0 && mode&(sys.OTRUNC|sys.OWRITE|sys.ORDWR|sys.ORCLOSE) != 0:
			return string(styxservers.Eperm)
		case mode&sys.ORCLOSE != 0 && (mf.parent == mf || !modeok(sys.OWRITE, mf.parent.perm, c.Uname, mf.parent.owner)):
			return string(styxservers.Eperm)
		}
		c.Open(mode, mf.qid)
		mf.nopen++
		if mode&sys.OTRUNC != 0 && mf.perm&styx.DMAPPEND == 0 {
			fs.freeblks += mf.nblks - 1
			mf.nblks = 1
			mf.data = nil
			mf.mtime = now()
		}
		srv.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: mf.qid, Iounit: styxservers.MAXFDATA})
	case styx.Tcreate:
		c, parent, err := fs.fidtomf(srv, t.Fid)
		mode := int(t.Mode)
		switch {
		case err != "":
			return err
		case c.IsOpen:
			return string(styxservers.Eopen)
		case parent.qid.Type&styx.QTDIR == 0:
			return string(styxservers.Enotdir)
		case !modeok(sys.OWRITE, parent.perm, c.Uname, parent.owner):
			return string(styxservers.Eperm)
		case t.Perm&styx.DMDIR != 0 && mode&(sys.OTRUNC|sys.OWRITE|sys.ORDWR|sys.ORCLOSE) != 0:
			return string(styxservers.Eperm)
		case t.Name == "" || t.Name == "." || t.Name == "..":
			return string(styxservers.Edot)
		case dirlookup(parent, t.Name) != nil:
			return string(styxservers.Eexists)
		}
		isdir := t.Perm&styx.DMDIR != 0
		if !isdir && fs.freeblks <= 0 {
			return efull
		}
		var perm uint32
		if isdir {
			perm = t.Perm&^0o777 | parent.perm&t.Perm&0o777
		} else {
			perm = t.Perm&(^uint32(0o777)|0o111) | parent.perm&t.Perm&0o666
		}
		nmf := fs.newmf(parent, t.Name, c.Uname, perm)
		if !isdir {
			fs.freeblks--
			nmf.nblks = 1
		}
		parent.kids = append([]*memfile{nmf}, parent.kids...)
		parent.mtime = now()
		c.Open(mode, nmf.qid)
		nmf.nopen = 1
		srv.Reply(&styx.Fcall{Type: styx.Rcreate, Tag: t.Tag, Qid: nmf.qid, Iounit: styxservers.MAXFDATA})
	case styx.Tread:
		c, mf, err := fs.fidtomf(srv, t.Fid)
		if err == "" && !c.IsOpen {
			err = string(styxservers.Ebadfid)
		}
		if err != "" {
			return err
		}
		mf.atime = now()
		if mf.perm&styx.DMDIR != 0 {
			srv.Read(t)
			return ""
		}
		srv.Reply(styxservers.ReadBytes(t, mf.data))
	case styx.Twrite:
		c, mf, err := fs.fidtomf(srv, t.Fid)
		switch {
		case err != "":
			return err
		case !c.IsOpen:
			return string(styxservers.Ebadfid)
		case mf.perm&styx.DMDIR != 0:
			return string(styxservers.Eperm)
		}
		if e := fs.writefile(mf, int64(t.Offset), t.Data); e != "" {
			return e
		}
		srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
	case styx.Tclunk:
		c, mf, err := fs.fidtomf(srv, t.Fid)
		if c != nil {
			srv.DelFid(c)
		}
		if err != "" {
			return err
		}
		if c.IsOpen {
			if c.Mode&sys.ORCLOSE != 0 {
				unlink(mf)
			}
			mf.nopen--
			fs.delfile(mf)
		}
		srv.Reply(&styx.Fcall{Type: styx.Rclunk, Tag: t.Tag})
	case styx.Tremove:
		c, mf, err := fs.fidtomf(srv, t.Fid)
		if err != "" {
			return err
		}
		srv.DelFid(c)
		parent := mf.parent
		switch {
		case !modeok(sys.OWRITE, parent.perm, c.Uname, parent.owner):
			return string(styxservers.Eperm)
		case mf.perm&styx.DMDIR != 0 && len(mf.kids) > 0:
			return "directory not empty"
		case mf == fs.root:
			return "root directory"
		}
		unlink(mf)
		parent.mtime = now()
		if c.IsOpen {
			mf.nopen--
		}
		fs.delfile(mf)
		srv.Reply(&styx.Fcall{Type: styx.Rremove, Tag: t.Tag})
	case styx.Twstat:
		c, mf, err := fs.fidtomf(srv, t.Fid)
		if err != "" {
			return err
		}
		d, derr := styx.UnmarshalDir(t.Stat)
		if derr != nil {
			return string(styxservers.Ebadarg)
		}
		if d.Name != "" && d.Name != mf.name {
			parent := mf.parent
			if !modeok(sys.OWRITE, parent.perm, c.Uname, parent.owner) {
				return string(styxservers.Eperm)
			} else if dirlookup(parent, d.Name) != nil {
				return string(styxservers.Eexists)
			}
		}
		if (d.Mode != ^uint32(0) && d.Mode != mf.perm || d.Mtime != ^uint32(0) && d.Mtime != mf.mtime) && c.Uname != mf.owner {
			return string(styxservers.Eperm)
		}
		isdir := mf.perm & styx.DMDIR
		if d.Name != "" {
			mf.name = d.Name
		}
		if d.Mode != ^uint32(0) {
			mf.perm = d.Mode | isdir
		}
		if d.Mtime != ^uint32(0) {
			mf.mtime = d.Mtime
		}
		if d.UID != "" {
			mf.owner = d.UID
		}
		tm := now()
		mf.atime = tm
		mf.parent.mtime = tm
		srv.Reply(&styx.Fcall{Type: styx.Rwstat, Tag: t.Tag})
	default:
		srv.Default(t)
	}
	return ""
}

func blocks(n int64) int { return int((n + blksz - 1) / blksz) }

func (fs *memfs) writefile(mf *memfile, off int64, data []byte) string {
	if mf.perm&styx.DMAPPEND != 0 {
		off = int64(len(mf.data))
	}
	end := off + int64(len(data))
	if need := blocks(end) - mf.nblks; need > 0 {
		if need > fs.freeblks {
			return efull
		}
		fs.freeblks -= need
		mf.nblks += need
	}
	if end > int64(len(mf.data)) {
		mf.data = append(mf.data, make([]byte, end-int64(len(mf.data)))...)
	}
	copy(mf.data[off:], data)
	mf.mtime = now()
	return ""
}

func unlink(mf *memfile) {
	p := mf.parent
	if p == nil || p == mf {
		return
	}
	for i, k := range p.kids {
		if k == mf {
			p.kids = append(p.kids[:i:i], p.kids[i+1:]...)
			break
		}
	}
	mf.parent = nil
}

// delfile frees a file that is unlinked and no longer open.
func (fs *memfs) delfile(mf *memfile) {
	if mf.nopen <= 0 && mf.parent == nil && len(mf.kids) == 0 {
		delete(fs.files, mf.qid.Path)
		fs.freeblks += mf.nblks
		mf.nblks = 0
		mf.data = nil
	}
}
