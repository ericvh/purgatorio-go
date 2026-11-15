package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

// The files rio serves each window (rio(4)), from its event loop.
//
// Each window has a pipe of its own, whose far end rio -W mounts before
// /dev in the window's name space, so the programs in the window see
// its cons, consctl, keyboard, pointer, label, screen, text, wctl,
// window, winid and winname there, with the rest of /dev after them,
// and wsys/n/ holding the same files of every window. A Qid path is the
// window's number above the low eight bits and the file below.
//
// rio answers each message at once, except reads of cons, pointer and
// keyboard with nothing to read: those wait, with their tags, until
// there is something, or a Tflush or the clunk of their fid takes them
// away.

const (
	edeleted = "window deleted"
	ebadwctl = "unrecognized wctl command"
	ebadrect = "bad rectangle in wctl request"
)

const (
	qroot = iota
	qcons
	qconsctl
	qkeyboard
	qlabel
	qpointer
	qscreen
	qtext
	qwctl
	qwindow
	qwinid
	qwinname
	qwsys
	qdir   // wsys/n
	qshift = 8
	qmask  = 0xFF
)

// files are a window's files, in its directory and in wsys/n.
var files = []struct {
	name string
	q    uint64
	perm uint32
}{
	{"cons", qcons, 0o600},
	{"consctl", qconsctl, 0o200},
	{"keyboard", qkeyboard, 0o400},
	{"label", qlabel, 0o600},
	{"pointer", qpointer, 0o600},
	{"screen", qscreen, 0o400},
	{"text", qtext, 0o400},
	{"wctl", qwctl, 0o600},
	{"window", qwindow, 0o400},
	{"winid", qwinid, 0o400},
	{"winname", qwinname, 0o400},
}

func qpath(id int, q uint64) uint64 { return uint64(id)<<qshift | q }

// session is one window's conversation.
type session struct {
	f       *os.File
	fd      int
	srv     *styxservers.Server
	pending []*styx.Fcall
	snap    map[uint32][]byte // what a read at offset 0 found, by fid
	carry   map[uint32][]byte // a rune split between writes to cons
}

// msgReader returns one whole Styx message a read, so that a message
// is never left in the server's buffer while rio waits on the pipe.
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

func (r *rio) newSession(fd, id int) *session {
	f := os.NewFile(uintptr(fd), "rio")
	s := &session{f: f, fd: fd, snap: map[uint32][]byte{}, carry: map[uint32][]byte{}}
	s.srv = styxservers.New(rw{msgReader{f}, f}, r, qpath(id, qroot))
	return s
}

func (r *rio) dir(path uint64, name string, perm uint32) *styx.Dir {
	d := &styx.Dir{Name: name, Mode: perm, UID: r.user, GID: r.user, MUID: r.user}
	d.Qid.Path = path
	if perm&styx.DMDIR != 0 {
		d.Qid.Type = styx.QTDIR
	}
	return d
}

// Stat, Walk and Readdir are the Navigator, for every window's server.
func (r *rio) Stat(path uint64) (*styx.Dir, error) {
	id := int(path >> qshift)
	if r.win(id) == nil {
		return nil, styxservers.Error(edeleted)
	}
	switch q := path & qmask; q {
	case qroot:
		return r.dir(path, ".", styx.DMDIR|0o500), nil
	case qwsys:
		return r.dir(path, "wsys", styx.DMDIR|0o500), nil
	case qdir:
		return r.dir(path, strconv.Itoa(id), styx.DMDIR|0o500), nil
	default:
		for _, f := range files {
			if f.q == q {
				return r.dir(path, f.name, f.perm), nil
			}
		}
	}
	return nil, styxservers.Enotfound
}

func (r *rio) Walk(parent uint64, name string) (*styx.Dir, error) {
	id := int(parent >> qshift)
	if r.win(id) == nil {
		return nil, styxservers.Error(edeleted)
	}
	q := parent & qmask
	switch {
	case name == "..":
		if q == qdir {
			// wsys/n/.. is wsys of the window the server is for, which
			// this path does not record; any window's wsys lists the
			// same.
			return r.Stat(qpath(id, qwsys))
		}
		return r.Stat(qpath(id, qroot))
	case q == qroot && name == "wsys":
		return r.Stat(qpath(id, qwsys))
	case q == qwsys:
		n, err := strconv.Atoi(name)
		if err != nil || strconv.Itoa(n) != name || r.win(n) == nil {
			return nil, styxservers.Enotfound
		}
		return r.Stat(qpath(n, qdir))
	case q == qroot || q == qdir:
		for _, f := range files {
			if f.name == name {
				return r.Stat(qpath(id, f.q))
			}
		}
	}
	return nil, styxservers.Enotfound
}

func (r *rio) Readdir(path uint64, offset, count int) []*styx.Dir {
	id := int(path >> qshift)
	var all []*styx.Dir
	switch path & qmask {
	case qroot, qdir:
		for _, f := range files {
			d, _ := r.Stat(qpath(id, f.q))
			all = append(all, d)
		}
		if path&qmask == qroot {
			d, _ := r.Stat(qpath(id, qwsys))
			all = append(all, d)
		}
	case qwsys:
		for _, w := range r.wins {
			d, _ := r.Stat(qpath(w.id, qdir))
			all = append(all, d)
		}
	}
	if offset >= len(all) {
		return nil
	}
	all = all[offset:]
	return all[:min(count, len(all))]
}

// input serves one message from window w's pipe; false when the pipe
// has closed.
func (r *rio) input(w *window) bool {
	s := w.sess
	t, err := s.srv.Next()
	if err != nil {
		return false
	}
	switch t.Type {
	case styx.Topen:
		r.open(s, t)
	case styx.Tread:
		r.read(s, t)
	case styx.Twrite:
		r.write(s, t)
	case styx.Tflush:
		for i, p := range s.pending {
			if p.Tag == t.Oldtag {
				s.pending = append(s.pending[:i], s.pending[i+1:]...)
				break
			}
		}
		s.srv.Default(t)
	case styx.Tclunk, styx.Tremove:
		if f := s.srv.GetFid(t.Fid); f != nil {
			r.clunk(s, f)
		}
		s.srv.Default(t)
	default:
		s.srv.Default(t)
	}
	return true
}

func (r *rio) open(s *session, t *styx.Fcall) {
	f, mode, d, err := s.srv.CanOpen(t)
	if err != nil {
		s.srv.Error(t, err.Error())
		return
	}
	w := r.win(int(d.Qid.Path >> qshift))
	switch d.Qid.Path & qmask {
	case qpointer:
		w.mouseOpens++
		w.mouseq = nil
	case qkeyboard:
		w.kbdOpens++
	case qconsctl:
		w.consctlOpens++
	}
	f.Open(mode, d.Qid)
	s.srv.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: d.Qid, Iounit: s.srv.Iounit()})
}

// clunk forgets what rio kept for fid f, and what its opening started.
func (r *rio) clunk(s *session, f *styxservers.Fid) {
	delete(s.snap, f.Fid)
	delete(s.carry, f.Fid)
	keep := s.pending[:0]
	for _, p := range s.pending {
		if p.Fid != f.Fid {
			keep = append(keep, p)
		}
	}
	s.pending = keep
	w := r.win(int(f.Path >> qshift))
	if !f.IsOpen || w == nil {
		return
	}
	switch f.Path & qmask {
	case qpointer:
		if w.mouseOpens--; w.mouseOpens == 0 {
			// The program that drew in the window has finished with
			// it: the text shows again.
			w.mouseq = nil
			w.dirty = true
		}
	case qkeyboard:
		if w.kbdOpens--; w.kbdOpens == 0 {
			// Keys nobody read are typing for the text.
			keys := string(w.kbdq)
			w.kbdq = nil
			for _, c := range keys {
				w.text.typed(c)
			}
			w.dirty = true
		}
	case qconsctl:
		if w.consctlOpens--; w.consctlOpens == 0 {
			w.raw = false
		}
	}
}

func (r *rio) read(s *session, t *styx.Fcall) {
	f, err := s.srv.CanRead(t)
	if err != nil {
		s.srv.Error(t, err.Error())
		return
	}
	q := f.Path & qmask
	w := r.win(int(f.Path >> qshift))
	if w == nil {
		s.srv.Error(t, edeleted)
		return
	}
	switch q {
	case qroot, qwsys, qdir:
		s.srv.Read(t)
		return
	case qcons, qpointer, qkeyboard:
		if b, ok := r.take(w, q, int(t.Count)); ok {
			s.srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: b})
		} else {
			s.pending = append(s.pending, t)
		}
		return
	}
	if t.Offset == 0 {
		var b []byte
		switch q {
		case qscreen, qwindow:
			img := r.screen
			if q == qwindow {
				img = w.img
			}
			if b, err = imageFile(img); err != nil {
				s.srv.Error(t, err.Error())
				return
			}
		default:
			b = []byte(r.contents(w, q))
		}
		s.snap[f.Fid] = b
	}
	b := s.snap[f.Fid]
	off := min(int(t.Offset), len(b))
	s.srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: b[off:min(off+int(t.Count), len(b))]})
}

// imageFile is img as an uncompressed image(6) file.
func imageFile(img *draw.Image) ([]byte, error) {
	r := img.R
	b := fmt.Appendf(nil, "%11s %11d %11d %11d %11d ", img.Chan, r.Min.X, r.Min.Y, r.Max.X, r.Max.Y)
	pix, err := img.Unload(r)
	if err != nil {
		return nil, err
	}
	return append(b, pix...), nil
}

// contents is what a plain file of window w reads as.
func (r *rio) contents(w *window, q uint64) string {
	switch q {
	case qlabel:
		return w.label
	case qtext:
		return w.text.all()
	case qwinid:
		return fmt.Sprintf("%11d ", w.id)
	case qwinname:
		return w.name
	case qwctl:
		vis, cur := "visible", "notcurrent"
		if w.hidden {
			vis = "hidden"
		}
		if r.current == w {
			cur = "current"
		}
		return fmt.Sprintf("%11d %11d %11d %11d %11s %11s ", w.r.Min.X, w.r.Min.Y, w.r.Max.X, w.r.Max.Y, vis, cur)
	}
	return ""
}

// take is what a read of cons, pointer or keyboard of window w gets
// now, if anything.
func (r *rio) take(w *window, q uint64, count int) ([]byte, bool) {
	switch {
	case q == qpointer && len(w.mouseq) > 0:
		b := w.mouseq[0]
		w.mouseq = w.mouseq[1:]
		return b, true
	case q == qkeyboard && len(w.kbdq) > 0:
		return takeRunes(&w.kbdq, count), true
	case q == qcons && w.raw && len(w.rawq) > 0:
		return takeRunes(&w.rawq, count), true
	case q == qcons && !w.raw:
		return w.text.read(count)
	}
	return nil, false
}

// takeRunes takes at most max bytes of whole runes off the front of q.
func takeRunes(q *[]byte, max int) []byte {
	n := min(len(*q), max)
	for n > 0 && n < len(*q) && !utf8.RuneStart((*q)[n]) {
		n--
	}
	b := append([]byte(nil), (*q)[:n]...)
	*q = (*q)[n:]
	return b
}

// serveReads answers the waiting reads that can be answered now.
func (r *rio) serveReads() {
	for _, w := range r.wins {
		s := w.sess
		var keep []*styx.Fcall
		for _, t := range s.pending {
			f := s.srv.GetFid(t.Fid)
			var fw *window
			if f != nil {
				fw = r.win(int(f.Path >> qshift))
			}
			if fw == nil {
				s.srv.Error(t, edeleted)
				continue
			}
			if b, ok := r.take(fw, f.Path&qmask, int(t.Count)); ok {
				s.srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: b})
			} else {
				keep = append(keep, t)
			}
		}
		s.pending = keep
	}
}

func (r *rio) write(s *session, t *styx.Fcall) {
	f, err := s.srv.CanWrite(t)
	if err != nil {
		s.srv.Error(t, err.Error())
		return
	}
	w := r.win(int(f.Path >> qshift))
	if w == nil {
		s.srv.Error(t, edeleted)
		return
	}
	if e := r.write1(s, f, w, t.Data); e != "" {
		s.srv.Error(t, e)
		return
	}
	s.srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
}

func (r *rio) write1(s *session, f *styxservers.Fid, w *window, data []byte) string {
	switch f.Path & qmask {
	case qcons:
		// A rune cut at the end waits for the next write.
		b := append(s.carry[f.Fid], data...)
		n := len(b)
		if i := lastStart(b); n > 0 && !utf8.FullRune(b[i:]) {
			n = i
		}
		s.carry[f.Fid] = append([]byte(nil), b[n:]...)
		w.text.output(string(b[:n]))
		w.follow = w.follow || w.scroll
		w.dirty = true
	case qconsctl:
		for _, c := range strings.Fields(string(data)) {
			switch c {
			case "rawon":
				w.raw = true
			case "rawoff":
				w.raw = false
			case "holdon", "holdoff":
			default:
				return "unknown control message"
			}
		}
	case qlabel:
		w.label = string(data)
	case qpointer:
		v := strings.Fields(strings.TrimPrefix(string(data), "m"))
		if len(v) < 2 {
			return "bad pointer message"
		}
		x, err1 := strconv.Atoi(v[0])
		y, err2 := strconv.Atoi(v[1])
		if err1 != nil || err2 != nil {
			return "bad pointer message"
		}
		// Move rio's pointer; rio sees the move as an event.
		if fd, err := sys.Open("/dev/pointer", sys.OWRITE); err == nil {
			_, _ = syscall.Write(fd, fmt.Appendf(nil, "m%d %d", x, y))
			syscall.Close(fd)
		}
	case qwctl:
		c, e := parseWctl(strings.TrimSpace(string(data)))
		if e != "" {
			return e
		}
		return r.wctl(w, c)
	default:
		return styxservers.Eperm.Error()
	}
	return ""
}

// lastStart is where the last rune of b starts.
func lastStart(b []byte) int {
	i := len(b) - 1
	for i > 0 && !utf8.RuneStart(b[i]) {
		i--
	}
	return max(i, 0)
}

// wctlreq is a wctl request: its verb, options and, for new, a command.
type wctlreq struct {
	verb       string
	r          *draw.Rectangle
	dx, dy     *int
	minx, miny *int
	cmd        string
}

// parseWctl parses a wctl request as rio(4) gives them: a verb, then
// -r x0 y0 x1 y1, -dx n, -dy n, -minx n, -miny n, and for new a
// command.
func parseWctl(s string) (wctlreq, string) {
	t := strings.Fields(s)
	if len(t) == 0 {
		return wctlreq{}, ebadwctl
	}
	c := wctlreq{verb: t[0]}
	t = t[1:]
	num := func() (int, bool) {
		if len(t) == 0 {
			return 0, false
		}
		v, err := strconv.Atoi(t[0])
		t = t[1:]
		return v, err == nil
	}
	for len(t) > 0 {
		var p **int
		switch t[0] {
		case "-r":
			t = t[1:]
			var v [4]int
			for i := range v {
				x, ok := num()
				if !ok {
					return c, ebadrect
				}
				v[i] = x
			}
			r := draw.Rect(v[0], v[1], v[2], v[3])
			c.r = &r
			continue
		case "-dx":
			p = &c.dx
		case "-dy":
			p = &c.dy
		case "-minx":
			p = &c.minx
		case "-miny":
			p = &c.miny
		}
		if p == nil {
			break
		}
		t = t[1:]
		v, ok := num()
		if !ok {
			return c, ebadrect
		}
		*p = &v
	}
	if len(t) > 0 {
		if c.verb != "new" {
			return c, ebadwctl
		}
		c.cmd = strings.Join(t, " ")
	}
	return c, ""
}
