// Command auth checks authentication end to end in one emu:
//
//	auth mount clientkey serverkey alg [serveralg...]
//
// authenticates with a server prog over a pipe, as mount -k and
// styxlisten do, pushes ssl with alg, and mounts the server's export of
// /lib over it.
//
//	auth login user password [wrongpassword]
//
// starts keyfs on /mnt/keys, makes the user with changelogin, and gets a
// certificate from logind with the Login protocol.
//
//	auth net
//
// runs styxlisten -k and mount -k over TCP.
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/auth"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/login"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var waitf *os.File

func fail(f string, a ...any) {
	fmt.Printf(f+"\n", a...)
	sys.Exits("fail:auth")
}

func main() {
	// These tests are of Inferno's keyring, which is no longer the
	// default protocol (docs/auth.md): name it for every command run.
	sys.Pctl(sys.FORKENV, nil)
	if fd, err := sys.Create("/env/authproto", sys.OWRITE, 0o666); err == nil {
		f := os.NewFile(uintptr(fd), "authproto")
		f.WriteString("keyring")
		f.Close()
	}
	if len(os.Args) < 2 {
		fail("usage: auth mount|serve|login ...")
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2], os.Args[3:])
		return
	}
	pid, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		fail("wait file: %s", sys.Errstr())
	}
	waitf = os.NewFile(uintptr(wfd), "wait")
	switch os.Args[1] {
	case "mount":
		mount(os.Args[2], os.Args[3], os.Args[4], os.Args[5:])
	case "login":
		dologin(os.Args[2:])
	case "net":
		network()
	default:
		fail("unknown mode %s", os.Args[1])
	}
}

// wait reads a child's exit status.
func wait() string {
	b := make([]byte, 256)
	n, _ := waitf.Read(b)
	s := string(b[:n])
	if i := strings.Index(s, ":"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func mount(ckey, skey, alg string, salgs []string) {
	p, err := sys.Pipe()
	if err != nil {
		fail("pipe: %s", sys.Errstr())
	}
	if _, err := sys.Spawn("/bin/test/auth.wasm", append([]string{"auth", "serve", skey}, salgs...), sys.NEWFD,
		[][2]int{{0, p[0]}, {1, 1}, {2, 2}}); err != nil {
		fail("spawn: %s", sys.Errstr())
	}
	os.NewFile(uintptr(p[0]), "p0").Close()
	conn := os.NewFile(uintptr(p[1]), "p1")

	var out []string
	served := false
	ai, err := keyring.ReadAuthinfo(ckey)
	if err != nil {
		fail("cannot read %s: %s", ckey, err)
	}
	fd, id, err := auth.Client(alg, ai, conn)
	if err != nil {
		out = append(out, "authentication failed: "+err.Error())
	} else {
		out = append(out, "remote username is "+id)
		if err := sys.Mount(int(fd.Fd()), -1, "/n", sys.MREPL, ""); err != nil {
			out = append(out, "mount failed")
		} else {
			b, err := os.ReadFile("/n/hello.txt")
			out = append(out, fmt.Sprintf("read %q %v", b, err))
			sys.Unmount("", "/n")
			served = err == nil
		}
		if fd != conn {
			fd.Close()
		}
	}
	conn.Close()
	// A server that is still exporting has printed its line before the
	// client could read the file; one that failed has exited.
	st := ""
	if !served {
		st = wait()
	}
	for _, s := range out {
		fmt.Println(s)
	}
	if !served {
		fmt.Printf("server status %q\n", st)
	}
}

// serve is the server end: authenticate the client on fd 0, then export
// /lib on the connection.
func serve(key string, algs []string) {
	ai, err := keyring.ReadAuthinfo(key)
	if err != nil {
		fail("server: cannot read %s: %s", key, err)
	}
	if len(algs) == 0 {
		algs = nil
	}
	fd, id, err := auth.Server(algs, ai, os.Stdin, false)
	if err != nil {
		fmt.Printf("server: %s\n", err)
		sys.Exits("fail:auth")
	}
	fmt.Printf("server: client is %s\n", id)
	if err := sys.Export(int(fd.Fd()), "/lib", 0); err != nil {
		fmt.Printf("server: export: %s\n", sys.Errstr())
	}
}

// run runs a command with stdin and returns its output and status.
func run(stdin string, argv ...string) (string, string) {
	in, _ := sys.Pipe()
	out, _ := sys.Pipe()
	if _, err := sys.Spawn("/bin/"+argv[0]+".wasm", argv, sys.NEWFD,
		[][2]int{{0, in[0]}, {1, out[0]}, {2, out[0]}}); err != nil {
		fail("spawn %s: %s", argv[0], sys.Errstr())
	}
	os.NewFile(uintptr(in[0]), "in0").Close()
	os.NewFile(uintptr(out[0]), "out0").Close()
	w := os.NewFile(uintptr(in[1]), "in1")
	w.Write([]byte(stdin))
	w.Close()
	r := os.NewFile(uintptr(out[1]), "out1")
	b, _ := io.ReadAll(r)
	r.Close()
	return string(b), wait()
}

var date = regexp.MustCompile(`return = [0-9]{8}`)

func dologin(args []string) {
	user, password := args[0], args[1]
	// Start from an empty database: the emu runs the test once per
	// wazero mode.
	if fd, err := sys.Create("/keydb/keys", sys.OWRITE, 0o600); err == nil {
		os.NewFile(uintptr(fd), "keys").Close()
	}
	// keyfs's server prog keeps its fds, so it writes to the console.
	if _, err := sys.Spawn("/bin/keyfs.wasm", []string{"keyfs", "-n", "/keydb/nvram", "/keydb/keys"}, sys.NEWFD,
		[][2]int{{1, 1}, {2, 2}}); err != nil {
		fail("spawn keyfs: %s", sys.Errstr())
	}
	fmt.Printf("keyfs %q\n", wait())
	o, st := run(password+"\n"+password+"\npermanent\n", "changelogin", user)
	fmt.Printf("changelogin %q %q\n", date.ReplaceAllString(o, "return = DATE"), st)
	b, err := os.ReadFile("/mnt/keys/" + user + "/expire")
	fmt.Printf("expire %q %v\n", b, err)
	b, _ = os.ReadFile("/mnt/keys/" + user + "/secret")
	fmt.Printf("secret is the password's hash: %v\n", string(b) == string(login.PasswordKey(password)))

	try := func(pw string) {
		p, _ := sys.Pipe()
		if _, err := sys.Spawn("/bin/logind.wasm", []string{"logind"}, sys.NEWFD,
			[][2]int{{0, p[0]}, {1, 1}, {2, 2}}); err != nil {
			fail("spawn logind: %s", sys.Errstr())
		}
		os.NewFile(uintptr(p[0]), "p0").Close()
		conn := os.NewFile(uintptr(p[1]), "p1")
		info, err := login.Conn(conn, user, pw)
		conn.Close()
		st := wait()
		if err != nil {
			fmt.Printf("login failed; logind status %q\n", st)
			return
		}
		pk := []byte(keyring.PKtoStr(info.MyPK))
		fmt.Printf("certificate for %s signed by %s expires %d\n", info.MyPK.Owner, info.Cert.Signer, info.Cert.Exp)
		fmt.Printf("certificate verifies: %v; logind status %q\n",
			keyring.Verify(info.SPK, info.Cert, keyring.SHA1(pk, nil, nil)), st)
		if err := keyring.WriteAuthinfo("/keydb/"+user+".login", info); err != nil {
			fmt.Printf("writeauthinfo: %s\n", err)
		}
	}
	try(password)
	if len(args) > 2 {
		// logind reports the failure on /dev/cons, in words that depend
		// on when the client hangs up; catch it on a pipe.
		sys.Pctl(sys.FORKNS, nil)
		if err := sys.Bind("#|", "/n", sys.MREPL); err != nil {
			fail("bind #|: %s", sys.Errstr())
		}
		if err := sys.Bind("/n/data1", "/dev/cons", sys.MREPL); err != nil {
			fail("bind cons: %s", sys.Errstr())
		}
		cons, err := os.Open("/n/data")
		if err != nil {
			fail("open pipe: %s", sys.Errstr())
		}
		try(args[2])
		b := make([]byte, 256)
		n, _ := cons.Read(b)
		fmt.Printf("logind complained: %v\n", strings.HasPrefix(string(b[:n]), "logind: "))
	}
	sys.Unmount("", "/mnt/keys")
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

func network() {
	before := announced()
	// styxlisten and its server keep their fds, so they write to the
	// console.
	if _, err := sys.Spawn("/bin/styxlisten.wasm", []string{"styxlisten", "-k", "/keydb/bob", "tcp!127.0.0.1!0", "export", "/lib"},
		sys.NEWFD, [][2]int{{1, 1}, {2, 2}}); err != nil {
		fail("spawn styxlisten: %s", sys.Errstr())
	}
	fmt.Printf("styxlisten %q\n", wait())
	port := ""
	for p := range announced() {
		if !before[p] {
			port = p
		}
	}
	if port == "" {
		fail("styxlisten did not announce")
	}
	addr := "tcp!127.0.0.1!" + port
	var o, st string
	for _, alg := range []string{"none", "rc4_256 sha1", "des_56_cbc/md5"} {
		o, st = run("", "mount", "-v", "-k", "/keydb/alice", "-C", alg, addr, "/n")
		b, err := os.ReadFile("/n/hello.txt")
		fmt.Printf("mount -C %s: %q %q; read %q %v\n", alg, o, st, b, err)
		sys.Unmount("", "/n")
	}
	o, st = run("", "mount", "-k", "/keydb/mallory", addr, "/n")
	fmt.Printf("mount with a foreign key: %q %q\n", o, st)
	o, st = run("", "mount", "-k", "/keydb/alice", "-C", "rc4_256", "tcp!127.0.0.1!"+port, "/n")
	b, err := os.ReadFile("/n/hello.txt")
	fmt.Printf("mount after a failure: %q %q; read %q %v\n", o, st, b, err)
}
