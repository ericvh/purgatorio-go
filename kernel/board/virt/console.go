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
