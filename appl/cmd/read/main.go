// Command read copies one read of at most count bytes (default 8192)
// from standard input to standard output (Inferno read(1)). -o, -e and
// -r first seek standard input from the start, the end or the current
// offset.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const atomicio = 8192

func usage() {
	os.Stderr.WriteString("usage: read [-[ero] offset] count\n")
	sys.Exits("fail:usage")
}

// num converts as Limbo's int and big of a string do: optional blanks
// and sign, then the leading digits; no digits is 0.
func num(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	var n int64
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int64(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}

func main() {
	count := int64(atomicio)
	var offset int64
	whence := -1
	args := os.Args[1:]
	if len(args) > 0 && args[0] != "" && args[0][0] == '-' {
		if len(args) < 2 {
			usage()
		}
		switch args[0] {
		case "-o":
			whence = io.SeekStart
		case "-e":
			whence = io.SeekEnd
		case "-r":
			whence = io.SeekCurrent
		default:
			usage()
		}
		offset = num(args[1])
		args = args[2:]
	}
	if len(args) > 0 {
		if len(args) > 1 {
			usage()
		}
		count = num(args[0])
	}
	if whence != -1 {
		_, _ = sys.Seek(0, offset, whence)
	}
	if count == 0 {
		return
	}
	if count < 0 {
		usage()
	}
	buf := make([]byte, count)
	n, err := os.Stdin.Read(buf)
	if n > 0 {
		os.Stdout.Write(buf[:n])
		return
	}
	if err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "read: read error: %s\n", sys.Errstr())
		sys.Exits("fail:error")
	}
	sys.Exits("fail:eof")
}
