// Command ndb/regquery asks a registry (registry(4)) for the services
// with the attributes and values given, or for those on each line of
// standard input after a "> " prompt, and prints them as the registry's
// find file gives them, or with -n only their names.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	mntpt := "/mnt/registry"
	namesonly := false
	a := arg.New(os.Args)
	a.SetUsage("regquery [-m mntpt] [-n] [attr val attr val ...]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'm':
			mntpt = a.EArg()
		case 'n':
			namesonly = true
		default:
			a.Usage()
		}
	}
	finder := mntpt + "/find"
	if args := a.Argv(); len(args) > 0 {
		q := make([]string, len(args))
		for i, s := range args {
			q[i] = quote.Quote(s)
		}
		regquery(finder, strings.Join(q, " "), namesonly)
		return
	}
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		s, err := in.ReadString('\n')
		if err != nil {
			break
		}
		regquery(finder, s[:len(s)-1], namesonly)
	}
}

func regquery(server, query string, namesonly bool) {
	fd, err := sys.Open(server, sys.ORDWR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "regquery: can't open %s: %s\n", server, sys.Errstr())
		sys.Exits("fail:open")
	}
	f := os.NewFile(uintptr(fd), server)
	defer f.Close()
	if _, err := f.Write([]byte(query)); err == nil || len(query) == 0 {
		f.Seek(0, 0)
		if namesonly {
			b := bufio.NewReader(f)
			for {
				s, err := b.ReadString('\n')
				if s == "" && err != nil {
					return
				}
				if l := quote.Unquoted(s); l != nil {
					fmt.Printf("%s\n", l[0])
				}
			}
		}
		buf := make([]byte, 8192)
		for {
			n, err := f.Read(buf)
			if err == io.EOF {
				return
			}
			if err != nil {
				break
			}
			os.Stdout.Write(buf[:n])
		}
	}
	fmt.Fprintf(os.Stderr, "regquery: %s\n", sys.Errstr())
}
