// Command strings prints runs of printable characters in files
// (Inferno strings(1)).
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
)

const (
	minspan = 6
	bufsize = 70
)

var out = bufio.NewWriter(os.Stdout)

func main() {
	defer out.Flush()
	args := os.Args[1:]
	if len(args) == 0 {
		stringit("")
		return
	}
	for _, a := range args {
		if len(args) > 1 {
			fmt.Fprintf(out, "%s:\n", a)
		}
		stringit(a)
	}
}

func stringit(name string) {
	in := io.Reader(os.Stdin)
	if name != "" {
		f, err := os.Open(name)
		if err != nil {
			out.Flush()
			fmt.Fprintf(os.Stderr, "cannot open %s\n", name)
			return
		}
		defer f.Close()
		in = f
	}
	r := bufio.NewReader(in)
	var buf []rune
	start := int64(-1)
	posn := int64(0)
	for {
		// A byte that is not UTF-8 reads as the error rune, which is
		// printable, as with Bufio's getc.
		c, n, err := r.ReadRune()
		if err != nil {
			break
		}
		if isprint(c) {
			if start == -1 {
				start = posn
			}
			buf = append(buf, c)
			if len(buf) == bufsize {
				fmt.Fprintf(out, "%8d: %s ...\n", start, string(buf))
				start = -1
				buf = buf[:0]
			}
		} else {
			if len(buf) >= minspan {
				fmt.Fprintf(out, "%8d: %s\n", start, string(buf))
			}
			start = -1
			buf = buf[:0]
		}
		posn += int64(n)
	}
	if len(buf) >= minspan {
		fmt.Fprintf(out, "%8d: %s\n", start, string(buf))
	}
}

func isprint(r rune) bool { return r >= ' ' && r < 0x7f || r > 0xa0 }
