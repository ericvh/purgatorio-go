// Command tail prints the end of a file (Inferno tail(1)).
package main

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const bsize = 8 * 1024

const (
	beg = iota
	end
)

const (
	chars = iota
	lines
)

var (
	count    int
	anycount int
	follow   bool
	origin   = end
	units    = lines
	rev      bool
	file     *os.File
	bout     = bufio.NewWriter(os.Stdout)
)

func main() {
	args := parse(os.Args[1:])
	if rev && (units == chars || follow || origin == beg) {
		fail("incompatible options")
	}
	if anycount == 0 {
		if rev {
			count = 0x7fffffff
		} else {
			count = 10
		}
	}
	if origin == beg && units == lines && count > 0 {
		count--
	}
	if len(args) > 1 {
		usage()
	}
	seekable := false
	if len(args) == 0 || args[0] == "-" {
		file = os.Stdin
	} else {
		fd, err := sys.Open(args[0], sys.OREAD)
		if err != nil {
			fatal(args[0])
		}
		file = os.NewFile(uintptr(fd), args[0])
		d, err := sys.Fstat(fd)
		off, serr := sys.Seek(fd, 0, 0)
		seekable = err == nil && serr == nil && off == 0 && d.Length > 0
	}
	switch {
	case !seekable && origin == end:
		keep()
	case !seekable && origin == beg:
		skip()
	case units == chars && origin == end:
		tseek(int64(-count), 2)
		copyout()
	case units == chars && origin == beg:
		tseek(int64(count), 0)
		copyout()
	case units == lines && origin == end:
		reverse()
	case units == lines && origin == beg:
		skip()
	}
	if follow {
		length := int64(-1)
		for {
			if seekable {
				// Start again if the file was truncated.
				d, err := sys.Fstat(int(file.Fd()))
				if err == nil {
					l := int64(d.Length)
					if l < length {
						l = tseek(0, 0)
					}
					length = l
				}
			}
			copyout()
			_ = sys.Sleep(5000)
		}
	}
}

// skip reads past the head of the file to find the tail.
func skip() {
	buf := make([]byte, bsize)
	if units == chars {
		for count > 0 {
			n := min(count, bsize)
			n = tread(buf[:n])
			if n == 0 {
				return
			}
			count -= n
		}
	} else {
		i, n := 0, 0
		for count > 0 {
			n = tread(buf)
			if n == 0 {
				return
			}
			for i = 0; i < n && count > 0; i++ {
				if buf[i] == '\n' {
					count--
				}
			}
		}
		twrite(buf[i:n])
	}
	copyout()
}

func copyout() {
	buf := make([]byte, bsize)
	for {
		n := tread(buf)
		if n <= 0 {
			break
		}
		twrite(buf[:n])
	}
	bout.Flush()
}

// keep reads the whole file, keeping its tail.
func keep() {
	var buf []byte
	n := 1
	for n > 0 {
		chunk := make([]byte, 2*bsize)
		for n > 0 && len(chunk) > 0 {
			n = tread(chunk)
			buf = append(buf, chunk[:n]...)
			chunk = chunk[n:]
		}
		if count >= len(buf) {
			continue
		}
		var j int
		if units == chars {
			j = len(buf) - count
		} else {
			j = len(buf)
			if buf[j-1] == '\n' {
				j--
			}
			for k := 0; j > 0; j-- {
				if buf[j-1] == '\n' {
					if k++; k >= count {
						break
					}
				}
			}
		}
		buf = append([]byte(nil), buf[j:]...)
	}
	if rev {
		if len(buf) > 0 && buf[len(buf)-1] != '\n' {
			buf = append(buf, '\n')
		}
		length := len(buf)
		for j := length - 1; j > 0; j-- {
			if buf[j-1] == '\n' {
				twrite(buf[j:length])
				if count--; count <= 0 {
					bout.Flush()
					return
				}
				length = j
			}
		}
		buf = buf[:length]
	}
	if count > 0 && len(buf) > 0 {
		twrite(buf)
	}
	bout.Flush()
}

// reverse counts lines backward from the end of a seekable file.
func reverse() {
	pos := tseek(0, 2)
	var buf []byte // the unprinted part read so far, at the front of the file's tail
	n := 0
	for first := true; pos > 0 && count > 0; first = false {
		n = int(min(pos, bsize))
		pos -= int64(n)
		chunk := make([]byte, n)
		tseek(pos, 0)
		if tread(chunk) != n {
			fatal("length error")
		}
		buf = append(chunk, buf...)
		if first && buf[len(buf)-1] != '\n' {
			buf = append(buf, '\n')
		}
		length := len(buf)
		for n = length - 1; n > 0 && count > 0; n-- {
			if buf[n-1] == '\n' {
				count--
				if rev {
					twrite(buf[n:length])
					bout.Flush()
				}
				length = n
			}
		}
		buf = buf[:length]
	}
	if !rev {
		if n == 0 {
			tseek(0, 0)
		} else {
			tseek(pos+int64(n)+1, 0)
		}
		copyout()
	} else if count > 0 {
		twrite(buf)
	}
	bout.Flush()
}

func tseek(o int64, whence int) int64 {
	o, err := sys.Seek(int(file.Fd()), o, whence)
	if err != nil {
		fatal("")
	}
	return o
}

func tread(buf []byte) int {
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		fatal("")
	}
	return n
}

func twrite(b []byte) {
	if _, err := bout.Write(b); err != nil {
		fatal("")
	}
}

// fatal, fail and usage end tail with no failure status, as Inferno's
// tail does.
func fatal(s string) {
	bout.Flush()
	os.Stderr.WriteString("tail: " + s + ": " + sys.Errstr() + "\n")
	os.Exit(0)
}

func fail(s string) {
	bout.Flush()
	os.Stderr.WriteString("tail: " + s + "\n")
	os.Exit(0)
}

func usage() {
	os.Stderr.WriteString("usage: tail [-n N] [-c N] [-f] [-r] [+-N[bc][fr]] [file]\n")
	os.Exit(0)
}

// getnumber takes s as a count if it is [+-]digits..., setting the
// origin from a leading +.
func getnumber(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '-' || s[0] == '+' {
		if len(s) == 1 {
			return false
		}
		i++
	}
	if s[i] < '0' || s[i] > '9' {
		return false
	}
	if s[0] == '+' {
		origin = beg
	}
	if anycount > 0 {
		fail("excess option")
	}
	anycount++
	s = strings.TrimPrefix(s, "-")
	j := 0
	if j < len(s) && s[j] == '+' {
		j++
	}
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	n, err := strconv.ParseInt(s[:j], 10, 32)
	if err != nil || n < 0 {
		fail("too big")
	}
	count = int(n)
	return true
}

func parse(args []string) []string {
	for ; len(args) > 0; args = args[1:] {
		a := args[0]
		if getnumber(a) {
			suffix(a)
			continue
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		switch a[1] {
		case 'c', 'n':
			if a[1] == 'c' {
				units = chars
			}
			if len(a) > 2 && getnumber(a[2:]) {
			} else if len(args) > 1 && getnumber(args[1]) {
				args = args[1:]
			} else {
				usage()
			}
		case 'r':
			rev = true
		case 'f':
			follow = true
		case '-':
			return args[1:]
		}
	}
	return args
}

func suffix(s string) {
	i := 0
	for i < len(s) && strings.IndexByte("0123456789+-", s[i]) >= 0 {
		i++
	}
	if i == len(s) {
		return
	}
	if s[i] == 'b' {
		if count *= 1024; count < 0 || count > 0x7fffffff {
			fail("too big")
		}
	}
	if s[i] == 'c' || s[i] == 'b' {
		units = chars
	}
	if s[i] == 'l' || s[i] == 'c' || s[i] == 'b' {
		i++
	}
	if i < len(s) {
		switch s[i] {
		case 'r':
			rev = true
			return
		case 'f':
			follow = true
			return
		}
	}
	i++
	if i < len(s) {
		usage()
	}
}
