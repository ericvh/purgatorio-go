package virt

import _ "unsafe"

// PL011 registers (ARM DDI 0183).
const (
	uartDR = 0x00
	uartFR = 0x18
	frTXFF = 1 << 5 // transmit FIFO full
	frRXFE = 1 << 4 // receive FIFO empty
)

//go:linkname printk runtime/goos.Printk
func printk(c byte) {
	for read32(UART0_BASE+uartFR)&frTXFF != 0 {
	}
	write32(UART0_BASE+uartDR, uint32(c))
}

// ReadByte returns a byte from the console, or false when none is
// waiting.
func ReadByte() (byte, bool) {
	if read32(UART0_BASE+uartFR)&frRXFE != 0 {
		return 0, false
	}
	return byte(read32(UART0_BASE + uartDR)), true
}

// Putc writes one byte to the console.
func Putc(c byte) { printk(c) }

// PL011 interrupt registers.
const (
	uartIMSC = 0x38
	uartICR  = 0x44
	imscRX   = 1 << 4 // receive
	imscRT   = 1 << 6 // receive timeout
)

// ConsoleInterrupt runs f, from the interrupt goroutine, whenever input
// arrives; f should drain it with ReadByte.
func ConsoleInterrupt(f func()) {
	// The interrupt is cleared before the FIFO is drained: a byte that
	// arrives after the drain raises it again, where clearing it after
	// the drain could lose that byte's interrupt and leave it unread.
	Handle(IRQ_UART0, func() {
		write32(UART0_BASE+uartICR, imscRX|imscRT)
		f()
	})
	write32(UART0_BASE+uartIMSC, imscRX|imscRT)
}
