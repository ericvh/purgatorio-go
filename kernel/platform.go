// Package kernel is the native kernel's platform: the hardware behind the
// portable core's platform.Platform interface.
package kernel

import (
	"runtime/debug"
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/kernel/board/virt"
	"github.com/ericvh/purgatorio-go/kernel/netstack"
	"github.com/ericvh/purgatorio-go/kernel/virtio"
	"github.com/ericvh/purgatorio-go/platform"
)

// Platform is QEMU virt as the core sees it.
type Platform struct {
	boot  time.Time
	epoch int64 // RTC seconds at boot
	cons  *console
	rng   *virtio.RNG
	disks []*virtio.Block
	net   *netstack.Stack
	ether *virtio.Net
}

// New starts the platform: interrupts, the console, the clock and the
// virtio devices.
func New() *Platform {
	// The runtime does not know how much memory it has: without a limit
	// the heap grows to twice what is live before a collection, which
	// can be more than there is.
	debug.SetMemoryLimit(int64(virt.RamSize()) * 3 / 4)
	virt.StartInterrupts()
	p := &Platform{boot: time.Now(), epoch: virt.Seconds(), cons: newConsole()}
	p.devices()
	return p
}

func (p *Platform) Now() int64 {
	return p.epoch*1e9 + int64(time.Since(p.boot))
}

// Entropy reads the virtio entropy device when there is one.
func (p *Platform) Entropy(b []byte) {
	if p.rng != nil {
		_, _ = p.rng.Read(b)
		return
	}
	fillRandom(b)
}

// Net is the IP stack #I serves.
func (p *Platform) Net() platform.NetStack {
	if p.net == nil {
		return nil
	}
	return p.net
}

func (p *Platform) Console() platform.Console { return p.cons }

func (p *Platform) User() string { return "inferno" }

func (p *Platform) Sysname() string { return "purgatorio" }

// console is the PL011: output is written straight out; input is taken
// into a buffer on the receive interrupt, and reads wait on the buffer.
type console struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte
}

func newConsole() *console {
	c := &console{}
	c.cond = sync.NewCond(&c.mu)
	virt.ConsoleInterrupt(c.input)
	c.input()
	return c
}

// input drains the receive FIFO; it runs on the interrupt.
func (c *console) input() {
	got := false
	c.mu.Lock()
	for {
		b, ok := virt.ReadByte()
		if !ok {
			break
		}
		if b == '\r' {
			b = '\n'
		}
		c.buf = append(c.buf, b)
		got = true
	}
	c.mu.Unlock()
	if got {
		c.cond.Broadcast()
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
	virt.Write(b)
	return len(b), nil
}

func (c *console) SetRaw(bool) error { return nil }
