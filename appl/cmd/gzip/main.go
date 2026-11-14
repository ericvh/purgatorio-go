// Command gzip compresses files into name.gz in the current directory,
// removing the originals, or standard input onto standard output
// (Inferno gzip(1)).
package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const argv0 = "gzip"

var (
	verbose int
	level   = 6
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: %s [-vD1-9] [file ...]\n", argv0)
	sys.Exits("fail:usage")
}

func main() {
	args := os.Args[1:]
	// Options as gzip.b's own Arg: one byte at a time.
	for len(args) > 0 {
		a := args[0]
		if len(a) < 2 || a[0] != '-' {
			break
		}
		args = args[1:]
		if a == "--" {
			break
		}
		for i := 1; i < len(a); i++ {
			switch c := a[i]; {
			case c == 'D':
			case c == 'v':
				verbose++
			case c >= '1' && c <= '9':
				level = int(c - '0')
			default:
				usage()
			}
		}
	}
	if len(args) == 0 {
		bout := bufio.NewWriter(os.Stdout)
		compress(os.Stdin, bout, "stdin", "stdout")
		bout.Flush()
		return
	}
	for _, f := range args {
		gzipf(f)
	}
}

func gzipf(file string) bool {
	in, err := os.Open(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: can't open %s: %s\n", argv0, file, sys.Errstr())
		return false
	}
	defer in.Close()
	ofile := file[strings.LastIndex(file, "/")+1:] + ".gz"
	fd, err := sys.Create(ofile, sys.OWRITE, 0o666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: can't open %s: %s\n", argv0, ofile, sys.Errstr())
		return false
	}
	of := os.NewFile(uintptr(fd), ofile)
	bout := bufio.NewWriter(of)
	ok := compress(in, bout, file, ofile)
	if ok && bout.Flush() != nil {
		fmt.Fprintf(os.Stderr, "%s: error writing %s: %s\n", argv0, ofile, sys.Errstr())
		ok = false
	}
	of.Close()
	if ok {
		sys.Remove(file)
	} else {
		sys.Remove(ofile)
	}
	return ok
}

// writer counts the compressed bytes for -v.
type writer struct {
	w io.Writer
	n int
}

func (w *writer) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += n
	return n, err
}

// compress writes a gzip stream as Inferno's deflate does: no name, a
// zero mtime and the Unix OS byte.
func compress(in io.Reader, out io.Writer, fin, fout string) bool {
	cw := &writer{w: out}
	zw, _ := gzip.NewWriterLevel(cw, level)
	zw.OS = 3
	buf := make([]byte, 8192)
	incount := 0
	for {
		n, err := in.Read(buf)
		if n > 0 {
			incount += n
			if _, werr := zw.Write(buf[:n]); werr != nil {
				fmt.Fprintf(os.Stderr, "%s: error writing %s: %s\n", argv0, fout, sys.Errstr())
				return false
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: error reading %s: %s\n", argv0, fin, sys.Errstr())
			return false
		}
	}
	if err := zw.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: error writing %s: %s\n", argv0, fout, sys.Errstr())
		return false
	}
	if verbose > 0 {
		comp := 0.0
		if incount > 0 {
			comp = 1.0 - float64(cw.n)/float64(incount)
		}
		fmt.Fprintf(os.Stderr, "%s: %5.2f%%\n", fin, comp*100.0)
	}
	return true
}
