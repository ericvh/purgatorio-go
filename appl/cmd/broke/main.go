// Command broke kills every broken prog and prints their pids (Inferno's
// appl/tiny/broke.b).
//
// A prog is broken when its status says so. Here a trap ends a prog,
// so broken progs are rare; broke finds those there are.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func fail(s string) {
	fmt.Fprintf(os.Stderr, "broke: %s\n", s)
	sys.Exits("fail:error")
}

func main() {
	fd, err := sys.Open("/prog", sys.OREAD)
	if err != nil {
		fail("can't open /prog: " + sys.Errstr())
	}
	killed := ""
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			fail("error reading /prog: " + sys.Errstr())
		}
		if len(ds) == 0 {
			break
		}
		for _, d := range ds {
			if isbroken(d.Name) && kill(d.Name) {
				killed += " " + d.Name
			}
		}
	}
	if killed != "" {
		fmt.Println(killed)
	}
}

func isbroken(pid string) bool {
	f, err := os.Open("/prog/" + pid + "/status")
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, err := f.Read(buf)
	if err != nil {
		return false // the prog died or is exiting
	}
	l := strings.Fields(string(buf[:n]))
	return len(l) >= 5 && l[4] == "broken"
}

func kill(pid string) bool {
	f, err := os.OpenFile("/prog/"+pid+"/ctl", os.O_WRONLY, 0)
	if err == nil {
		_, err = f.WriteString("kill")
		f.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "broke: can't kill %s: %s\n", pid, sys.Errstr())
		return false
	}
	return true
}
