// Command window makes a rio window, after Plan 9's window(1).
//
//	window [-m] [-r minx miny maxx maxy] [-dx n] [-dy n] [-minx n] [-miny n]
//		[-hide] [-scroll|-noscroll] [-cd dir] [cmd [arg ...]]
//
// The options are rio's wctl options for new (rio(4)): -r gives the
// rectangle, -minx and -miny its origin and -dx and -dy its size, each
// relative to a default rectangle when signed. -hide makes the window
// hidden, -scroll and -noscroll set whether output scrolls it, and -cd
// gives the directory the command runs in, the current one by default.
// The command is the arguments joined with spaces, run by sh -c; with
// none, the window runs sh -i.
//
// Without -m, window writes a new request to /dev/wctl, and rio runs
// the command in a name space of its own, copied from rio's. Outside a
// rio window, where there is no /dev/wctl, it mounts rio's service
// file on /mnt/wsys and writes the request to its wctl.
//
// With -m, window mounts rio's service file on /mnt/wsys with the
// attach name new and the options, which makes the window, binds
// /mnt/wsys before /dev and runs the command there, in window's own
// name space; window does not wait for it. The window goes when the
// command and everything sharing its name space have finished.
//
// rio's service file is $wsys, which rio sets for its windows; when
// that is not set, it is the first /chan/rio.* file.
package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const mnt = "/mnt/wsys"

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "window: "+f+"\n", a...)
	sys.Exits("fail:window")
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: window [-m] [-r minx miny maxx maxy] [-dx n] [-dy n] [-minx n] [-miny n] "+
		"[-hide] [-scroll|-noscroll] [-cd dir] [cmd [arg ...]]")
	sys.Exits("fail:usage")
}

func main() {
	args := os.Args[1:]
	mflag := false
	var opts []string
	dir := ""
	need := func(n int) []string {
		if len(args) < n+1 {
			usage()
		}
		v := args[1 : n+1]
		args = args[n+1:]
		return v
	}
	isnum := func(s string) bool {
		s = strings.TrimLeft(s, "+-")
		return s != "" && strings.Trim(s, "0123456789") == ""
	}
loop:
	for len(args) > 0 {
		switch a := args[0]; a {
		case "-m":
			mflag = true
			args = args[1:]
		case "-r", "-dx", "-dy", "-minx", "-miny":
			n := 1
			if a == "-r" {
				n = 4
			}
			v := need(n)
			for _, s := range v {
				if !isnum(s) {
					usage()
				}
			}
			opts = append(opts, a)
			opts = append(opts, v...)
		case "-hide", "-scroll", "-noscroll":
			opts = append(opts, a)
			args = args[1:]
		case "-cd":
			dir = need(1)[0]
		case "--":
			args = args[1:]
			break loop
		default:
			if strings.HasPrefix(a, "-") {
				usage()
			}
			break loop
		}
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if strings.ContainsAny(dir, " \t\n") {
		fatal("cannot use directory %q", dir)
	}
	cmd := strings.Join(args, " ")
	if mflag {
		mounted(opts, dir, cmd)
		return
	}
	req := "new " + strings.Join(append(opts, "-cd", dir), " ")
	if cmd != "" {
		req += " " + cmd
	}
	fd, err := sys.Open("/dev/wctl", sys.OWRITE)
	if err != nil {
		// Not in a rio window: rio's service file has a wctl too.
		_, _ = sys.Pctl(sys.FORKNS, nil)
		mount("")
		if fd, err = sys.Open(mnt+"/wctl", sys.OWRITE); err != nil {
			fatal("%s/wctl: %v", mnt, err)
		}
	}
	if _, err := syscall.Write(fd, []byte(req)); err != nil {
		fatal("cannot make window: %s", sys.Errstr())
	}
	syscall.Close(fd)
}

// service is the name of rio's service file.
func service() string {
	if b, err := os.ReadFile("/env/wsys"); err == nil && len(b) > 0 {
		return strings.TrimRight(string(b), "\x00")
	}
	ents, _ := os.ReadDir("/chan")
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "rio.") {
			return "/chan/" + e.Name()
		}
	}
	fatal("no rio: $wsys is not set and /chan has no rio service file")
	return ""
}

// mount mounts rio's service file on /mnt/wsys with attach name spec.
func mount(spec string) {
	srv := service()
	fd, err := sys.Open(srv, sys.ORDWR)
	if err != nil {
		fatal("%s: %v", srv, err)
	}
	_ = os.MkdirAll(mnt, 0o755)
	if _, err := sys.Mount(fd, -1, mnt, sys.MREPL, spec); err != nil {
		fatal("cannot mount %s: %v", srv, err)
	}
	syscall.Close(fd)
}

// mounted is window -m: the window is made by the mount, and the
// command runs in this name space with the window's files before /dev.
func mounted(opts []string, dir, cmd string) {
	if _, err := sys.Pctl(sys.FORKNS|sys.FORKENV|sys.NEWPGRP, nil); err != nil {
		fatal("pctl: %v", err)
	}
	mount(strings.Join(append([]string{"new"}, opts...), " "))
	if _, err := sys.Bind(mnt, "/dev", sys.MBEFORE); err != nil {
		fatal("cannot bind %s: %v", mnt, err)
	}
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintf(os.Stderr, "window: cd %s: %v\n", dir, err)
	}
	cons, err := sys.Open("/dev/cons", sys.ORDWR)
	if err != nil {
		fatal("/dev/cons: %v", err)
	}
	argv := []string{"sh", "-i"}
	if cmd != "" {
		argv = []string{"sh", "-c", cmd}
	}
	if _, err := sys.Spawn("/bin/sh.wasm", argv, sys.NEWFD, [][2]int{{0, cons}, {1, cons}, {2, cons}}); err != nil {
		fatal("cannot run sh: %v", err)
	}
	syscall.Close(cons)
}
