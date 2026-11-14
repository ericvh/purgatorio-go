// Command bench times the benchmarks of docs/perf.md inside the system,
// so neither the emu's nor the kernel's start is counted. Each prints a
// line "bench <name> <seconds>".
//
//	bench run name cmd [arg ...]   run cmd with standard output on /dev/null
//	bench fs name dir n            create, read, stat and remove n files of 64 bytes
//	bench spawn name n             spawn echo -n n times, waiting for each
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var waitf *os.File

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "bench: "+f+"\n", a...)
	sys.Exits("fail")
}

func report(name string, d time.Duration) {
	fmt.Printf("bench %s %.6f\n", name, d.Seconds())
}

// spawn starts the command with fds 0 and 2, and 1 as out.
func spawn(out int, args ...string) int {
	path := args[0]
	if !strings.HasPrefix(path, "/") {
		path = "/bin/" + path + ".wasm"
	}
	pid, err := sys.Spawn(path, args, sys.NEWFD, [][2]int{{0, 0}, {1, out}, {2, 2}})
	if err != nil {
		fail("spawn %s: %s", args[0], sys.Errstr())
	}
	return pid
}

// await waits for child pid and fails if it failed.
func await(pid int) {
	buf := make([]byte, 256)
	for {
		n, err := waitf.Read(buf)
		if err != nil || n == 0 {
			fail("wait: %s", sys.Errstr())
		}
		p, rest, _ := strings.Cut(string(buf[:n]), " ")
		if p != strconv.Itoa(pid) {
			continue
		}
		if _, st, _ := strings.Cut(rest, "\":"); st != "" {
			fail("%s", rest)
		}
		return
	}
}

func fsbench(name, dir string, n int) {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		fail("%v", err)
	}
	data := []byte(strings.Repeat("0123456789abcdef", 4))
	path := func(i int) string { return dir + "/f" + strconv.Itoa(i) }
	t := time.Now()
	for i := range n {
		if err := os.WriteFile(path(i), data, 0o666); err != nil {
			fail("create: %v", err)
		}
	}
	report(name+"-create", time.Since(t))
	t = time.Now()
	for i := range n {
		if b, err := os.ReadFile(path(i)); err != nil || len(b) != len(data) {
			fail("read: %v", err)
		}
	}
	report(name+"-read", time.Since(t))
	t = time.Now()
	for i := range n {
		if _, err := sys.Stat(path(i)); err != nil {
			fail("stat: %v", err)
		}
	}
	report(name+"-stat", time.Since(t))
	t = time.Now()
	for i := range n {
		if err := sys.Remove(path(i)); err != nil {
			fail("remove: %v", err)
		}
	}
	report(name+"-remove", time.Since(t))
}

func main() {
	if len(os.Args) < 3 {
		fail("usage: bench run|fs|spawn name ...")
	}
	me, _ := sys.Pctl(0, nil)
	var err error
	if waitf, err = os.Open("/prog/" + strconv.Itoa(me) + "/wait"); err != nil {
		fail("open wait: %s", sys.Errstr())
	}
	what, name, args := os.Args[1], os.Args[2], os.Args[3:]
	switch what {
	case "run":
		null, err := sys.Open("/dev/null", sys.OWRITE)
		if err != nil || len(args) == 0 {
			fail("run: %s", sys.Errstr())
		}
		t := time.Now()
		await(spawn(null, args...))
		report(name, time.Since(t))
	case "fs":
		if len(args) != 2 {
			fail("usage: bench fs name dir n")
		}
		n, _ := strconv.Atoi(args[1])
		fsbench(name, args[0], n)
	case "spawn":
		n, _ := strconv.Atoi(args[0])
		null, _ := sys.Open("/dev/null", sys.OWRITE)
		t := time.Now()
		for range n {
			await(spawn(null, "echo", "-n"))
		}
		d := time.Since(t)
		report(name+"-total", d)
	default:
		fail("unknown benchmark kind %s", what)
	}
}
