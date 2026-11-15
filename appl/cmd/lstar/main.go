// Command lstar lists the files in the tar archive on standard input:
// name, mtime, size and 0, as du -n -t does (Inferno gettar(1)).
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/ustar"
)

const tblock = ustar.BlockSize

var tr = ustar.NewReader(os.Stdin)

// fail stops with no failure status, as Inferno's Error does.
func fail(mess string) {
	fmt.Fprintf(os.Stderr, "lstar: %s: %s\n", mess, sys.Errstr())
	sys.Exits("")
}

func getblock() []byte {
	b, err := tr.Block()
	if err == ustar.ErrBlocksize {
		fail("blocksize error")
	}
	if err != nil {
		fail("read error")
	}
	return b
}

func main() {
	for {
		b := getblock()
		if b == nil || b[0] == 0 {
			break
		}
		h, err := ustar.Parse(b)
		if err != nil {
			fail(err.Error())
		}
		name := h.Name
		size, mtime := 0, 0
		switch t := h.Type; t {
		case '0', '5', '7', 0:
			size = int(h.Size)
			mtime = int(h.Mtime)
		case '1':
			fmt.Fprintf(os.Stderr, "skipping link %s -> %s\n", name, h.Link)
		case '2', 's':
			fmt.Fprintf(os.Stderr, "skipping symlink %s\n", name)
		case '3', '4', '6':
			fmt.Fprintf(os.Stderr, "skipping special file %s\n", name)
		default:
			fail(fmt.Sprintf("unrecognized typeflag %d for %s", t, name))
		}
		for blocks := (size + tblock - 1) / tblock; blocks > 0; blocks-- {
			getblock()
		}
		fmt.Printf("%s %d %d 0\n", name, mtime, size)
	}
}
