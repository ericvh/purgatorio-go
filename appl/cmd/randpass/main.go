// Command randpass prints a random password of the given length,
// 16 by default (Inferno randpass(1)).
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	pwlen := 16
	if len(os.Args) > 1 {
		s := os.Args[1]
		n, err := strconv.Atoi(s)
		if !numeric(s) || err != nil || n <= 8 || n > 256 {
			os.Stderr.WriteString("Usage: randpass [password-length(<256, default=16)]\n")
			sys.Exits("fail:usage")
		}
		pwlen = n
	}
	// Inferno takes the base64 text of a random pwlen*8-bit integer;
	// leading zero bytes drop out of an integer, so skip them too.
	b := make([]byte, pwlen)
	for {
		rand.Read(b)
		if b[0] != 0 {
			break
		}
	}
	fmt.Printf("%s\n", base64.StdEncoding.EncodeToString(b)[:pwlen])
}

func numeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
