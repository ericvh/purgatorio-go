// Package feed reads a file in a child prog, so that a guest, which has
// one thread, can wait for the file's data with a timeout or alongside
// other files. It stands in for the reader proc and timer an Inferno
// program spawns and alts on: the network commands (ip/ping, ndb/dns,
// ip/sntp, ...) read their datagram conversations through it.
//
// The child is the command itself, started with the argument Arg. It
// reads its standard input and writes each read to the parent as one
// frame: a 4-byte length, the 8-byte time of the read in nanoseconds,
// then the data. A zero length ends the feed, followed by the error
// string, if any.
package feed

import (
	"encoding/binary"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Arg is the argument that makes a command a feeder.
const Arg = "-#feed"

const hdrlen = 12

// Serve runs the feeder when the command was started as one, and then
// exits; otherwise it returns. Commands that use Start call it first.
func Serve() {
	if len(os.Args) != 2 || os.Args[1] != Arg {
		return
	}
	buf := make([]byte, hdrlen+64*1024+512)
	for {
		n, err := os.Stdin.Read(buf[hdrlen:])
		if n <= 0 {
			msg := ""
			if err != nil && err != io.EOF {
				msg = sys.Errstr()
			}
			clear(buf[:hdrlen])
			copy(buf[hdrlen:], msg)
			os.Stdout.Write(buf[:hdrlen+len(msg)])
			sys.Exits("")
		}
		binary.BigEndian.PutUint32(buf[0:], uint32(n))
		binary.BigEndian.PutUint64(buf[4:], uint64(time.Now().UnixNano()))
		if _, err := os.Stdout.Write(buf[:hdrlen+n]); err != nil {
			sys.Exits("")
		}
	}
}

// Reader is a feed of one file.
type Reader struct {
	f   *os.File
	pid int
	err error
}

// Start starts the feeder self (the command's own /bin path) on fd.
func Start(self string, fd int) (*Reader, error) {
	p, err := sys.Pipe()
	if err != nil {
		return nil, err
	}
	pid, err := sys.Spawn(self, []string{self, Arg}, sys.NEWFD, [][2]int{{0, fd}, {1, p[1]}, {2, 2}})
	sys.Close(p[1])
	if err != nil {
		sys.Close(p[0])
		return nil, err
	}
	return &Reader{f: os.NewFile(uintptr(p[0]), "feed"), pid: pid}, nil
}

// Fd is the file descriptor to wait on for the next frame.
func (r *Reader) Fd() int { return int(r.f.Fd()) }

// Read returns the next read of the file and its time, waiting at most
// timeout milliseconds (forever when negative). On a timeout it returns
// nil data and no error; at the end of the file, io.EOF or the error
// the read gave.
func (r *Reader) Read(timeout int) ([]byte, time.Time, error) {
	if r.err != nil {
		return nil, time.Time{}, r.err
	}
	if timeout >= 0 {
		i, err := sys.Wait([][2]int{{sys.WaitRead, r.Fd()}}, timeout)
		if err != nil {
			return nil, time.Time{}, err
		}
		if i < 0 {
			return nil, time.Time{}, nil
		}
	}
	return r.Next()
}

// Next returns the next read, waiting for it.
func (r *Reader) Next() ([]byte, time.Time, error) {
	if r.err != nil {
		return nil, time.Time{}, r.err
	}
	var h [hdrlen]byte
	if _, err := io.ReadFull(r.f, h[:]); err != nil {
		r.err = io.EOF
		return nil, time.Time{}, r.err
	}
	n := binary.BigEndian.Uint32(h[:])
	t := time.Unix(0, int64(binary.BigEndian.Uint64(h[4:])))
	if n == 0 {
		msg, _ := io.ReadAll(r.f)
		r.err = io.EOF
		if len(msg) > 0 {
			r.err = sys.Error(string(msg))
		}
		return nil, time.Time{}, r.err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r.f, b); err != nil {
		r.err = io.EOF
		return nil, time.Time{}, r.err
	}
	return b, t, nil
}

// Close stops the feeder.
func (r *Reader) Close() {
	if fd, err := sys.Open("/prog/"+strconv.Itoa(r.pid)+"/ctl", sys.OWRITE); err == nil {
		f := os.NewFile(uintptr(fd), "ctl")
		f.Write([]byte("kill"))
		f.Close()
	}
	r.f.Close()
}
