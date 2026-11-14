// Command xargs runs a command once for each line of standard input, with
// the line's words appended to its arguments (Inferno xargs(1)).
package main

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		os.Stderr.WriteString("Usage: xargs command [command args] <[list of last command arg]\n")
		return
	}
	cmd, args := args[0], args[1:]
	if !strings.HasSuffix(cmd, ".wasm") {
		cmd += ".wasm"
	}
	// Inferno loads the module once, from the name given or from /dis.
	if _, err := sys.Stat(cmd); err != nil && !strings.HasPrefix(cmd, "/") {
		cmd = "/bin/" + cmd
	}
	if _, err := sys.Stat(cmd); err != nil {
		os.Stderr.WriteString("xargs: can't load " + cmd + ": " + sys.Errstr() + "\n")
		return
	}
	me, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(me)+"/wait", sys.OREAD)
	if err != nil {
		os.Stderr.WriteString("xargs: can't open wait file: " + sys.Errstr() + "\n")
		sys.Exits("fail:wait")
	}
	wait := os.NewFile(uintptr(wfd), "wait")
	in := bufio.NewReader(os.Stdin)
	for {
		line, rerr := in.ReadString('\n')
		if line == "" && rerr != nil {
			if rerr != io.EOF {
				os.Stderr.WriteString("xargs: read error: " + sys.Errstr() + "\n")
			}
			return
		}
		words := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
		if len(words) == 0 {
			continue
		}
		argv := append(append([]string{cmd}, args...), words...)
		pid, err := sys.Spawn(cmd, argv, sys.NEWFD, [][2]int{{0, 0}, {1, 1}, {2, 2}})
		if err != nil {
			os.Stderr.WriteString("xargs: can't load " + cmd + ": " + sys.Errstr() + "\n")
			return
		}
		// The command runs in xargs's own thread in Inferno, so its
		// failure is xargs's failure.
		if st := waitfor(wait, pid); st != "" {
			sys.Exits(st)
		}
	}
}

// waitfor reads exit messages, pid "name":status, until pid's, and
// returns its status.
func waitfor(wait *os.File, pid int) string {
	buf := make([]byte, 256)
	for {
		n, err := wait.Read(buf)
		if err != nil || n == 0 {
			return ""
		}
		msg := string(buf[:n])
		i := 0
		for i < len(msg) && msg[i] >= '0' && msg[i] <= '9' {
			i++
		}
		who, _ := strconv.Atoi(msg[:i])
		if who != pid {
			continue
		}
		if j := strings.Index(msg[i:], "\":"); j >= 0 {
			return msg[i+j+2:]
		}
		return ""
	}
}
