// Command test evaluates a conditional expression and succeeds when it
// is true (Inferno test(1)): file tests, string and integer comparisons,
// file age, and ! -a -o with parentheses.
package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const dmtmp = 0x04000000

var args []string

func main() {
	args = os.Args[1:]
	if len(args) == 0 {
		sys.Exits("fail:usage")
	}
	if !e() {
		sys.Exits("fail:false")
	}
}

// nextarg returns the next argument. At the end it is a syntax error
// unless mayEnd is set, when it returns "" (Limbo's nil string).
func nextarg(mayEnd bool) string {
	if len(args) == 0 {
		if mayEnd {
			return ""
		}
		synbad("argument expected")
	}
	s := args[0]
	args = args[1:]
	return s
}

func nextint() (int, bool) {
	if len(args) > 0 && isint(args[0]) {
		return atoi(nextarg(false)), true
	}
	return 0, false
}

func isnext(s string) bool {
	if len(args) > 0 && args[0] == s {
		args = args[1:]
		return true
	}
	return false
}

func e() bool {
	p := e1()
	if isnext("-o") {
		return p || e()
	}
	return p
}

func e1() bool {
	p := e2()
	if isnext("-a") {
		return p && e1()
	}
	return p
}

func e2() bool {
	if isnext("!") {
		return !e2()
	}
	return e3()
}

func e3() bool {
	a := nextarg(false)
	switch a {
	case "(":
		p := e()
		if nextarg(false) != ")" {
			synbad(") expected")
		}
		return p
	case "-A":
		return hasmode(nextarg(false), sys.DMAPPEND)
	case "-L":
		return hasmode(nextarg(false), sys.DMEXCL)
	case "-T":
		return hasmode(nextarg(false), dmtmp)
	case "-f":
		f := nextarg(false)
		return exists(f) && !hasmode(f, sys.DMDIR)
	case "-d":
		return hasmode(nextarg(false), sys.DMDIR)
	case "-r":
		return canopen(nextarg(false), sys.OREAD)
	case "-w":
		return canopen(nextarg(false), sys.OWRITE)
	case "-x":
		fd, err := sys.Open(nextarg(false), sys.OREAD)
		if err != nil {
			return false
		}
		d, err := sys.Fstat(fd)
		closefd(fd)
		return err == nil && d.Mode&0o111 != 0
	case "-e":
		return exists(nextarg(false))
	case "-s":
		d, err := sys.Stat(nextarg(false))
		return err == nil && d.Length > 0
	case "-t":
		if fd, ok := nextint(); ok {
			return iscons(fd)
		}
		return iscons(1)
	case "-n":
		return nextarg(false) != ""
	case "-z":
		return nextarg(false) == ""
	}
	p2 := nextarg(true)
	if p2 == "" {
		return a != ""
	}
	switch p2 {
	case "=":
		return nextarg(false) == a
	case "!=":
		return nextarg(false) != a
	case "-older":
		return isolder(nextarg(false), a)
	case "-ot":
		return cmptime(a, nextarg(false), func(x, y uint32) bool { return x < y })
	case "-nt":
		return cmptime(a, nextarg(false), func(x, y uint32) bool { return x > y })
	}
	if !isint(a) {
		return a != ""
	}
	i1 := atoi(a)
	if i2, ok := nextint(); ok {
		switch p2 {
		case "-eq":
			return i1 == i2
		case "-ne":
			return i1 != i2
		case "-gt":
			return i1 > i2
		case "-lt":
			return i1 < i2
		case "-ge":
			return i1 >= i2
		case "-le":
			return i1 <= i2
		}
	}
	synbad("unknown operator " + p2)
	return false
}

func synbad(s string) {
	os.Stderr.WriteString("test: bad syntax: " + s + "\n")
	sys.Exits("fail:bad syntax")
}

func isint(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// atoi converts a string of digits as Limbo's int does, wrapping at 32
// bits.
func atoi(s string) int {
	n, _ := strconv.ParseUint(s, 10, 64)
	return int(int32(n))
}

func exists(f string) bool { _, err := sys.Stat(f); return err == nil }

func hasmode(f string, m uint32) bool {
	d, err := sys.Stat(f)
	return err == nil && d.Mode&m != 0
}

func canopen(f string, mode int) bool {
	fd, err := sys.Open(f, mode)
	if err != nil {
		return false
	}
	closefd(fd)
	return true
}

func closefd(fd int) { _ = os.NewFile(uintptr(fd), "").Close() }

func iscons(fd int) bool {
	s, err := sys.Fd2path(fd)
	return err == nil && (s == "#c/cons" || strings.HasSuffix(s, "/dev/cons"))
}

// isolder reports whether file f was last modified longer ago than t, a
// count of seconds or a sequence of numbers each followed by y, M, d, h,
// m or s. As in Inferno, a trailing bare number replaces the sum so far.
func isolder(t, f string) bool {
	d, err := sys.Stat(f)
	if err != nil {
		return false
	}
	n := 0
	for i := 0; i < len(t); {
		j := i
		for j < len(t) && t[j] >= '0' && t[j] <= '9' {
			j++
		}
		if i == j {
			synbad("bad time syntax, " + t)
		}
		m := atoi(t[i:j])
		i = j
		if i == len(t) {
			n = m
			break
		}
		unit := t[i]
		i++
		switch unit {
		case 'y':
			n += m * 12 * 30 * 24 * 3600
		case 'M':
			n += m * 30 * 24 * 3600
		case 'd':
			n += m * 24 * 3600
		case 'h':
			n += m * 3600
		case 'm':
			n += m * 60
		case 's':
			n += m
		default:
			synbad("bad time syntax, " + t)
		}
	}
	return int(d.Mtime)+n < daytime.Now()
}

func cmptime(a, b string, f func(x, y uint32) bool) bool {
	da, err := sys.Stat(a)
	if err != nil {
		return false
	}
	db, err := sys.Stat(b)
	if err != nil {
		return false
	}
	return f(da.Mtime, db.Mtime)
}
