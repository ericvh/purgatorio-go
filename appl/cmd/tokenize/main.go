// Command tokenize splits a string into tokens at any of the delimiter
// characters and prints one per line (Inferno tokenize(1)).
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) != 3 {
		os.Stderr.WriteString("Usage: tokenize string delimiters\n")
		sys.Exits("fail: usage")
	}
	delim := os.Args[2]
	for _, t := range strings.FieldsFunc(os.Args[1], func(r rune) bool { return strings.ContainsRune(delim, r) }) {
		os.Stdout.WriteString(t + "\n")
	}
}
