// Command mkdir makes directories (Inferno mkdir(1)).
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		return
	}
	pflag := false
	if args[0] == "-p" {
		pflag = true
		args = args[1:]
	}
	failed := false
	for _, dir := range args {
		if pflag {
			if mkpath(dir) < 0 {
				failed = true
			}
			continue
		}
		if _, err := sys.Stat(dir); err != nil {
			if mkdir(dir) < 0 {
				failed = true
			}
		} else {
			os.Stderr.WriteString("mkdir: " + dir + " already exists\n")
			failed = true
		}
	}
	if failed {
		sys.Exits("fail:error")
	}
}

// mkpath makes dir and any missing parents.
func mkpath(dir string) int {
	s := ""
	if dir != "" && dir[0] != '/' {
		s = "."
	}
	for _, f := range strings.Split(dir, "/") {
		if f == "" {
			continue
		}
		s += "/" + f
		d, err := sys.Stat(s)
		if err != nil {
			if mkdir(s) < 0 {
				return -1
			}
		} else if d.Mode&sys.DMDIR == 0 {
			os.Stderr.WriteString("mkdir: can't create " + dir + ": " + s + " not a directory\n")
			return -1
		}
	}
	return 0
}

func mkdir(dir string) int {
	fd, err := sys.Create(dir, sys.OREAD, sys.DMDIR|0o777)
	if err != nil {
		os.Stderr.WriteString("mkdir: can't create " + dir + ": " + sys.Errstr() + "\n")
		return -1
	}
	closefd(fd)
	return 0
}

func closefd(fd int) { os.NewFile(uintptr(fd), "").Close() }
