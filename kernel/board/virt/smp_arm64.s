#include "go_asm.h"
#include "textflag.h"

#define SY $0b1111

// func apEntryPC() uint64
TEXT ·apEntryPC(SB),NOSPLIT,$0-8
	MOVD	$·apEntry(SB), R0
	MOVD	R0, ret+0(FP)
	RET

// A secondary CPU starts here from PSCI CPU_ON, at EL1 with the MMU and
// caches off, with its pcpu's address (physical, which is virtual too:
// the boot CPU's tables map memory flat) in R0.
TEXT ·apEntry(SB),NOSPLIT|NOFRAME,$0
	MSR	$0b1111, DAIFSet

	// floating point and SIMD
	MRS	CPACR_EL1, R1
	ORR	$(3<<20), R1
	MSR	R1, CPACR_EL1
	ISB	SY

	// the boot CPU's translation tables and system control
	MOVD	pcpu_mair(R0), R1
	MSR	R1, MAIR_EL1
	MOVD	pcpu_tcr(R0), R1
	MSR	R1, TCR_EL1
	MOVD	pcpu_ttbr0(R0), R1
	MSR	R1, TTBR0_EL1
	ISB	SY
	TLBI	VMALLE1
	DSB	SY
	ISB	SY
	MOVD	pcpu_sctlr(R0), R1
	MSR	R1, SCTLR_EL1
	ISB	SY

	MOVD	pcpu_vbar(R0), R1
	MSR	R1, VBAR_EL1
	MSR	R0, TPIDR_EL1

	// the idle timer off, then the GIC CPU interface
	MOVD	$0, R1
	MSR	R1, CNTV_CTL_EL0
	MOVD	$1, R1
	MSR	R1, ICC_SRE_EL1
	ISB	SY
	MOVD	$0xff, R1
	MSR	R1, ICC_PMR_EL1
	MOVD	$0, R1
	MSR	R1, ICC_BPR1_EL1
	MOVD	$1, R1
	MSR	R1, ICC_IGRPEN1_EL1
	ISB	SY

	// up: wait for a task
	MOVW	$1, R1
	ADD	$pcpu_up, R0, R2
	STLRW	R1, (R2)
	ADD	$pcpu_taskPC, R0, R2
wait:
	WFE
	LDAR	(R2), R3
	CBZ	R3, wait

	MOVD	pcpu_taskSP(R0), R1
	MOVD	pcpu_taskG(R0), g
	MOVD	R1, RSP
	MOVD	$0, R29
	MOVD	$0, R30
	MSR	$0b0011, DAIFClr
	B	(R3)

// func self() *pcpu
TEXT ·self(SB),NOSPLIT,$0-8
	MRS	TPIDR_EL1, R0
	MOVD	R0, ret+0(FP)
	RET

// func setSelf(c *pcpu)
TEXT ·setSelf(SB),NOSPLIT,$0-8
	MOVD	c+0(FP), R0
	MSR	R0, TPIDR_EL1
	RET

// func psci(fn, a1, a2, a3 uint64) int64
TEXT ·psci(SB),NOSPLIT,$0-40
	MOVD	fn+0(FP), R0
	MOVD	a1+8(FP), R1
	MOVD	a2+16(FP), R2
	MOVD	a3+24(FP), R3
	HVC	$0
	MOVD	R0, ret+32(FP)
	RET

// func cleanDcache(addr, n uint64)
TEXT ·cleanDcache(SB),NOSPLIT,$0-16
	MOVD	addr+0(FP), R0
	MOVD	n+8(FP), R1
	ADD	R0, R1, R1
	AND	$~15, R0
clean:
	DC	CVAC, R0
	ADD	$16, R0
	CMP	R1, R0
	BLO	clean
	DSB	SY
	RET

// func syncIcache(addr uint64)
TEXT ·syncIcache(SB),NOSPLIT,$0-8
	MOVD	addr+0(FP), R0
	DC	CVAU, R0
	DSB	SY
	WORD	$0xd508711f	// IC IALLUIS
	DSB	SY
	ISB	SY
	RET

// func sev()
TEXT ·sev(SB),NOSPLIT,$0
	DSB	SY
	SEV
	RET

// func sendSGI(v uint64)
TEXT ·sendSGI(SB),NOSPLIT,$0-8
	MOVD	v+0(FP), R0
	DSB	SY
	MSR	R0, ICC_SGI1R_EL1
	ISB	SY
	RET

// func waitIRQ(woken *uint32, ticks uint64)
//
// With interrupts masked, it sets the virtual timer to fire in ticks and,
// unless *woken is already set, waits for an interrupt. WFI returns
// for a pending interrupt even while masked, so one that comes after the
// check is not missed; it is taken when interrupts are unmasked.
TEXT ·waitIRQ(SB),NOSPLIT,$0-16
	MOVD	woken+0(FP), R0
	MOVD	ticks+8(FP), R1
	MSR	$0b0011, DAIFSet
	MSR	R1, CNTV_TVAL_EL0
	MOVD	$1, R2
	MSR	R2, CNTV_CTL_EL0
	ISB	SY
	LDARW	(R0), R3
	CBNZW	R3, out
	WFI
out:
	MOVD	$0, R2
	MSR	R2, CNTV_CTL_EL0
	ISB	SY
	MSR	$0b0011, DAIFClr
	RET

// func readCNTFRQ() uint64
TEXT ·readCNTFRQ(SB),NOSPLIT,$0-8
	MRS	CNTFRQ_EL0, R0
	MOVD	R0, ret+0(FP)
	RET

// func readSysregs(c *pcpu)
TEXT ·readSysregs(SB),NOSPLIT,$0-8
	MOVD	c+0(FP), R0
	MRS	MAIR_EL1, R1
	MOVD	R1, pcpu_mair(R0)
	MRS	TCR_EL1, R1
	MOVD	R1, pcpu_tcr(R0)
	MRS	TTBR0_EL1, R1
	MOVD	R1, pcpu_ttbr0(R0)
	MRS	SCTLR_EL1, R1
	MOVD	R1, pcpu_sctlr(R0)
	MRS	VBAR_EL1, R1
	MOVD	R1, pcpu_vbar(R0)
	RET
