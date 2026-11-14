// Command sum prints a CRC checksum and the length of each file
// (Inferno sum(1)).
package main

import (
	"fmt"
	"hash/crc32"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Inferno's Crc module with init(0, 0): the IEEE polynomial, reflected,
// with no initial or final inversion.
var tab = crc32.MakeTable(crc32.IEEE)

func crc(c uint32, b []byte) uint32 {
	for _, x := range b {
		c = tab[byte(c)^x] ^ c>>8
	}
	return c
}

func main() {
	failed := false
	buf := make([]byte, 8192)
	for _, s := range os.Args[1:] {
		d, err := sys.Stat(s)
		if err != nil {
			os.Stderr.WriteString("sum: cannot get status of " + s + ": " + sys.Errstr() + "\n")
			failed = true
			continue
		}
		if d.Mode&sys.DMDIR != 0 {
			continue
		}
		fd, err := sys.Open(s, sys.OREAD)
		if err != nil {
			os.Stderr.WriteString("sum: cannot open " + s + ": " + sys.Errstr() + "\n")
			failed = true
			continue
		}
		f := os.NewFile(uintptr(fd), s)
		c := uint32(0)
		nbytes := int64(0)
		for {
			n, err := f.Read(buf)
			c = crc(c, buf[:n])
			nbytes += int64(n)
			if err == io.EOF {
				break
			}
			if err != nil {
				os.Stderr.WriteString("sum: error reading " + s + ": " + sys.Errstr() + "\n")
				failed = true
				break
			}
		}
		f.Close()
		// Encode the length, so that an empty file does not sum to 0.
		l := uint32(nbytes)
		c = crc(c, []byte{byte(l>>24) ^ 0xcc, byte(l>>16) ^ 0x55, byte(l>>8) ^ 0xcc, byte(l) ^ 0x55})
		fmt.Printf("%.8x %6d %s\n", c, nbytes, s)
	}
	if failed {
		sys.Exits("fail:error")
	}
}
