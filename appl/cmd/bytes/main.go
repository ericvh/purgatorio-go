// Command bytes shows or replaces the bytes from start up to end of its
// standard input (Inferno bytes(1)). Either bound may be "end".
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const end = 0x7fffffff

var stdout = bufio.NewWriter(os.Stdout)

func usage() {
	os.Stderr.WriteString("usage: bytes start end [bytes]\n")
	sys.Exits("fail:usage")
}

func main() {
	args := os.Args[1:]
	if len(args) < 2 {
		usage()
	}
	start, stop := end, end
	if args[0] != "end" {
		start = limboInt(args[0])
	}
	if args[1] != "end" {
		stop = limboInt(args[1])
	}
	if stop < start {
		os.Stderr.WriteString("bytes: out of order range\n")
		sys.Exits("fail:bad range")
	}
	args = args[2:]
	switch len(args) {
	case 0:
		show(start, stop)
	case 1:
		set(start, stop, s2bytes(args[0]))
	default:
		usage()
	}
	stdout.Flush()
}

// show prints the bytes in [start, stop): valid UTF-8 as it is, with
// backslashes doubled, and other bytes as \XX.
func show(start, stop int) {
	data := readUpTo(stop)
	if start >= len(data) {
		return
	}
	d := data[start:]
	for len(d) > 0 {
		r, n := utf8.DecodeRune(d)
		if r == utf8.RuneError && n <= 1 {
			fmt.Fprintf(stdout, "\\%2.2X", d[0])
			d = d[1:]
			continue
		}
		if r == '\\' {
			stdout.WriteByte('\\')
		}
		stdout.Write(d[:n])
		d = d[n:]
	}
}

// set copies its input with the bytes in [start, stop) replaced by b.
// A start of "end" appends b; a stop of "end" drops the rest.
func set(start, stop int, b []byte) {
	data := readUpTo(-1)
	switch {
	case start == end:
		stdout.Write(data)
		stdout.Write(b)
	case start <= len(data):
		stdout.Write(data[:start])
		stdout.Write(b)
		if stop != end && stop < len(data) {
			stdout.Write(data[stop:])
		}
	default:
		stdout.Write(data)
	}
}

// readUpTo reads standard input up to max bytes, or all of it when max
// is negative.
func readUpTo(max int) []byte {
	var r io.Reader = os.Stdin
	if max >= 0 {
		r = io.LimitReader(os.Stdin, int64(max))
	}
	data, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bytes: read error: %s\n", sys.Errstr())
		sys.Exits("fail:read error")
	}
	return data
}

func badseq() {
	os.Stderr.WriteString("bytes: invalid backslash sequence\n")
	sys.Exits("fail:bad args")
}

// s2bytes decodes \n, \r, \t, \v, \\ and \XX hex escapes.
func s2bytes(s string) []byte {
	var d []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			d = append(d, s[i])
			continue
		}
		if i+1 >= len(s) {
			badseq()
		}
		switch s[i+1] {
		case 'n':
			d = append(d, '\n')
		case 'r':
			d = append(d, '\r')
		case 't':
			d = append(d, '\t')
		case 'v':
			d = append(d, '\v')
		case '\\':
			d = append(d, '\\')
		default:
			if i+2 >= len(s) {
				badseq()
			}
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil || s[i+1] == '+' {
				badseq()
			}
			d = append(d, byte(v))
			i += 2
			continue
		}
		i++
	}
	return d
}

// limboInt converts a string as Limbo's int cast does: an optional sign
// and the leading decimal digits, 0 if there are none.
func limboInt(s string) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	n := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}
