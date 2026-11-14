// Package kernel is the native kernel's platform: the hardware behind the
// portable core's platform.Platform interface.
package kernel

import (
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/kernel/board/virt"
	"github.com/ericvh/purgatorio-go/platform"
)

// Platform is QEMU virt as the core sees it.
type Platform struct {
	boot  time.Time
	epoch int64 // RTC seconds at boot
	cons  *console
}

// New starts the platform: the console reader and the clock.
func New() *Platform {
	p := &Platform{boot: time.Now(), epoch: virt.Seconds(), cons: newConsole()}
	return p
}

func (p *Platform) Now() int64 {
	return p.epoch*1e9 + int64(time.Since(p.boot))
}

func (p *Platform) Entropy(b []byte) { fillRandom(b) }

func (p *Platform) Console() platform.Console { return p.cons }

func (p *Platform) User() string { return "inferno" }

func (p *Platform) Sysname() string { return "purgatorio" }

// console is the PL011: output is written straight out; input is polled
// by a goroutine into a buffer, which reads wait on.
type console struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte
}

func newConsole() *console {
	c := &console{}
	c.cond = sync.NewCond(&c.mu)
	go c.poll()
	return c
}

func (c *console) poll() {
	for {
		got := false
		for {
			b, ok := virt.ReadByte()
			if !ok {
				break
			}
			if b == '\r' {
				b = '\n'
			}
			c.mu.Lock()
			c.buf = append(c.buf, b)
			c.mu.Unlock()
			got = true
		}
		if got {
			c.cond.Broadcast()
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (c *console) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.buf) == 0 {
		c.cond.Wait()
	}
	n := copy(b, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *console) Write(b []byte) (int, error) {
	for _, x := range b {
		if x == '\n' {
			virt.Putc('\r')
		}
		virt.Putc(x)
	}
	return len(b), nil
}

func (c *console) SetRaw(bool) error { return nil }
