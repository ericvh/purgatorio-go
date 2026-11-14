// Command unmount removes a bind or mount from the name space (Inferno
// unmount(1)); with only a target it removes everything there.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	var err error
	switch args := os.Args[1:]; len(args) {
	case 1:
		err = sys.Unmount("", args[0])
	case 2:
		err = sys.Unmount(args[0], args[1])
	default:
		os.Stderr.WriteString("Usage: unmount [source] target\n")
		return
	}
	if err != nil {
		os.Stderr.WriteString("unmount: " + sys.Errstr() + "\n")
	}
}
