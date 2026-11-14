// Package virt supports QEMU's aarch64 virt machine for the TamaGo runtime
// (GOOS=tamago): RAM at 0x40000000, a PL011 console, the generic timer
// and GICv3, with PSCI for power off. Importing it sets up the runtime
// hooks of runtime/goos.
package virt

import (
	_ "unsafe"

	"github.com/usbarmory/tamago/arm64"
)

// Memory map of QEMU virt (hw/arm/virt.c).
const (
	GICD_BASE  = 0x08000000
	GICR_BASE  = 0x080a0000
	UART0_BASE = 0x09000000
	RAM_BASE   = 0x40000000
)

var (
	// ARM64 is the boot CPU.
	ARM64 = &arm64.CPU{TimerOffset: 1}
)

//go:linkname ramStart runtime/goos.RamStart
var ramStart uint = RAM_BASE

// ramSize must not exceed the memory QEMU is given (-m).
//
//go:linkname ramSize runtime/goos.RamSize
var ramSize uint = 0x60000000 // 1.5 GiB

// RamSize is the memory the runtime manages.
func RamSize() uint { return ramSize }

//go:linkname ramStackOffset runtime/goos.RamStackOffset
var ramStackOffset uint = 0x100

// Init is the runtime's second hardware hook, after the MMU is on.
//
//go:linkname Init runtime/goos.Hwinit1
func Init() {
	ARM64.Init()
	ARM64.EnableCache()
	ARM64.InitGenericTimers(0, 0)
	setExit()
}

//go:linkname nanotime runtime/goos.Nanotime
func nanotime() int64 { return ARM64.GetTime() }
