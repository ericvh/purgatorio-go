package virt

// The GICv3 in group 1 (ARM IHI 0069). TamaGo's arm64/gic uses group 0,
// which KVM's virtual GICv3 does not deliver; group 1 works under KVM
// and under QEMU's emulated GIC alike, and arrives as an IRQ.
//
// Every CPU has a redistributor, for its SGIs and PPIs, and a CPU
// interface in system registers. Device interrupts (SPIs) are routed to
// the boot CPU. Each CPU enables two interrupts of its own: SGI 0, which
// another CPU sends to wake it (see Wake), and the virtual timer, which
// bounds how long it sleeps when idle (see idle).

const (
	gicdCTLR      = 0x0000
	gicdIGROUPR   = 0x0080
	gicdISENABLER = 0x0100
	gicdICENABLER = 0x0180
	gicdICPENDR   = 0x0280
	gicdIPRIORITY = 0x0400
	gicdTYPER     = 0x0004
	gicdIROUTER   = 0x6000

	ctlrEnableGrp1 = 1 << 1
	ctlrARE        = 1 << 4
	ctlrRWP        = 1 << 31

	gicrTYPER     = 0x0008
	gicrWAKER     = 0x0014
	gicrSGI       = 0x10000
	gicrIGROUPR0  = gicrSGI + 0x0080
	gicrISENABLE  = gicrSGI + 0x0100
	gicrICENABLE  = gicrSGI + 0x0180
	gicrIPRIO     = gicrSGI + 0x0400
	gicrTyperLast = 1 << 4
	gicrTyperVLPI = 1 << 1

	wakerProcessorSleep = 1 << 1
	wakerChildrenAsleep = 1 << 2

	priority = 0xa0

	// sgiWake is the SGI one CPU sends another to wake it.
	sgiWake = 0
	// irqVtimer is the virtual timer's PPI.
	irqVtimer = 27
)

func gicSysInit()
func readMPIDR() uint64

func write64(addr uint32, v uint64) {
	write32(addr, uint32(v))
	write32(addr+4, uint32(v>>32))
}

// affinity is an MPIDR's affinity fields as GICR_TYPER holds them: Aff3
// in bits 24-31, then Aff2, Aff1 and Aff0.
func affinity(mpidr uint64) uint32 {
	return uint32(mpidr&0xffffff | (mpidr>>32&0xff)<<24)
}

// redistributors calls f with the base and affinity of each
// redistributor, one per CPU, in order, until f returns false.
func redistributors(f func(base uint32, aff uint32) bool) {
	base := uint32(GICR_BASE)
	for range maxCPUs {
		typer := read32(base + gicrTYPER)
		if !f(base, read32(base+gicrTYPER+4)) || typer&gicrTyperLast != 0 {
			return
		}
		if typer&gicrTyperVLPI != 0 {
			base += 0x40000
		} else {
			base += 0x20000
		}
	}
}

// gicrInit wakes a CPU's redistributor, puts its SGIs and PPIs in group
// 1, and enables the wake SGI and the virtual timer. Any CPU may do this
// for any other: the redistributor is memory mapped.
func gicrInit(base uint32) {
	write32(base+gicrWAKER, read32(base+gicrWAKER)&^wakerProcessorSleep)
	for read32(base+gicrWAKER)&wakerChildrenAsleep != 0 {
	}
	write32(base+gicrICENABLE, 0xffffffff)
	write32(base+gicrIGROUPR0, 0xffffffff)
	for n := uint32(0); n < 32; n += 4 {
		write32(base+gicrIPRIO+n, priority*0x01010101)
	}
	write32(base+gicrISENABLE, 1<<sgiWake|1<<irqVtimer)
}

// gicInit sets up the distributor, with every SPI in group 1 and
// disabled, and the boot CPU's redistributor and CPU interface.
func gicInit(boot *pcpu) {
	write32(GICD_BASE+gicdCTLR, 0)
	for read32(GICD_BASE+gicdCTLR)&ctlrRWP != 0 {
	}
	lines := (read32(GICD_BASE+gicdTYPER)&0x1f + 1) * 32
	for n := uint32(32); n < lines; n += 32 {
		write32(GICD_BASE+gicdICENABLER+n/8, 0xffffffff)
		write32(GICD_BASE+gicdICPENDR+n/8, 0xffffffff)
		write32(GICD_BASE+gicdIGROUPR+n/8, 0xffffffff)
	}
	write32(GICD_BASE+gicdCTLR, ctlrARE|ctlrEnableGrp1)
	for read32(GICD_BASE+gicdCTLR)&ctlrRWP != 0 {
	}
	gicrInit(uint32(boot.gicr))
	gicSysInit()
}

// gicEnable routes SPI id to the boot CPU and enables it. Any CPU may
// call it; the caller holds irqMu.
func gicEnable(id int) {
	if id < 32 {
		return // SGIs and PPIs belong to the CPUs (gicrInit)
	}
	n := uint32(id)
	aff := uint64(affinity(cpus[0].mpidr))
	write64(GICD_BASE+gicdIROUTER+8*n, aff&0xffffff|aff>>24<<32)
	prio := GICD_BASE + gicdIPRIORITY + n&^3
	shift := 8 * (n & 3)
	write32(prio, read32(prio)&^(0xff<<shift)|priority<<shift)
	gicUnmask(id)
}

// gicUnmask enables SPI id, again after the interrupt entry disabled it.
func gicUnmask(id int) {
	n := uint32(id)
	write32(GICD_BASE+gicdISENABLER+4*(n/32), 1<<(n%32))
}
