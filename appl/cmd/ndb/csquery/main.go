// Command ndb/csquery asks the connection server (cs(8)) to translate
// each address given, or each line of standard input after a "> "
// prompt, and prints the answers one per line.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("usage: csquery [-x /net] [-s server] [address ...]\n")
	sys.Exits("fail:usage")
}

func main() {
	net := "/net"
	server := ""
	a := arg.New(os.Args)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'x':
			if net = a.Arg(); net == "" {
				usage()
			}
		case 's':
			if server = a.Arg(); server == "" {
				usage()
			}
		default:
			usage()
		}
	}
	if server == "" {
		server = net + "/cs"
	}
	if args := a.Argv(); len(args) > 0 {
		for _, s := range args {
			csquery(server, s)
		}
		return
	}
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		s, err := in.ReadString('\n')
		if err != nil {
			break
		}
		csquery(server, s[:len(s)-1])
	}
}

func csquery(server, addr string) {
	fd, err := sys.Open(server, sys.ORDWR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csquery: can't open %s: %s\n", server, sys.Errstr())
		sys.Exits("fail:open")
	}
	cs := os.NewFile(uintptr(fd), server)
	defer cs.Close()
	if n, _ := cs.Write([]byte(addr)); n > 0 {
		cs.Seek(0, 0)
		buf := make([]byte, 256)
		for {
			n, err := cs.Read(buf)
			if err == io.EOF {
				return
			}
			if err != nil {
				break
			}
			fmt.Printf("%s\n", buf[:n])
		}
	}
	fmt.Printf("%s: %s\n", addr, sys.Errstr())
}
