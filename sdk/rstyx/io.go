package rstyx

import (
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Pump is the argument that makes a command a pump: it copies fd 0 to fd
// 1, piece by piece, so a blocking read (a connection) can be waited for
// on the pipe the pump writes.
const Pump = "--pump"

// RunPump copies fd 0 to fd 1 until end of file or an error.
func RunPump() {
	buf := make([]byte, 16*1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n <= 0 || err != nil {
			return
		}
		if _, err := os.Stdout.Write(buf[:n]); err != nil {
			return
		}
	}
}

// ReadSome is one read of at most max bytes from fd; end of file and an
// error are both empty.
func ReadSome(fd int, max int) []byte {
	buf := make([]byte, max)
	n, err := syscall.Read(fd, buf)
	if err != nil || n < 0 {
		return nil
	}
	return buf[:n]
}

// WriteAll writes all of b to fd.
func WriteAll(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := syscall.Write(fd, b)
		if err != nil {
			return err
		}
		if n <= 0 {
			return ErrHungup
		}
		b = b[n:]
	}
	return nil
}

// WaitReadable waits for one of fds to be readable; ok is false on a
// timeout (a negative timeout waits for ever).
func WaitReadable(fds []int, ms int) (int, bool, error) {
	ops := make([][2]int, len(fds))
	for i, fd := range fds {
		ops[i] = [2]int{0, fd}
	}
	start := time.Now()
	i, err := sys.Wait(ops, ms)
	if err == nil && i >= 0 {
		return i, true, nil
	}
	if ms >= 0 && time.Since(start) >= time.Duration(ms)*time.Millisecond {
		return 0, false, nil
	}
	if ms == 0 || err == nil {
		return 0, false, nil
	}
	return 0, false, err
}

// Kill kills prog pid.
func Kill(pid int) {
	os.WriteFile("/prog/"+strconv.Itoa(pid)+"/ctl", []byte("kill"), 0)
}

// Children reaps this prog's children through its wait file.
type Children struct{ wait int }

// NewChildren opens the wait file; call it before starting a child.
func NewChildren() *Children {
	pid, _ := sys.Pctl(0, nil)
	fd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		fd = -1
	}
	return &Children{wait: fd}
}

// Reap waits until n children have exited, or for ms milliseconds.
func (c *Children) Reap(n, ms int) {
	if c.wait < 0 {
		return
	}
	end := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for range n {
		left := int(time.Until(end) / time.Millisecond)
		if left <= 0 {
			return
		}
		if _, ok, _ := WaitReadable([]int{c.wait}, left); !ok {
			return
		}
		ReadSome(c.wait, 4096)
	}
}
