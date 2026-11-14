// Command ns prints the construction of a prog's name space as bind and
// mount commands (Inferno ns(1)). Without -r, a mount of a network data
// file is shown as its network address.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("usage: ns [-r] [pid]\n")
	sys.Exits("fail:usage")
}

func main() {
	a := arg.New(os.Args)
	pid, _ := sys.Pctl(0, nil)
	raw := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'r':
			raw = true
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) > 1 {
		usage()
	}
	if len(args) == 1 {
		pid = atoi(args[0])
	}
	name := fmt.Sprintf("/prog/%d/ns", pid)
	f, err := os.Open(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ns: can't open %s: %s\n", name, sys.Errstr())
		sys.Exits("fail:open")
	}
	r := bufio.NewReader(f)
	for {
		l, err := r.ReadString('\n')
		if l != "" {
			line(l, raw)
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "ns: error reading %s: %s\n", name, sys.Errstr())
			}
			break
		}
	}
}

// atoi is Limbo's int of a string: leading digits, else 0.
func atoi(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}

func line(l string, raw bool) {
	f := strings.FieldsFunc(l, func(r rune) bool { return r == ' ' || r == '\n' })
	if len(f) < 2 {
		return
	}
	cmd, f := f[0], f[1:]
	if cmd == "cd" {
		fmt.Printf("%s %s\n", cmd, quoted(f[0]))
		return
	}
	flag := ""
	if f[0][0] == '-' {
		flag, f = f[0]+" ", f[1:]
	}
	if len(f) < 2 {
		return
	}
	src, dest := f[0], f[1]
	// #/ and #U are the root anyway.
	if len(src) >= 3 && (src[:2] == "#/" || src[:2] == "#U") {
		src = src[2:]
	}
	if dest == "#M" {
		dest = "/"
	}
	if cmd == "mount" && !raw {
		src = netaddr(src)
	}
	fmt.Printf("%s %s%s %s\n", cmd, flag, quoted(src), quoted(dest))
}

// netaddr turns /net/proto/n/data into proto!remote-address.
func netaddr(f string) string {
	if f == "" || f[0] != '/' {
		return f
	}
	fl := strings.FieldsFunc(f, func(r rune) bool { return r == '/' })
	if len(fl) < 4 {
		return f
	}
	netdir, proto, d := fl[0], fl[1], fl[2]
	if netdir != "net" && netdir != "net.alt" || fl[3] != "data" {
		return f
	}
	rf, err := os.Open("/" + netdir + "/" + proto + "/" + d + "/remote")
	if err != nil {
		return f
	}
	defer rf.Close()
	buf := make([]byte, 256)
	n, _ := rf.Read(buf)
	if n <= 0 {
		return f
	}
	if buf[n-1] == '\n' {
		n--
	}
	if netdir != "net" {
		proto = "/" + netdir + "/" + proto
	}
	return proto + "!" + string(buf[:n])
}

// quoted quotes s for the shell if it has any of its metacharacters.
func quoted(s string) string {
	if !strings.ContainsAny(s, "' \t\\$#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
