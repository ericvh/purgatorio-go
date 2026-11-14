// Command newer succeeds if its first file is no older than its second
// (Inferno newer(1)); its exit status says why not.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) != 3 {
		os.Stderr.WriteString("usage: newer newfile oldfile\n")
		sys.Exits("fail:usage")
	}
	d1, err := sys.Stat(os.Args[1])
	if err != nil {
		sys.Exits("fail:new:" + sys.Errstr())
	}
	if d1.Mode&sys.DMDIR != 0 {
		sys.Exits("fail:new:directory")
	}
	d2, err := sys.Stat(os.Args[2])
	if err != nil {
		sys.Exits("fail:old:" + sys.Errstr())
	}
	if d2.Mode&sys.DMDIR != 0 {
		sys.Exits("fail:old:directory")
	}
	if d2.Mtime > d1.Mtime {
		sys.Exits("fail:older")
	}
}
