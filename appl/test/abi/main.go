// Command abi exercises the purgatorio import module: werrstr and errstr
// round-trip, and exits carries an Inferno status string to the host.
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
	if len(os.Args) > 1 {
		sys.Exits(os.Args[1])
	}
}
