// Command timestamp copies standard input to standard output, prefixing
// each line with the milliseconds since it started and an optional note
// (Inferno timestamp(1)).
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var (
	timefd *os.File
	start  int64
)

func main() {
	note := ""
	if len(os.Args) > 1 {
		note = os.Args[1] + " "
	}
	timefd, _ = os.Open("/dev/time")
	start = now()
	fmt.Printf("%.10d %sstart %d\n", now(), note, start)
	in := bufio.NewReader(os.Stdin)
	for {
		s, _ := in.ReadString('\n')
		if s == "" {
			break
		}
		fmt.Printf("%.10d %s%s", now(), note, s)
	}
}

// now returns the milliseconds since start, from /dev/time's
// microseconds.
func now() int64 {
	if timefd == nil {
		return 0
	}
	buf := make([]byte, 24)
	n, _ := timefd.ReadAt(buf, 0)
	if n <= 0 {
		return 0
	}
	us, _ := strconv.ParseInt(strings.TrimSpace(string(buf[:n])), 10, 64)
	return us/1000 - start
}
