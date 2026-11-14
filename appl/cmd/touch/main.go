// Command touch sets files' modification times, creating them if needed
// (Inferno touch(1)).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// OEXCL is Inferno's exclusive-create open mode bit.
const oexcl = 0x1000

func main() {
	a := arg.New(os.Args)
	a.SetUsage("touch [-c] [-t time] file ...")
	force := true
	now := daytime.Now()
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 't':
			now = atoi(a.EArg())
		case 'c':
			force = false
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	status := 0
	for _, name := range args {
		status += touch(force, name, now)
	}
	if status != 0 {
		sys.Exits("fail:touch")
	}
}

func touch(force bool, name string, now int) int {
	d := sys.NullDir()
	d.Mtime = uint32(now)
	if _, err := sys.Stat(name); err == nil {
		if sys.Wstat(name, &d) == nil {
			return 0
		}
		force = false // it's there; we just can't wstat it
	}
	if !force {
		os.Stderr.WriteString("touch: " + name + ": cannot change time: " + sys.Errstr() + "\n")
		return 1
	}
	fd, err := sys.Create(name, sys.OREAD|oexcl, 0o666)
	if err != nil {
		os.Stderr.WriteString("touch: " + name + ": cannot create: " + sys.Errstr() + "\n")
		return 1
	}
	_ = sys.Fwstat(fd, &d)
	os.NewFile(uintptr(fd), name).Close()
	return 0
}

// atoi converts as Limbo's int of a string does: leading blanks, a sign
// and decimal digits; anything else ends the number.
func atoi(s string) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	n := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}
