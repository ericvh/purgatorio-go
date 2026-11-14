// Command ndb/dnsquery asks the domain name server (dns(8)) each query
// given, or each line of standard input after a "> " prompt, and prints
// the answers. A query is a name and a record type; with no type, ptr
// is asked for an IP address and ip for a name. For ptr, an address is
// turned into its in-addr.arpa name.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("usage: dnsquery [-x /net] [-s server] [address ...]\n")
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
		server = net + "/dns"
	}
	if args := a.Argv(); len(args) > 0 {
		for _, s := range args {
			dnsquery(server, s)
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
		dnsquery(server, s[:len(s)-1])
	}
}

func dnsquery(server, query string) {
	fd, err := sys.Open(server, sys.ORDWR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dnsquery: can't open %s: %s\n", server, sys.Errstr())
		sys.Exits("fail:open")
	}
	dns := os.NewFile(uintptr(fd), server)
	defer dns.Close()
	i := strings.LastIndexByte(query, ' ')
	if i < 0 {
		i = len(query)
		if dbattr(query) == "ip" {
			query += " ptr"
		} else {
			query += " ip"
		}
	}
	if query[i+1:] == "ptr" {
		for i > 0 && query[i-1] == ' ' {
			i--
		}
		if q := query[:i]; !strings.HasSuffix(q, ".in-addr.arpa") && !strings.HasSuffix(q, ".IN-ADDR.ARPA") {
			query = addr2arpa(q) + " ptr"
		}
	}
	if n, _ := dns.Write([]byte(query)); n > 0 {
		dns.Seek(0, 0)
		buf := make([]byte, 256)
		for {
			n, err := dns.Read(buf)
			if err == io.EOF {
				return
			}
			if err != nil {
				break
			}
			fmt.Printf("%s\n", buf[:n])
		}
	}
	fmt.Printf("!%s\n", sys.Errstr())
}

func addr2arpa(a string) string {
	f := strings.FieldsFunc(a, func(r rune) bool { return r == '.' })
	for i, j := 0, len(f)-1; i < j; i, j = i+1, j-1 {
		f[i], f[j] = f[j], f[i]
	}
	return strings.Join(f, ".") + ".in-addr.arpa"
}

// dbattr guesses what kind of name s is: ip, dom or sys.
func dbattr(s string) string {
	var dot, alpha, hex, colon bool
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F':
			hex = true
		case c == '.':
			dot = true
		case c == ':':
			colon = true
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '&':
			alpha = true
		}
	}
	switch {
	case alpha:
		if dot {
			return "dom"
		}
		return "sys"
	case colon:
		return "ip"
	case dot && !hex:
		return "ip"
	case dot:
		return "dom"
	}
	return "sys"
}
