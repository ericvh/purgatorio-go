// Command ps prints the status of every prog, one line each, from
// /prog/n/status (Inferno ps(1)).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func fail(msg, status string) {
	os.Stderr.WriteString("ps: " + msg + ": " + sys.Errstr() + "\n")
	sys.Exits(status)
}

func main() {
	_, _ = sys.Pctl(sys.FORKNS, nil)
	if err := sys.Chdir("/prog"); err != nil {
		fail("can't chdir to /prog", "fail:no /prog")
	}
	fd, err := sys.Open(".", sys.OREAD)
	if err != nil {
		fail("cannot open /prog", "fail:no /prog")
	}
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			fail("error reading /prog", "fail:error on /prog")
		}
		if len(ds) == 0 {
			break
		}
		for _, d := range ds {
			if d.Name != "" && d.Name[0] >= '0' && d.Name[0] <= '9' {
				ps(d.Name)
			}
		}
	}
}

func ps(pid string) {
	f, err := os.Open(pid + "/status")
	if err != nil {
		return // the prog has exited
	}
	defer f.Close()
	buf := make([]byte, 128)
	if n, _ := f.Read(buf); n > 0 {
		os.Stdout.Write(append(buf[:n:n], '\n'))
	}
}
