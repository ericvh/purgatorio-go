// Command abi exercises the purgatorio import module and the host's
// handling of bad guest pointers: werrstr and errstr round-trip, a bad
// iovec gets EFAULT and "bad address" without harming the host, and
// exits carries an Inferno status string to the host.
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
	if len(os.Args) > 1 {
		sys.Exits(os.Args[1])
	}
}
