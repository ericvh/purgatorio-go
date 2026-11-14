// Command unicode converts between characters and their Unicode values
// and prints tables of character ranges (Inferno unicode(1)).
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const (
	usage   = "unicode { [-t] hex hex ... | hexmin-hexmax ... | [-n] char ... }"
	hex     = "0123456789abcdefABCDEF"
	runemax = 0x10FFFF
)

var (
	numout, text bool
	out          = bufio.NewWriter(os.Stdout)
)

func main() {
	args := os.Args[1:]
	for len(args) > 0 && (args[0] == "" || args[0][0] == '-') {
		switch args[0] {
		case "-n":
			numout = true
		case "-t":
			text = true
		}
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s\n", usage)
		return
	}
	defer out.Flush()
	switch {
	case !numout && strings.Contains(args[0], "-"):
		ranges(args)
	case numout || !ishex(args[0]):
		nums(args)
	default:
		chars(args)
	}
}

func ishex(s string) bool { return s != "" && strings.IndexByte(hex, s[0]) >= 0 }

// toint parses a hexadecimal number at the start of s, as String's toint
// does, and returns it with the rest of s.
func toint(s string) (int64, string) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var n int64
	for ; i < len(s); i++ {
		d := strings.IndexByte("0123456789abcdef", s[i]|0x20)
		if d < 0 || s[i] < '0' {
			break
		}
		n = n*16 + int64(d)
		if n > 1<<40 {
			n = 1 << 40
		}
	}
	if i == start {
		return 0, s
	}
	if neg {
		n = -n
	}
	return n, s[i:]
}

func ranges(args []string) {
	for _, a := range args {
		bad := func() { fmt.Fprintf(os.Stderr, "unicode: bad range %s\n", a) }
		if !ishex(a) {
			bad()
			return
		}
		lo, q := toint(a)
		if lo < 0 || lo > runemax || q == "" || q[0] != '-' {
			bad()
			return
		}
		q = q[1:]
		if !ishex(q) {
			bad()
			return
		}
		hi, q := toint(q)
		if hi < 0 || hi > runemax || hi < lo || q != "" {
			bad()
			return
		}
		for i := 1; ; i++ {
			fmt.Fprintf(out, "%.4x %c", lo, rune(lo))
			if lo == hi || i&7 == 0 {
				out.WriteByte('\n')
			} else {
				out.WriteByte('\t')
			}
			if lo == hi {
				break
			}
			lo++
		}
	}
}

func nums(args []string) {
	for _, a := range args {
		for _, r := range a {
			fmt.Fprintf(out, "%.4x\n", r)
		}
	}
}

func chars(args []string) {
	for _, a := range args {
		bad := func() { fmt.Fprintf(os.Stderr, "unicode: bad unicode value %s\n", a) }
		if !ishex(a) {
			bad()
			return
		}
		m, q := toint(a)
		if m < 0 || m > runemax || q != "" {
			bad()
			return
		}
		fmt.Fprintf(out, "%c", rune(m))
		if !text {
			out.WriteByte('\n')
		}
	}
}
