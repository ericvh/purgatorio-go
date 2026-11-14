// Command p pages its input (Inferno p(1)): it prints a screenful of
// lines, then waits for a line on /dev/cons. An empty line shows the next
// page, q or end of file quits, and !cmd runs cmd with the shell.
package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	nlines   = 22
	progname = "p"
	out      = bufio.NewWriter(os.Stdout)
	cons     *bufio.Reader
	consfd   int
)

func main() {
	sys.Pctl(sys.FORKFD, nil)
	fd, err := sys.Open("/dev/cons", sys.OREAD)
	if err != nil {
		os.Stderr.WriteString("p: can't open /dev/cons: " + sys.Errstr() + "\n")
		sys.Exits("fail:cons")
	}
	consfd = fd
	cons = bufio.NewReader(os.NewFile(uintptr(fd), "/dev/cons"))
	argv := os.Args
	if len(argv) > 0 {
		progname, argv = argv[0], argv[1:]
		if len(argv) > 0 {
			if s := argv[0]; len(s) > 1 && s[0] == '-' {
				n, err := strconv.Atoi(s[1:])
				if err != nil || n <= 0 || s[1] == '+' || s[1] == '-' {
					os.Stderr.WriteString("Usage: p [-number] [file...]\n")
					sys.Exits("fail:usage")
				}
				nlines = n
				argv = argv[1:]
			}
		}
	}
	if len(argv) == 0 {
		argv = []string{"-"}
	}
	for _, file := range argv {
		f := os.Stdin
		if file == "-" {
			file = "stdin"
		} else {
			var err error
			if f, err = os.Open(file); err != nil {
				os.Stderr.WriteString(progname + ": can't open " + file + ": " + sys.Errstr() + "\n")
				continue
			}
		}
		page(f)
		if f != os.Stdin {
			f.Close()
		}
	}
}

func page(f *os.File) {
	in := bufio.NewReader(f)
	nl := nlines
	for {
		line, err := in.ReadString('\n')
		out.WriteString(line)
		if line != "" {
			if nl--; nl == 0 {
				out.Flush()
				nl = nlines
				pause()
			}
		}
		if err != nil {
			break
		}
	}
	out.Flush()
}

func pause() {
	for {
		line, _ := cons.ReadString('\n')
		switch {
		case line == "" || line[0] == 'q':
			out.Flush()
			sys.Exits("")
		case line[0] == '!':
			command(strings.TrimSuffix(line[1:], "\n"))
		default:
			return
		}
	}
}

// command runs cmdline with sh -c, with /dev/cons as its standard input,
// and waits for it.
func command(cmdline string) {
	const sh = "/bin/sh.wasm"
	me, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(me)+"/wait", sys.OREAD)
	if err != nil {
		os.Stderr.WriteString(progname + ": can't open wait file: " + sys.Errstr() + "\n")
		return
	}
	wait := os.NewFile(uintptr(wfd), "wait")
	defer wait.Close()
	pid, err := sys.Spawn(sh, []string{"sh", "-c", cmdline}, sys.NEWFD, [][2]int{{0, consfd}, {1, 1}, {2, 2}})
	if err != nil {
		os.Stderr.WriteString(progname + ": can't load " + sh + ": " + sys.Errstr() + "\n")
		return
	}
	buf := make([]byte, 256)
	for {
		n, err := wait.Read(buf)
		if err != nil || n == 0 {
			return
		}
		s := string(buf[:n])
		i := strings.IndexByte(s, ' ')
		if i > 0 && s[:i] == strconv.Itoa(pid) {
			return
		}
	}
}
