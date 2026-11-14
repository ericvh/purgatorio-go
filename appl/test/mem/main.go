// Command mem checks the memory limit of docs/abi.md: it limits itself
// to 2 MiB more than it holds, checks the limit cannot be raised and that
// /prog/<pid>/mem shows it, then grows its memory within the limit and
// past it, where memory.grow returns -1, and loads a module that does
// not fit. With the argument hog it then
// allocates past the limit, which ends it with the Go runtime's out of
// memory error.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func fail(f string, a ...any) {
	fmt.Printf(f+"\n", a...)
	sys.Exits("fail")
}

func mem(pid int) (uint64, string) {
	b, err := os.ReadFile(fmt.Sprintf("/prog/%d/mem", pid))
	if err != nil {
		fail("read mem: %v", err)
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		fail("mem reads %q", b)
	}
	held, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		fail("mem reads %q", b)
	}
	return held, f[1]
}

// ctl writes msg to the prog's ctl and returns the error string, if any.
func ctl(pid int, msg string) string {
	f, err := os.OpenFile(fmt.Sprintf("/prog/%d/ctl", pid), os.O_WRONLY, 0)
	if err != nil {
		return sys.Errstr()
	}
	defer f.Close()
	if _, err := f.WriteString(msg); err != nil {
		return sys.Errstr()
	}
	return ""
}

func main() {
	pid, _ := sys.Pctl(0, nil)
	held, limit := mem(pid)
	fmt.Println("limit", limit)
	max := held + 2<<20
	if err := ctl(pid, fmt.Sprintf("memlimit %d", max)); err != "" {
		fail("memlimit: %s", err)
	}
	if err := ctl(pid, "memlimit 1g"); err != "permission denied" {
		fail("raising the limit: %q", err)
	}
	if _, limit = mem(pid); limit != strconv.FormatUint(max, 10) {
		fail("limit %s, want %d", limit, max)
	}
	fmt.Println("limit set")
	if growMemory(1) < 0 {
		fail("grow within the limit failed")
	}
	fmt.Println("grow within the limit ok")
	fmt.Println("grow past the limit", growMemory(64))
	// A loaded module's memory is the prog's too: testmod starts with
	// more than is left.
	if _, err := sys.Load("/bin/lib/testmod.wasm"); err != nil {
		fmt.Println("load:", err)
	}
	if len(os.Args) > 1 && os.Args[1] == "hog" {
		b := make([]byte, 64<<20)
		fmt.Println("allocated", len(b))
	}
}
