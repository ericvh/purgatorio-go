// Command netstat lists the network connections in /net/tcp and /net/udp
// (Inferno netstat(1)): for each, its name, owner, state and local and
// remote addresses.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	nstat("/net/tcp", true)
	nstat("/net/udp", true)
	nstat("/net/il", false)
}

func nstat(file string, whine bool) {
	fd, err := sys.Open(file, sys.OREAD)
	if err != nil {
		if whine {
			fmt.Fprintf(os.Stderr, "netstat: %s: %s\n", file, sys.Errstr())
		}
		return
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	d, err := sys.Fstat(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "netstat: fstat %s: %s\n", file, sys.Errstr())
		return
	}
	if d.Mode&sys.DMDIR == 0 {
		fmt.Fprintf(os.Stderr, "netstat: not a protocol directory: %s\n", file)
		return
	}
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			break
		}
		for _, e := range ds {
			if e.Name != "" && e.Name[0] <= '9' {
				nsprint(file+"/"+e.Name, e.UID)
			}
		}
	}
}

func fc(file string) string {
	fd, err := sys.Open(file, sys.OREAD)
	if err != nil {
		return "??"
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	buf := make([]byte, 64)
	n, _ := f.Read(buf)
	if n <= 1 {
		return "??"
	}
	if buf[n-1] == '\n' {
		n--
	}
	return string(buf[:n])
}

func nsprint(name, user string) {
	st := strings.Fields(fc(name + "/status"))
	state := ""
	if len(st) > 0 {
		state = st[0]
	}
	fmt.Printf("%-10s %-10s %-12s %-20s %s\n", name[5:], user, state, fc(name+"/local"), fc(name+"/remote"))
}
