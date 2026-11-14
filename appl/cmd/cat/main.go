// Command cat concatenates files to standard output (Inferno cat(1)).
package main

import (
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const atomicio = 8192

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"-"}
	}
	for _, file := range args {
		if file == "-" {
			cat(os.Stdin, "<stdin>")
			continue
		}
		f, err := os.Open(file)
		if err != nil {
			os.Stderr.WriteString("cat: cannot open " + file + ": " + sys.Errstr() + "\n")
			sys.Exits("fail:bad open")
		}
		cat(f, file)
		f.Close()
	}
}

func cat(f *os.File, name string) {
	buf := make([]byte, atomicio)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if w, _ := os.Stdout.Write(buf[:n]); w < n {
				os.Stderr.WriteString("cat: write error: " + sys.Errstr() + "\n")
				sys.Exits("fail:write error")
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			os.Stderr.WriteString("cat: error reading " + name + ": " + sys.Errstr() + "\n")
			sys.Exits("fail:read error")
		}
	}
}
