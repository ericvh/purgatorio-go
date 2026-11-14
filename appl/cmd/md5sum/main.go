// Command md5sum prints the MD5 digest of files or standard input
// (Inferno md5sum(1)).
package main

import (
	"crypto/md5"
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	failed := false
	if len(os.Args) > 1 {
		for _, s := range os.Args[1:] {
			f, err := os.Open(s)
			if err != nil {
				os.Stderr.WriteString("md5sum: cannot open " + s + ": " + sys.Errstr() + "\n")
				failed = true
				continue
			}
			failed = sum(f, s) || failed
			f.Close()
		}
	} else {
		failed = sum(os.Stdin, "")
	}
	if failed {
		sys.Exits("fail:error")
	}
}

func sum(f *os.File, file string) bool {
	failed := false
	h := md5.New()
	buf := make([]byte, 8192)
	for {
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			os.Stderr.WriteString("md5sum: error reading " + file + ": " + sys.Errstr() + "\n")
			failed = true
			break
		}
	}
	if file != "" {
		fmt.Printf("%x\t%s\n", h.Sum(nil), file)
	} else {
		fmt.Printf("%x\n", h.Sum(nil))
	}
	return failed
}
