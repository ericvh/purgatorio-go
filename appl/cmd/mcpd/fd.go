package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Raw fd I/O. An *os.File closes its fd when it is collected, and mcpd
// juggles many fds it hands to other progs, so it reads and writes fds
// directly.

// rd reads from fd; an error is the prog's error string.
func rd(fd int, b []byte) (int, error) {
	for {
		n, err := syscall.Read(fd, b)
		if err == syscall.EINTR || err == syscall.EAGAIN {
			continue
		}
		if err != nil {
			return 0, errstr()
		}
		return n, nil
	}
}

// writeAll writes all of b to fd.
func writeAll(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := syscall.Write(fd, b)
		if err == syscall.EINTR || err == syscall.EAGAIN {
			continue
		}
		if err != nil {
			return errstr()
		}
		if n <= 0 {
			return sys.Error("write error")
		}
		b = b[n:]
	}
	return nil
}

func closefd(fd int) {
	if fd >= 0 {
		_ = syscall.Close(fd)
	}
}

// readAll reads fd to its end.
func readAll(fd int) ([]byte, error) {
	var out []byte
	buf := make([]byte, 8192)
	for {
		n, err := rd(fd, buf)
		if err != nil {
			return out, err
		}
		if n == 0 {
			return out, nil
		}
		out = append(out, buf[:n]...)
	}
}

// errstr is the prog's error string as an error.
func errstr() error {
	e := sys.Errstr()
	if e == "" {
		e = "i/o error"
	}
	return sys.Error(e)
}

// fail is "what: why".
func fail(what string, err error) error { return sys.Error(what + ": " + err.Error()) }

// readFile reads a whole file by name.
func readFile(path string) (string, error) {
	fd, err := sys.Open(path, sys.OREAD)
	if err != nil {
		return "", err
	}
	defer closefd(fd)
	b, err := readAll(fd)
	return string(b), err
}

// writeFile writes s to an existing file, as one write.
func writeFile(path, s string) error {
	fd, err := sys.Open(path, sys.OWRITE)
	if err != nil {
		return err
	}
	defer closefd(fd)
	return writeAll(fd, []byte(s))
}

// waitReadable is purgatorio.wait on fds that would read without
// blocking: the index of one, or -1 at the timeout (ms, -1 forever).
func waitReadable(fds []int, timeout int) (int, error) {
	ops := make([][2]int, len(fds))
	for i, fd := range fds {
		ops[i] = [2]int{sys.WaitRead, fd}
	}
	before := time.Now()
	sys.Werrstr("")
	r, _ := sys.Wait(ops, timeout)
	if r >= 0 {
		return r, nil
	}
	// A timeout and an error both return -1; only an error comes back
	// early.
	if timeout >= 0 && time.Since(before) >= time.Duration(timeout)*time.Millisecond {
		return -1, nil
	}
	e := sys.Errstr()
	if timeout == 0 || e == "" {
		return -1, nil
	}
	return -1, sys.Error(e)
}

// msUntil is milliseconds from now until at, for a wait; -1 is forever.
func msUntil(at time.Time) int {
	if at.IsZero() {
		return -1
	}
	d := time.Until(at)
	if d < 0 {
		d = 0
	}
	// Round up, so the wait does not end just short of at.
	ms := d.Milliseconds() + 1
	if ms > 1<<31-1 {
		ms = 1<<31 - 1
	}
	return int(ms)
}

// mypid is this prog's pid.
func mypid() int {
	pid, _ := sys.Pctl(0, nil)
	return pid
}

// parseWait splits an exit message, `pid "name":status`.
func parseWait(msg string) (int, string, bool) {
	sp := strings.IndexByte(msg, ' ')
	if sp < 0 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(msg[:sp])
	if err != nil {
		return 0, "", false
	}
	rest := msg[sp+1:]
	i := strings.Index(rest, "\":")
	if i < 0 {
		return 0, "", false
	}
	return pid, rest[i+2:], true
}

// commandPath finds a command as sh does: /bin/<name>.wasm unless the
// name starts with /, ./, ../ or #.
func commandPath(cmd string) string {
	if strings.HasPrefix(cmd, "/") || strings.HasPrefix(cmd, "#") ||
		strings.HasPrefix(cmd, "./") || strings.HasPrefix(cmd, "../") {
		return cmd
	}
	return "/bin/" + cmd + ".wasm"
}

// stderr writes to fd 2.
func stderr(s string) { _, _ = os.Stderr.WriteString(s) }
