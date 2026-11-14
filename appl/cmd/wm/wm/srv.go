package main

import (
	"encoding/binary"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/styx"
)

// The file tree. A client's files have its number above the low four
// bits of the Qid path.
const (
	qroot = iota
	qclone
	qwmrect
	qdir
	qwmctl
	qpointer
	qkeyboard
	qwinname
	qevent
	shift = 4
	mask  = 0xF
)

// Client flags, as in wm.b.
const (
	ptrStarted = 1 << iota
	kbdStarted
	controlStarted
	controller
	fixedOrigin
)

type window struct {
	tag  string
	r    draw.Rectangle
	img  *draw.Image
	name string
}

// client is one connection, made by opening clone.
type client struct {
	w      *wm
	id     int
	fid    uint32 // the clone fid that made it; its clunk ends the client
	flags  int
	wins   []*window
	iname  string // the image name winname gives
	nreads int    // reads of wmctl
	cursor string
	dead   bool

	useEvent bool // the client reads the event file
	evq      []string
	ptrq     []draw.Pointer
	kbdq     []rune
	ctlq     []string

	// Reads waiting for something to return.
	pending []*styx.Fcall
}

func (c *client) window(tag string) *window {
	for _, w := range c.wins {
		if w.tag == tag {
			return w
		}
	}
	return nil
}

func (c *client) images() []*draw.Image {
	var imgs []*draw.Image
	for _, w := range c.wins {
		if w.img != nil {
			imgs = append(imgs, w.img)
		}
	}
	return imgs
}

// putptr queues a pointer event, merging it with a queued one with the
// same buttons (wmsrv's Ptrqueue).
func (c *client) putptr(p draw.Pointer) {
	if c.useEvent {
		line := "m" + string(p.Bytes()[1:])
		if n := len(c.evq); n > 0 && strings.HasPrefix(c.evq[n-1], "m") {
			if q, ok := draw.ParsePointer([]byte(c.evq[n-1])); ok && q.Buttons == p.Buttons {
				c.evq[n-1] = line
				return
			}
		}
		c.evq = append(c.evq, line)
		return
	}
	if n := len(c.ptrq); n > 0 && c.ptrq[n-1].Buttons == p.Buttons {
		c.ptrq[n-1] = p
		return
	}
	c.ptrq = append(c.ptrq, p)
}

func (c *client) putkey(r rune) {
	if c.useEvent {
		c.evq = append(c.evq, "k "+strconv.Itoa(int(r)))
		return
	}
	c.kbdq = append(c.kbdq, r)
}

func (c *client) putctl(s string) {
	if c.useEvent {
		c.evq = append(c.evq, "c "+strings.ReplaceAll(s, "\n", " "))
		return
	}
	c.ctlq = append(c.ctlq, s)
}

// server serves the file tree on one end of a pipe.
type server struct {
	w     *wm
	srv   *styxservers.Server
	out   *os.File
	conns map[int]*client
}

// msgReader returns one whole Styx message a read, so that a message is
// never left in the server's buffer while it waits on the pipe.
type msgReader struct{ f *os.File }

func (m msgReader) Read(b []byte) (int, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(m.f, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.LittleEndian.Uint32(hdr[:]))
	if n < 4 || n > len(b) {
		return 0, io.ErrUnexpectedEOF
	}
	copy(b, hdr[:])
	if _, err := io.ReadFull(m.f, b[4:n]); err != nil {
		return 0, err
	}
	return n, nil
}

type rw struct {
	io.Reader
	io.Writer
}

func newServer(w *wm, fd int) *server {
	f := os.NewFile(uintptr(fd), "wm")
	s := &server{w: w, out: f, conns: map[int]*client{}}
	s.srv = styxservers.New(rw{msgReader{f}, f}, s, qroot)
	return s
}

func (s *server) client(path uint64) *client {
	c := s.conns[int(path>>shift)]
	if c == nil || c.dead {
		return nil
	}
	return c
}

func dir(path uint64, name string, perm uint32) *styx.Dir {
	d := &styx.Dir{Name: name, Mode: perm, UID: "wm", GID: "wm", MUID: "wm"}
	d.Qid.Path = path
	if perm&styx.DMDIR != 0 {
		d.Qid.Type = styx.QTDIR
	}
	return d
}

// Stat, Walk and Readdir are the Navigator.
func (s *server) Stat(path uint64) (*styx.Dir, error) {
	n := strconv.Itoa(int(path >> shift))
	switch path & mask {
	case qroot:
		return dir(qroot, ".", styx.DMDIR|0o555), nil
	case qclone:
		return dir(qclone, "clone", 0o666), nil
	case qwmrect:
		return dir(qwmrect, "wmrect", 0o444), nil
	}
	if s.client(path) == nil {
		return nil, styxservers.Enotfound
	}
	switch path & mask {
	case qdir:
		return dir(path, n, styx.DMDIR|0o555), nil
	case qwmctl:
		return dir(path, "wmctl", 0o666), nil
	case qpointer:
		return dir(path, "pointer", 0o444), nil
	case qkeyboard:
		return dir(path, "keyboard", 0o444), nil
	case qwinname:
		return dir(path, "winname", 0o444), nil
	case qevent:
		return dir(path, "event", 0o444), nil
	}
	return nil, styxservers.Enotfound
}

var dirfiles = []struct {
	name string
	q    uint64
}{{"wmctl", qwmctl}, {"pointer", qpointer}, {"keyboard", qkeyboard}, {"winname", qwinname}, {"event", qevent}}

func (s *server) Walk(parent uint64, name string) (*styx.Dir, error) {
	switch parent & mask {
	case qroot:
		switch name {
		case "..":
			return s.Stat(qroot)
		case "clone":
			return s.Stat(qclone)
		case "wmrect":
			return s.Stat(qwmrect)
		}
		if n, err := strconv.Atoi(name); err == nil && strconv.Itoa(n) == name {
			if c := s.conns[n]; c != nil && !c.dead {
				return s.Stat(uint64(n)<<shift | qdir)
			}
		}
	case qdir:
		if name == ".." {
			return s.Stat(qroot)
		}
		for _, f := range dirfiles {
			if f.name == name {
				return s.Stat(parent&^mask | f.q)
			}
		}
	}
	return nil, styxservers.Enotfound
}

func (s *server) Readdir(path uint64, offset, count int) []*styx.Dir {
	var all []*styx.Dir
	switch path & mask {
	case qroot:
		for _, q := range []uint64{qclone, qwmrect} {
			d, _ := s.Stat(q)
			all = append(all, d)
		}
		for _, c := range s.w.clients {
			if d, _ := s.Stat(uint64(c.id)<<shift | qdir); d != nil {
				all = append(all, d)
			}
		}
	case qdir:
		for _, f := range dirfiles {
			if d, _ := s.Stat(path&^mask | f.q); d != nil {
				all = append(all, d)
			}
		}
	}
	if offset >= len(all) {
		return nil
	}
	all = all[offset:]
	return all[:min(count, len(all))]
}

// one serves one message; false when the conversation is over.
func (s *server) one() bool {
	t, err := s.srv.Next()
	if err != nil {
		return false
	}
	switch t.Type {
	case styx.Topen:
		s.open(t)
	case styx.Tread:
		s.read(t)
	case styx.Twrite:
		s.write(t)
	case styx.Tflush:
		for _, c := range s.conns {
			for i, p := range c.pending {
				if p.Tag == t.Oldtag {
					c.pending = append(c.pending[:i], c.pending[i+1:]...)
					break
				}
			}
		}
		s.srv.Default(t)
	case styx.Tclunk:
		f := s.srv.GetFid(t.Fid)
		s.srv.Default(t)
		if f != nil && f.IsOpen && f.Path&mask == qwmctl {
			if c := s.conns[int(f.Path>>shift)]; c != nil && c.fid == f.Fid {
				s.w.delclient(c)
			}
		}
	default:
		s.srv.Default(t)
	}
	return true
}

func (s *server) open(t *styx.Fcall) {
	f, mode, d, err := s.srv.CanOpen(t)
	if err != nil {
		s.srv.Error(t, err.Error())
		return
	}
	q := d.Qid
	switch q.Path & mask {
	case qclone:
		c := s.w.newclient(t.Fid)
		s.conns[c.id] = c
		q = styx.Qid{Path: uint64(c.id)<<shift | qwmctl}
	case qpointer:
		s.client(q.Path).flags |= ptrStarted
	case qkeyboard:
		c := s.client(q.Path)
		c.flags |= kbdStarted
		s.w.setfocus(c)
	case qevent:
		// What was queued for the other files now comes as events.
		c := s.client(q.Path)
		c.useEvent = true
		ctlq, ptrq, kbdq := c.ctlq, c.ptrq, c.kbdq
		c.ctlq, c.ptrq, c.kbdq = nil, nil, nil
		for _, m := range ctlq {
			c.putctl(m)
		}
		for _, p := range ptrq {
			c.putptr(p)
		}
		for _, k := range kbdq {
			c.putkey(k)
		}
	}
	f.Open(mode, q)
	s.srv.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: q, Iounit: s.srv.Iounit()})
}

func (s *server) read(t *styx.Fcall) {
	f, err := s.srv.CanRead(t)
	if err != nil {
		s.srv.Error(t, err.Error())
		return
	}
	switch f.Path & mask {
	case qroot, qdir:
		s.srv.Read(t)
		return
	case qwmrect:
		s.srv.Reply(styxservers.ReadStr(t, r2s(s.w.d.Image.R)))
		return
	}
	c := s.conns[int(f.Path>>shift)]
	if c == nil || c.dead {
		s.srv.Error(t, "connection is dead")
		return
	}
	switch f.Path & mask {
	case qwinname:
		s.srv.Reply(styxservers.ReadStr(t, c.iname))
		return
	case qwmctl:
		if c.nreads == 0 {
			c.nreads++
			t.Offset = 0
			s.srv.Reply(styxservers.ReadStr(t, strconv.Itoa(c.id)))
			return
		}
	}
	c.pending = append(c.pending, t)
}

// deliver answers the reads that can now be answered.
func (s *server) deliver() {
	for _, c := range s.conns {
		var keep []*styx.Fcall
		for _, t := range c.pending {
			f := s.srv.GetFid(t.Fid)
			if f == nil {
				continue
			}
			var data []byte
			ready := true
			switch {
			case c.dead:
				// End of file: the client has gone.
			case f.Path&mask == qevent && len(c.evq) > 0:
				for len(c.evq) > 0 && len(data)+len(c.evq[0])+1 <= int(t.Count) {
					data = append(append(data, c.evq[0]...), '\n')
					c.evq = c.evq[1:]
				}
				if data == nil { // a short read takes part of a line
					data = []byte(c.evq[0])[:t.Count]
					c.evq[0] = c.evq[0][t.Count:]
				}
			case f.Path&mask == qpointer && len(c.ptrq) > 0:
				data = c.ptrq[0].Bytes()
				c.ptrq = c.ptrq[1:]
			case f.Path&mask == qkeyboard && len(c.kbdq) > 0:
				data = []byte(string(c.kbdq[0]))
				c.kbdq = c.kbdq[1:]
			case f.Path&mask == qwmctl && len(c.ctlq) > 0:
				data = []byte(c.ctlq[0])
				c.ctlq = c.ctlq[1:]
			default:
				ready = false
			}
			if !ready {
				keep = append(keep, t)
				continue
			}
			if len(data) > int(t.Count) {
				data = data[:t.Count]
			}
			s.srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: data})
		}
		c.pending = keep
		if c.dead && len(c.pending) == 0 {
			delete(s.conns, c.id)
		}
	}
}

func (s *server) write(t *styx.Fcall) {
	f, err := s.srv.CanWrite(t)
	if err != nil {
		s.srv.Error(t, err.Error())
		return
	}
	if f.Path&mask != qwmctl {
		s.srv.Error(t, styxservers.Eperm.Error())
		return
	}
	c := s.client(f.Path)
	if c == nil {
		s.srv.Error(t, "connection is dead")
		return
	}
	if e := s.w.request(c, string(t.Data)); e != "" {
		s.srv.Error(t, e)
		return
	}
	s.srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
}
