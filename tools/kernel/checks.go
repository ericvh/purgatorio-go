package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
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
	disks  []string
	netdev string
}

// console runs QEMU, passing each console line to line, which may write
// to the machine's console through in. It returns the whole output.
func console(q *exec.Cmd, line func(s string, in io.Writer)) (string, error) {
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
	timer := time.AfterFunc(10*time.Minute, func() { _ = q.Process.Kill() })
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
// the loopback interface (the emu's net test guest), a connection out to
// a server on the host (10.0.2.2), and one in from the host through a
// forwarded port.
type netChecker struct {
	ln      net.Listener
	hostGot chan string
	fwd     int
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
	init := fmt.Sprintf("echo @@ loopback; /bin/test/net.wasm; "+
		"echo @@ dial; dial -A tcp!10.0.2.2!%d {echo ping from the kernel; read >[1=2]}; "+
		"listen -A tcp!*!%d {echo hello from the kernel} & "+
		"echo @@ listening; read < /dev/cons; echo @@ end", port, guestPort)
	return checkConfig{
		init:   init,
		cmds:   []string{"test/net", "dial", "listen", "read"},
		netdev: fmt.Sprintf("hostfwd=tcp:127.0.0.1:%d-:%d", c.fwd, guestPort),
	}
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
	out, err := console(q, func(s string, w io.Writer) {
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
	var got string
	select {
	case got = <-c.hostGot:
	default:
	}
	r.check("dial out to the host", got == "ping from the kernel\n" && strings.Contains(out, "pong from the host"),
		fmt.Sprintf("host got %q", got))
	r.check("connection in from the host", in == "hello from the kernel\n", fmt.Sprintf("host read %q", in))
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
	out, err := console(q, func(string, io.Writer) {})
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
