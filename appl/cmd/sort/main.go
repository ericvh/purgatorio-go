// Command sort sorts lines (Inferno sort(1)).
package main

import (
	"bufio"
	"io"
	"os"
	"sort"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("usage: sort [-n] [file]\n")
	sys.Exits("fail:usage")
}

func main() {
	a := arg.New(os.Args)
	nflag, rflag := false, false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'n':
			nflag = true
		case 'r':
			rflag = true
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) > 1 {
		usage()
	}
	in := io.Reader(os.Stdin)
	if len(args) == 1 {
		fd, err := sys.Open(args[0], sys.OREAD)
		if err != nil {
			os.Stderr.WriteString("sort: cannot open " + args[0] + ": " + sys.Errstr() + "\n")
			sys.Exits("fail:open file")
		}
		in = os.NewFile(uintptr(fd), args[0])
	}
	b := bufio.NewReader(in)
	var lines []string
	for {
		s, _ := b.ReadString('\n')
		if s == "" {
			break
		}
		lines = append(lines, s)
	}
	if nflag {
		sort.SliceStable(lines, func(i, j int) bool { return atoi(lines[i]) < atoi(lines[j]) })
	} else {
		sort.SliceStable(lines, func(i, j int) bool { return lines[i] < lines[j] })
	}
	out := bufio.NewWriter(os.Stdout)
	if rflag {
		for i := len(lines) - 1; i >= 0; i-- {
			out.WriteString(lines[i])
		}
	} else {
		for _, s := range lines {
			out.WriteString(s)
		}
	}
	out.Flush()
}

// atoi is Limbo's int of a string: leading blanks, a sign and decimal
// digits, with 32-bit wraparound.
func atoi(s string) int32 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	n := int32(0)
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int32(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}
