// Command stream copies data from file1 to standard output, or both ways
// between file1 and file2, with Sys->stream (Inferno stream(1)). With -a
// it returns at once and the copying carries on in the background.
package main

import (
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const atomicio = 8192

func usage() {
	os.Stderr.WriteString("Usage: stream [-a] [-b bufsize] file1 [file2]\n")
	sys.Exits("fail:usage")
}

func main() {
	bsize := 0
	async := false
	argv := os.Args[1:]
	for ; len(argv) > 0 && len(argv[0]) > 1 && argv[0][0] == '-'; argv = argv[1:] {
		s := argv[0]
		switch s[1] {
		case 'b':
			if len(s) > 2 {
				bsize = atoi(s[2:])
			} else if argv = argv[1:]; len(argv) > 0 {
				bsize = atoi(argv[0])
			} else {
				usage()
			}
		case 'a':
			async = true
		default:
			usage()
		}
	}
	if bsize <= 0 || bsize > 2*1024*1024 {
		bsize = atomicio
	}
	if len(argv) < 1 {
		usage()
	}
	if len(argv) > 1 {
		f1 := eopen(argv[0], sys.ORDWR)
		f2 := eopen(argv[1], sys.ORDWR)
		if async {
			sys.Detach()
		}
		both(f1, f2, bsize)
		return
	}
	f1 := eopen(argv[0], sys.OREAD)
	if async {
		sys.Detach()
	}
	if sys.Stream(f1, 1, bsize) < 0 {
		os.Stderr.WriteString("stream: error streaming data: " + sys.Errstr() + "\n")
	}
}

// both copies f1 to f2 and f2 to f1 until each source ends. Inferno uses
// a thread for each direction; a guest has one, so it waits for either
// source to be readable.
func both(f1, f2, bsize int) {
	type dir struct {
		src, dst int
		in, out  *os.File
	}
	a, b := os.NewFile(uintptr(f1), ""), os.NewFile(uintptr(f2), "")
	live := []dir{{f1, f2, a, b}, {f2, f1, b, a}}
	buf := make([]byte, bsize)
	for len(live) > 0 {
		ops := make([][2]int, len(live))
		for i, d := range live {
			ops[i] = [2]int{sys.WaitRead, d.src}
		}
		i, err := sys.Wait(ops, -1)
		if err != nil || i < 0 {
			os.Stderr.WriteString("stream: error streaming data: " + sys.Errstr() + "\n")
			return
		}
		d := live[i]
		n, rerr := d.in.Read(buf)
		if n > 0 {
			if _, werr := d.out.Write(buf[:n]); werr != nil {
				os.Stderr.WriteString("stream: error streaming data: " + sys.Errstr() + "\n")
				live = append(live[:i], live[i+1:]...)
				continue
			}
		}
		if n == 0 || rerr != nil {
			if rerr != nil && n == 0 && rerr != io.EOF {
				os.Stderr.WriteString("stream: error streaming data: " + sys.Errstr() + "\n")
			}
			live = append(live[:i], live[i+1:]...)
		}
	}
}

func eopen(name string, mode int) int {
	fd, err := sys.Open(name, mode)
	if err != nil {
		os.Stderr.WriteString("stream: can't open " + name + ": " + sys.Errstr() + "\n")
		sys.Exits("fail:open")
	}
	return fd
}

// atoi converts a leading decimal number, as Limbo's int of a string.
func atoi(s string) int {
	i, neg := 0, false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	n := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		n = -n
	}
	return n
}
