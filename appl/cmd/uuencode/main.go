// Command uuencode writes a file, or standard input, on standard output
// in uuencode format under the given remote name (Inferno uuencode(1)).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// fatal prints s and exits without a failure status, as Inferno's does.
func fatal(s string) {
	os.Stderr.WriteString(s + "\n")
	sys.Exits("")
}

func usage() { fatal("usage: uuencode [ sourcefile ] remotefile") }

const lineLen = 45

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
	}
	in := os.Stdin
	mode := uint32(0o666)
	if len(args) > 1 {
		fd, err := sys.Open(args[0], sys.OREAD)
		if err != nil {
			fatal("cannot open " + args[0])
		}
		d, err := sys.Fstat(fd)
		if err != nil {
			fatal("cannot stat " + args[0] + ": " + sys.Errstr())
		}
		if d.Mode&sys.DMDIR != 0 {
			fatal("cannot uuencode a directory")
		}
		mode = d.Mode
		in = os.NewFile(uintptr(fd), args[0])
		args = args[1:]
	}
	if len(args) > 1 {
		usage()
	}
	fmt.Printf("begin %o %s\n", mode, args[0])
	encode(in)
	fmt.Printf("end\n")
}

func code(c byte) byte { return c&0x3f + ' ' }

func encode(in *os.File) {
	ib := make([]byte, lineLen)
	ob := make([]byte, 0, 4*lineLen/3+2)
	for {
		n, err := in.Read(ib)
		if err != nil && err != io.EOF {
			fatal("cannot read input file: " + sys.Errstr())
		}
		if n == 0 {
			break
		}
		ob = append(ob[:0], code(byte(n)))
		for j := 0; j < n; j += 3 {
			var c, d, e byte = ib[j], 0, 0
			if j+1 < n {
				d = ib[j+1]
			}
			if j+2 < n {
				e = ib[j+2]
			}
			ob = append(ob, code(c>>2), code(c<<4&0x30|d>>4), code(d<<2&0x3c|e>>6), code(e))
		}
		ob = append(ob, '\n')
		if _, err := os.Stdout.Write(ob); err != nil {
			fatal("bad write to output: " + sys.Errstr())
		}
	}
	if _, err := os.Stdout.Write([]byte{code(0), '\n'}); err != nil {
		fatal("bad write to output: " + sys.Errstr())
	}
}
