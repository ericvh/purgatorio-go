// Command logfile makes a file that keeps the last bytes written to it in
// a circular buffer (Inferno logfile(1)). Each reader first gets what the
// buffer holds, then blocks until more is written.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const atomicio = 8192

func usage() {
	os.Stderr.WriteString("usage: logfile [-size] file\n")
	sys.Exits("fail: usage")
}

func main() {
	bufsize := atomicio * 4
	argv := os.Args[1:]
	if len(argv) > 0 && len(argv[0]) > 1 && argv[0][0] == '-' {
		if bufsize = atoi(argv[0][1:]); bufsize <= 0 {
			os.Stderr.WriteString("logfile: can't have a zero buffer size\n")
			usage()
		}
		argv = argv[1:]
	}
	if len(argv) != 1 {
		usage()
	}
	path := argv[0]
	dir, f := pathsplit(path)
	if _, err := sys.Bind("#s", dir, sys.MBEFORE); err != nil {
		os.Stderr.WriteString("logfile: bind #s failed: " + sys.Errstr() + "\n")
		return
	}
	fio, err := sys.File2chan(dir, f)
	if err != nil {
		os.Stderr.WriteString("logfile: couldn't make " + path + ": " + sys.Errstr() + "\n")
		return
	}
	sys.Detach()
	serve(fio, newCircbuf(bufsize))
}

// reader is one open fid: its position and the reads it has waiting.
type reader struct {
	pos int
	rq  []*sys.ReadReq
}

func serve(fio *sys.FileIO, buf *circbuf) {
	readers := map[int]*reader{}
	var waiting []*reader
	for {
		req, err := fio.Next()
		if err != nil {
			return
		}
		switch m := req.(type) {
		case *sys.ReadReq:
			r := readers[m.Fid]
			if m.Closed() {
				delete(readers, m.Fid)
				continue
			}
			if r == nil {
				// A reader starts at the oldest byte the buffer holds.
				r = &reader{pos: max(buf.start, len(buf.data))}
				readers[m.Fid] = r
			}
			s, d := buf.get(r.pos, m.Count)
			r.pos = s + len(d)
			if d != nil {
				m.Reply(d, "")
				continue
			}
			if len(r.rq) == 0 {
				waiting = append(waiting, r)
			}
			r.rq = append(r.rq, m)
		case *sys.WriteReq:
			if m.Closed() {
				continue
			}
			if n := buf.put(m.Data); n < len(m.Data) {
				m.Reply(n, "write too long for buffer")
			} else {
				m.Reply(n, "")
			}
			// Each waiting reader gets one of its reads answered.
			wl := waiting
			waiting = nil
			for _, r := range wl {
				q := r.rq[0]
				r.rq = r.rq[1:]
				s, d := buf.get(r.pos, q.Count)
				r.pos = s + len(d)
				q.Reply(d, "")
				if len(r.rq) > 0 {
					waiting = append(waiting, r)
				}
			}
		}
	}
}

// circbuf holds the last len(data) bytes written. start counts every
// byte ever written; a reader's position runs len(data) ahead of the
// stream offset of the byte it reads next.
type circbuf struct {
	start int
	data  []byte
}

func newCircbuf(size int) *circbuf { return &circbuf{data: make([]byte, size)} }

// put stores d, truncated to the buffer's size, and returns how much it
// stored.
func (b *circbuf) put(d []byte) int {
	n := len(b.data)
	if len(d) > n {
		d = d[:n]
	}
	o := b.start % n
	c := copy(b.data[o:], d)
	copy(b.data, d[c:])
	b.start += len(d)
	return len(d)
}

// get returns up to n bytes from position s, moved forward past anything
// overwritten, and the position it read from.
func (b *circbuf) get(s, n int) (int, []byte) {
	if s < b.start {
		s = b.start
	}
	blen := len(b.data)
	if s+n > b.start+blen {
		n = b.start + blen - s
	}
	if n <= 0 {
		return s, nil
	}
	o := s % blen
	d := make([]byte, n)
	c := copy(d, b.data[o:])
	copy(d[c:], b.data)
	return s, d
}

func pathsplit(p string) (string, string) {
	i := len(p) - 1
	for i >= 0 && p[i] == '/' {
		i--
	}
	if i < 0 {
		return p, ""
	}
	p = p[:i+1]
	for i = len(p) - 1; i >= 0 && p[i] != '/'; i-- {
	}
	if i < 0 {
		return ".", p
	}
	return p[:i+1], p[i+1:]
}

// atoi converts a leading decimal number, as Limbo's int of a string.
func atoi(s string) int {
	i, neg := 0, false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	n := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		n = -n
	}
	return n
}
