package virt

import "sync"

// Interrupt numbers on QEMU virt: SPI n is GIC interrupt 32+n.
const (
	IRQ_UART0  = 32 + 1
	IRQ_VIRTIO = 32 + 16 // + the transport's slot
)

var (
	irqOnce  sync.Once
	irqMu    sync.Mutex
	handlers = map[int]func(){}
)

// StartInterrupts brings up the GIC and the goroutine that services
// interrupts. TamaGo relays an interrupt to that goroutine as a signal,
// with interrupts masked until it has run the handlers, so a handler
// runs in goroutine context and must not block: it quiets its device
// and wakes whoever waits for it.
func StartInterrupts() {
	irqOnce.Do(func() {
		gicInit()
		go ARM64.ServiceInterrupts(isr)
	})
}

// Handle installs f as the handler of interrupt id and enables it.
func Handle(id int, f func()) {
	StartInterrupts()
	irqMu.Lock()
	handlers[id] = f
	irqMu.Unlock()
	gicEnable(id)
}

func isr() {
	for {
		id := gicNext()
		if id >= spurious {
			return
		}
		irqMu.Lock()
		f := handlers[id]
		irqMu.Unlock()
		if f != nil {
			f()
		}
	}
}
