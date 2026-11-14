// Command whois asks a whois server about a domain name or network
// address (Inferno whois(1)), following referrals to other servers
// unless -n is given.
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	addr := "tcp!whois.iana.org!43"
	refers := 5
	a := arg.New(os.Args)
	a.SetUsage("whois [-a addr] [-n] host")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			addr = dial.Netmkaddr(a.EArg(), "tcp", "43")
		case 'n':
			refers = 0
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) != 1 {
		a.Usage()
	}
	host := args[0]
	f := whois(addr, host)
	buf := make([]byte, 512)
	for {
		n, err := f.Read(buf)
		if n <= 0 {
			if err != nil && err.Error() != "EOF" {
				os.Stderr.WriteString("whois: reading info: " + sys.Errstr() + "\n")
				sys.Exits("fail:errors")
			}
			return
		}
		if refers > 0 {
			if next := referral(string(buf[:n])); next != "" {
				f = whois(next, host)
				refers--
				continue
			}
		}
		os.Stdout.Write(buf[:n])
	}
}

// referral looks in a chunk of a reply for the next server to ask.
func referral(s string) string {
	for _, l := range strings.Split(s, "\n") {
		f := strings.FieldsFunc(l, func(r rune) bool { return r == ' ' || r == '\t' })
		switch {
		case len(f) == 2 && f[0] == "refer:":
			return dial.Netmkaddr(f[1], "tcp", "43")
		case len(f) == 3 && f[0] == "Whois" && f[1] == "Server:":
			return dial.Netmkaddr(f[2], "tcp", "43")
		}
	}
	return ""
}

func whois(addr, host string) *os.File {
	os.Stdout.WriteString("[using server " + addr + "]\n")
	c, err := dial.Dial(addr, "")
	if err != nil {
		os.Stderr.WriteString("whois: dialing " + addr + ": " + err.Error() + "\n")
		sys.Exits("fail:errors")
	}
	if n, _ := c.Dfd.Write([]byte(host + "\r\n")); n != len(host)+2 {
		os.Stderr.WriteString("whois: sending name: " + sys.Errstr() + "\n")
		sys.Exits("fail:errors")
	}
	return c.Dfd
}
