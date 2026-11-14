// Command date prints the date (Inferno date(1)): local time, GMT with
// -u, or seconds since the epoch with -n; a numeric argument is the time
// to print instead of now.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("usage: date [-un] [seconds]\n")
	sys.Exits("fail:usage")
}

func numeric(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func main() {
	a := arg.New(os.Args)
	nflag, uflag := false, false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'n':
			nflag = true
		case 'u':
			uflag = true
		default:
			usage()
		}
	}
	args := a.Argv()
	if len(args) > 1 || len(args) == 1 && !numeric(args[0]) {
		usage()
	}
	var now int
	if len(args) == 1 {
		n, _ := strconv.ParseInt(args[0], 10, 32)
		now = int(n)
	} else {
		now = daytime.Now()
	}
	switch {
	case nflag:
		fmt.Printf("%d\n", now)
	case uflag:
		fmt.Printf("%s\n", daytime.Text(daytime.Gmt(now)))
	default:
		fmt.Printf("%s\n", daytime.Text(daytime.Local(now)))
	}
}
