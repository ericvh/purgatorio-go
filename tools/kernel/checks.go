package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/lethe"
)

// A checker boots the kernel with devices and a first command of its
// own, talks to it through the console, and checks what it sees.
type checker interface {
	setup() checkConfig
	run(q *exec.Cmd) int
}

type checkConfig struct {
	init   string   // the first command
	cmds   []string // commands the root needs beyond the defaults
	files  []string // more files under root/ for the root
	disks  []string
	netdev string
}

// console runs QEMU, passing each console line to line, which may write
// to the machine's console through in, and kills it after timeout. It
// returns the whole output.
func console(q *exec.Cmd, timeout time.Duration, line func(s string, in io.Writer)) (string, error) {
	in, err := q.StdinPipe()
	if err != nil {
		return "", err
	}
	out, err := q.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := q.Start(); err != nil {
		return "", err
	}
	timer := time.AfterFunc(timeout, func() { _ = q.Process.Kill() })
	defer timer.Stop()
	var all strings.Builder
	r := bufio.NewReader(out)
	for {
		s, err := r.ReadString('\n')
		s = strings.TrimRight(s, "\r\n")
		if s != "" || err == nil {
			fmt.Println(s)
			all.WriteString(s + "\n")
			line(s, in)
		}
		if err != nil {
			break
		}
	}
	in.Close()
	return all.String(), q.Wait()
}

// between is the part of s after start and before the end that follows.
func between(s, start, end string) string {
	_, after, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	before, _, _ := strings.Cut(after, end)
	return before
}

// results reports a list of named checks.
type results struct{ failed int }

func (r *results) check(name string, ok bool, detail string) {
	if ok {
		fmt.Printf("ok   %s\n", name)
		return
	}
	r.failed++
	fmt.Printf("FAIL %s: %s\n", name, detail)
}

func (r *results) status() int {
	if r.failed > 0 {
		fmt.Printf("%d checks failed\n", r.failed)
		return 1
	}
	return 0
}

// netChecker checks #I over virtio-net and QEMU user networking: TCP on
// the loopback interface (the emu's net test guest); /net/ipifc, by
// removing the interface's address and getting it back with ip/dhcp
// from QEMU's DHCP server; ip/ping to the gateway (10.0.2.2) over
// /net/icmp; a connection out to a server on the host, and one in from
// the host through a forwarded port.
type netChecker struct {
	ln      net.Listener
	hostGot chan string
	fwd     int
	lethed  *host.LethedServer
	sealed  *host.LethedServer
	bigSum  string
}

const guestPort = 5555

func (c *netChecker) setup() checkConfig {
	var err error
	c.ln, err = net.Listen("tcp", "127.0.0.1:0")
	check(err)
	c.hostGot = make(chan string, 1)
	go func() {
		conn, err := c.ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Minute))
		s, _ := bufio.NewReader(conn).ReadString('\n')
		c.hostGot <- s
		_, _ = conn.Write([]byte("pong from the host\n"))
	}()
	// A free port on the host for the forwarded one.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	check(err)
	c.fwd = l.Addr().(*net.TCPAddr).Port
	l.Close()
	port := c.ln.Addr().(*net.TCPAddr).Port
	lport, sport := c.startLethed()
	init := fmt.Sprintf("echo @@ loopback; /bin/test/net.wasm; "+
		"echo @@ ipifc; cat /net/ipifc/0/status; echo remove 10.0.2.15 255.255.255.0 > /net/ipifc/0/ctl; "+
		"echo @@ removed; cat /net/ipifc/0/status; "+
		"echo @@ dhcp; ip/dhcp -p /net/ipifc/0; cat /net/ipifc/0/status /net/ipifc/0/local; "+
		"echo @@ ping; ip/ping -n 3 -i 200 10.0.2.2; "+
		"echo @@ dial; dial -A tcp!10.0.2.2!%d {echo ping from the kernel; read >[1=2]}; "+
		"echo @@ lethe; lethe -A tcp!10.0.2.2!%d /n/remote; cat /n/remote/hello; md5sum /n/remote/big; "+
		"echo @@ lethe tickets; lethe tcp!10.0.2.2!%d /n/client; cat /n/client/hello; "+
		"listen -A tcp!*!%d {echo hello from the kernel} & "+
		"echo @@ listening; read < /dev/cons; echo @@ end", port, lport, sport, guestPort)
	return checkConfig{
		init:   init,
		cmds:   []string{"test/net", "dial", "listen", "read", "lethe", "cat", "md5sum", "ip/dhcp", "ip/ping"},
		netdev: fmt.Sprintf("hostfwd=tcp:127.0.0.1:%d-:%d", c.fwd, guestPort),
	}
}

// startLethed serves a small tree over Lethe on two host ports and
// returns them: one in the clear (lethed -A), one with hybrid tickets for
// the test key.
func (c *netChecker) startLethed() (int, int) {
	dir, err := os.MkdirTemp("", "kernel-lethe-*")
	check(err)
	// The clear mode reads as none, with the other bits.
	check(os.Chmod(dir, 0o755))
	check(os.WriteFile(filepath.Join(dir, "hello"), []byte("hello over lethe\n"), 0o644))
	big := bytes.Repeat([]byte("0123456789abcdef"), 20000)
	check(os.WriteFile(filepath.Join(dir, "big"), big, 0o644))
	c.bigSum = fmt.Sprintf("%x", md5.Sum(big))
	c.lethed, err = host.NewLethed(&host.LetheOptions{Addr: "127.0.0.1:0", Root: dir, Clear: true, Config: lethe.DefaultConfig()})
	check(err)
	go c.lethed.Serve()
	c.sealed, err = host.NewLethed(&host.LetheOptions{Addr: "127.0.0.1:0", Root: dir, Config: lethe.DefaultConfig(),
		Keys: []string{"root/lib/test/auth.hkey.pub"}})
	check(err)
	go c.sealed.Serve()
	return c.lethed.Port(), c.sealed.Port()
}

// dialIn connects to the forwarded port until the kernel answers: QEMU
// accepts the connection before the guest listens.
func (c *netChecker) dialIn() string {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", c.fwd), 5*time.Second)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
			b, _ := io.ReadAll(conn)
			conn.Close()
			if len(b) > 0 {
				return string(b)
			}
		}
		time.Sleep(time.Second)
	}
	return ""
}

func (c *netChecker) run(q *exec.Cmd) int {
	var in string
	out, err := console(q, 10*time.Minute, func(s string, w io.Writer) {
		if strings.Contains(s, "@@ listening") {
			go func() {
				in = c.dialIn()
				_, _ = io.WriteString(w, "done\n")
			}()
		}
	})
	c.ln.Close()
	var r results
	r.check("kernel ran", err == nil && strings.Contains(out, "@@ end"), fmt.Sprint(err))
	r.check("loopback tcp", strings.Contains(out, "server got \"ping\"\nclient got \"pong\"\nstatus Established\n"), "see output")
	ifc := between(out, "@@ ipifc\n", "@@ removed\n")
	r.check("ipifc status", strings.HasPrefix(ifc, "device /net/ether0 maxtu 1500 ") && strings.Contains(ifc, "\n\t10.0.2.15 "), ifc)
	removed := between(out, "@@ removed\n", "@@ dhcp\n")
	r.check("ipifc remove", strings.HasPrefix(removed, "device /net/ether0 ") && !strings.Contains(removed, "10.0.2.15"), removed)
	dhcp := between(out, "@@ dhcp\n", "@@ ping\n")
	r.check("dhcp configures the interface", strings.HasPrefix(dhcp, "ip=10.0.2.15 ipmask=255.255.255.0 ipgw=10.0.2.2 ") &&
		strings.Contains(dhcp, "\n\t10.0.2.15 ") && strings.Contains(dhcp, "\n10.0.2.15 ") && strings.Contains(dhcp, " 10.0.2.255 "), dhcp)
	ping := between(out, "@@ ping\n", "@@ dial\n")
	r.check("ping the gateway", strings.Contains(ping, "\n2: rtt ") && !strings.Contains(ping, "lost"), ping)
	var got string
	select {
	case got = <-c.hostGot:
	default:
	}
	r.check("dial out to the host", got == "ping from the kernel\n" && strings.Contains(out, "pong from the host"),
		fmt.Sprintf("host got %q", got))
	r.check("connection in from the host", in == "hello from the kernel\n", fmt.Sprintf("host read %q", in))
	c.lethed.Close()
	c.sealed.Close()
	os.RemoveAll(c.lethed.Opts.Root)
	r.check("lethe from the host", strings.Contains(out, "hello over lethe\n"+c.bigSum), "see output")
	r.check("lethe with hybrid tickets", strings.Contains(out, "@@ lethe tickets\nhello over lethe\n"), "see output")
	return r.status()
}

// diskChecker checks #S over virtio-blk on a scratch image: the
// geometry, a read of what the host wrote, a write the host then finds,
// and a partition; then kfs on a second disk: it reams it, writes files,
// and a second kfs reads them back.
type diskChecker struct {
	image, kfsImage string
}

const (
	diskSize   = 8 << 20
	hostText   = "hello from the host\n"
	kernelText = "written by the kernel\n"
	partText   = "in partition p1\n"
	kfsText    = "written through kfs\n"
)

func (c *diskChecker) setup() checkConfig {
	f, err := os.CreateTemp("", "kernel-disk-*.img")
	check(err)
	c.image = f.Name()
	check(f.Truncate(diskSize))
	_, err = f.WriteAt([]byte(hostText), 1024)
	check(err)
	check(f.Close())
	f, err = os.CreateTemp("", "kernel-kfs-*.img")
	check(err)
	c.kfsImage = f.Name()
	check(f.Truncate(diskSize))
	check(f.Close())
	init := fmt.Sprintf("echo @@ ctl; cat '#S/sdC0/ctl'; "+
		"echo @@ read; read -o 1024 %d < '#S/sdC0/data'; "+
		"echo @@ write; echo '%s' > '#S/sdC0/data'; "+
		"echo 'part p1 16 24' > '#S/sdC0/ctl'; echo '%s' > '#S/sdC0/p1'; ls -l '#S/sdC0/p1'; "+
		"echo @@ kfs; mount -c {kfs -r /dev/sdC1/data} /n; echo '%s' > /n/file; mkdir /n/dir; echo nested > /n/dir/f; "+
		"unmount /n; mount -c {kfs /dev/sdC1/data} /n; echo @@ kfs read; cat /n/file /n/dir/f; ls -l /n/dir/f; "+
		"echo @@ end",
		len(hostText), strings.TrimSuffix(kernelText, "\n"), strings.TrimSuffix(partText, "\n"), strings.TrimSuffix(kfsText, "\n"))
	return checkConfig{init: init, cmds: []string{"read", "mount", "unmount", "kfs"}, disks: []string{c.image, c.kfsImage}}
}

func (c *diskChecker) run(q *exec.Cmd) int {
	defer os.Remove(c.image)
	defer os.Remove(c.kfsImage)
	out, err := console(q, 10*time.Minute, func(string, io.Writer) {})
	img, rerr := os.ReadFile(c.image)
	check(rerr)
	kimg, rerr := os.ReadFile(c.kfsImage)
	check(rerr)
	var r results
	r.check("kernel ran", err == nil && strings.Contains(out, "@@ end"), fmt.Sprint(err))
	r.check("ctl", strings.Contains(out, fmt.Sprintf("geometry %d 512\npart data 0 %d\n", diskSize/512, diskSize/512)), "see output")
	r.check("read", strings.Contains(out, "@@ read\n"+hostText), "see output")
	r.check("write", bytes.HasPrefix(img, []byte(kernelText)) && bytes.Equal(img[1024:1024+len(hostText)], []byte(hostText)),
		fmt.Sprintf("image starts %q", img[:32]))
	r.check("partition", bytes.HasPrefix(img[16*512:], []byte(partText)) && strings.Contains(out, " 4096 "),
		fmt.Sprintf("sector 16 holds %q", img[16*512:16*512+32]))
	r.check("kfs", strings.Contains(out, "@@ kfs read\n"+kfsText+"nested\n") &&
		strings.Contains(out, " inferno adm 7 ") && bytes.Contains(kimg, []byte(kfsText)) &&
		bytes.Contains(kimg[256:], []byte("kfs wren device\n1024\n")), "see output")
	return r.status()
}

// aotChecker checks the aot engine's choices (kernel/aot): a module with
// its image runs from it; one with another module's image bound over its
// own, or an image signed only with Ed25519 under the default (hybrid)
// policy, runs on the interpreter, and so does one with no image. The
// eight modules that run besides (emuinit, sh, echo, wc, memfs, cp, bind
// and ls) come from their images.
type aotChecker struct{}

func (aotChecker) setup() checkConfig {
	aot := exec.Command("go", "run", "./tools/aot", "-classical", "-o", "root/tmp/aotcheck", "bin/tr.wasm")
	aot.Stdout, aot.Stderr = os.Stdout, os.Stderr
	check(aot.Run())
	return checkConfig{
		cmds:  []string{"cp"},
		files: []string{"tmp/aotcheck"},
		init: "echo one | wc -w; memfs /mnt; cp /bin/cat.aot /mnt/grep.aot; bind /mnt/grep.aot /bin/grep.aot; " +
			"echo two | grep two; bind /tmp/aotcheck/bin/tr.aot /bin/tr.aot; echo three | tr a-z A-Z; " +
			"cp /bin/mkdir.wasm /mnt/mk.wasm; /mnt/mk.wasm /mnt/four; ls /mnt",
	}
}

func (aotChecker) run(q *exec.Cmd) int {
	out, err := console(q, 10*time.Minute, func(string, io.Writer) {})
	var r results
	r.check("the kernel ran", err == nil, fmt.Sprint(err))
	r.check("a module runs from its image", strings.Contains(out, "\n      1\n"), out)
	r.check("a module runs with another module's image", strings.Contains(out, "\ntwo\n"), out)
	r.check("a module runs with a classical image", strings.Contains(out, "\nTHREE\n"), out)
	r.check("a module runs without an image", strings.Contains(out, "\n/mnt/four\n"), out)
	var sum string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "kernel: engine aot") && strings.Contains(line, "modules from images") {
			sum = line
		}
	}
	r.check("two images refused, one module without", strings.Contains(sum, "(1 without an image, 2 refused, 0 not loaded)"), sum)
	r.check("the others from images", strings.Contains(sum, ": 8 modules from images, 3 interpreted"), sum)
	return r.status()
}
