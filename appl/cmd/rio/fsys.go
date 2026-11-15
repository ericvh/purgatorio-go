package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode"
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
// its cons, consctl, kbdin, keyboard, pointer, label, screen, text,
// wctl, window, winid and winname there, with the rest of /dev after
// them, and wsys/n/ holding the same files of every window. A Qid path
// is the window's number above the low eight bits and the file below.
//
// rio also serves its files on /chan/rio.user.pid, made with
// file2chan, which stands in for Plan 9's /srv/rio.user.pid: each open
// of it is a conversation of its own, to be mounted. Attached with no
// name it holds wctl, which makes windows, and wsys/; attached with
// the name new and wctl's options it makes a window and is that
// window's directory, for as long as the window lasts. Window 0 in a
// Qid path is that root.

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
	qdir // wsys/n
	qkbdin
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
	{"kbdin", qkbdin, 0o200},
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

// session is a conversation with rio's file server: a window's pipe,
// or one open of the service file, /chan/rio.user.pid.
type session struct {
	srv     *styxservers.Server
	pending []*styx.Fcall
	snap    map[uint32][]byte // what a read at offset 0 found, by fid
	carry   map[uint32][]byte // a rune split between writes to cons or kbdin

	// A window's pipe.
	f   *os.File
	fd  int // -1 on the service file
	win *window

	// An open of the service file: file2chan's fid, the T-messages
	// written and not yet served (and part of one), and the R-messages
	// and the reads waiting for them.
	fid   int
	in    []byte
	msgs  [][]byte
	out   [][]byte
	reads []*sys.ReadReq
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

// newSession serves window w on the pipe fd.
func (r *rio) newSession(fd int, w *window) *session {
	f := os.NewFile(uintptr(fd), "rio")
	s := &session{f: f, fd: fd, win: w, snap: map[uint32][]byte{}, carry: map[uint32][]byte{}}
	s.srv = styxservers.New(rw{msgReader{f}, f}, r, qpath(w.id, qroot))
	r.sess = append(r.sess, s)
	return s
}

// srvConn is the session of an open of the service file as an
// io.ReadWriter: a read is the next whole T-message written, a write
// an R-message for the reads.
type srvConn struct{ s *session }

func (c srvConn) Read(b []byte) (int, error) {
	if len(c.s.msgs) == 0 {
		return 0, io.EOF
	}
	n := copy(b, c.s.msgs[0])
	c.s.msgs = c.s.msgs[1:]
	return n, nil
}

func (c srvConn) Write(b []byte) (int, error) {
	c.s.out = append(c.s.out, append([]byte(nil), b...))
	return len(b), nil
}

// conn finds the session of file2chan's fid, making it if mk is set.
func (r *rio) conn(fid int, mk bool) *session {
	for _, s := range r.sess {
		if s.fd < 0 && s.fid == fid {
			return s
		}
	}
	if !mk {
		return nil
	}
	s := &session{fd: -1, fid: fid, snap: map[uint32][]byte{}, carry: map[uint32][]byte{}}
	s.srv = styxservers.New(srvConn{s}, r, qpath(0, qroot))
	r.sess = append(r.sess, s)
	return s
}

// service takes one request on the service file. Each open of it is a
// Styx conversation of its own, as a pipe to rio is: it is mounted, and
// the attach name new makes a window (see attach).
func (r *rio) service() {
	req, err := r.srvio.Next()
	if err != nil {
		r.srvio = nil
		return
	}
	switch q := req.(type) {
	case *sys.ReadReq:
		if q.Closed() {
			r.hangup(r.conn(q.Fid, false))
			return
		}
		s := r.conn(q.Fid, true)
		s.reads = append(s.reads, q)
		s.flush()
	case *sys.WriteReq:
		if q.Closed() {
			r.hangup(r.conn(q.Fid, false))
			return
		}
		s := r.conn(q.Fid, true)
		q.Reply(len(q.Data), "")
		s.in = append(s.in, q.Data...)
		for {
			n := int(styx.Size(s.in))
			if n < 7 || n > len(s.in) {
				break
			}
			s.msgs = append(s.msgs, append([]byte(nil), s.in[:n]...))
			s.in = s.in[n:]
		}
		for len(s.msgs) > 0 {
			if !r.input(s) {
				r.hangup(s)
				return
			}
		}
		s.flush()
	}
}

// flush answers the waiting reads of the service file with whole
// R-messages, as many as fit.
func (s *session) flush() {
	for len(s.reads) > 0 && len(s.out) > 0 {
		q := s.reads[0]
		s.reads = s.reads[1:]
		var b []byte
		for len(s.out) > 0 && len(b)+len(s.out[0]) <= q.Count {
			b = append(b, s.out[0]...)
			s.out = s.out[1:]
		}
		if len(b) == 0 {
			n := min(q.Count, len(s.out[0]))
			b = s.out[0][:n]
			s.out[0] = s.out[0][n:]
		}
		q.Reply(b, "")
	}
}

// hangup ends session s, an open of the service file that has been
// closed, and takes away the windows made on it.
func (r *rio) hangup(s *session) {
	if s == nil {
		return
	}
	r.dropSession(s)
	for _, w := range append([]*window(nil), r.wins...) {
		if w.conn == s {
			r.remove(w)
		}
	}
}

// dropSession forgets session s; its waiting reads fail.
func (r *rio) dropSession(s *session) {
	for i, x := range r.sess {
		if x == s {
			r.sess = append(r.sess[:i], r.sess[i+1:]...)
			break
		}
	}
	for _, t := range s.pending {
		s.srv.Error(t, edeleted)
	}
	s.pending = nil
	if s.f != nil {
		s.f.Close()
	}
	s.flush()
}

func (r *rio) dir(path uint64, name string, perm uint32) *styx.Dir {
	d := &styx.Dir{Name: name, Mode: perm, UID: r.user, GID: r.user, MUID: r.user}
	d.Qid.Path = path
	if perm&styx.DMDIR != 0 {
		d.Qid.Type = styx.QTDIR
	}
	return d
}

// srvfiles are the files of the service file's root (window 0).
var srvfiles = map[uint64]bool{qroot: true, qwsys: true, qwctl: true}

// Stat, Walk and Readdir are the Navigator, for every window's server.
func (r *rio) Stat(path uint64) (*styx.Dir, error) {
	id := int(path >> qshift)
	if id == 0 && !srvfiles[path&qmask] || id != 0 && r.win(id) == nil {
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
	if id != 0 && r.win(id) == nil {
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
	case q == qroot && id == 0:
		if name == "wctl" {
			return r.Stat(qpath(id, qwctl))
		}
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
		if id == 0 {
			for _, q := range []uint64{qwctl, qwsys} {
				d, _ := r.Stat(qpath(id, q))
				all = append(all, d)
			}
			break
		}
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

// input serves one message of session s; false when the conversation
// is over.
func (r *rio) input(s *session) bool {
	t, err := s.srv.Next()
	if err != nil {
		return false
	}
	switch t.Type {
	case styx.Tattach:
		r.attach(s, t)
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

// attach attaches to the root of the session's window, or of the
// service file. As in Plan 9's rio, the attach name new, with wctl's
// options for new and no command, makes a window and attaches to its
// root instead: window -m mounts the service file so. The window goes
// when the last of its files open is closed, or the conversation ends.
func (r *rio) attach(s *session, t *styx.Fcall) {
	f := strings.Fields(t.Aname)
	if len(f) == 0 || f[0] != "new" {
		s.srv.Attach(t)
		return
	}
	c, e := parseWctl(t.Aname)
	if e == "" && c.cmd != "" {
		e = ebadwctl
	}
	var w *window
	if e == "" {
		w, e = r.wctlNew(c, s)
	}
	if e != "" {
		s.srv.Error(t, e)
		return
	}
	root := s.srv.RootPath
	s.srv.RootPath = qpath(w.id, qroot)
	if s.srv.Attach(t) == nil {
		r.remove(w)
	}
	s.srv.RootPath = root
}

func (r *rio) open(s *session, t *styx.Fcall) {
	f, mode, d, err := s.srv.CanOpen(t)
	if err != nil {
		s.srv.Error(t, err.Error())
		return
	}
	w := r.win(int(d.Qid.Path >> qshift))
	if w != nil {
		w.opens++
	}
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
	if w.opens--; w.opens == 0 && w.conn != nil {
		// A window made by an attach goes when the last of its files
		// open is closed, as a window in Plan 9's rio goes with its
		// last reference. (Its conversation can outlive its programs
		// for a while: the mount driver clunks walked fids late.)
		defer r.remove(w)
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
			// As in Plan 9, raw and hold modes end with the last
			// close of consctl; hold mode only if consctl started it.
			w.raw = false
			if w.consHold {
				r.setHold(w, false)
			}
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
	id := int(f.Path >> qshift)
	w := r.win(id)
	if w == nil && id != 0 {
		s.srv.Error(t, edeleted)
		return
	}
	switch q {
	case qroot, qwsys, qdir:
		s.srv.Read(t)
		return
	}
	if w == nil {
		// The service file's wctl, which has no window.
		s.srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag})
		return
	}
	switch q {
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
	for _, s := range r.sess {
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
		s.flush()
	}
}

func (r *rio) write(s *session, t *styx.Fcall) {
	f, err := s.srv.CanWrite(t)
	if err != nil {
		s.srv.Error(t, err.Error())
		return
	}
	id := int(f.Path >> qshift)
	w := r.win(id)
	if w == nil && id != 0 {
		s.srv.Error(t, edeleted)
		return
	}
	if e := r.write1(s, f, w, t.Data); e != "" {
		s.srv.Error(t, e)
		return
	}
	s.srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
}

// whole takes the whole runes of data, after what an earlier write to
// fid left of a rune cut in two, and keeps what is left of one now.
func (s *session) whole(fid uint32, data []byte) string {
	b := append(s.carry[fid], data...)
	n := len(b)
	if i := lastStart(b); n > 0 && !utf8.FullRune(b[i:]) {
		n = i
	}
	s.carry[fid] = append([]byte(nil), b[n:]...)
	return string(b[:n])
}

func (r *rio) write1(s *session, f *styxservers.Fid, w *window, data []byte) string {
	if w == nil {
		// The service file's wctl: it can only make windows.
		c, e := parseWctl(strings.TrimSpace(string(data)))
		if e == "" && c.verb != "new" {
			e = ebadwctl
		}
		if e == "" {
			_, e = r.wctlNew(c, nil)
		}
		return e
	}
	switch f.Path & qmask {
	case qcons:
		w.text.output(s.whole(f.Fid, data))
		w.follow = w.follow || w.scroll
		w.dirty = true
	case qkbdin:
		// rio(4): what is written is typed at the window.
		for _, c := range s.whole(f.Fid, data) {
			r.keyTo(w, c)
		}
	case qconsctl:
		for _, c := range strings.Fields(string(data)) {
			switch c {
			case "rawon":
				w.raw = true
			case "rawoff":
				w.raw = false
			case "holdon":
				r.setHold(w, true)
				w.consHold = true
			case "holdoff":
				r.setHold(w, false)
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

// num is a number in a wctl request: with a sign, it is relative to
// the window's current value, as in Plan 9's rio.
type num struct {
	v   int
	rel bool
}

func (n num) of(cur int) int {
	if n.rel {
		return cur + n.v
	}
	return n.v
}

// wctlreq is a wctl request: its verb, options and, for new, a command.
type wctlreq struct {
	verb       string
	r          *[4]num
	dx, dy     *num
	minx, miny *num
	hide       bool
	scroll     *bool  // -scroll or -noscroll
	dir        string // -cd
	cmd        string
}

// rect is rectangle cur changed as the request says: -r gives the
// corners, -minx and -miny move it, -dx and -dy give its size.
func (c *wctlreq) rect(cur draw.Rectangle) draw.Rectangle {
	nr := cur
	if c.r != nil {
		nr = draw.Rect(c.r[0].of(cur.Min.X), c.r[1].of(cur.Min.Y), c.r[2].of(cur.Max.X), c.r[3].of(cur.Max.Y))
	}
	if c.minx != nil {
		nr = nr.Add(draw.Pt(c.minx.of(nr.Min.X)-nr.Min.X, 0))
	}
	if c.miny != nil {
		nr = nr.Add(draw.Pt(0, c.miny.of(nr.Min.Y)-nr.Min.Y))
	}
	if c.dx != nil {
		nr.Max.X = nr.Min.X + c.dx.of(nr.Dx())
	}
	if c.dy != nil {
		nr.Max.Y = nr.Min.Y + c.dy.of(nr.Dy())
	}
	return nr
}

// parseWctl parses a wctl request as rio(4) gives them: a verb, then
// -r x0 y0 x1 y1, -dx n, -dy n, -minx n, -miny n, and for new also
// -hide, -scroll, -noscroll, -cd dir, -pid n (taken and not used) and
// a command.
func parseWctl(s string) (wctlreq, string) {
	s = strings.TrimSpace(s)
	t := strings.Fields(s)
	if len(t) == 0 {
		return wctlreq{}, ebadwctl
	}
	c := wctlreq{verb: t[0]}
	t = t[1:]
	number := func() (num, bool) {
		if len(t) == 0 {
			return num{}, false
		}
		v, err := strconv.Atoi(t[0])
		n := num{v: v, rel: t[0][0] == '+' || t[0][0] == '-'}
		t = t[1:]
		return n, err == nil
	}
	isnew := c.verb == "new"
	for len(t) > 0 {
		var p **num
		switch t[0] {
		case "-r":
			t = t[1:]
			var v [4]num
			for i := range v {
				x, ok := number()
				if !ok {
					return c, ebadrect
				}
				v[i] = x
			}
			c.r = &v
			continue
		case "-dx":
			p = &c.dx
		case "-dy":
			p = &c.dy
		case "-minx":
			p = &c.minx
		case "-miny":
			p = &c.miny
		case "-hide", "-scroll", "-noscroll":
			if !isnew {
				return c, ebadwctl
			}
			if t[0] == "-hide" {
				c.hide = true
			} else {
				on := t[0] == "-scroll"
				c.scroll = &on
			}
			t = t[1:]
			continue
		case "-cd", "-pid":
			if !isnew || len(t) < 2 {
				return c, ebadwctl
			}
			if t[0] == "-cd" {
				c.dir = t[1]
			}
			t = t[2:]
			continue
		}
		if p == nil {
			break
		}
		t = t[1:]
		v, ok := number()
		if !ok {
			return c, ebadrect
		}
		*p = &v
	}
	if len(t) > 0 {
		if !isnew {
			return c, ebadwctl
		}
		// The command is the rest of the line as it was written.
		rest := s
		for range len(strings.Fields(s)) - len(t) {
			rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
			if i := strings.IndexFunc(rest, unicode.IsSpace); i >= 0 {
				rest = rest[i:]
			} else {
				rest = ""
			}
		}
		c.cmd = strings.TrimSpace(rest)

	}
	return c, ""
}
