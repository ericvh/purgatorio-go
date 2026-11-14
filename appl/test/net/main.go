// Command net checks #I end to end: it announces on a local TCP port,
// spawns itself as a client that dials it through sdk/dial, and passes a
// message each way. Each side is its own prog: a host call blocks the
// whole guest.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func fail(f string, a ...any) {
	fmt.Printf(f+"\n", a...)
	sys.Exits("fail:net")
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "client" {
		client(os.Args[2])
		return
	}
	ac, err := dial.Announce("tcp!127.0.0.1!0")
	if err != nil {
		fail("announce: %v", err)
	}
	local, _ := os.ReadFile(ac.Dir + "/local")
	_, port, _ := strings.Cut(strings.TrimSpace(string(local)), "!")
	pid, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		fail("wait: %s", sys.Errstr())
	}
	if _, err := sys.Spawn("/bin/test/net.wasm", []string{"net", "client", port}, sys.NEWFD,
		[][2]int{{0, 0}, {1, 1}, {2, 2}}); err != nil {
		fail("spawn: %s", sys.Errstr())
	}
	lc, err := dial.Listen(ac)
	if err != nil {
		fail("listen: %v", err)
	}
	dfd, err := dial.Accept(lc)
	if err != nil {
		fail("accept: %v", err)
	}
	buf := make([]byte, 64)
	n, _ := dfd.Read(buf)
	fmt.Printf("server got %q\n", buf[:n])
	remote, _ := os.ReadFile(lc.Dir + "/remote")
	if !strings.HasPrefix(string(remote), "127.0.0.1!") {
		fail("remote %q", remote)
	}
	dfd.Write([]byte("pong"))
	w := make([]byte, 128)
	n, _ = os.NewFile(uintptr(wfd), "wait").Read(w)
	if !strings.HasSuffix(string(w[:n]), `"net":`) {
		fail("client exit %q", w[:n])
	}
	st, _ := os.ReadFile(lc.Dir + "/status")
	fmt.Printf("status %s", st)
}

func client(port string) {
	c, err := dial.Dial("tcp!127.0.0.1!"+port, "")
	if err != nil {
		fail("dial: %v", err)
	}
	c.Dfd.Write([]byte("ping"))
	buf := make([]byte, 64)
	n, _ := c.Dfd.Read(buf)
	fmt.Printf("client got %q\n", buf[:n])
	if _, err := dial.Dial("tcp!127.0.0.1!1", ""); err == nil || !strings.Contains(err.Error(), "refused") {
		fail("dial closed port: %v", err)
	}
}
