// Command styx checks the Styx and network commands end to end: memfs,
// cs, export and mount over a pipe, styxlisten with a mount of it over
// TCP, mount {mntgen}, listen, dial and netstat. Each command is its own
// prog, spawned in this prog's name space and waited for.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var waitf *os.File

func fail(f string, a ...any) {
	fmt.Printf(f+"\n", a...)
	sys.Exits("fail:styx")
}

// spawn starts /bin/<name>.wasm with fds 0, 1 and 2 and any extra
// (child, parent) pairs.
func spawn(extra [][2]int, args ...string) int {
	fds := extra
	for fd := range 3 {
		mapped := false
		for _, x := range extra {
			mapped = mapped || x[0] == fd
		}
		if !mapped {
			fds = append(fds, [2]int{fd, fd})
		}
	}
	pid, err := sys.Spawn("/bin/"+args[0]+".wasm", args, sys.NEWFD, fds)
	if err != nil {
		fail("spawn %s: %s", args[0], sys.Errstr())
	}
	return pid
}

// await returns the exit status of child pid.
func await(pid int) string {
	buf := make([]byte, 256)
	for {
		n, err := waitf.Read(buf)
		if err != nil || n == 0 {
			fail("wait: %v %s", err, sys.Errstr())
		}
		msg := string(buf[:n])
		p, rest, _ := strings.Cut(msg, " ")
		if p != strconv.Itoa(pid) {
			continue
		}
		_, st, _ := strings.Cut(rest, "\":")
		return st
	}
}

func run(extra [][2]int, args ...string) string { return await(spawn(extra, args...)) }

// noauth runs a command that should refuse to run without -A, as there
// is no key, with its standard error (which names the user) captured,
// and checks it.
func noauth(extra [][2]int, want string, args ...string) {
	p, err := sys.Pipe()
	if err != nil {
		fail("pipe: %s", sys.Errstr())
	}
	child := spawn(append(extra, [2]int{2, p[1]}), args...)
	os.NewFile(uintptr(p[1]), "p1").Close()
	msg, _ := io.ReadAll(os.NewFile(uintptr(p[0]), "p0"))
	st := await(child)
	if st != want || !strings.Contains(string(msg), "cannot read") {
		fail("%s without -A: %q %q", args[0], st, msg)
	}
	fmt.Printf("%s without -A: %s\n", args[0], st)
}

func mustrun(args ...string) {
	if st := run(nil, args...); st != "" {
		fail("%s: status %q", strings.Join(args, " "), st)
	}
}

func main() {
	pid, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		fail("wait file: %s", sys.Errstr())
	}
	waitf = os.NewFile(uintptr(wfd), "wait")

	memfs()
	cs()
	exportpipe()
	styxlisten()
	mntgen()
	listen()
	dialcmd()
	netstat()
}

func memfs() {
	mustrun("memfs", "/tmp")
	if err := os.WriteFile("/tmp/a", []byte("hello memfs"), 0o666); err != nil {
		fail("memfs create: %s", sys.Errstr())
	}
	if err := os.Mkdir("/tmp/d", 0o777); err != nil {
		fail("memfs mkdir: %s", sys.Errstr())
	}
	f, err := os.OpenFile("/tmp/d/f", os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		fail("memfs create in dir: %s", sys.Errstr())
	}
	f.Write([]byte("0123456789"))
	f.WriteAt([]byte("ab"), 4)
	f.Close()
	b, _ := os.ReadFile("/tmp/d/f")
	a, _ := os.ReadFile("/tmp/a")
	fmt.Printf("memfs read %q %q\n", a, b)
	if err := sys.Remove("/tmp/d"); err == nil || sys.Errstr() != "directory not empty" {
		fail("remove non-empty dir: %v %q", err, sys.Errstr())
	}
	if err := sys.Remove("/tmp/a"); err != nil {
		fail("memfs remove: %s", sys.Errstr())
	}
	if _, err := sys.Stat("/tmp/a"); err == nil || sys.Errstr() != "file does not exist" {
		fail("removed file: %v %q", err, sys.Errstr())
	}
	ents, _ := os.ReadDir("/tmp")
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	d, _ := sys.Stat("/tmp/d/f")
	fmt.Printf("memfs dir %v, f length %d gid %s\n", names, d.Length, d.GID)
	sys.Unmount("", "/tmp")
}

func csquery(q string) ([]string, string) {
	fd, err := sys.Open("/net/cs", sys.ORDWR)
	if err != nil {
		fail("open /net/cs: %s", sys.Errstr())
	}
	f := os.NewFile(uintptr(fd), "cs")
	defer f.Close()
	if _, err := f.Write([]byte(q)); err != nil {
		return nil, sys.Errstr()
	}
	f.Seek(0, 0)
	var out []string
	buf := make([]byte, 256)
	for {
		n, _ := f.Read(buf)
		if n <= 0 {
			return out, ""
		}
		out = append(out, string(buf[:n]))
	}
}

func cs() {
	mustrun("cs")
	l, e := csquery("tcp!localhost!styx")
	found := false
	for _, s := range l {
		found = found || s == "/net/tcp/clone 127.0.0.1!6666"
	}
	if !found {
		fail("cs localhost: %q %s", l, e)
	}
	fmt.Println("cs tcp!localhost!styx -> /net/tcp/clone 127.0.0.1!6666")
	for _, q := range []string{"net!10.0.0.1!9fs", "tcp!*!styx", "bogus!a!b", "tcp!x", "!tcp=styx"} {
		l, e := csquery(q)
		if e != "" {
			e = " error " + e
		}
		fmt.Printf("cs %s -> %q%s\n", q, l, e)
	}
}

func exportpipe() {
	p, err := sys.Pipe()
	if err != nil {
		fail("pipe: %s", sys.Errstr())
	}
	spawn([][2]int{{0, p[0]}}, "export", "/lib")
	os.NewFile(uintptr(p[0]), "p0").Close()
	keep := [][2]int{{p[1], p[1]}}
	fd := "/fd/" + strconv.Itoa(p[1])
	noauth(keep, "fail:error", "mount", fd, "/n")
	if st := run(keep, "mount", "-A", fd, "/n"); st != "" {
		fail("mount -A %s: %q", fd, st)
	}
	b, err := os.ReadFile("/n/hello.txt")
	if err != nil {
		fail("read through export: %s", sys.Errstr())
	}
	fmt.Printf("export/mount pipe: %s", b)
	sys.Unmount("", "/n")
	os.NewFile(uintptr(p[1]), "p1").Close()
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

func newport(before map[string]bool) string {
	for p := range announced() {
		if !before[p] {
			return p
		}
	}
	fail("no new announced port")
	return ""
}

func styxlisten() {
	noauth(nil, "fail:error", "styxlisten", "tcp!127.0.0.1!0", "export", "/lib")
	before := announced()
	mustrun("styxlisten", "-A", "tcp!127.0.0.1!0", "export", "/lib")
	port := newport(before)
	for i := range 2 {
		mustrun("mount", "-A", "tcp!127.0.0.1!"+port, "/n")
		b, err := os.ReadFile("/n/hello.txt")
		if err != nil {
			fail("read through styxlisten: %s", sys.Errstr())
		}
		fmt.Printf("styxlisten client %d: %s", i, b)
		sys.Unmount("", "/n")
	}
}

func mntgen() {
	mustrun("mount", "-a", "{mntgen}", "/n")
	d, err := sys.Stat("/n/anything")
	if err != nil || d.Mode&sys.DMDIR == 0 {
		fail("mntgen: %v %s", d, sys.Errstr())
	}
	fmt.Printf("mntgen made %s\n", d.Name)
	sys.Unmount("", "/n")
}

func listen() {
	before := announced()
	mustrun("listen", "tcp!127.0.0.1!0", "cat")
	port := newport(before)
	for i := range 2 {
		c, err := dial.Dial("tcp!127.0.0.1!"+port, "")
		if err != nil {
			fail("dial listen: %v", err)
		}
		c.Dfd.Write([]byte("ping"))
		buf := make([]byte, 64)
		n, _ := c.Dfd.Read(buf)
		fmt.Printf("listen echo %d: %s\n", i, buf[:n])
		c.Dfd.Close()
		c.Cfd.Close()
	}
}

func dialcmd() {
	noauth(nil, "fail:bad keyfile", "dial", "tcp!127.0.0.1!1", "echo")
	ac, err := dial.Announce("tcp!127.0.0.1!0")
	if err != nil {
		fail("announce: %v", err)
	}
	local, _ := os.ReadFile(ac.Dir + "/local")
	_, port, _ := strings.Cut(strings.TrimSpace(string(local)), "!")
	child := spawn(nil, "dial", "-A", "tcp!127.0.0.1!"+port, "echo", "hello", "from", "dial")
	lc, err := dial.Listen(ac)
	if err != nil {
		fail("listen: %v", err)
	}
	dfd, err := dial.Accept(lc)
	if err != nil {
		fail("accept: %v", err)
	}
	b, _ := io.ReadAll(dfd)
	fmt.Printf("dial sent %q, status %q\n", b, await(child))
}

func netstat() {
	p, err := sys.Pipe()
	if err != nil {
		fail("pipe: %s", sys.Errstr())
	}
	child := spawn([][2]int{{1, p[1]}}, "netstat")
	os.NewFile(uintptr(p[1]), "p1").Close()
	out, _ := io.ReadAll(os.NewFile(uintptr(p[0]), "p0"))
	if st := await(child); st != "" {
		fail("netstat status %q", st)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	announced := 0
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 4 || !strings.HasPrefix(f[0], "tcp/") && !strings.HasPrefix(f[0], "udp/") {
			fail("netstat line %q", l)
		}
		if f[2] == "Announced" {
			announced++
		}
	}
	fmt.Printf("netstat: %d announced\n", announced)
}
