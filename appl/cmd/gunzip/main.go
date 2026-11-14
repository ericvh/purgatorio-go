// Command gunzip decompresses name.gz files into name in the current
// directory, removing the originals, or standard input onto standard
// output (Inferno gunzip(1)).
package main

import (
	"bufio"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const argv0 = "gunzip"

func main() {
	args := os.Args[1:]
	ok := true
	if len(args) == 0 {
		bout := bufio.NewWriter(os.Stdout)
		ok = gunzip(os.Stdin, bout, "stdin", "stdout")
		bout.Flush()
	} else {
		for _, f := range args {
			if !gunzipf(f) {
				ok = false
			}
		}
	}
	if !ok {
		sys.Exits("fail:errors")
	}
}

func gunzipf(file string) bool {
	in, err := os.Open(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: can't open %s: %s\n", argv0, file, sys.Errstr())
		return false
	}
	defer in.Close()
	ofile := file[strings.LastIndex(file, "/")+1:]
	n := len(ofile)
	if n < 4 || ofile[n-3:] != ".gz" {
		fmt.Fprintf(os.Stderr, "%s: .gz extension required: %s\n", argv0, file)
		return false
	}
	ofile = ofile[:n-3]
	fd, err := sys.Create(ofile, sys.OWRITE, 0o666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: can't open %s: %s\n", argv0, ofile, sys.Errstr())
		return false
	}
	of := os.NewFile(uintptr(fd), ofile)
	bout := bufio.NewWriter(of)
	ok := gunzip(in, bout, file, ofile)
	bout.Flush() // what was decoded stays, even after an error
	of.Close()
	if ok {
		if sys.Remove(file) != nil {
			fmt.Fprintf(os.Stderr, "%s: cannot remove %s: %s\n", argv0, file, sys.Errstr())
		}
	}
	return ok
}

// reader notes a read error so it is not taken for bad data.
type reader struct {
	r   io.Reader
	err bool
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF {
		r.err = true
	}
	return n, err
}

// inflateError is one of Inferno's inflate messages.
type inflateError string

func (e inflateError) Error() string { return string(e) }

const (
	gzFHCRC    = 1 << 1
	gzFEXTRA   = 1 << 2
	gzFNAME    = 1 << 3
	gzFCOMMENT = 1 << 4
	gzFMASK    = 1<<5 - 1
)

func gunzip(in io.Reader, bout *bufio.Writer, fin, fout string) bool {
	rd := &reader{r: in}
	br := bufio.NewReader(rd)
	werr := false
	err := inflate(br, writerFunc(func(p []byte) (int, error) {
		n, err := bout.Write(p)
		if err != nil {
			werr = true
		}
		return n, err
	}))
	switch {
	case werr:
		fmt.Fprintf(os.Stderr, "%s: %s: write error: %s\n", argv0, fout, sys.Errstr())
		return false
	case rd.err:
		fmt.Fprintf(os.Stderr, "%s: %s: read error: %s\n", argv0, fin, sys.Errstr())
		return false
	case err != nil:
		fmt.Fprintf(os.Stderr, "%s: %s: inflate error: %s\n", argv0, fin, err)
		return false
	}
	if bout.Flush() != nil {
		fmt.Fprintf(os.Stderr, "%s: %s: flush error: %s\n", argv0, fout, sys.Errstr())
		return false
	}
	return true
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

var errShort = inflateError("premature end of stream")

// inflate decodes one gzip member from br onto w, checking the trailer.
func inflate(br *bufio.Reader, w io.Writer) error {
	getb := func() byte {
		c, err := br.ReadByte()
		if err != nil {
			panic(errShort)
		}
		return c
	}
	var err error
	func() {
		defer func() {
			if e := recover(); e != nil {
				ie, ok := e.(inflateError)
				if !ok {
					panic(e)
				}
				err = ie
			}
		}()
		if getb() != 0x1f || getb() != 0x8b {
			panic(inflateError("not a gzip file"))
		}
		if getb() != 8 {
			panic(inflateError("not compressed with deflate"))
		}
		flags := getb()
		if flags&^gzFMASK != 0 {
			panic(inflateError("reserved flag bits set"))
		}
		for range 6 { // mtime, xfl, os
			getb()
		}
		if flags&gzFEXTRA != 0 {
			n := int(getb()) | int(getb())<<8
			for range n {
				getb()
			}
		}
		if flags&gzFNAME != 0 {
			for getb() != 0 {
			}
		}
		if flags&gzFCOMMENT != 0 {
			for getb() != 0 {
			}
		}
		if flags&gzFHCRC != 0 {
			getb()
			getb()
		}
	}()
	if err != nil {
		return err
	}
	crc := crc32.NewIEEE()
	fr := flate.NewReader(br)
	tot, err := io.Copy(io.MultiWriter(w, crc), fr)
	if err != nil {
		var ce flate.CorruptInputError
		switch {
		case errors.As(err, &ce):
			return inflateError("corrupted data")
		case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
			return errShort
		}
		return err
	}
	var tr [8]byte
	if _, err := io.ReadFull(br, tr[:]); err != nil {
		return errShort
	}
	if fcrc := binary.LittleEndian.Uint32(tr[:]); fcrc != crc.Sum32() {
		return inflateError(fmt.Sprintf("crc mismatch: computed %x, expected %x", crc.Sum32(), fcrc))
	}
	if ftot := int32(binary.LittleEndian.Uint32(tr[4:])); ftot != int32(tot) {
		return inflateError(fmt.Sprintf("byte count mismatch: computed %d, expected %d", int32(tot), ftot))
	}
	return nil
}
