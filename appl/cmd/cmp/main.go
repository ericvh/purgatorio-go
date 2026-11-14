// Command cmp compares two files byte by byte (Inferno cmp(1)).
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const bufsize = 65536

func usage() {
	os.Stderr.WriteString("Usage: cmp [-lsL] file1 file2 [offset1 [offset2] ]\n")
	sys.Exits("fail:usage")
}

func main() {
	var lflag, Lflag, sflag bool
	a := arg.New(os.Args)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'l':
			lflag = true
		case 'L':
			Lflag = true
		case 's':
			sflag = true
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) < 2 {
		usage()
	}
	name1, name2 := args[0], args[1]
	f1 := open(name1)
	f2 := open(name2)
	if len(args) > 2 {
		if _, err := sys.Seek(int(f1.Fd()), atoi(args[2]), 0); err != nil {
			os.Stderr.WriteString("cmp: seek by offset1 failed: " + sys.Errstr() + "\n")
			sys.Exits("fail:seek 1")
		}
	}
	if len(args) > 3 {
		if _, err := sys.Seek(int(f2.Fd()), atoi(args[3]), 0); err != nil {
			os.Stderr.WriteString("cmp: seek by offset2 failed: " + sys.Errstr())
			sys.Exits("fail:seek 2")
		}
	}
	if len(args) > 4 {
		usage()
	}
	out := os.Stdout
	buf1 := make([]byte, bufsize)
	buf2 := make([]byte, bufsize)
	var b1, b2 []byte
	nc := int64(1)
	l := int64(1)
	diff := false
	for {
		if len(b1) == 0 {
			b1 = fill(f1, buf1, name1, nc, sflag)
		}
		if len(b2) == 0 {
			b2 = fill(f2, buf2, name2, nc, sflag)
		}
		n := min(len(b1), len(b2))
		if n == 0 {
			break
		}
		for i := 0; i < n; i++ {
			if Lflag && b1[i] == '\n' {
				l++
			}
			if b1[i] != b2[i] {
				if !lflag {
					if !sflag {
						fmt.Fprintf(out, "%s %s differ: char %d", name1, name2, nc+int64(i))
						if Lflag {
							fmt.Fprintf(out, " line %d\n", l)
						} else {
							fmt.Fprintf(out, "\n")
						}
					}
					sys.Exits("fail:differ")
				}
				fmt.Fprintf(out, "%6d 0x%.2x 0x%.2x\n", nc+int64(i), b1[i], b2[i])
				diff = true
			}
		}
		nc += int64(n)
		b1, b2 = b1[n:], b2[n:]
	}
	if len(b1) != len(b2) {
		nc--
		name := name1
		if len(b1) > len(b2) {
			name = name2
		}
		fmt.Fprintf(out, "EOF on %s after %d bytes\n", name, nc)
		sys.Exits("fail:EOF")
	}
	if diff {
		sys.Exits("fail:differ")
	}
}

func open(name string) *os.File {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		os.Stderr.WriteString("cmp: can't open " + name + ": " + sys.Errstr() + "\n")
		sys.Exits("fail:open")
	}
	return os.NewFile(uintptr(fd), name)
}

func fill(f *os.File, buf []byte, name string, nc int64, sflag bool) []byte {
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		if !sflag {
			fmt.Printf("error on %s after %d bytes\n", name, nc-1)
		}
		sys.Exits("fail:read error")
	}
	return buf[:n]
}

// atoi is Limbo's big of a string: the leading decimal number.
func atoi(s string) int64 {
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.ParseInt(s[:i], 10, 64)
	return n
}
