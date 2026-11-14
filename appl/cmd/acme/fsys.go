package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

// acme's files (acme(4), fsys.b and xfid.b). A Qid path is a window
// number shifted left 8 bits and the file's kind.
const (
	qDir = iota
	qAcme
	qCons
	qConsctl
	qIndex
	qLabel
	qNew
	qwAddr
	qwBody
	qwCtl
	qwData
	qwEvent
	qwRdsel
	qwTag
	qwWrsel
	qwXdata
	qwErrors
	nqid
)

const srvfd = 5 // the pipe's end in the mounting helper

type fentry struct {
	name string
	q    int
	perm uint32
}

var rootfiles = []fentry{
	{"cons", qCons, 0o600},
	{"consctl", qConsctl, 0o000},
	{"index", qIndex, 0o400},
	{"label", qLabel, 0o600},
	{"new", qNew, styx.DMDIR | 0o500},
}

var winfiles = []fentry{
	{"addr", qwAddr, 0o600},
	{"body", qwBody, styx.DMAPPEND | 0o600},
	{"ctl", qwCtl, 0o600},
	{"data", qwData, 0o600},
	{"errors", qwErrors, 0o200},
	{"event", qwEvent, 0o600},
	{"rdsel", qwRdsel, 0o400},
	{"tag", qwTag, styx.DMAPPEND | 0o600},
	{"wrsel", qwWrsel, 0o200},
	{"xdata", qwXdata, 0o600},
}

// Acme's errors.
const (
	Ebadctl   = "ill-formed control message"
	Eaddr     = "address out of range"
	Einuse    = "already in use"
	Ebadevent = "bad event syntax"
	Edeleted  = "window deleted"
)

func qpath(id, q int) uint64 { return uint64(id)<<8 | uint64(q) }
func qid(p uint64) int       { return int(p & 0x7F) }
func qwin(p uint64) int      { return int(p >> 8) }

type fsys struct {
	fd    int
	srv   *styxservers.Server
	user  string
	reads []*styx.Fcall // event reads waiting, with their windows
	rwin  []*Window
}

type fdrw int

func (f fdrw) Read(b []byte) (int, error) {
	n, err := syscall.Read(int(f), b)
	if n < 0 {
		n = 0
	}
	return n, err
}

func (f fdrw) Write(b []byte) (int, error) { return syscall.Write(int(f), b) }

// startfsys makes the pipe, starts the helper that mounts its other end
// on /mnt/acme, and serves this end.
func startfsys() {
	p, err := sys.Pipe()
	if err != nil {
		fatal("pipe: " + err.Error())
	}
	fs := &fsys{fd: p[1], user: "inferno"}
	if b, err := os.ReadFile("/dev/user"); err == nil {
		fs.user = string(b)
	}
	fs.srv = styxservers.New(fdrw(p[1]), fs, qpath(0, qDir))
	g.srv = fs
	_ = os.MkdirAll("/mnt/acme", 0o755)
	if _, err := sys.Spawn("/bin/acme.wasm", []string{"acme", "-M"}, sys.NEWFD,
		[][2]int{{0, 0}, {1, 1}, {2, 2}, {srvfd, p[0]}}); err != nil {
		fatal("cannot mount /mnt/acme: " + err.Error())
	}
	syscall.Close(p[0])
}

func (fs *fsys) dir(name string, q uint64, perm uint32) *styx.Dir {
	d := &styx.Dir{Name: name, Mode: perm, UID: fs.user, GID: fs.user, MUID: fs.user}
	d.Qid.Path = q
	if perm&styx.DMDIR != 0 {
		d.Qid.Type = styx.QTDIR
	}
	if perm&styx.DMAPPEND != 0 {
		d.Qid.Type |= styx.QTAPPEND
	}
	return d
}

// Stat, Walk and Readdir are the Navigator.
func (fs *fsys) Stat(p uint64) (*styx.Dir, error) {
	id, q := qwin(p), qid(p)
	if q == qDir {
		if id == 0 {
			return fs.dir(".", p, styx.DMDIR|0o500), nil
		}
		if g.row.lookid(id) == nil {
			return nil, styxservers.Enotfound
		}
		return fs.dir(strconv.Itoa(id), p, styx.DMDIR|0o500), nil
	}
	if q == qNew {
		return fs.dir("new", p, styx.DMDIR|0o500), nil
	}
	for _, f := range rootfiles {
		if f.q == q && id == 0 {
			return fs.dir(f.name, p, f.perm), nil
		}
	}
	for _, f := range winfiles {
		if f.q == q {
			return fs.dir(f.name, p, f.perm), nil
		}
	}
	return nil, styxservers.Enotfound
}

func (fs *fsys) Walk(parent uint64, name string) (*styx.Dir, error) {
	id, q := qwin(parent), qid(parent)
	if name == ".." {
		return fs.Stat(qpath(0, qDir))
	}
	switch {
	case q == qDir && id == 0:
		for _, f := range rootfiles {
			if f.name == name {
				return fs.dir(f.name, qpath(0, f.q), f.perm), nil
			}
		}
		if n, err := strconv.Atoi(name); err == nil && g.row.lookid(n) != nil {
			return fs.dir(name, qpath(n, qDir), styx.DMDIR|0o500), nil
		}
	case q == qDir || q == qNew:
		for _, f := range winfiles {
			if f.name == name {
				// Files in new belong to window 0 until opened.
				wid := id
				if q == qNew {
					wid = 0
				}
				return fs.dir(f.name, qpath(wid, f.q)|newflag(q == qNew), f.perm), nil
			}
		}
	}
	return nil, styxservers.Enotfound
}

// newflag marks the files of new, which make a window when opened.
func newflag(isnew bool) uint64 {
	if isnew {
		return 0x80
	}
	return 0
}

func (fs *fsys) Readdir(p uint64, offset, count int) []*styx.Dir {
	var all []*styx.Dir
	id, q := qwin(p), qid(p)
	if q == qDir && id == 0 {
		for _, f := range rootfiles {
			all = append(all, fs.dir(f.name, qpath(0, f.q), f.perm))
		}
		for _, w := range g.row.windows() {
			all = append(all, fs.dir(strconv.Itoa(w.id), qpath(w.id, qDir), styx.DMDIR|0o500))
		}
	} else {
		for _, f := range winfiles {
			all = append(all, fs.dir(f.name, qpath(id, f.q), f.perm))
		}
	}
	if offset >= len(all) {
		return nil
	}
	return all[offset:min(len(all), offset+count)]
}

// one answers the messages waiting.
func (fs *fsys) one() {
	for {
		t, err := fs.srv.Next()
		if err != nil {
			fatal("file server: " + err.Error())
		}
		fs.handle(t)
		if !fs.srv.Buffered() {
			return
		}
	}
}

func (fs *fsys) handle(t *styx.Fcall) {
	s := fs.srv
	switch t.Type {
	case styx.Topen:
		fs.open(t)
	case styx.Tread:
		c := s.GetFid(t.Fid)
		if c != nil && c.Qtype&styx.QTDIR != 0 {
			s.Read(t)
			return
		}
		if _, err := s.CanRead(t); err != nil {
			s.Error(t, err.Error())
			return
		}
		fs.read(t, c)
	case styx.Twrite:
		c, err := s.CanWrite(t)
		if err != nil {
			s.Error(t, err.Error())
			return
		}
		fs.write(t, c)
	case styx.Tflush:
		for i, r := range fs.reads {
			if r.Tag == t.Oldtag {
				fs.reads = append(fs.reads[:i], fs.reads[i+1:]...)
				fs.rwin = append(fs.rwin[:i], fs.rwin[i+1:]...)
				break
			}
		}
		s.Default(t)
	case styx.Tclunk:
		c := s.GetFid(t.Fid)
		if c != nil && c.IsOpen {
			if w := g.row.lookid(qwin(c.Path)); w != nil {
				fs.closed(w, qid(c.Path))
			}
		}
		s.Default(t)
	case styx.Twstat:
		// Truncating a file is allowed and ignored, as acme does.
		s.Reply(&styx.Fcall{Type: styx.Rwstat, Tag: t.Tag})
	default:
		s.Default(t)
	}
}

// open opens a file; a file in new makes a window first.
func (fs *fsys) open(t *styx.Fcall) {
	s := fs.srv
	c, mode, d, err := s.CanOpen(t)
	if err != nil {
		s.Error(t, err.Error())
		return
	}
	q := qid(c.Path)
	if c.Path&0x80 != 0 {
		col := g.row.col[len(g.row.col)-1]
		w := col.add(nil, nil, -1)
		c.Path = qpath(w.id, q)
		d.Qid.Path = c.Path
	}
	c.Open(mode, d.Qid)
	if id := qwin(c.Path); id != 0 {
		w := g.row.lookid(id)
		if w == nil {
			s.Error(t, Edeleted)
			c.IsOpen = false
			return
		}
		switch q {
		case qwEvent:
			if w.nopen[qwEvent] > 0 {
				c.IsOpen = false
				s.Error(t, Einuse)
				return
			}
			w.filemenu = false
		case qwAddr:
			if w.nopen[qwAddr] == 0 {
				w.addr = [2]int{0, 0}
				w.limit = [2]int{-1, -1}
			}
		case qwRdsel, qwWrsel:
			w.addr = [2]int{w.body.q0, w.body.q1}
		}
		w.nopen[q]++
		c.Data = &ofile{}
	}
	s.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: d.Qid, Iounit: s.Iounit()})
}

// ofile is an open file's state: a snapshot for reads of long files,
// and a partial rune left from the last write.
type ofile struct {
	snap    []byte
	snapset bool
	partial []byte
}

func (fs *fsys) closed(w *Window, q int) {
	if w.nopen[q] > 0 {
		w.nopen[q]--
	}
	if w.nopen[q] == 0 {
		switch q {
		case qwData, qwXdata:
			w.nomark = false
		case qwEvent:
			w.filemenu = true
			w.events = nil
			w.dumpstr, w.dumpdir = "", ""
			for i := 0; i < len(fs.reads); i++ {
				if fs.rwin[i] == w {
					fs.srv.Error(fs.reads[i], Edeleted)
					fs.reads = append(fs.reads[:i], fs.reads[i+1:]...)
					fs.rwin = append(fs.rwin[:i], fs.rwin[i+1:]...)
					i--
				}
			}
		}
	}
	g.retag[w] = true
}

// readbytes replies with the part of b at the read's offset.
func readbytes(s *styxservers.Server, t *styx.Fcall, b []byte) {
	off := int(t.Offset)
	if off > len(b) {
		off = len(b)
	}
	b = b[off:]
	if len(b) > int(t.Count) {
		b = b[:t.Count]
	}
	s.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: b})
}

// utfprefix is the longest prefix of b of whole runes no longer than n.
func utfprefix(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	b = b[:n]
	for len(b) > 0 && !utf8.FullRune(b[lastrunestart(b):]) {
		b = b[:lastrunestart(b)]
	}
	return b
}

func lastrunestart(b []byte) int {
	i := len(b) - 1
	for i > 0 && !utf8.RuneStart(b[i]) {
		i--
	}
	return max(i, 0)
}

func (fs *fsys) read(t *styx.Fcall, c *styxservers.Fid) {
	s := fs.srv
	q := qid(c.Path)
	id := qwin(c.Path)
	switch q {
	case qCons, qLabel:
		s.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag})
		return
	case qIndex:
		var b strings.Builder
		for _, w := range g.row.windows() {
			tag := string(w.tag.file.b)
			if i := strings.IndexByte(tag, '\n'); i >= 0 {
				tag = tag[:i]
			}
			fmt.Fprintf(&b, "%11d %11d %11d %11d %11d %s\n", w.id, w.tag.file.Nc(), w.body.file.Nc(),
				b2i(w.isdir), b2i(w.body.file.Dirty()), tag)
		}
		readbytes(s, t, []byte(b.String()))
		return
	}
	w := g.row.lookid(id)
	if w == nil {
		s.Error(t, Edeleted)
		return
	}
	of, _ := c.Data.(*ofile)
	switch q {
	case qwBody, qwTag:
		// The whole text, as UTF-8, snapshotted at offset 0.
		tx := &w.body
		if q == qwTag {
			tx = &w.tag
		}
		if t.Offset == 0 || of == nil || !of.snapset {
			if of == nil {
				of = &ofile{}
				c.Data = of
			}
			of.snap, of.snapset = []byte(string(tx.file.b)), true
		}
		off := min(int(t.Offset), len(of.snap))
		s.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: utfprefix(of.snap[off:], int(t.Count))})
	case qwCtl:
		readbytes(s, t, []byte(ctlprint(w)))
	case qwAddr:
		readbytes(s, t, []byte(fmt.Sprintf("%11d %11d ", w.addr[0], w.addr[1])))
	case qwData, qwXdata:
		nc := w.body.file.Nc()
		if w.addr[0] > nc {
			s.Error(t, Eaddr)
			return
		}
		end := nc
		if q == qwXdata {
			end = min(w.addr[1], nc)
		}
		// Whole runes from addr, as many as fit.
		var b []byte
		q0 := w.addr[0]
		for q0 < end {
			r := w.body.file.b[q0]
			if len(b)+utf8.RuneLen(r) > int(t.Count) {
				break
			}
			b = utf8.AppendRune(b, r)
			q0++
		}
		w.addr[0] = q0
		if q == qwData {
			w.addr[1] = q0
		}
		s.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: b})
	case qwRdsel:
		var b []byte
		for w.addr[0] < w.addr[1] && w.addr[0] < w.body.file.Nc() {
			r := w.body.file.b[w.addr[0]]
			if len(b)+utf8.RuneLen(r) > int(t.Count) {
				break
			}
			b = utf8.AppendRune(b, r)
			w.addr[0]++
		}
		s.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: b})
	case qwEvent:
		if len(w.events) > 0 {
			fs.answer(t, w)
			return
		}
		fs.reads = append(fs.reads, t)
		fs.rwin = append(fs.rwin, w)
	default:
		s.Error(t, styxservers.Eperm.Error())
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ctlprint is what ctl reads give: the window's number, the tag's and
// body's lengths, whether it is a directory and dirty, and the body's
// width, font and tab width.
func ctlprint(w *Window) string {
	return fmt.Sprintf("%11d %11d %11d %11d %11d %11d %s %11d ", w.id, w.tag.file.Nc(), w.body.file.Nc(),
		b2i(w.isdir), b2i(w.body.file.Dirty()), w.body.fr.r.Dx(), quoteFont(g.font.Name), w.body.fr.maxtab)
}

func quoteFont(s string) string {
	if s == "" || strings.ContainsAny(s, " '") {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return s
}

// answer replies to an event read with what is queued.
func (fs *fsys) answer(t *styx.Fcall, w *Window) {
	n := min(len(w.events), int(t.Count))
	// Whole events only, unless one is longer than the read.
	if i := strings.LastIndexByte(string(w.events[:n]), '\n'); i >= 0 && n < len(w.events) {
		n = i + 1
	}
	fs.srv.Reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: append([]byte(nil), w.events[:n]...)})
	w.events = w.events[n:]
}

// fsysEvents answers reads waiting for w's events.
func fsysEvents(w *Window) {
	fs := g.srv
	if fs == nil {
		return
	}
	for i := 0; i < len(fs.reads) && len(w.events) > 0; i++ {
		if fs.rwin[i] == w {
			t := fs.reads[i]
			fs.reads = append(fs.reads[:i], fs.reads[i+1:]...)
			fs.rwin = append(fs.rwin[:i], fs.rwin[i+1:]...)
			fs.answer(t, w)
			i--
		}
	}
}

// fsysWindowGone ends the reads of a deleted window.
func fsysWindowGone(w *Window) {
	fs := g.srv
	if fs == nil {
		return
	}
	for i := 0; i < len(fs.reads); i++ {
		if fs.rwin[i] == w {
			fs.srv.Error(fs.reads[i], Edeleted)
			fs.reads = append(fs.reads[:i], fs.reads[i+1:]...)
			fs.rwin = append(fs.rwin[:i], fs.rwin[i+1:]...)
			i--
		}
	}
}

// runes turns a write's data into runes, keeping a partial rune at the
// end for the next write.
func runes(of *ofile, data []byte) []rune {
	if of != nil && len(of.partial) > 0 {
		data = append(append([]byte(nil), of.partial...), data...)
		of.partial = nil
	}
	k := len(data)
	if i := lastrunestart(data); k > 0 && !utf8.FullRune(data[i:]) {
		k = i
	}
	if of != nil && k < len(data) {
		of.partial = append([]byte(nil), data[k:]...)
	}
	return []rune(string(data[:k]))
}

func (fs *fsys) write(t *styx.Fcall, c *styxservers.Fid) {
	s := fs.srv
	q := qid(c.Path)
	ok := func() { s.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))}) }
	switch q {
	case qCons:
		warning(nil, string(t.Data))
		ok()
		return
	case qLabel, qConsctl:
		ok()
		return
	}
	w := g.row.lookid(qwin(c.Path))
	if w == nil {
		s.Error(t, Edeleted)
		return
	}
	of, _ := c.Data.(*ofile)
	if of != nil {
		of.snapset = false
	}
	switch q {
	case qwBody, qwTag:
		r := runes(of, t.Data)
		tx := &w.body
		if q == qwTag {
			tx = &w.tag
		}
		if len(r) > 0 {
			if q == qwBody && !w.nomark {
				g.seq++
				tx.file.Mark()
			}
			q0 := tx.file.Nc()
			tx.insert(q0, r, true)
			c1 := 'I'
			if q == qwTag {
				c1 = 'i'
			}
			w.event("E", c1, q0, q0+len(r), 0, r)
			if q == qwBody && !w.noscroll {
				tx.show(q0+len(r), q0+len(r), true)
			}
			if q == qwTag {
				w.tagchanged = true
			} else {
				g.retag[w] = true
			}
		}
		ok()
	case qwErrors:
		d := w.dir()
		warning(&d, string(t.Data))
		ok()
	case qwCtl:
		if err := ctlwrite(w, string(t.Data)); err != "" {
			s.Error(t, err)
			return
		}
		ok()
	case qwAddr:
		a := strings.TrimRight(string(t.Data), "\n")
		q0, q1, err := address(&w.body, w.addr[0], w.addr[1], a)
		if err != "" {
			if err == "address out of range" || err == "addresses out of order" {
				s.Error(t, Eaddr)
			} else {
				s.Error(t, "bad address syntax")
			}
			return
		}
		w.addr = [2]int{q0, q1}
		ok()
	case qwData, qwXdata, qwWrsel:
		r := runes(of, t.Data)
		nc := w.body.file.Nc()
		if w.addr[0] > nc || w.addr[1] > nc {
			s.Error(t, Eaddr)
			return
		}
		if !w.nomark {
			g.seq++
			w.body.file.Mark()
		}
		b := &w.body
		q0 := w.addr[0]
		if q == qwWrsel {
			q0 = w.addr[1]
		} else if w.addr[1] > q0 {
			w.event("F", 'D', q0, w.addr[1], 0, nil)
			b.delete(q0, w.addr[1], true)
			w.addr[1] = q0
		}
		if len(r) > 0 {
			b.insert(q0, r, true)
			w.event("F", 'I', q0, q0+len(r), 0, r)
		}
		if q == qwWrsel {
			w.addr[1] += len(r)
		} else {
			w.addr[0] = q0 + len(r)
			w.addr[1] = w.addr[0]
		}
		g.retag[w] = true
		ok()
	case qwEvent:
		if err := eventwrite(w, string(t.Data)); err != "" {
			s.Error(t, err)
			return
		}
		ok()
	default:
		s.Error(t, styxservers.Eperm.Error())
	}
}

// ctlwrite carries out ctl messages (xfid.b's ctlwrite).
func ctlwrite(w *Window, s string) string {
	settag := false
	for len(s) > 0 {
		s = strings.TrimLeft(s, "\n")
		if s == "" {
			break
		}
		line := s
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			line, s = s[:i], s[i+1:]
		} else {
			s = ""
		}
		f := strings.SplitN(line, " ", 2)
		arg := ""
		if len(f) > 1 {
			arg = f[1]
		}
		switch f[0] {
		case "lock", "unlock":
		case "clean":
			w.body.eq0 = -1
			w.body.file.Reset()
			w.body.file.mod = false
			w.dirty = false
			settag = true
		case "dirty":
			w.body.file.Mark()
			w.body.file.mod = true
			w.body.file.putseq = -1
			settag = true
		case "show":
			w.body.show(w.body.q0, w.body.q1, true)
		case "name":
			if arg == "" || len(f) < 2 {
				return Ebadctl
			}
			for _, c := range arg {
				if c <= ' ' {
					return "bad character in file name"
				}
			}
			g.seq++
			w.body.file.Mark()
			w.setname(arg)
		case "dump":
			if arg == "" {
				return Ebadctl
			}
			w.dumpstr = arg
		case "dumpdir":
			if arg == "" {
				return Ebadctl
			}
			w.dumpdir = arg
		case "delete":
			w.col.close(w, true)
			return ""
		case "del":
			if !w.clean(true) {
				return "file dirty"
			}
			w.col.close(w, true)
			return ""
		case "get":
			get(&w.body, "", false)
		case "put":
			put(&w.body, "")
		case "dot=addr":
			clampaddr(w)
			w.body.setselect(w.addr[0], w.addr[1])
			settag = true
		case "addr=dot":
			w.addr = [2]int{w.body.q0, w.body.q1}
		case "limit=addr":
			clampaddr(w)
			w.limit = w.addr
		case "nomark":
			w.nomark = true
		case "mark":
			g.seq++
			w.body.file.Mark()
			settag = true
		case "noscroll":
			w.noscroll = true
		case "scroll":
			w.noscroll = false
		case "cleartag":
			tag := string(w.tag.file.b)
			if i := strings.IndexByte(tag, '|'); i >= 0 {
				n := runelen(tag[:i+1])
				w.tag.delete(n, w.tag.file.Nc(), true)
				w.tag.insert(n, []rune(" "), true)
			}
			settag = true
		case "noecho", "echo", "menu", "nomenu":
		default:
			return Ebadctl
		}
	}
	if settag {
		w.settag()
	}
	return ""
}

func clampaddr(w *Window) {
	nc := w.body.file.Nc()
	w.addr[0] = max(0, min(w.addr[0], nc))
	w.addr[1] = max(w.addr[0], min(w.addr[1], nc))
}

// eventwrite carries out events written back: "Mx q0 q1\n" executes and
// "Ml q0 q1\n" looks (xfid.b's eventwrite).
func eventwrite(w *Window, s string) string {
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 || i < 2 {
			return Ebadevent
		}
		line := s[:i]
		s = s[i+1:]
		c := rune(line[1])
		f := strings.Fields(line[2:])
		if len(f) < 2 {
			return Ebadevent
		}
		q0, e0 := strconv.Atoi(f[0])
		q1, e1 := strconv.Atoi(f[1])
		if e0 != nil || e1 != nil {
			return Ebadevent
		}
		var t *Text
		switch {
		case c >= 'a' && c <= 'z':
			t = &w.tag
		case c >= 'A' && c <= 'Z':
			t = &w.body
		default:
			return Ebadevent
		}
		if q0 > t.file.Nc() || q1 > t.file.Nc() || q0 > q1 || q0 < 0 {
			return Ebadevent
		}
		switch c {
		case 'x', 'X':
			execute(t, q0, q1, true, nil)
		case 'l', 'L':
			look3(t, q0, q1, true)
		default:
			return Ebadevent
		}
		if w.deleted {
			return ""
		}
	}
	return ""
}
