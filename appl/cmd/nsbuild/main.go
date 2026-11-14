// Command nsbuild builds a name space from a description file (Inferno
// nsbuild(1)): namespace in the current directory by default. As in
// Inferno, new and fork affect only nsbuild itself.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/newns"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) > 2 {
		os.Stderr.WriteString("Usage: nsbuild [nsfile]\n")
		sys.Exits("fail:usage")
	}
	file := "namespace"
	if len(os.Args) == 2 {
		file = os.Args[1]
	}
	if err := newns.Newns("", file); err != nil {
		os.Stderr.WriteString("nsbuild: error building namespace: " + err.Error() + "\n")
		sys.Exits("fail:newns")
	}
}
