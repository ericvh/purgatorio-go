// Command freq prints a histogram of the byte or rune values in its input
// (Inferno freq(1)).
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	fdec = 1 << iota
	fhex
	foct
	fchar
	frune
)

var (
	flag  int
	count = make([]int64, 1<<16)
)

func main() {
	a := arg.New(os.Args)
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'd':
			flag |= fdec
		case 'x':
			flag |= fhex
		case 'o':
			flag |= foct
		case 'c':
			flag |= fchar
		case 'r':
			flag |= frune
		default:
			fmt.Fprintf(os.Stderr, "freq: unknown option %c\n", c)
			sys.Exits("fail:usage")
		}
	}
	args := a.Argv()
	if flag&(fdec|fhex|foct|fchar) == 0 {
		flag |= fdec | fhex | foct | fchar
	}
	out := bufio.NewWriter(os.Stdout)
	if len(args) == 0 {
		freq(os.Stdin, "-", out)
		return
	}
	for _, name := range args {
		f, err := os.Open(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot open %s\n", name)
			continue
		}
		freq(f, name, out)
		f.Close()
	}
}

// freq adds f's values to the counts, which carry over from file to
// file as in Inferno, and prints the table.
func freq(f *os.File, name string, out *bufio.Writer) {
	in := bufio.NewReader(f)
	var err error
	for {
		if flag&frune != 0 {
			var r rune
			r, _, err = in.ReadRune()
			if err != nil {
				break
			}
			if r == utf8.RuneError || int(r) >= len(count) {
				continue
			}
			count[r]++
		} else {
			var b byte
			b, err = in.ReadByte()
			if err != nil {
				break
			}
			count[b]++
		}
	}
	if err != io.EOF {
		fmt.Fprintf(os.Stderr, "freq: read error on %s: %s\n", name, sys.Errstr())
	}
	for i := 0; i < len(count)/4; i++ {
		if count[i] == 0 {
			continue
		}
		if flag&fdec != 0 {
			fmt.Fprintf(out, "%3d ", i)
		}
		if flag&foct != 0 {
			fmt.Fprintf(out, "%.3o ", i)
		}
		if flag&fhex != 0 {
			fmt.Fprintf(out, "%.2x ", i)
		}
		if flag&fchar != 0 {
			if i <= 0x20 || i >= 0x7f && i < 0xa0 || i > 0xff && flag&frune == 0 {
				out.WriteString("- ")
			} else {
				fmt.Fprintf(out, "%c ", rune(i))
			}
		}
		fmt.Fprintf(out, "%8d\n", count[i])
	}
	out.Flush()
}
