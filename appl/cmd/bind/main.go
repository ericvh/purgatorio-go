// Command bind adds a file or directory to the name space (Inferno
// bind(1)): -b and -a put source before or after target in a union, -c
// allows creation, and -q suppresses the error message.
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("usage: bind [-a|-b|-c|-ac|-bc] [-q] source target\n")
	sys.Exits("fail:usage")
}

func main() {
	flags, qflag := 0, false
	args := os.Args[1:]
	for len(args) > 0 && args[0] != "" && args[0][0] == '-' {
		a := args[0]
		args = args[1:]
		if a == "--" {
			break
		}
		for _, c := range a[1:] {
			switch c {
			case 'a':
				flags |= sys.MAFTER
			case 'b':
				flags |= sys.MBEFORE
			case 'c':
				flags |= sys.MCREATE
			case 'q':
				qflag = true
			default:
				usage()
			}
		}
	}
	if len(args) != 2 || flags&sys.MAFTER != 0 && flags&sys.MBEFORE != 0 {
		usage()
	}
	f1, f2 := args[0], args[1]
	if err := sys.Bind(f1, f2, flags); err != nil {
		if qflag {
			return
		}
		// Say which name is bad if one of them is.
		e := sys.Errstr()
		if _, err := sys.Stat(f1); err != nil {
			fmt.Fprintf(os.Stderr, "bind: %s: %s\n", f1, sys.Errstr())
		} else if _, err := sys.Stat(f2); err != nil {
			fmt.Fprintf(os.Stderr, "bind: %s: %s\n", f2, sys.Errstr())
		} else {
			fmt.Fprintf(os.Stderr, "bind: cannot bind %s onto %s: %s\n", f1, f2, e)
		}
		sys.Exits("fail:bind")
	}
}
