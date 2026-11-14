package virt

import "runtime/goos"

// PSCI function IDs (ARM DEN 0022).
const (
	psciSystemOff   = 0x84000008
	psciSystemReset = 0x84000009
)

// hvc calls the hypervisor (QEMU's PSCI conduit at EL1).
func hvc(fn uint64)

// PowerOff turns the machine off; QEMU exits.
func PowerOff() { hvc(psciSystemOff) }

func setExit() {
	goos.Exit = func(code int32) { PowerOff() }
}
