// Command uniq reports or filters repeated adjacent lines
// (Inferno uniq(1)).
package main

import (
	"bufio"
	"io"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func fail(ex, msg string) {
	os.Stderr.WriteString(msg + "\n")
	sys.Exits("fail:" + ex)
}

func usage() { fail("usage", "usage: uniq [-udc] [file]") }

func main() {
	a := arg.New(os.Args)
	var uflag, dflag, cflag bool
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'u':
			uflag = true
		case 'd':
			dflag = true
		case 'c':
			cflag = true
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) > 1 {
		usage()
	}
	in := io.Reader(os.Stdin)
	if len(args) == 1 {
		fd, err := sys.Open(args[0], sys.OREAD)
		if err != nil {
			// Inferno's message ends with two newlines.
			fail("open file", "uniq: cannot open "+args[0]+": "+sys.Errstr()+"\n")
		}
		in = os.NewFile(uintptr(fd), args[0])
	}
	b := bufio.NewReader(in)
	out := bufio.NewWriter(os.Stdout)
	if !uflag && !dflag {
		uflag, dflag = true, true
	}
	prev := ""
	n := 0
	put := func() {
		if uflag && n == 1 || dflag && n > 1 {
			if cflag {
				out.WriteString(strconv.Itoa(n) + "\t")
			}
			out.WriteString(prev)
		}
	}
	for {
		s, _ := b.ReadString('\n')
		if s == "" {
			break
		}
		if s == prev {
			n++
			continue
		}
		put()
		n = 1
		prev = s
	}
	put()
	out.Flush()
}
