// Command mkproto prints a proto file describing the named files and
// directories (Inferno install/mkproto). An argument -name leaves out
// the top-level name. With one argument the proto lists what is below
// it; with several, each argument is listed with what is below it.
package main

import (
	"bufio"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/proto"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	not  []string
	bout = bufio.NewWriter(os.Stdout)
)

func main() {
	argv := os.Args[1:]
	for len(argv) > 0 && strings.HasPrefix(argv[0], "-") {
		not = append([]string{argv[0][1:]}, not...)
		argv = argv[1:]
	}
	switch len(argv) {
	case 0:
		visit(".", "", -1)
	case 1:
		visit(argv[0], "", -1)
	default:
		for _, a := range argv {
			visit(a, a, 0)
		}
	}
	bout.Flush()
}

func warn(s string) { os.Stderr.WriteString("mkproto: " + s + "\n") }

func visit(fulln, reln string, depth int) {
	if depth == 0 {
		for _, n := range not {
			if n == reln {
				return
			}
		}
	}
	d, err := sys.Stat(fulln)
	if err != nil {
		warn("cannot stat " + fulln + ": " + sys.Errstr())
		return
	}
	if depth >= 0 {
		bout.WriteString(strings.Repeat("\t", depth) + quote.Quote(reln) + "\n")
	}
	if d.Mode&sys.DMDIR != 0 {
		ds, _ := proto.ReadDir(fulln)
		for _, e := range ds {
			visit(fulln+"/"+e.Name, e.Name, depth+1)
		}
	}
}
