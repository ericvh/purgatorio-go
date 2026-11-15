// Command m3 checks the M3 calls end to end: file2chan through ramfile,
// an export/mount loopback over a pipe, and kernel channels with alt.
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
	sys.Exits("fail:m3")
}

func main() {
	pid, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		fail("wait file: %s", sys.Errstr())
	}

	// ramfile detaches: its wait message comes back while it serves.
	// It gets a group of its own, to be killed by at the end.
	child, err := sys.Spawn("/bin/ramfile.wasm", []string{"ramfile", "/tmp/rf", "initial"}, sys.NEWFD|sys.NEWPGRP,
		[][2]int{{0, 0}, {1, 1}, {2, 2}})
	if err != nil {
		fail("spawn: %s", sys.Errstr())
	}
	w := make([]byte, 128)
	f := os.NewFile(uintptr(wfd), "wait")
	n, _ := f.Read(w)
	if want := strconv.Itoa(child) + ` "ramfile":`; string(w[:n]) != want {
		fail("wait message %q, want %q", w[:n], want)
	}
	b, err := os.ReadFile("/tmp/rf")
	if err != nil || string(b) != "initial" {
		fail("ramfile read %q %v %s", b, err, sys.Errstr())
	}
	if err := os.WriteFile("/tmp/rf", []byte("changed"), 0o666); err != nil {
		fail("ramfile write: %v %s", err, sys.Errstr())
	}
	b, _ = os.ReadFile("/tmp/rf")
	fmt.Printf("ramfile %s\n", b)

	// Export /lib on a pipe and mount the other end.
	fds, err := sys.Pipe()
	if err != nil {
		fail("pipe: %s", sys.Errstr())
	}
	if err := sys.Export(fds[0], "/lib", sys.EXPASYNC); err != nil {
		fail("export: %s", sys.Errstr())
	}
	if _, err := sys.Mount(fds[1], -1, "/n", sys.MREPL, ""); err != nil {
		fail("mount: %s", sys.Errstr())
	}
	b, err = os.ReadFile("/n/hello.txt")
	if err != nil {
		fail("read through mount: %s", sys.Errstr())
	}
	fmt.Printf("mounted %s", b)
	if _, err := os.Stat("/n/nope"); err == nil || sys.Errstr() != "file does not exist" {
		fail("missing through mount: %v %q", err, sys.Errstr())
	}
	ns, _ := os.ReadFile("/prog/" + strconv.Itoa(pid) + "/ns")
	if !strings.Contains(string(ns), "mount  #|/data1 /n \n") {
		fail("ns %q", ns)
	}

	// Kernel channels.
	c := sys.NewChan(1)
	if err := c.Send([]byte("ping")); err != nil {
		fail("send: %s", sys.Errstr())
	}
	ops := []sys.AltOp{{C: sys.NewChan(0)}, {C: c}}
	i, err := sys.Alt(ops, true)
	if err != nil || i != 1 || string(ops[1].Got) != "ping" {
		fail("alt %d %q %v", i, ops[1].Got, err)
	}
	if i, _ := sys.Alt([]sys.AltOp{{C: c}}, false); i != -1 {
		fail("nonblocking alt on empty channel: %d", i)
	}
	c.Close()
	if _, _, err := c.Recv(10); err == nil {
		fail("recv on closed handle succeeded")
	}
	fmt.Println("chans ok")

	// ramfile serves until it is killed; leave nothing running. Its
	// server is a prog of the group the spawned child made.
	progs, _ := os.ReadDir("/prog")
	for _, d := range progs {
		st, err := os.ReadFile("/prog/" + d.Name() + "/status")
		if f := strings.Fields(string(st)); err == nil && len(f) > 1 && f[1] == strconv.Itoa(child) {
			_ = os.WriteFile("/prog/"+d.Name()+"/ctl", []byte("killgrp"), 0)
			break
		}
	}
}
