// Command lstar lists the files in the tar archive on standard input:
// name, mtime, size and 0, as du -n -t does (Inferno gettar(1)).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	tblock = 512
	nblock = 20
)

var (
	tarbuf = make([]byte, nblock*tblock)
	nblk   = nblock
	recno  = nblock
)

// fail stops with no failure status, as Inferno's Error does.
func fail(mess string) {
	fmt.Fprintf(os.Stderr, "lstar: %s: %s\n", mess, sys.Errstr())
	sys.Exits("")
}

func getblock() []byte {
	if recno >= nblk {
		i, err := os.Stdin.Read(tarbuf)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			fail("read error")
		}
		if i%tblock != 0 {
			fail("blocksize error")
		}
		nblk = i / tblock
		recno = 0
	}
	recno++
	return tarbuf[(recno-1)*tblock : recno*tblock]
}

func main() {
	for {
		b := getblock()
		if b == nil || b[0] == 0 {
			break
		}
		name := nullterm(b[0:100])
		if b[345] != 0 {
			name = nullterm(b[345:500]) + "/" + name
		}
		magic := b[257:262]
		if magic[0] != 0 && string(magic) != "ustar" {
			fail("bad magic " + name)
		}
		chk := octal(b[148:156])
		copy(b[148:156], "        ")
		for _, c := range b {
			chk -= int(c)
		}
		if chk != 0 {
			fail("directory checksum error " + name)
		}
		size, mtime := 0, 0
		switch t := b[156]; t {
		case '0', '5', '7', 0:
			size = octal(b[124:136])
			mtime = octal(b[136:148])
		case '1':
			fmt.Fprintf(os.Stderr, "skipping link %s -> %s\n", name, nullterm(b[157:257]))
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

func octal(b []byte) int {
	sum := 0
	for _, c := range b {
		if c == ' ' {
			continue
		}
		if c == 0 {
			break
		}
		sum = 8*sum + int(c) - '0'
	}
	return sum
}

func nullterm(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
