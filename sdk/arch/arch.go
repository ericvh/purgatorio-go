// Package arch reads and writes the archives of mkfs(8) -a and the
// install tools (Inferno's appl/cmd/install/arch.b): each file is a
// header line "name mode uid gid mtime length" and length bytes of data,
// and the archive ends with "end of archive". An archive compressed
// with gzip is read through a gunzip reader.
package arch

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	// EOArch is the archive's last line.
	EOArch = "end of archive\n"
	// PremEOArch is the error when an archive ends early.
	PremEOArch = "premature end of archive"
	nflds      = 6
	atomicio   = 8192
)

// Ahdr is one file's header.
type Ahdr struct {
	Name string
	D    sys.Dir
}

// Archive is an archive open for reading.
type Archive struct {
	B       *bufio.Reader
	f       *os.File
	off     int64 // bytes consumed from B
	nexthdr int64
	CanSeek bool
	Err     string
	hdr     Ahdr
}

var addp = true

// AddPerms sets whether Puthdr adds read and write permission for all
// and execute permission to directories and executables (the default).
func AddPerms(p bool) { addp = p }

// Open opens an archive file, uncompressing it when it is gzipped.
func Open(file string) *Archive {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	ar := &Archive{f: f, CanSeek: true}
	var magic [2]byte
	n, _ := io.ReadFull(f, magic[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil
	}
	if n == 2 && magic[0] == 0x1f && (magic[1] == 0x8b || magic[1] == 0x9d) {
		z, err := gzip.NewReader(bufio.NewReader(f))
		if err != nil {
			f.Close()
			return nil
		}
		ar.CanSeek = false
		ar.B = bufio.NewReaderSize(z, atomicio)
		return ar
	}
	ar.B = bufio.NewReaderSize(f, atomicio)
	return ar
}

// OpenGz copies the /wrap files at the start of a compressed archive to
// a temporary archive and returns its name and file, so that it can be
// mounted; it returns "" for an archive that is not compressed.
func OpenGz(file string) (string, *os.File) {
	ar := Open(file)
	if ar == nil || ar.CanSeek {
		if ar != nil {
			ar.Close()
		}
		return "", nil
	}
	name, fd := Opentemp("wrap.gz")
	if fd == nil {
		return "", nil
	}
	bout := bufio.NewWriter(fd)
	for {
		a := ar.Gethdr()
		if a == nil || !strings.HasPrefix(a.Name, "/wrap") {
			break
		}
		Puthdr(bout, a.Name, &a.D)
		ar.Getfile(bout, int64(a.D.Length))
	}
	ar.Close()
	bout.WriteString(EOArch)
	bout.Flush()
	fd.Seek(0, io.SeekStart)
	return name, fd
}

// Opentemp creates a temporary file in /tmp whose name starts with
// prefix.
func Opentemp(prefix string) (string, *os.File) {
	pid, _ := sys.Pctl(0, nil)
	name := fmt.Sprintf("/tmp/%s.%d.%d", prefix, uint32(daytime.Now()), pid)
	fd, err := sys.Create(name, sys.ORDWR, 0o600)
	if err != nil {
		return name, nil
	}
	return name, os.NewFile(uintptr(fd), name)
}

// Close closes the archive.
func (ar *Archive) Close() {
	if ar.f != nil {
		ar.f.Close()
	}
}

func (ar *Archive) read(p []byte) (int, error) {
	n, err := io.ReadFull(ar.B, p)
	ar.off += int64(n)
	return n, err
}

// Read reads file data from the archive.
func (ar *Archive) Read(p []byte) (int, error) {
	n, err := ar.B.Read(p)
	ar.off += int64(n)
	return n, err
}

// Gethdr returns the next file's header, or nil at the end of the
// archive or on an error, which Err then holds.
func (ar *Archive) Gethdr() *Ahdr {
	if ar.off != ar.nexthdr {
		if ar.CanSeek {
			if _, err := ar.f.Seek(ar.nexthdr, io.SeekStart); err != nil {
				ar.Err = err.Error()
				return nil
			}
			ar.B.Reset(ar.f)
			ar.off = ar.nexthdr
		} else {
			if ar.off > ar.nexthdr {
				fmt.Fprintf(os.Stderr, "bad offset in gethdr: m=%d n=%d\n", ar.off, ar.nexthdr)
				sys.Exits("fail:error")
			}
			if ar.Drain(ar.nexthdr-ar.off) < 0 {
				return nil
			}
		}
	}
	s, err := ar.B.ReadString('\n')
	ar.off += int64(len(s))
	if err != nil && s == "" {
		ar.Err = PremEOArch
		return nil
	}
	if s == EOArch {
		return nil
	}
	fs := strings.Fields(s)
	if len(fs) != nflds {
		ar.Err = "too few fields in file header"
		return nil
	}
	a := &ar.hdr
	a.Name = fs[0]
	a.D = sys.Dir{}
	a.D.Mode = uint32(toint(fs[1], 8))
	a.D.UID = fs[2]
	a.D.GID = fs[3]
	a.D.Mtime = uint32(toint(fs[4], 10))
	a.D.Length = uint64(uint32(toint(fs[5], 10)))
	ar.nexthdr = ar.off + int64(a.D.Length)
	return a
}

// toint is String->toint: the leading digits of s in base.
func toint(s string, base int) int64 {
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var v int64
	for _, c := range s {
		d := int64(-1)
		switch {
		case c >= '0' && c <= '9':
			d = int64(c - '0')
		case c >= 'a' && c <= 'z':
			d = int64(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			d = int64(c-'A') + 10
		}
		if d < 0 || d >= int64(base) {
			break
		}
		v = v*int64(base) + d
	}
	if neg {
		v = -v
	}
	return v
}

// Getfile copies the next n bytes of the archive to bout.
func (ar *Archive) Getfile(bout io.Writer, n int64) string {
	var err string
	buf := make([]byte, atomicio)
	for n > 0 {
		m := int64(len(buf))
		if n < m {
			m = n
		}
		p, _ := ar.read(buf[:m])
		if int64(p) != m {
			return PremEOArch
		}
		if k, e := bout.Write(buf[:m]); int64(k) != m || e != nil {
			err = "cannot write: " + errstr(e)
		}
		n -= m
	}
	return err
}

// Drain skips n bytes.
func (ar *Archive) Drain(n int64) int {
	buf := make([]byte, atomicio)
	for n > 0 {
		m := n
		if m > int64(len(buf)) {
			m = int64(len(buf))
		}
		p, _ := ar.read(buf[:m])
		if int64(p) != m {
			ar.Err = "unexpectedly short read"
			return -1
		}
		n -= m
	}
	return 0
}

// Puthdr writes a header for name.
func Puthdr(b io.Writer, name string, d *sys.Dir) {
	mode := d.Mode
	if addp {
		mode |= 0o664
		if mode&sys.DMDIR != 0 || mode&0o111 != 0 {
			mode |= 0o111
		}
	}
	fmt.Fprintf(b, "%s %o %s %s %d %d\n", name, mode, d.UID, d.GID, d.Mtime, int32(d.Length))
}

// Putstring writes s.
func Putstring(b io.Writer, s string) { io.WriteString(b, s) }

// Putfile copies file f, which should hold n bytes, into the archive.
func Putfile(b io.Writer, f string, n int64) string {
	fd, err := os.Open(f)
	if err != nil {
		return "cannot open " + f + ": " + sys.Errstr()
	}
	defer fd.Close()
	var i int64
	buf := make([]byte, atomicio)
	for {
		m, err := fd.Read(buf)
		if m > 0 {
			if k, e := b.Write(buf[:m]); k != m || e != nil {
				return f + ": cannot write: " + errstr(e)
			}
			i += int64(m)
		}
		if err == io.EOF || m == 0 && err == nil {
			break
		}
		if err != nil {
			return "cannot read " + f + ": " + sys.Errstr()
		}
	}
	if i != n {
		if i < n {
			b.Write(make([]byte, n-i))
		}
		return fmt.Sprintf("%s: %d bytes written: should be %d", f, i, n)
	}
	return ""
}

// Putend ends the archive.
func Putend(b *bufio.Writer) {
	b.WriteString(EOArch)
	b.Flush()
}

func errstr(e error) string {
	if e == nil {
		return sys.Errstr()
	}
	return e.Error()
}
