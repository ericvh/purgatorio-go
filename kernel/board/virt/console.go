package virt

import (
	"sync/atomic"
	_ "unsafe"
)

// PL011 registers (ARM DDI 0183).
const (
	uartDR = 0x00
	uartFR = 0x18
	frTXFF = 1 << 5 // transmit FIFO full
	frRXFE = 1 << 4 // receive FIFO empty
)

// conOwner is the CPU writing to the console, plus one, or 0. A write
// holds the console for all its bytes, so writes from different CPUs
// do not interleave. The owner is named by MPIDR, which is valid from
// reset, and a CPU that already holds it (a fault in a write) goes on.
var conOwner atomic.Uint64

func lockConsole() bool {
	me := readMPIDR()&0xff00ffffff + 1
	if conOwner.Load() == me {
		return false
	}
	for !conOwner.CompareAndSwap(0, me) {
	}
	return true
}

func unlockConsole(took bool) {
	if took {
		conOwner.Store(0)
	}
}

func putc(c byte) {
	for read32(UART0_BASE+uartFR)&frTXFF != 0 {
	}
	write32(UART0_BASE+uartDR, uint32(c))
}

//go:linkname printk runtime/goos.Printk
func printk(c byte) {
	took := lockConsole()
	putc(c)
	unlockConsole(took)
}

// Write writes b to the console as one piece, with each newline
// preceded by a carriage return.
func Write(b []byte) {
	took := lockConsole()
	for _, c := range b {
		if c == '\n' {
			putc('\r')
		}
		putc(c)
	}
	unlockConsole(took)
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
