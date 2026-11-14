// Command pgauth tests pgauth's mount handshake through the kernel
// (docs/auth.md): an export started with a protocol, and a client that
// authenticates with another or the same, over a pipe.
//
//	pgauth clientproto serverproto [trust]
//
// trust is "keys" (bind /lib/test/keys on /lib/keys, the default) or
// "none" (trust nothing). -S makes the client require the server's proof.
// -R puts two relay progs between client and server that record each
// direction, and reports whether the file's text crossed in the clear.
//
//	pgauth relay logfile
//
// is one relay: it copies fd 0 to fd 1 and appends what it copies to
// logfile. pgauth module prints the prog's /prog/<pid>/module and tries
// to lower its policy and spawn an unsigned module.
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
	srvauth, relayed := false, false
	for len(args) > 0 && (args[0] == "-S" || args[0] == "-R") {
		if args[0] == "-S" {
			srvauth = true
		} else {
			relayed = true
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
	if !relayed {
		return
	}
	time.Sleep(200 * time.Millisecond)
	s2c, _ := os.ReadFile("/env/pgauth.s2c")
	c2s, _ := os.ReadFile("/env/pgauth.c2s")
	fmt.Printf("file text in the clear: %v; name in the clear: %v\n",
		strings.Contains(string(s2c), "hybrid test key"), strings.Contains(string(c2s), "users"))
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
