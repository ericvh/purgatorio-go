// Command zeros writes blocks of zeros, a given byte value or random
// bytes (Inferno zeros(1)).
package main

import (
	"crypto/rand"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	a := arg.New(os.Args)
	rflag := false
	val := 0
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'r':
			rflag = true
		case 'v':
			val = toint(a.Arg(), 16)
		default:
			sys.Exits("fail:unknown option (" + string(o) + ")\n")
		}
	}
	args := a.Argv()
	bs, n := 1, 1
	if len(args) >= 1 {
		bs = toint(args[0], 10)
	}
	if len(args) >= 2 {
		n = toint(args[1], 10)
	}
	if bs == 0 || n == 0 {
		os.Stderr.WriteString("usage: zeros [-r] [-v value] blocksize [number]\n")
		sys.Exits("fail:usage")
	}
	if bs < 0 {
		// Limbo cannot make a negative array.
		sys.Exits("fail:negative array size")
	}
	z := make([]byte, bs)
	if rflag {
		_, _ = rand.Read(z)
	} else {
		for i := range z {
			z[i] = byte(val)
		}
	}
	for i := 0; i < n; i++ {
		_, _ = os.Stdout.Write(z)
	}
}

// toint parses the leading number of s in base, after blanks and a sign,
// as String->toint does; 0 when there is none.
func toint(s string, base int) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	j := i
	for j < len(s) && digit(s[j], base) {
		j++
	}
	v, _ := strconv.ParseInt(s[i:j], base, 64)
	if neg {
		v = -v
	}
	return int(v)
}

func digit(c byte, base int) bool {
	switch {
	case c >= '0' && c <= '9':
		return int(c-'0') < base
	case c >= 'a' && c <= 'z':
		return int(c-'a')+10 < base
	case c >= 'A' && c <= 'Z':
		return int(c-'A')+10 < base
	}
	return false
}
