// Command sleep suspends execution for a number of seconds (Inferno
// sleep(1)).
package main

import (
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// valid accepts an optionally signed string of digits.
func valid(t string) bool {
	if t != "" && (t[0] == '-' || t[0] == '+') {
		t = t[1:]
	}
	if t == "" {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return false
		}
	}
	return true
}

func main() {
	if len(os.Args) < 2 || !valid(os.Args[1]) {
		os.Stderr.WriteString("usage: sleep time\n")
		sys.Exits("fail:usage")
	}
	t, _ := strconv.ParseInt(os.Args[1], 10, 32)
	if t > 0x7fffffff/1000 {
		t = 0x7fffffff / 1000
	}
	if t > 0 {
		_ = sys.Sleep(int(t) * 1000)
	}
}
