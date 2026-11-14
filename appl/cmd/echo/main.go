// Command echo writes its arguments, separated by spaces, followed by a
// newline unless -n is given (Inferno echo(1)).
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	args := os.Args[1:]
	nl := true
	if len(args) > 0 && args[0] == "-n" {
		nl = false
		args = args[1:]
	}
	s := strings.Join(args, " ")
	if nl {
		s += "\n"
	}
	if _, err := os.Stdout.WriteString(s); err != nil {
		sys.Fatal("echo: write error: " + sys.Errstr())
	}
}
