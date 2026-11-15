// Command ip/virgild answers virgil(2) queries (Inferno virgild(8)): a
// datagram on udp!*!virgil holding user?name is answered with
// user?name=address, the address that cs gives for tcp!name.
package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const udphdrsize = 52

var stderr = os.Stderr

func main() {
	sys.Pctl(sys.FORKNS|sys.FORKFD, nil)
	if err := sys.Chdir("/lib/ndb"); err != nil {
		stderr.WriteString("virgild: no database\n")
		return
	}
	for ; ; time.Sleep(10 * time.Second) {
		fd := openlisten()
		if fd == nil {
			return
		}
		buf := make([]byte, 512)
		for {
			n, _ := fd.Read(buf)
			if n <= udphdrsize {
				break
			}
			if n <= udphdrsize+1 {
				continue
			}
			// dump any cruft after the question
			i := udphdrsize
			for ; i < n; i++ {
				if c := buf[i]; c == ' ' || c == 0 || c == '\n' {
					break
				}
			}
			answer := query(string(buf[udphdrsize:i]))
			if answer == "" {
				continue
			}
			if len(answer) > len(buf)-udphdrsize {
				continue
			}
			copy(buf[udphdrsize:], answer)
			fd.Write(buf[:udphdrsize+len(answer)])
		}
		fd.Close()
	}
}

func openlisten() *os.File {
	c, err := dial.Announce("udp!*!virgil")
	if err != nil {
		stderr.WriteString("virgild: can't open port: " + sys.Errstr() + "\n")
		return nil
	}
	if _, err := c.Cfd.Write([]byte("headers")); err != nil {
		stderr.WriteString("virgild: can't set headers: " + sys.Errstr() + "\n")
		return nil
	}
	fd, err := sys.Open(c.Dir+"/data", sys.ORDWR)
	if err != nil {
		stderr.WriteString("virgild: can't open data file\n")
		return nil
	}
	return os.NewFile(uintptr(fd), "data")
}

// query answers user?question; the user is ignored for now.
func query(request string) string {
	l := strings.FieldsFunc(request, func(r rune) bool { return r == '?' })
	if len(l) < 2 {
		stderr.WriteString("virgild: bad request " + request + " " + strconv.Itoa(len(l)) + "\n")
		return ""
	}
	// until we have something better, ask cs to translate
	fd, err := sys.Open("/net/cs", sys.ORDWR)
	if err != nil {
		stderr.WriteString("virgild: can't open /net/cs - " + sys.Errstr() + "\n")
		return ""
	}
	cs := os.NewFile(uintptr(fd), "cs")
	defer cs.Close()
	q := "tcp!" + l[1] + "!1000"
	if _, err := cs.Write([]byte(q)); err != nil {
		stderr.WriteString("virgild: can't write /net/cs - " + sys.Errstr() + ": " + q + "\n")
		return ""
	}
	cs.Seek(0, 0)
	buf := make([]byte, 512-udphdrsize-len(request)-1)
	n, _ := cs.Read(buf)
	if n <= 0 {
		stderr.WriteString("virgild: can't read /net/cs - " + sys.Errstr() + "\n")
		return ""
	}
	f := strings.Fields(string(buf[:n]))
	if len(f) < 2 {
		return ""
	}
	addr, _, _ := strings.Cut(f[1], "!")
	return request + "=" + addr
}
