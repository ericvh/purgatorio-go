// Command time runs a command and reports on standard error the seconds
// spent loading it, running it and in total (Inferno time(1)).
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	me, _ := sys.Pctl(0, nil)
	wait, err := os.Open("#p/" + strconv.Itoa(me) + "/wait")
	if err != nil {
		fmt.Fprintf(os.Stderr, "time: open wait: %s\n", sys.Errstr())
		return
	}
	argv := os.Args[1:]
	if len(argv) == 0 {
		os.Stderr.WriteString("usage: time cmd ...\n")
		return
	}
	file := argv[0]
	if !strings.HasSuffix(file, ".wasm") {
		file += ".wasm"
	}
	fds := [][2]int{{0, 0}, {1, 1}, {2, 2}}
	t0 := sys.Millisec()
	pid, err := sys.Spawn(file, argv, sys.NEWFD, fds)
	if err != nil {
		pid, err = sys.Spawn("/bin/"+file, argv, sys.NEWFD, fds)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "time: %s: %s\n", argv[0], sys.Errstr())
		return
	}
	t1 := sys.Millisec()
	waitfor(wait, pid)
	t2 := sys.Millisec()
	f1 := float64(t1-t0) / 1000
	f2 := float64(t2-t1) / 1000
	fmt.Fprintf(os.Stderr, "%.4gl %.4gr %.4gt\n", f1, f2, f1+f2)
}

// waitfor reads exit messages until pid's, reporting any that carry a
// status.
func waitfor(wait *os.File, pid int) {
	buf := make([]byte, 256)
	for {
		n, err := wait.Read(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sh: read wait: %s\n", sys.Errstr())
			return
		}
		status := string(buf[:n])
		if status == "" {
			continue
		}
		if status[len(status)-1] != ':' {
			fmt.Fprintf(os.Stderr, "%s\n", status)
		}
		if who := atoi(status); who != 0 && who == pid {
			return
		}
	}
}

func atoi(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}
