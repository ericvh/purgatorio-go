// Package styxservers is a framework for writing Styx (9P2000) file
// servers in guests, after Inferno's Styxservers module (styxservers(2)).
//
// A Server reads T-messages from a connection (a pipe end or a network
// data file) and writes R-messages. It keeps the table of fids and gives
// default replies for walk, open, read of directories, stat and clunk,
// which ask a Navigator about the files. A program handles the messages
// it cares about (usually read, write and create) and passes the rest to
// Default.
//
// A guest is single-threaded and a host call blocks all of it, so a
// server takes one request at a time from one loop:
//
//	srv := styxservers.New(conn, nav, root)
//	for {
//		t, err := srv.Next()
//		if err != nil {
//			break
//		}
//		switch t.Type {
//		case styx.Tread:
//			...
//		default:
//			srv.Default(t)
//		}
//	}
//
// Where Limbo's Navigator talks to a navigator thread over channels, here
// it is an interface called directly.
package styxservers

import (
	"fmt"
	"io"

	"github.com/ericvh/purgatorio-go/styx"
)

// Error is an error string sent to the client in Rerror.
type Error string

func (e Error) Error() string { return string(e) }

// The errors of styxservers.m.
const (
	Einuse    Error = "fid already in use"
	Ebadfid   Error = "bad fid"
	Eopen     Error = "fid already opened"
	Enotfound Error = "file does not exist"
	Enotdir   Error = "not a directory"
	Eperm     Error = "permission denied"
	Ebadarg   Error = "bad argument"
	Eexists   Error = "file already exists"
	Emode     Error = "open/create -- unknown mode"
	Eoffset   Error = "read/write -- bad offset"
	Ecount    Error = "read/write -- count negative or exceeds msgsize"
	Enotopen  Error = "read/write -- on non open fid"
	Eaccess   Error = "read/write -- not open in suitable mode"
	Ename     Error = "bad character in file name"
	Edot      Error = ". and .. are illegal names"
)

// Open modes, as in Inferno's sys.m.
const (
	OREAD   = 0
	OWRITE  = 1
	ORDWR   = 2
	OEXEC   = 3
	OTRUNC  = 0x10
	ORCLOSE = 0x40
)

const (
	// MAXFDATA is Styx's reasonable iounit, and MAXRPC the default msize.
	MAXFDATA = 8192
	MAXRPC   = styx.IOHDRSZ + MAXFDATA

	statfixlen  = 2 + 13 + 5*2 + 4*4 + 8
	dirreadsize = statfixlen + 4*20 // a reasonable chunk for reading directories

	// maxmsg bounds a message before Tversion has set msize.
	maxmsg = 1 << 20
)

// Navigator answers the server's questions about the file tree, by Qid
// path. Walk must handle "..", and for remove it must work on files as
// well as directories. Readdir returns up to count entries of directory
// path, starting at entry offset (origin 0), and none past the end.
type Navigator interface {
	Stat(path uint64) (*styx.Dir, error)
	Walk(parent uint64, name string) (*styx.Dir, error)
	Readdir(path uint64, offset, count int) []*styx.Dir
}

// Fid is the server's state for one client fid.
type Fid struct {
	Fid    uint32 // the client's fid
	Path   uint64 // the file's Qid path
	Qtype  uint8  // the file's Qid type
	IsOpen bool
	Mode   int    // if open, the open mode (with ORCLOSE)
	Uname  string // user name from the attach
	Param  string // aname from the attach
	Data   any    // the application's

	doffset, dindex int // directory read position: byte offset and entry
}

// Clone copies f's state, but not its fid number, into nf and returns nf.
func (f *Fid) Clone(nf *Fid) *Fid {
	fid := nf.Fid
	*nf = *f
	nf.Fid = fid
	return nf
}

// Walk moves f to the file with qid.
func (f *Fid) Walk(q styx.Qid) {
	f.Path = q.Path
	f.Qtype = q.Type
}

// Open marks f open with mode on the file with qid.
func (f *Fid) Open(mode int, q styx.Qid) {
	f.IsOpen = true
	f.Mode = mode
	f.doffset, f.dindex = 0, 0
	f.Path = q.Path
	f.Qtype = q.Type
}

// Server is one Styx conversation.
type Server struct {
	Nav      Navigator
	RootPath uint64
	Msize    uint32 // negotiated by Tversion; 0 before it
	// Trace, when set, gets a line for each message in and out.
	Trace io.Writer

	r    io.Reader
	w    io.Writer
	fids map[uint32]*Fid
	buf  []byte
	rbuf []byte
}

// New makes a server for the conversation on rw, with nav to answer for
// the tree whose root has Qid path rootpath.
func New(rw io.ReadWriter, nav Navigator, rootpath uint64) *Server {
	return &Server{Nav: nav, RootPath: rootpath, r: rw, w: rw, fids: map[uint32]*Fid{}}
}

// Next returns the next T-message. At end of file, or on a read error or
// a malformed message, it returns an error and the conversation is over.
func (s *Server) Next() (*styx.Fcall, error) {
	lim := s.Msize
	if lim == 0 {
		lim = maxmsg
	}
	for {
		if n := styx.Size(s.buf); n != 0 {
			if n > lim || n < 7 {
				return nil, fmt.Errorf("styxservers: bad message size %d", n)
			}
			if int(n) <= len(s.buf) {
				msg := s.buf[:n]
				t, err := styx.Unmarshal(msg, lim)
				s.buf = append(s.buf[:0], s.buf[n:]...)
				if err != nil {
					return nil, err
				}
				if t.Type%2 != 0 || t.Type == styx.Terror {
					return nil, styx.ErrBadType
				}
				if s.Trace != nil {
					fmt.Fprintf(s.Trace, "<- %s\n", Text(t))
				}
				return t, nil
			}
		}
		if s.rbuf == nil {
			s.rbuf = make([]byte, 64*1024)
		}
		k, err := s.r.Read(s.rbuf)
		if k > 0 {
			s.buf = append(s.buf, s.rbuf[:k]...)
			continue
		}
		if err == nil {
			err = io.EOF
		}
		return nil, err
	}
}

// Buffered reports whether a whole message is already read, so that Next
// will return it without reading: a loop that waits for the connection
// to be readable before calling Next should call Next again while it is
// true.
func (s *Server) Buffered() bool {
	n := styx.Size(s.buf)
	return n != 0 && int(n) <= len(s.buf)
}

// Reply sends r. Before Tversion, or when r does not fit in msize, the
// client gets an error instead, as in Inferno.
func (s *Server) Reply(r *styx.Fcall) error {
	if s.Trace != nil {
		fmt.Fprintf(s.Trace, "-> %s\n", Text(r))
	}
	if s.Msize == 0 {
		r = &styx.Fcall{Type: styx.Rerror, Tag: r.Tag, Ename: "Tversion not seen"}
	}
	b, err := styx.Marshal(nil, r)
	if err != nil {
		return err
	}
	if s.Msize != 0 && len(b) > int(s.Msize) {
		b, _ = styx.Marshal(nil, &styx.Fcall{Type: styx.Rerror, Tag: r.Tag, Ename: "Styx reply didn't fit"})
	}
	_, err = s.w.Write(b)
	return err
}

// Error replies to t with the error msg.
func (s *Server) Error(t *styx.Fcall, msg string) {
	s.Reply(&styx.Fcall{Type: styx.Rerror, Tag: t.Tag, Ename: msg})
}

func (s *Server) rerr(t *styx.Fcall, err error) { s.Error(t, err.Error()) }

// Attach attaches t.Fid to the root and returns the new fid, or replies
// with an error and returns nil.
func (s *Server) Attach(t *styx.Fcall) *Fid {
	d, err := s.Nav.Stat(s.RootPath)
	if d == nil {
		s.rerr(t, err)
		return nil
	}
	if d.Qid.Type&styx.QTDIR == 0 {
		s.rerr(t, Enotdir)
		return nil
	}
	c := s.NewFid(t.Fid)
	if c == nil {
		s.rerr(t, Einuse)
		return nil
	}
	c.Uname = t.Uname
	c.Param = t.Aname
	c.Walk(d.Qid)
	s.Reply(&styx.Fcall{Type: styx.Rattach, Tag: t.Tag, Qid: d.Qid})
	return c
}

func (s *Server) walk1(c *Fid, name string) (*styx.Dir, error) {
	d, err := s.Nav.Stat(c.Path)
	if d == nil {
		return nil, err
	}
	if d.Qid.Type&styx.QTDIR == 0 {
		return nil, Enotdir
	}
	if !OpenOK(c.Uname, OEXEC, d.Mode, d.UID, d.GID) {
		return nil, Eperm
	}
	return s.Nav.Walk(d.Qid.Path, name)
}

// Walk does a Twalk with the Navigator and returns the walked fid, or
// nil when the walk failed (it has replied either way).
func (s *Server) Walk(t *styx.Fcall) *Fid {
	c := s.GetFid(t.Fid)
	if c == nil {
		s.rerr(t, Ebadfid)
		return nil
	}
	if c.IsOpen {
		s.rerr(t, Eopen)
		return nil
	}
	if t.Newfid != t.Fid {
		nc := s.NewFid(t.Newfid)
		if nc == nil {
			s.rerr(t, Einuse)
			return nil
		}
		c = c.Clone(nc)
	}
	qids := make([]styx.Qid, 0, len(t.Wname))
	oldpath, oldqtype := c.Path, c.Qtype
	for i, name := range t.Wname {
		d, err := s.walk1(c, name)
		if d == nil {
			c.Path, c.Qtype = oldpath, oldqtype
			if t.Newfid != t.Fid {
				s.DelFid(c)
			}
			if i == 0 {
				s.rerr(t, err)
			} else {
				s.Reply(&styx.Fcall{Type: styx.Rwalk, Tag: t.Tag, Wqid: qids})
			}
			return nil
		}
		c.Walk(d.Qid)
		qids = append(qids, d.Qid)
	}
	s.Reply(&styx.Fcall{Type: styx.Rwalk, Tag: t.Tag, Wqid: qids})
	return c
}

// CanOpen checks a Topen without replying. It returns the fid, the mode
// to open it with and the file's Dir, or an error.
func (s *Server) CanOpen(t *styx.Fcall) (*Fid, int, *styx.Dir, error) {
	c := s.GetFid(t.Fid)
	if c == nil {
		return nil, 0, nil, Ebadfid
	}
	if c.IsOpen {
		return nil, 0, nil, Eopen
	}
	f, err := s.Nav.Stat(c.Path)
	if f == nil {
		return nil, 0, nil, err
	}
	mode := OpenMode(int(t.Mode))
	if mode == -1 {
		return nil, 0, nil, Ebadarg
	}
	if mode != OREAD && f.Qid.Type&styx.QTDIR != 0 {
		return nil, 0, nil, Eperm
	}
	if !OpenOK(c.Uname, int(t.Mode), f.Mode, f.UID, f.GID) {
		return nil, 0, nil, Eperm
	}
	if t.Mode&ORCLOSE != 0 {
		dir, _ := s.Nav.Walk(c.Path, "..")
		if dir == nil || dir.Qid.Path == f.Qid.Path && dir.Qid.Type == f.Qid.Type ||
			!OpenOK(c.Uname, OWRITE, dir.Mode, dir.UID, dir.GID) {
			return nil, 0, nil, Eperm
		}
		mode |= ORCLOSE
	}
	return c, mode, f, nil
}

// Open opens a fid after CanOpen's checks and replies.
func (s *Server) Open(t *styx.Fcall) *Fid {
	c, mode, f, err := s.CanOpen(t)
	if c == nil {
		s.rerr(t, err)
		return nil
	}
	c.Open(mode, f.Qid)
	s.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: f.Qid, Iounit: s.Iounit()})
	return c
}

// CanCreate checks a Tcreate without replying. It returns the fid, the
// open mode and a Dir for the new file with name, mode, qid type, owner
// and group set; the caller fills in the times and the Qid path.
func (s *Server) CanCreate(t *styx.Fcall) (*Fid, int, *styx.Dir, error) {
	c := s.GetFid(t.Fid)
	if c == nil {
		return nil, 0, nil, Ebadfid
	}
	if c.IsOpen {
		return nil, 0, nil, Eopen
	}
	d, err := s.Nav.Stat(c.Path)
	if d == nil {
		return nil, 0, nil, err
	}
	if d.Mode&styx.DMDIR == 0 {
		return nil, 0, nil, Enotdir
	}
	if t.Name == "" {
		return nil, 0, nil, Ename
	}
	if t.Name == "." || t.Name == ".." {
		return nil, 0, nil, Edot
	}
	if !OpenOK(c.Uname, OWRITE, d.Mode, d.UID, d.GID) {
		return nil, 0, nil, Eperm
	}
	if x, _ := s.Nav.Walk(d.Qid.Path, t.Name); x != nil {
		return nil, 0, nil, Eexists
	}
	mode := OpenMode(int(t.Mode))
	if mode == -1 {
		return nil, 0, nil, Ebadarg
	}
	mode |= int(t.Mode) & ORCLOSE
	f := &styx.Dir{Name: t.Name, UID: c.Uname, MUID: c.Uname, GID: d.GID, Type: d.Type, Dev: d.Dev}
	if t.Perm&styx.DMDIR != 0 {
		f.Mode = t.Perm & (^uint32(0o777) | d.Mode&0o777)
		f.Qid.Type = styx.QTDIR
	} else {
		f.Mode = t.Perm & (^uint32(0o666) | d.Mode&0o666)
		f.Qid.Type = styx.QTFILE
	}
	return c, mode, f, nil
}

// CanRead checks a Tread without replying.
func (s *Server) CanRead(t *styx.Fcall) (*Fid, error) {
	c := s.GetFid(t.Fid)
	if c == nil {
		return nil, Ebadfid
	}
	if !c.IsOpen {
		return nil, Enotopen
	}
	if m := c.Mode & 3; m != OREAD && m != ORDWR {
		return nil, Eaccess
	}
	if int64(t.Count) > int64(s.Msize)-styx.IOHDRSZ {
		return nil, Ecount
	}
	if int64(t.Offset) < 0 {
		return nil, Eoffset
	}
	return c, nil
}

// Read replies to a read of a directory from the Navigator's Readdir. A
// read of a file is permission denied: the program handles those.
func (s *Server) Read(t *styx.Fcall) *Fid {
	c, err := s.CanRead(t)
	if c == nil {
		s.rerr(t, err)
		return nil
	}
	if c.Qtype&styx.QTDIR == 0 {
		s.rerr(t, Eperm)
		return nil
	}
	count := int(t.Count)
	if count <= 0 {
		s.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag})
		return c
	}
	a := make([]byte, 0, count)
	offset, index := c.doffset, c.dindex
	if int64(t.Offset) != int64(offset) { // rescan from the start
		offset, index = 0, 0
	}
dread:
	for {
		ds := s.Nav.Readdir(c.Path, index, (count+dirreadsize-1)/dirreadsize)
		if len(ds) == 0 {
			break
		}
		for _, d := range ds {
			de := styx.MarshalDir(nil, d)
			size := len(de)
			offset += size
			index++
			if int64(offset) < int64(t.Offset) {
				continue
			}
			if count -= size; count < 0 { // won't fit: save the place for next time
				offset -= size
				index--
				break dread
			}
			a = append(a, de...)
		}
	}
	c.doffset, c.dindex = offset, index
	s.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: a})
	return c
}

// CanWrite checks a Twrite without replying.
func (s *Server) CanWrite(t *styx.Fcall) (*Fid, error) {
	c := s.GetFid(t.Fid)
	if c == nil {
		return nil, Ebadfid
	}
	if !c.IsOpen {
		return nil, Enotopen
	}
	if c.Qtype&styx.QTDIR != 0 {
		return nil, Eperm
	}
	if m := c.Mode & 3; m != OWRITE && m != ORDWR {
		return nil, Eaccess
	}
	if int64(t.Offset) < 0 {
		return nil, Eoffset
	}
	return c, nil
}

// Stat replies with the Navigator's Dir for the fid's file.
func (s *Server) Stat(t *styx.Fcall) {
	c := s.GetFid(t.Fid)
	if c == nil {
		s.rerr(t, Ebadfid)
		return
	}
	d, err := s.Nav.Stat(c.Path)
	if d == nil {
		s.rerr(t, err)
		return
	}
	s.Reply(&styx.Fcall{Type: styx.Rstat, Tag: t.Tag, Stat: styx.MarshalDir(nil, d)})
}

// CanRemove checks a Tremove without replying and returns the fid and
// the Qid path of its parent directory.
func (s *Server) CanRemove(t *styx.Fcall) (*Fid, uint64, error) {
	c := s.GetFid(t.Fid)
	if c == nil {
		return nil, 0, Ebadfid
	}
	dir, _ := s.Nav.Walk(c.Path, "..") // .. must work for files too
	if dir == nil {
		return nil, 0, Error("can't find parent directory")
	}
	if dir.Qid.Path == c.Path && dir.Qid.Type == c.Qtype ||
		!OpenOK(c.Uname, OWRITE, dir.Mode, dir.UID, dir.GID) {
		return nil, 0, Eperm
	}
	return c, dir.Qid.Path, nil
}

// Remove clunks the fid, as remove always does, and replies permission
// denied: a program that can remove files handles Tremove itself.
func (s *Server) Remove(t *styx.Fcall) *Fid {
	c := s.GetFid(t.Fid)
	if c == nil {
		s.rerr(t, Ebadfid)
		return nil
	}
	s.DelFid(c)
	s.rerr(t, Eperm)
	return c
}

// Clunk forgets the fid and replies; it returns the fid that was clunked.
func (s *Server) Clunk(t *styx.Fcall) *Fid {
	c := s.GetFid(t.Fid)
	if c == nil {
		s.rerr(t, Ebadfid)
		return nil
	}
	s.DelFid(c)
	s.Reply(&styx.Fcall{Type: styx.Rclunk, Tag: t.Tag})
	return c
}

// Default gives t the default reply: version negotiation, no
// authentication, flush, attach, walk, open, directory reads, stat and
// clunk work; create, write, remove and wstat are permission denied.
func (s *Server) Default(t *styx.Fcall) {
	switch t.Type {
	case styx.Tversion:
		msize, version := Compatible(t, MAXRPC, styx.Version)
		if msize < 256 {
			s.Msize = MAXRPC
			s.Error(t, "message size too small")
			return
		}
		s.Msize = msize
		if version != "unknown" {
			clear(s.fids)
		}
		s.Reply(&styx.Fcall{Type: styx.Rversion, Tag: t.Tag, Msize: msize, Version: version})
	case styx.Tauth:
		s.Error(t, "authentication not required")
	case styx.Tflush:
		s.Reply(&styx.Fcall{Type: styx.Rflush, Tag: t.Tag})
	case styx.Tattach:
		s.Attach(t)
	case styx.Twalk:
		s.Walk(t)
	case styx.Topen:
		s.Open(t)
	case styx.Tread:
		s.Read(t)
	case styx.Tclunk:
		s.Clunk(t)
	case styx.Tstat:
		s.Stat(t)
	case styx.Tremove:
		s.Remove(t)
	case styx.Tcreate, styx.Twrite, styx.Twstat:
		s.rerr(t, Eperm)
	default:
		s.Error(t, "unknown message")
	}
}

// Serve answers every message with Default until the conversation ends.
func (s *Server) Serve() {
	for {
		t, err := s.Next()
		if err != nil {
			return
		}
		s.Default(t)
	}
}

// Iounit is the largest read or write that fits in msize, 0 if unknown.
func (s *Server) Iounit() uint32 {
	if s.Msize <= styx.IOHDRSZ {
		return 0
	}
	return s.Msize - styx.IOHDRSZ
}

// GetFid returns the server's state for fid, or nil.
func (s *Server) GetFid(fid uint32) *Fid { return s.fids[fid] }

// NewFid makes a fid, or returns nil if it is in use.
func (s *Server) NewFid(fid uint32) *Fid {
	if s.fids[fid] != nil {
		return nil
	}
	c := &Fid{Fid: fid, Path: ^uint64(0)}
	s.fids[fid] = c
	return c
}

// DelFid forgets c.
func (s *Server) DelFid(c *Fid) {
	if s.fids[c.Fid] == c {
		delete(s.fids, c.Fid)
	}
}

// AllFids returns every fid in use.
func (s *Server) AllFids() []*Fid {
	l := make([]*Fid, 0, len(s.fids))
	for _, c := range s.fids {
		l = append(l, c)
	}
	return l
}

// ReadBytes replies to a read of a file holding d.
func ReadBytes(t *styx.Fcall, d []byte) *styx.Fcall {
	r := &styx.Fcall{Type: styx.Rread, Tag: t.Tag}
	if t.Offset >= uint64(len(d)) {
		return r
	}
	e := min(t.Offset+uint64(t.Count), uint64(len(d)))
	r.Data = d[t.Offset:e]
	return r
}

// ReadStr replies to a read of a file holding s.
func ReadStr(t *styx.Fcall, s string) *styx.Fcall { return ReadBytes(t, []byte(s)) }

// OpenMode strips OTRUNC and ORCLOSE from an open mode, or returns -1
// if it is not one.
func OpenMode(o int) int {
	o &^= OTRUNC | ORCLOSE
	if o > ORDWR {
		return -1
	}
	return o
}

var access = [4]uint32{0o400, 0o200, 0o600, 0o100}

// OpenOK reports whether uname may open a file with permissions perm,
// owned by fuid and group fgid, with mode omode.
func OpenOK(uname string, omode int, perm uint32, fuid, fgid string) bool {
	t := access[omode&3]
	if omode&OTRUNC != 0 {
		if perm&styx.DMDIR != 0 {
			return false
		}
		t |= 0o200
	}
	if uname == fuid && t&perm == t {
		return true
	}
	if uname == fgid && t&(perm<<3) == t {
		return true
	}
	return t&(perm<<6) == t
}

// Compatible is Styx->compatible: the msize and version to agree to for a
// Tversion, given the server's largest msize and version.
func Compatible(t *styx.Fcall, msize uint32, version string) (uint32, string) {
	if version == "" {
		version = styx.Version
	}
	msize = min(msize, t.Msize)
	v := t.Version
	if len(v) < 2 || v[:2] != "9P" {
		return msize, "unknown"
	}
	for i := 2; i < len(v); i++ {
		if c := v[i]; c == '.' {
			v = v[:i]
			break
		} else if c < '0' || c > '9' {
			return msize, "unknown"
		}
	}
	if v < styx.Version {
		return msize, "unknown"
	}
	if v < version {
		version = v
	}
	return msize, version
}

// Text describes a message for tracing.
func Text(f *styx.Fcall) string {
	switch f.Type {
	case styx.Tversion, styx.Rversion:
		return fmt.Sprintf("%s tag %d msize %d version %q", tname(f.Type), f.Tag, f.Msize, f.Version)
	case styx.Tattach:
		return fmt.Sprintf("Tattach tag %d fid %d afid %d uname %q aname %q", f.Tag, f.Fid, int32(f.Afid), f.Uname, f.Aname)
	case styx.Rattach, styx.Rauth:
		return fmt.Sprintf("%s tag %d qid %v", tname(f.Type), f.Tag, f.Qid)
	case styx.Rerror:
		return fmt.Sprintf("Rerror tag %d ename %q", f.Tag, f.Ename)
	case styx.Twalk:
		return fmt.Sprintf("Twalk tag %d fid %d newfid %d %q", f.Tag, f.Fid, f.Newfid, f.Wname)
	case styx.Rwalk:
		return fmt.Sprintf("Rwalk tag %d %v", f.Tag, f.Wqid)
	case styx.Topen:
		return fmt.Sprintf("Topen tag %d fid %d mode %d", f.Tag, f.Fid, f.Mode)
	case styx.Ropen, styx.Rcreate:
		return fmt.Sprintf("%s tag %d qid %v iounit %d", tname(f.Type), f.Tag, f.Qid, f.Iounit)
	case styx.Tcreate:
		return fmt.Sprintf("Tcreate tag %d fid %d name %q perm %#o mode %d", f.Tag, f.Fid, f.Name, f.Perm, f.Mode)
	case styx.Tread:
		return fmt.Sprintf("Tread tag %d fid %d offset %d count %d", f.Tag, f.Fid, f.Offset, f.Count)
	case styx.Rread:
		return fmt.Sprintf("Rread tag %d count %d", f.Tag, len(f.Data))
	case styx.Twrite:
		return fmt.Sprintf("Twrite tag %d fid %d offset %d count %d", f.Tag, f.Fid, f.Offset, len(f.Data))
	case styx.Rwrite:
		return fmt.Sprintf("Rwrite tag %d count %d", f.Tag, f.Count)
	case styx.Tflush:
		return fmt.Sprintf("Tflush tag %d oldtag %d", f.Tag, f.Oldtag)
	}
	return fmt.Sprintf("%s tag %d fid %d", tname(f.Type), f.Tag, f.Fid)
}

var names = []string{"Tversion", "Rversion", "Tauth", "Rauth", "Tattach", "Rattach", "Terror", "Rerror",
	"Tflush", "Rflush", "Twalk", "Rwalk", "Topen", "Ropen", "Tcreate", "Rcreate", "Tread", "Rread",
	"Twrite", "Rwrite", "Tclunk", "Rclunk", "Tremove", "Rremove", "Tstat", "Rstat", "Twstat", "Rwstat"}

func tname(t uint8) string {
	if t >= styx.Tversion && int(t-styx.Tversion) < len(names) {
		return names[t-styx.Tversion]
	}
	return fmt.Sprintf("type%d", t)
}
