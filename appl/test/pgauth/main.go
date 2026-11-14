// Command pgauth tests pgauth's mount handshake through the kernel
// (docs/auth.md): an export started with a protocol, and a client that
// authenticates with another or the same, over a pipe.
//
//	pgauth clientproto serverproto [trust]
//
// trust is "keys" (bind /lib/test/keys on /lib/keys, the default) or
// "none" (trust nothing). -S makes the client require the server's proof.
// -E tries a second attach without the auth fd once encrypted.
// -R puts two relay progs between client and server that record each
// direction, and reports whether the file's text crossed in the clear.
//
//	pgauth relay logfile
//	pgauth net proto
//
// is one relay: it copies fd 0 to fd 1 and appends what it copies to
// logfile. pgauth module prints the prog's /prog/<pid>/module and tries
// to lower its policy and spawn an unsigned module. pgauth net runs
// styxlisten with proto over TCP and mounts it with the mount command.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "relay" {
		relay(args[1])
		return
	}
	if len(args) == 1 && args[0] == "module" {
		module()
		return
	}
	if len(args) == 2 && args[0] == "net" {
		network(args[1])
		return
	}
	srvauth, relayed, again := false, false, false
	for len(args) > 0 && (args[0] == "-S" || args[0] == "-R" || args[0] == "-E") {
		switch args[0] {
		case "-S":
			srvauth = true
		case "-R":
			relayed = true
		case "-E":
			again = true
		}
		args = args[1:]
	}
	if len(args) < 2 {
		fmt.Println("usage: pgauth [-S] clientproto serverproto [keys|none]")
		sys.Exits("fail:usage")
	}
	cproto, sproto := args[0], args[1]
	trust := "keys"
	if len(args) > 2 {
		trust = args[2]
	}
	sys.Pctl(sys.FORKNS|sys.FORKENV, nil)
	if trust == "keys" {
		if err := sys.Bind("/lib/test/keys", "/lib/keys", sys.MREPL); err != nil {
			fmt.Println("bind:", sys.Errstr())
		}
	}
	p, err := sys.Pipe()
	if err != nil {
		fmt.Println("pipe:", sys.Errstr())
		return
	}
	key := "/lib/test/auth.hkey"
	if sproto == authproto.Classical {
		key = "/lib/test/auth.key"
	}
	argv := []string{"export", "-p", sproto, "-k", key, "/lib/test/keys"}
	if sproto == authproto.None {
		argv = []string{"export", "-A", "/lib/test/keys"}
	}
	srvfd, fd := p[0], p[1]
	if relayed {
		q, _ := sys.Pipe()
		os.Remove("/env/pgauth.c2s")
		os.Remove("/env/pgauth.s2c")
		// The client's end p[1]; p[0] and q[1] are relayed both ways; the
		// export serves q[0].
		sys.Spawn("/bin/test/pgauth.wasm", []string{"pgauth", "relay", "/env/pgauth.c2s"}, sys.NEWFD, [][2]int{{0, p[0]}, {1, q[1]}, {2, 2}})
		sys.Spawn("/bin/test/pgauth.wasm", []string{"pgauth", "relay", "/env/pgauth.s2c"}, sys.NEWFD, [][2]int{{0, q[1]}, {1, p[0]}, {2, 2}})
		os.NewFile(uintptr(p[0]), "p0").Close()
		os.NewFile(uintptr(q[1]), "q1").Close()
		srvfd = q[0]
	}
	if _, err := sys.Spawn("/bin/export.wasm", argv, sys.NEWFD, [][2]int{{0, srvfd}, {1, 1}, {2, 2}}); err != nil {
		fmt.Println("spawn:", sys.Errstr())
		return
	}
	os.NewFile(uintptr(srvfd), "srv").Close()
	if cproto == authproto.None {
		if err := sys.Mount(fd, -1, "/n/remote", sys.MREPL, ""); err != nil {
			fmt.Println("mount:", sys.Errstr())
			return
		}
		report(relayed)
		return
	}
	hybrid := cproto == authproto.Hybrid
	k, c, err := authproto.Key("", hybrid)
	if err != nil {
		fmt.Println("key:", err)
		return
	}
	afd, err := authproto.Authenticate(fd, "", hybrid, k, c)
	if err != nil {
		fmt.Println("authenticate:", err)
		return
	}
	flags := sys.MREPL
	if srvauth {
		if err := authproto.CheckHost(afd, hybrid, "", uint64(time.Now().Unix())); err != nil {
			fmt.Println("server:", err)
			return
		}
		flags |= sys.MSRVAUTH
	}
	if again {
		encryptedFd = fd
	}
	if err := sys.Mount(fd, int(afd.Fd()), "/n/remote", flags, ""); err != nil {
		fmt.Println("mount:", sys.Errstr())
		return
	}
	report(relayed)
}

func report(relayed bool) {
	b, err := os.ReadFile("/n/remote/users")
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Printf("read %d bytes\n", len(b))
	if encryptedFd >= 0 {
		// Attaching again without the auth fd is refused.
		if err := sys.Mount(encryptedFd, -1, "/n/remote", sys.MREPL, ""); err != nil {
			fmt.Println("mount again:", sys.Errstr())
		}
	}
	if !relayed {
		return
	}
	time.Sleep(200 * time.Millisecond)
	s2c, _ := os.ReadFile("/env/pgauth.s2c")
	c2s, _ := os.ReadFile("/env/pgauth.c2s")
	fmt.Printf("file text in the clear: %v; name in the clear: %v\n",
		strings.Contains(string(s2c), "hybrid test key"), strings.Contains(string(c2s), "users"))
}

// network runs styxlisten with proto over TCP on the loopback and mounts
// it with the mount command, with -S, and once with the other protocol.
func network(proto string) {
	sys.Pctl(sys.FORKNS|sys.FORKENV, nil)
	sys.Bind("/lib/test/keys", "/lib/keys", sys.MREPL)
	key, other := "/lib/test/auth.hkey", authproto.Classical
	if proto == authproto.Classical {
		key, other = "/lib/test/auth.key", authproto.Hybrid
	}
	before := announced()
	if st := run("/bin/styxlisten.wasm", "styxlisten", "-p", proto, "-k", key, "tcp!127.0.0.1!0", "export", "/lib/test/keys"); st != "" {
		fmt.Println("styxlisten:", st)
		return
	}
	port := ""
	for p := range announced() {
		if !before[p] {
			port = p
		}
	}
	addr := "tcp!127.0.0.1!" + port
	for _, m := range [][]string{
		{"mount", "-p", proto, "-S", addr, "/n/remote"},
		{"mount", "-p", other, addr, "/n/remote"},
		{"mount", "-p", proto, "-A", addr, "/n/remote"},
	} {
		st := run("/bin/mount.wasm", m...)
		b, err := os.ReadFile("/n/remote/users")
		label := strings.Join(m[1:len(m)-2], " ")
		fmt.Printf("mount %s: status %q, read %d bytes %v\n", label, st, len(b), err == nil)
		sys.Unmount("", "/n/remote")
	}
}

// announced returns the ports of the announced TCP conversations.
func announced() map[string]bool {
	ports := map[string]bool{}
	ents, _ := os.ReadDir("/net/tcp")
	for _, e := range ents {
		st, _ := os.ReadFile("/net/tcp/" + e.Name() + "/status")
		if !strings.HasPrefix(string(st), "Announced") {
			continue
		}
		local, _ := os.ReadFile("/net/tcp/" + e.Name() + "/local")
		_, port, _ := strings.Cut(strings.TrimSpace(string(local)), "!")
		ports[port] = true
	}
	return ports
}

// run runs a command and returns its status.
func run(path string, argv ...string) string {
	pid, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		return "no wait file"
	}
	wf := os.NewFile(uintptr(wfd), "wait")
	defer wf.Close()
	child, err := sys.Spawn(path, argv, 0, nil)
	if err != nil {
		return sys.Errstr()
	}
	buf := make([]byte, 512)
	for {
		n, err := wf.Read(buf)
		if err != nil || n == 0 {
			return "wait failed"
		}
		id, rest, _ := strings.Cut(string(buf[:n]), " ")
		if id == strconv.Itoa(child) {
			if i := strings.Index(rest, "\":"); i >= 0 {
				return rest[i+2:]
			}
			return ""
		}
	}
}

// module prints the prog's /prog/<pid>/module, then tries to lower its
// policy and to spawn an unsigned ls (docs/signing.md).
func module() {
	pid, _ := sys.Pctl(0, nil)
	dir := "/prog/" + strconv.Itoa(pid)
	b, _ := os.ReadFile(dir + "/module")
	os.Stdout.Write(b)
	if err := os.WriteFile(dir+"/ctl", []byte("modules warn"), 0); err != nil {
		fmt.Println("lower:", sys.Errstr())
	}
	if _, err := sys.Spawn("/bin/ls.wasm", []string{"ls"}, 0, nil); err != nil {
		fmt.Println("spawn:", sys.Errstr())
	}
}

// encryptedFd, with -E, is the conversation to try to attach on again
// without its auth fd.
var encryptedFd = -1

// relay copies fd 0 to fd 1 and appends what it copies to log.
func relay(log string) {
	lf, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	buf := make([]byte, 16384)
	for {
		n, err := os.Stdin.Read(buf)
		if n <= 0 || err != nil {
			return
		}
		lf.Write(buf[:n])
		if _, err := os.Stdout.Write(buf[:n]); err != nil {
			return
		}
	}
}
