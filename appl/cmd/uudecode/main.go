// Command uudecode decodes uuencoded files, or standard input, into the
// files their begin lines name, or onto standard output with -p
// (Inferno uudecode(1)).
package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// fatal prints s and exits without a failure status, as Inferno's does.
func fatal(s string) {
	os.Stderr.WriteString(s + "\n")
	sys.Exits("")
}

const lineLen = 45

func main() {
	args := os.Args[1:]
	tostdout := false
	if len(args) > 0 && args[0] == "-p" {
		tostdout = true
		args = args[1:]
	}
	if len(args) == 0 {
		decode(os.Stdin, tostdout)
		return
	}
	for _, a := range args {
		f, err := os.Open(a)
		if err != nil {
			fatal("cannot open " + a)
		}
		decode(f, tostdout)
	}
}

func code(c byte) int { return int(c-' ') & 0x3f }

func decode(in *os.File, tostdout bool) {
	bio := bufio.NewReader(in)
	var ofile string
	mode := 0
	for {
		s, _ := bio.ReadString('\n')
		if s == "" {
			break
		}
		if strings.HasPrefix(s, "begin ") {
			f := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\n' })
			if len(f) < 3 {
				fatal("bad begin line")
			}
			mode = octal(f[1])
			ofile = f[2]
			break
		}
	}
	if ofile == "" {
		fatal("no begin line")
	}
	out := os.Stdout
	if !tostdout {
		if ofile[0] == '~' {
			ofile = "/usr/" + ofile[1:]
		}
		fd, err := sys.Create(ofile, sys.OWRITE, 0o666)
		if err != nil {
			fatal("cannot create " + ofile + ": " + sys.Errstr())
		}
		out = os.NewFile(uintptr(fd), ofile)
	}
	ob := make([]byte, 0, lineLen)
	for {
		s, _ := bio.ReadString('\n')
		if s == "" {
			break
		}
		b := []byte(s)
		n := code(b[0])
		if n == 0 && (len(b) != 2 || b[1] != '\n') {
			fatal("bad 0 count line")
		}
		if n <= 0 {
			break
		}
		if n > lineLen {
			fatal("too many bytes on line")
		}
		e, f := 0, 0
		switch n % 3 {
		case 1:
			e, f = 2, 4
		case 2:
			e, f = 3, 4
		}
		if len(b) < 4*(n/3)+e+2 || len(b) > 4*(n/3)+f+2 {
			fatal("bad uuencode count")
		}
		b = b[1:]
		ob = ob[:0]
		for j, nl := 0, n; nl > 0; j, nl = j+4, nl-3 {
			ob = append(ob, byte(code(b[j])<<2|code(b[j+1])>>4))
			if nl >= 2 {
				ob = append(ob, byte(code(b[j+1])<<4|code(b[j+2])>>2))
			}
			if nl >= 3 {
				ob = append(ob, byte(code(b[j+2])<<6|code(b[j+3])))
			}
		}
		if _, err := out.Write(ob); err != nil {
			fatal("bad write to output: " + sys.Errstr())
		}
	}
	s, _ := bio.ReadString('\n')
	if !strings.HasPrefix(s, "end\n") {
		fatal("missing end line")
	}
	if !tostdout {
		d := sys.NullDir()
		d.Mode = uint32(mode)
		if sys.Fwstat(int(out.Fd()), &d) != nil {
			fatal("cannot wstat " + ofile + ": " + sys.Errstr())
		}
	}
}

// octal reads the leading octal digits of s, as String's toint does.
func octal(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '7' {
		i++
	}
	n, _ := strconv.ParseInt(s[:i], 8, 64)
	return int(n)
}
