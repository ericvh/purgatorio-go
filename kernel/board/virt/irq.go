package virt

import (
	"math/bits"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
)

// Interrupt numbers on QEMU virt: SPI n is GIC interrupt 32+n.
const (
	IRQ_UART0  = 32 + 1
	IRQ_VIRTIO = 32 + 16 // + the transport's slot
)

// irqSignal is the signal the interrupt entry relays to the goroutine
// that services interrupts.
const irqSignal = int(syscall.SIGTRAP)

var (
	irqOnce  sync.Once
	irqMu    sync.Mutex
	handlers = map[int]func(){}

	// irqPending holds the device interrupts taken and not yet
	// serviced, one bit each; the interrupt entry sets them.
	irqPending [32]uint32
)

// irqEntry is the IRQ exception entry (irq_arm64.s). On any CPU it
// acknowledges the interrupt and ends it at once. A wake SGI or the
// idle timer only marks the CPU woken; the sync SGI is acknowledged
// after an ISB (isync.go). A device interrupt is disabled at
// the distributor, marked pending in irqPending, and relayed as a signal
// to the goroutine that services interrupts, which may run on any CPU;
// the entry then sends the wake SGI to the other CPUs so an idle one
// runs it. No CPU is left with interrupts masked, which TamaGo's entry
// does until its goroutine unmasks them, a thing only one CPU can do.
func irqEntry()

// irqEntryPC returns irqEntry's address.
func irqEntryPC() uint64

// StartInterrupts starts the goroutine that services device interrupts.
// A handler runs in goroutine context and must not block: it quiets its
// device and wakes whoever waits for it. Its interrupt is enabled again
// when it returns.
func StartInterrupts() {
	irqOnce.Do(func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTRAP)
		go func() {
			for range c {
				isr()
			}
		}()
	})
}

// Handle installs f as the handler of device interrupt id and enables it.
func Handle(id int, f func()) {
	StartInterrupts()
	irqMu.Lock()
	handlers[id] = f
	gicEnable(id)
	irqMu.Unlock()
}

func isr() {
	for i := range irqPending {
		v := atomic.SwapUint32(&irqPending[i], 0)
		for v != 0 {
			n := bits.TrailingZeros32(v)
			v &^= 1 << n
			id := i*32 + n
			irqMu.Lock()
			f := handlers[id]
			irqMu.Unlock()
			if f == nil {
				continue // left disabled
			}
			f()
			gicUnmask(id)
		}
	}
}
