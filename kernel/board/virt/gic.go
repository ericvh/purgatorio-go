package virt

// The GICv3 in group 1 (ARM IHI 0069). TamaGo's arm64/gic uses group 0,
// which KVM's virtual GICv3 does not deliver; group 1 works under KVM
// and under QEMU's emulated GIC alike, and arrives as an IRQ.

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

	gicrWAKER    = 0x0014
	gicrSGI      = 0x10000
	gicrIGROUPR0 = gicrSGI + 0x0080
	gicrISENABLE = gicrSGI + 0x0100
	gicrIPRIO    = gicrSGI + 0x0400

	wakerProcessorSleep = 1 << 1
	wakerChildrenAsleep = 1 << 2

	spurious = 1020
	priority = 0xa0
)

func gicSysInit()
func gicAck() uint32
func gicEOI(id uint32)
func readMPIDR() uint64

func write64(addr uint32, v uint64) {
	write32(addr, uint32(v))
	write32(addr+4, uint32(v>>32))
}

// gicInit wakes this CPU's redistributor, puts every interrupt in group
// 1, disabled, and enables the distributor and the CPU interface.
func gicInit() {
	write32(GICR_BASE+gicrWAKER, read32(GICR_BASE+gicrWAKER)&^wakerProcessorSleep)
	for read32(GICR_BASE+gicrWAKER)&wakerChildrenAsleep != 0 {
	}
	write32(GICD_BASE+gicdCTLR, 0)
	for read32(GICD_BASE+gicdCTLR)&ctlrRWP != 0 {
	}
	lines := (read32(GICD_BASE+gicdTYPER)&0x1f + 1) * 32
	for n := uint32(32); n < lines; n += 32 {
		write32(GICD_BASE+gicdICENABLER+n/8, 0xffffffff)
		write32(GICD_BASE+gicdICPENDR+n/8, 0xffffffff)
		write32(GICD_BASE+gicdIGROUPR+n/8, 0xffffffff)
	}
	write32(GICR_BASE+gicrIGROUPR0, 0xffffffff)
	write32(GICD_BASE+gicdCTLR, ctlrARE|ctlrEnableGrp1)
	for read32(GICD_BASE+gicdCTLR)&ctlrRWP != 0 {
	}
	gicSysInit()
}

// gicEnable routes interrupt id to this CPU and enables it.
func gicEnable(id int) {
	n := uint32(id)
	if id >= 32 {
		mpidr := readMPIDR()
		aff := mpidr&0xffffff | (mpidr>>32&0xff)<<32
		write64(GICD_BASE+gicdIROUTER+8*n, aff)
		prio := GICD_BASE + gicdIPRIORITY + n&^3
		shift := 8 * (n & 3)
		write32(prio, read32(prio)&^(0xff<<shift)|priority<<shift)
		write32(GICD_BASE+gicdISENABLER+4*(n/32), 1<<(n%32))
		return
	}
	prio := GICR_BASE + gicrIPRIO + n&^3
	shift := 8 * (n & 3)
	write32(prio, read32(prio)&^(0xff<<shift)|priority<<shift)
	write32(GICR_BASE+gicrISENABLE, 1<<n)
}

// gicNext acknowledges the highest pending interrupt and ends it,
// returning its number or a value of spurious or more when there is
// none.
func gicNext() int {
	id := gicAck() & 0xffffff
	if id < spurious {
		gicEOI(id)
	}
	return int(id)
}
