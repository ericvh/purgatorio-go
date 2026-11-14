// Command auth/modpolicy sets this prog's module policy
// (docs/signing.md), then runs cmd under it. The command and everything
// it starts inherit the policy and cannot lower it. The trust lists are
// the /lib/keys/modules and /lib/keys/revoked this prog sees when it
// starts. The policy takes hybrid signatures; with ,classical it takes
// Ed25519 ones too. It exits with the command's status.
//
//	auth/modpolicy warn|require[,classical] cmd [arg ...]
package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const usage = "usage: auth/modpolicy warn|require[,classical] cmd [arg ...]"

func fail(msg string) {
	os.Stderr.WriteString("auth/modpolicy: " + msg + "\n")
	sys.Exits("fail:" + msg)
}

func main() {
	if len(os.Args) < 3 {
		fail(usage)
	}
	level, rest, _ := strings.Cut(os.Args[1], ",")
	if (level != "warn" && level != "require") || (rest != "" && rest != "classical") {
		fail(usage)
	}
	msg := "modules " + level
	if rest != "" {
		msg += " " + rest
	}
	pid, _ := sys.Pctl(0, nil)
	ctl := "/prog/" + strconv.Itoa(pid) + "/ctl"
	if err := os.WriteFile(ctl, []byte(msg), 0); err != nil {
		fail(ctl + ": " + sys.Errstr())
	}
	wfd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		fail("can't open wait file: " + sys.Errstr())
	}
	wf := os.NewFile(uintptr(wfd), "wait")
	argv := os.Args[2:]
	path := argv[0]
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "./") && !strings.HasPrefix(path, "#") {
		path = "/bin/" + path
	}
	if !strings.HasSuffix(path, ".wasm") {
		path += ".wasm"
	}
	child, err := sys.Spawn(path, argv, 0, nil)
	if err != nil {
		e := sys.Errstr()
		if !strings.HasPrefix(e, path+": ") {
			e = argv[0] + ": " + e
		}
		fail(e)
	}
	buf := make([]byte, 512)
	for {
		n, err := wf.Read(buf)
		if err != nil || n == 0 {
			sys.Exits("fail:wait")
		}
		// pid "name":status
		id, rest, _ := strings.Cut(string(buf[:n]), " ")
		if id == strconv.Itoa(child) {
			if i := strings.Index(rest, "\":"); i >= 0 {
				sys.Exits(rest[i+2:])
			}
			sys.Exits("")
		}
	}
}
