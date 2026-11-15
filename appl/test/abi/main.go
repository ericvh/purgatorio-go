// Command abi exercises the purgatorio import module and the host's
// handling of bad guest pointers: werrstr and errstr round-trip, a bad
// iovec gets EFAULT and "bad address" without harming the host, and
// exits carries an Inferno status string to the host. bind returns a mount
// id.
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	sys.Werrstr("file does not exist")
	if got := sys.Errstr(); got != "file does not exist" {
		fmt.Printf("errstr: got %q\n", got)
		sys.Exits("fail:errstr")
	}
	fmt.Println("errstr ok")
	if e := badRead(); e != 21 || sys.Errstr() != "bad address" {
		fmt.Printf("bad pointer: errno %d errstr %q\n", e, sys.Errstr())
		sys.Exits("fail:fault")
	}
	// bind returns the mount id: positive, and new for each entry.
	id1, err1 := sys.Bind("/lib", "/lib", sys.MREPL)
	id2, err2 := sys.Bind("/lib", "/lib", sys.MAFTER)
	if err1 != nil || err2 != nil || id1 <= 0 || id2 <= id1 {
		fmt.Printf("bind: ids %d %d: %v %v\n", id1, id2, err1, err2)
		sys.Exits("fail:bind")
	}
	if len(os.Args) > 1 {
		sys.Exits(os.Args[1])
	}
}
