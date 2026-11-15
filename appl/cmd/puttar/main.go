// Command puttar writes a POSIX.1 tar archive of the named files and
// directory trees, or of the files named one per line on standard
// input, to standard output (Inferno gettar(1)).
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/ustar"
)

const (
	tblock = ustar.BlockSize
	nblock = ustar.NBlock
)

var (
	tw   = ustar.NewWriter(os.Stdout)
	ibuf = make([]byte, nblock*tblock)
)

func warning(mess string) {
	fmt.Fprintf(os.Stderr, "warning: puttar: %s: %s\n", mess, sys.Errstr())
}

// fail ends the archive early with no failure status, as Inferno's
// Error does.
func fail(mess string) {
	fmt.Fprintf(os.Stderr, "puttar: %s: %s\n", mess, sys.Errstr())
	sys.Exits("")
}

func putblock(data []byte) {
	if tw.Block(data) != nil {
		fail("write error")
	}
}

func packname(hdr []byte, name string) {
	switch ustar.PackName(hdr, name) {
	case ustar.ErrNoSplit:
		fail(name + " > 100 bytes")
	case ustar.ErrPrefixLen:
		fail(name + " too long\n")
	}
}

func octal(b []byte, width int, val uint64) { ustar.PutOctal(b, width, val) }

func tar(file string) {
	st, err := sys.Stat(file)
	if err != nil {
		warning("cannot stat " + file)
		return
	}
	fd, err := sys.Open(file, sys.OREAD)
	if err != nil {
		fail("cannot open " + file)
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	hdr := make([]byte, tblock)
	packname(hdr, file)
	octal(hdr[100:], 7, uint64(st.Mode&0o777))
	octal(hdr[108:], 7, 1)
	octal(hdr[116:], 7, 1)
	octal(hdr[124:], 11, st.Length)
	octal(hdr[136:], 11, uint64(st.Mtime))
	hdr[156] = '0'
	if st.Mode&sys.DMDIR != 0 {
		hdr[156] = '5'
	}
	copy(hdr[257:], "ustar")
	copy(hdr[263:], "00")
	copy(hdr[265:297], st.UID)
	copy(hdr[297:329], st.GID)
	octal(hdr[329:], 8, uint64(st.Dev))
	octal(hdr[337:], 8, st.Qid.Path)
	ustar.Checksum(hdr)
	putblock(hdr)
	for bytes := int(st.Length); bytes > 0; {
		n := min(len(ibuf), bytes)
		if _, err := io.ReadFull(f, ibuf[:n]); err != nil {
			fail("read error on " + file)
		}
		nb := (n + tblock - 1) / tblock
		clear(ibuf[n : nb*tblock])
		for i := range nb {
			putblock(ibuf[i*tblock : (i+1)*tblock])
		}
		bytes -= n
	}
}

func rtar(file string) {
	tar(file)
	st, err := sys.Stat(file)
	if err != nil {
		warning("cannot stat " + file)
		return
	}
	if st.Mode&sys.DMDIR == 0 {
		return
	}
	fd, err := sys.Open(file, sys.OREAD)
	if err != nil {
		fail("cannot open " + file)
	}
	defer os.NewFile(uintptr(fd), file).Close()
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			break
		}
		for _, d := range ds {
			if strings.HasSuffix(file, "/") {
				rtar(file + d.Name)
			} else {
				rtar(file + "/" + d.Name)
			}
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		in := bufio.NewReader(os.Stdin)
		for {
			file, _ := in.ReadString('\n')
			if file == "" {
				break
			}
			tar(strings.TrimSuffix(file, "\n"))
		}
	} else {
		for _, f := range os.Args[1:] {
			rtar(f)
		}
	}
	if tw.Close() != nil { // the format ends with two empty blocks
		fail("write error")
	}
}
