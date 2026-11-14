// Command grep searches files for lines that match a regular expression
// (Inferno grep(1)). Expressions are Inferno's, regexp(6), as package
// regex implements them.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/regex"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	multi                                    bool
	lflag, nflag, vflag, iflag, Lflag, sflag bool
	out                                      = bufio.NewWriter(os.Stdout)
)

func usage() {
	os.Stderr.WriteString("usage: grep [-lnviLs] pattern [file...]\n")
	sys.Exits("fail:usage")
}

func main() {
	a := arg.New(os.Args)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'l':
			lflag = true
		case 'n':
			nflag = true
		case 'v':
			vflag = true
		case 'i':
			iflag = true
		case 'L':
			Lflag = true
		case 's':
			sflag = true
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		usage()
	}
	pattern := args[0]
	args = args[1:]
	if iflag {
		pattern = tolower(pattern)
	}
	re, diag := regex.Compile(pattern, 0)
	if re == nil {
		os.Stderr.WriteString("grep: " + diag + "\n")
		sys.Exits("fail:bad regex")
	}
	matched := false
	if len(args) == 0 {
		matched = grep(re, os.Stdin, "stdin")
	} else {
		multi = len(args) > 1
		for _, name := range args {
			fd, err := sys.Open(name, sys.OREAD)
			if err != nil {
				out.Flush()
				os.Stderr.WriteString("grep: cannot open " + name + ": " + sys.Errstr() + "\n")
				continue
			}
			f := os.NewFile(uintptr(fd), name)
			if grep(re, f, name) {
				matched = true
			}
			f.Close()
		}
	}
	out.Flush()
	if !matched {
		sys.Exits("fail:no matches")
	}
}

func grep(re *regex.Re, f io.Reader, file string) bool {
	matched := false
	b := bufio.NewReader(f)
	for line := 1; ; line++ {
		t, _ := b.ReadString('\n')
		if t == "" {
			break
		}
		s := t
		if iflag {
			s = tolower(s)
		}
		// As in Inferno, the last character of the line (its newline)
		// is outside the range searched.
		r := []rune(s)
		if (re.ExecuteSE(r, 0, len(r)-1, true, true) != nil) != vflag {
			matched = true
			if lflag || sflag {
				if !sflag {
					fmt.Fprintf(out, "%s\n", file)
				}
				return matched
			}
			if !Lflag {
				switch {
				case nflag && multi:
					fmt.Fprintf(out, "%s:%d: %s", file, line, t)
				case nflag:
					fmt.Fprintf(out, "%d:%s", line, t)
				case multi:
					fmt.Fprintf(out, "%s: %s", file, t)
				default:
					out.WriteString(t)
				}
			}
		}
	}
	if Lflag && !matched && !sflag {
		fmt.Fprintf(out, "%s\n", file)
	}
	return matched
}

// tolower lowers ASCII letters only, as Inferno's grep does.
func tolower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c - 'A' + 'a'
		}
	}
	return string(b)
}
