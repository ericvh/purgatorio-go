package main

// bundle and unbundle: a tree as an archive of headers and data.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const archiveEOF = "end of archive\n"

// fdwriter writes a raw fd.
type fdwriter int

func (f fdwriter) Write(b []byte) (int, error) {
	if err := writefd(int(f), b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func writefd(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := syscall.Write(fd, b)
		if err != nil || n <= 0 {
			return sys.Error(sys.Errstr())
		}
		b = b[n:]
	}
	return nil
}

type fdreader int

func (f fdreader) Read(b []byte) (int, error) {
	n, err := syscall.Read(int(f), b)
	if err != nil {
		return 0, sys.Error(sys.Errstr())
	}
	if n == 0 && len(b) > 0 {
		return 0, io.EOF
	}
	return n, nil
}

func bundlerun(r *Report, _ []option, args []value) value {
	iob := bufio.NewWriterSize(fdwriter(1), atomicio)
	c := vx(args[0])
	sync := make(chan int)
	errorc := r.start("bundle")
	proc(func() { bundleproc(c, sync, iob, errorc) })
	return vV{sync}
}

func bundleproc(c fschan, sync chan int, iob *bufio.Writer, errorc chan string) {
	if <-sync == 0 {
		(<-c).reply <- Quit
		quit(errorc)
	}
	m := <-c
	if m.d.dir == nil {
		report(errorc, "no root directory")
		endarchive(iob, errorc)
	}
	if puts(iob, dir2header(m.d.dir), errorc) < 0 {
		m.reply <- Quit
		quit(errorc)
	}
	m.reply <- Down
	bundledir(m.d.dir.Name, m.d, c, iob, errorc)
	endarchive(iob, errorc)
}

func endarchive(iob *bufio.Writer, errorc chan string) {
	if puts(iob, archiveEOF, errorc) != -1 {
		_ = iob.Flush()
	}
	quit(errorc)
}

func bundledir(path string, d fsdata, c fschan, iob *bufio.Writer, errorc chan string) {
	if d.dir.Mode&sys.DMDIR != 0 {
		path += "/"
		for {
			m := <-c
			if m.d.dir == nil {
				m.reply <- Skip
				break
			}
			if puts(iob, dir2header(m.d.dir), errorc) < 0 {
				m.reply <- Quit
				quit(errorc)
			}
			m.reply <- Down
			bundledir(path+m.d.dir.Name, m.d, c, iob, errorc)
		}
		_ = iob.WriteByte('\n')
		return
	}
	length := d.dir.Length
	var n uint64
	for {
		m := <-c
		buf := m.d.data
		if buf == nil {
			m.reply <- Skip
			break
		}
		if write(iob, buf, errorc) != len(buf) {
			m.reply <- Quit
			quit(errorc)
		}
		n += uint64(len(buf))
		if n > length {
			report(errorc, fmt.Sprintf("%s is longer than expected (fatal)", quote.Quote(path)))
			m.reply <- Quit
			quit(errorc)
		}
		if n == length {
			m.reply <- Skip
			break
		}
		m.reply <- Next
	}
	if n < length {
		report(errorc, fmt.Sprintf("%s is shorter than expected (%d/%d); adding null bytes", quote.Quote(path), n, length))
		buf := make([]byte, atomicio)
		for n < length {
			nb := len(buf)
			if length-n < uint64(len(buf)) {
				nb = int(length - n)
			}
			if write(iob, buf[:nb], errorc) != nb {
				(<-c).reply <- Quit
				quit(errorc)
			}
			report(errorc, fmt.Sprintf("added %d null bytes", nb))
			n += uint64(nb)
		}
	}
}

func dir2header(d *sys.Dir) string {
	return fmt.Sprintf("%s %o %s %s %d %d\n", quote.Quote(d.Name), d.Mode, quote.Quote(d.UID), quote.Quote(d.GID), d.Mtime, d.Length)
}

func puts(iob *bufio.Writer, s string, errorc chan string) int {
	if _, err := iob.WriteString(s); err != nil {
		if err.Error() == "write on closed pipe" {
			return -1
		}
		report(errorc, "write error: "+err.Error())
	}
	return 0
}

func write(iob *bufio.Writer, buf []byte, errorc chan string) int {
	nw, err := iob.Write(buf)
	if nw < len(buf) {
		if err != nil && err.Error() == "write on closed pipe" {
			report(errorc, "write on closed pipe")
			return -1
		}
		if err != nil {
			report(errorc, "write error: "+err.Error())
		} else {
			report(errorc, "short write")
		}
	}
	return nw
}

func unbundlerun(r *Report, _ []option, args []value) value {
	p := vs(args[0])
	var fd int
	if p == "-" {
		fd = 0
	} else {
		var err error
		fd, err = sys.Open(p, sys.OREAD)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fs: unbundle: cannot open %s: %s\n", quote.Quote(p), err)
			return nil
		}
	}
	seekable := p != "-" && isseekable(fd)
	return vX{unbundle(r, fd, seekable, atomicio)}
}

// isseekable is Inferno's heuristic: not a pipe or a network
// connection.
func isseekable(fd int) bool {
	d, err := sys.Fstat(fd)
	return !(err == nil && d.Type == '|' || d != nil && d.Type == 'I')
}

func unbundle(r *Report, fd int, seekable bool, blocksize int) fschan {
	c := make(fschan)
	errorc := r.start("bundle")
	u := &unbundler{iob: bufio.NewReaderSize(fdreader(fd), atomicio), fd: fd, seekable: seekable}
	proc(func() { u.proc(c, blocksize, errorc) })
	return c
}

type unbundler struct {
	iob      *bufio.Reader
	fd       int
	seekable bool
}

func (u *unbundler) gets() string {
	s, _ := u.iob.ReadString('\n')
	return s
}

func (u *unbundler) proc(c fschan, blocksize int, errorc chan string) {
	reply := make(chan int)
	p := u.gets()
	if p == "" || p == archiveEOF {
		sendnulldir(c)
		quit(errorc)
	}
	d := header2dir(p)
	if d == nil {
		sendnulldir(c)
		report(errorc, "invalid first header")
		quit(errorc)
	}
	if d.Mode&sys.DMDIR == 0 {
		sendnulldir(c)
		report(errorc, "first entry is not a directory")
		quit(errorc)
	}
	c <- fsmsg{fsdata{d, nil}, reply}
	switch <-reply {
	case Down:
		u.dir(c, false, blocksize, errorc)
		c <- fsmsg{fsdata{}, reply}
		<-reply
	case Skip, Next:
		u.dir(c, true, blocksize, errorc)
	}
	quit(errorc)
}

func (u *unbundler) dir(c fschan, skipping bool, blocksize int, errorc chan string) int {
	reply := make(chan int)
	var p string
	for {
		p = u.gets()
		if p == "" || p == archiveEOF || p[0] == '\n' {
			break
		}
		d := header2dir(p)
		if d == nil {
			report(errorc, "invalid bundle header "+quote.Quote(p[:len(p)-1]))
			return -1
		}
		if d.Mode&sys.DMDIR != 0 {
			if skipping {
				continue
			}
			c <- fsmsg{fsdata{d, nil}, reply}
			switch <-reply {
			case Quit:
				quit(errorc)
			case Down:
				r := u.dir(c, false, blocksize, errorc)
				c <- fsmsg{fsdata{}, reply}
				if <-reply == Quit {
					quit(errorc)
				}
				if r == -1 {
					return -1
				}
			case Skip:
				if u.dir(c, true, blocksize, errorc) == -1 {
					return -1
				}
				skipping = true
			case Next:
				if u.dir(c, true, blocksize, errorc) == -1 {
					return -1
				}
			}
		} else if skipping {
			if u.skipdata(d.Length) == -1 {
				return -1
			}
		} else {
			switch u.file(d, c, errorc, blocksize) {
			case -1:
				return -1
			case Skip:
				skipping = true
			}
		}
	}
	if p == "" {
		report(errorc, "unexpected eof")
	}
	return 0
}

func (u *unbundler) skipdata(length uint64) int {
	if _, err := io.CopyN(io.Discard, u.iob, int64(length)); err != nil {
		return -1
	}
	return 0
}

func (u *unbundler) file(d *sys.Dir, c fschan, errorc chan string, blocksize int) int {
	reply := make(chan int)
	c <- fsmsg{fsdata{d, nil}, reply}
	switch r := <-reply; r {
	case Quit:
		quit(errorc)
	case Skip, Next:
		if u.skipdata(d.Length) == -1 {
			return -1
		}
		return r
	}
	length := d.Length
	for n := uint64(0); n < length; {
		nr := blocksize
		if n+uint64(blocksize) > length {
			nr = int(length - n)
		}
		buf := make([]byte, nr)
		nr, err := io.ReadFull(u.iob, buf)
		if nr <= 0 {
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				report(errorc, "read error: "+err.Error())
			} else {
				report(errorc, "premature eof")
			}
			return -1
		}
		buf = buf[:nr]
		c <- fsmsg{fsdata{nil, buf}, reply}
		n += uint64(nr)
		switch <-reply {
		case Quit:
			quit(errorc)
		case Skip:
			if u.skipdata(length-n) == -1 {
				return -1
			}
			return Next
		}
	}
	c <- fsmsg{fsdata{}, reply}
	if <-reply == Quit {
		quit(errorc)
	}
	return Next
}

func header2dir(s string) *sys.Dir {
	toks := quote.Unquoted(s)
	if len(toks) != 6 {
		return nil
	}
	d := nulldir()
	d.Name = toks[0]
	m, _ := strconv.ParseUint(toks[1], 8, 32)
	d.Mode = uint32(m)
	d.UID = toks[2]
	d.GID = toks[3]
	t, _ := strconv.ParseUint(toks[4], 10, 32)
	d.Mtime = uint32(t)
	l, _ := strconv.ParseUint(toks[5], 10, 64)
	d.Length = l
	return d
}
