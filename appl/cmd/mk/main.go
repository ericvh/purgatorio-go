// Command mk maintains related files from the dependency rules in a
// mkfile (Inferno mk(1)). Recipes run as separate progs under /bin/sh,
// with the recipe on standard input, as Inferno's mk runs them.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	mkfile   = "mkfile"
	nregexp  = 10
	errlen   = 64
	shell    = "/bin/sh.wasm"
	shname0  = "sh"
	waitlen  = 512
	bufsize  = 8192
	cmdlargs = "command line arguments"
)

// Debug flags.
const (
	dParse = 1 << iota
	dGraph
	dExec
)

var (
	debug    int
	nflag    bool
	tflag    bool
	iflag    bool
	kflag    bool
	aflag    bool
	uflag    bool
	explain  bool
	target1  []string
	nreps    = 1
	bout     *bufio.Writer
	mypid    int
	runerrs  int
	patrule  *Rule
	infile   string
	mkinline int
)

func main() {
	bout = bufio.NewWriterSize(os.Stdout, bufsize)
	mypid, _ = sys.Pctl(sys.FORKNS, nil)
	argv := os.Args[1:]
	var flags []string
	var files []string
	whatif := ""
	havewhatif := false
	for len(argv) > 0 && strings.HasPrefix(argv[0], "-") {
		a := argv[0]
		flags = append(flags, a)
		c := byte(0)
		if len(a) > 1 {
			c = a[1]
		}
		switch c {
		case 'a':
			aflag = true
		case 'd':
			if len(a) > 2 {
				for _, d := range a[2:] {
					switch d {
					case 'p':
						debug |= dParse
					case 'g':
						debug |= dGraph
					case 'e':
						debug |= dExec
					}
				}
			} else {
				debug = 0xffff
			}
		case 'e':
			explain = true
		case 'f':
			argv = argv[1:]
			if len(argv) == 0 {
				badusage()
			}
			files = append(files, argv[0])
			flags = append(flags, argv[0])
		case 'i':
			iflag = true
		case 'k':
			kflag = true
		case 'n':
			nflag = true
		case 's':
			// Targets are always made in sequence.
		case 't':
			tflag = true
		case 'u':
			uflag = true
		case 'w':
			if havewhatif {
				whatif += " "
			}
			havewhatif = true
			if len(a) > 2 {
				whatif += a[2:]
			} else {
				argv = argv[1:]
				if len(argv) == 0 {
					badusage()
				}
				whatif += argv[0]
			}
		default:
			badusage()
		}
		argv = argv[1:]
	}
	if aflag {
		iflag = true
	}
	usage()
	initenv()
	initbind()
	openwait()
	usage()

	// Assignment arguments are parsed as a mkfile that overrides the
	// first assignment to each variable; they become empty arguments.
	var assigns strings.Builder
	targs := make([]string, len(argv))
	copy(targs, argv)
	for i, a := range targs {
		if strings.Contains(a, "=") {
			flags = append(flags, a)
			assigns.WriteString(a + "\n")
			targs[i] = ""
		}
	}
	if assigns.Len() > 0 {
		parse("command line args", []byte(assigns.String()), true)
	}
	vars["MKFLAGS"] = stow(strings.Join(flags, " "))
	var args []string
	for _, a := range targs {
		if a != "" {
			args = append(args, a)
		}
	}
	vars["MKARGS"] = stow(strings.Join(args, " "))
	if len(files) == 0 {
		if b, err := readfile(mkfile); err == nil {
			parse(mkfile, b, false)
		}
	} else {
		for _, f := range files {
			b, err := readfile(f)
			if err != nil {
				perror(f)
				exit()
			}
			parse(f, b, false)
		}
	}
	if debug&dParse != 0 {
		dumpw("default targets", target1)
		dumpr("rules", rules)
		dumpr("metarules", metarules)
		dumpv("variables")
	}
	if havewhatif {
		timeinit(whatif)
	}
	execinit()
	if len(args) == 0 {
		if target1 == nil {
			fmt.Fprintf(os.Stderr, "mk: nothing to mk\n")
			exit()
		}
		for _, t := range target1 {
			mk(t)
		}
	} else {
		for _, t := range args {
			mk(t)
		}
	}
	if uflag {
		prusage()
	}
	bout.Flush()
}

func badusage() {
	fmt.Fprintf(os.Stderr, "Usage: mk [-f file] [-n] [-a] [-e] [-t] [-k] [-i] [-d[egp]] [targets ...]\n")
	exit()
}

func assert(s string, ok bool) {
	if !ok {
		fmt.Fprintf(os.Stderr, "mk: Assertion ``%s'' failed.\n", s)
		exit()
	}
}

func perror(s string) { fmt.Fprintf(os.Stderr, "mk: %s: %s\n", s, sys.Errstr()) }

func syntax(line int) {
	fmt.Fprintf(os.Stderr, "mk: %s:%d: syntax error; ", infile, line)
}

func regerror(s string) {
	if patrule != nil {
		fmt.Fprintf(os.Stderr, "mk: %s:%d: regular expression error; %s\n", patrule.file, patrule.line, s)
	} else {
		fmt.Fprintf(os.Stderr, "mk: %s:%d: regular expression error; %s\n", infile, mkinline, s)
	}
	exit()
}

// exit waits for the recipes still running and fails, as mk's Exit.
func exit() {
	for {
		if pid, _ := wait(); pid < 0 {
			break
		}
	}
	bout.Flush()
	sys.Exits("fail:error")
}

func access(name string) bool {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return false
	}
	syscall.Close(fd)
	return true
}

func readfd(fd int) []byte {
	var out []byte
	buf := make([]byte, 8192)
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil || n <= 0 {
			break
		}
		out = append(out, buf[:n]...)
	}
	return out
}

func readfile(name string) ([]byte, error) {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return nil, err
	}
	b := readfd(fd)
	syscall.Close(fd)
	return b, nil
}

func writefd(fd int, b []byte) bool {
	for len(b) > 0 {
		n, err := syscall.Write(fd, b)
		if err != nil || n <= 0 {
			return false
		}
		b = b[n:]
	}
	return true
}

func getuser() string {
	b, err := readfile("/dev/user")
	if err != nil {
		return ""
	}
	return string(b)
}

// initbind applies the binds listed in the user's mkbinds or
// /lib/mk/binds.
func initbind() {
	b, err := readfile("/usr/" + getuser() + "/lib/mkbinds")
	if err != nil {
		b, err = readfile("/lib/mk/binds")
	}
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.FieldsFunc(l, func(r rune) bool { return r == ' ' || r == '\t' })
		if len(f) == 2 {
			_, _ = sys.Bind(f[0], f[1], sys.MREPL)
		}
	}
}

// now is daytime->now().
func now() int { return daytime.Now() }

// Usage accounting for -u: seconds spent with each number of recipes
// running.
var (
	tslot [1000]int
	tick  int
)

func usage() {
	t := now()
	if tick != 0 {
		tslot[nrunning] += t - tick
	}
	tick = t
}

func prusage() {
	usage()
	bout.Flush()
	for i := 0; i <= nevents; i++ {
		fmt.Fprintf(os.Stdout, "%d: %d\n", i, tslot[i])
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// atoi is Limbo's int of a string: leading digits, else 0.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}
