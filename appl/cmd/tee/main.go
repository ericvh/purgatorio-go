// Command tee copies standard input to standard output and to files
// (Inferno tee(1)).
package main

import (
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

type file struct {
	f    *os.File
	name string
}

func usage() {
	os.Stderr.WriteString("Usage: tee [-a] [file ...]\n")
	sys.Exits("fail:usage")
}

func fail(s string) {
	os.Stderr.WriteString("tee: " + s + "\n")
	sys.Exits("fail:error")
}

func main() {
	a := arg.New(os.Args)
	appendf := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			appendf = true
		default:
			usage()
		}
	}
	var files []file
	for _, name := range a.Argv() {
		var fd int
		var err error
		if appendf {
			if fd, err = sys.Open(name, sys.OWRITE); err == nil {
				_, _ = sys.Seek(fd, 0, 2)
			} else {
				fd, err = sys.Create(name, sys.OWRITE, 0o666)
			}
		} else {
			fd, err = sys.Create(name, sys.OWRITE, 0o666)
		}
		if err != nil {
			fail("cannot open " + name + ": " + sys.Errstr())
		}
		files = append(files, file{os.NewFile(uintptr(fd), name), name})
	}
	files = append(files, file{os.Stdout, "standard output"})
	buf := make([]byte, 8192)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			for _, f := range files {
				if w, _ := f.f.Write(buf[:n]); w != n {
					fail("error writing " + f.name + ": " + sys.Errstr())
				}
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			fail("read error: " + sys.Errstr())
		}
	}
}
