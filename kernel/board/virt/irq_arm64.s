#include "go_asm.h"
#include "textflag.h"

#define SY $0b1111

// func irqEntryPC() uint64
TEXT ·irqEntryPC(SB),NOSPLIT,$0-8
	MOVD	$·irqEntry(SB), R0
	MOVD	R0, ret+0(FP)
	RET

// The IRQ entry; TamaGo's vector table jumps here through R18_PLATFORM (see
// patchVectors). It runs with interrupts masked, on this CPU's
// interrupt stack, whose top is the first word of the pcpu that
// TPIDR_EL1 points to.
TEXT ·irqEntry(SB),NOSPLIT|NOFRAME,$0
	// switch to the interrupt stack, keeping the interrupted one in R18_PLATFORM
	MRS	TPIDR_EL1, R18_PLATFORM
	MOVD	pcpu_stackTop(R18_PLATFORM), R18_PLATFORM
	SUB	R18_PLATFORM, RSP, R18_PLATFORM
	SUB	R18_PLATFORM, RSP, RSP
	ADD	R18_PLATFORM, RSP, R18_PLATFORM

	SUB	$(17*16), RSP
	MOVD	R18_PLATFORM, (16*16+8)(RSP)
	STP	(R0, R1), (1*16)(RSP)
	STP	(R2, R3), (2*16)(RSP)
	STP	(R4, R5), (3*16)(RSP)
	STP	(R6, R7), (4*16)(RSP)
	STP	(R8, R9), (5*16)(RSP)
	STP	(R10, R11), (6*16)(RSP)
	STP	(R12, R13), (7*16)(RSP)
	STP	(R14, R15), (8*16)(RSP)
	STP	(R16, R17), (9*16)(RSP)
	STP	(R19, R20), (10*16)(RSP)
	STP	(R21, R22), (11*16)(RSP)
	STP	(R23, R24), (12*16)(RSP)
	STP	(R25, R26), (13*16)(RSP)
	STP	(R27, g), (14*16)(RSP)
	STP	(R29, R30), (15*16)(RSP)
	MOVD	NZCV, R0
	MOVD	R0, (16*16)(RSP)

	MRS	ICC_IAR1_EL1, R0
	AND	$0xffffff, R0
	CMP	$1020, R0
	BHS	done		// spurious
	CMP	$const_sgiSync, R0
	BEQ	isync
	CMP	$16, R0
	BLO	wake		// another SGI
	CMP	$const_irqVtimer, R0
	BEQ	timer
	AND	$31, R0, R2
	MOVD	$1, R3
	LSL	R2, R3, R3	// R3: the interrupt's bit in its word
	LSR	$5, R0, R1	// R1: the word
	CMP	$32, R0
	BLO	ppi

	// a device interrupt: disable it, end it, mark it pending
	MOVD	$(const_GICD_BASE+const_gicdICENABLER), R4
	ADD	R1<<2, R4, R4
	MOVW	R3, (R4)
	DSB	SY
	MSR	R0, ICC_EOIR1_EL1
	MOVD	$·irqPending(SB), R4
	ADD	R1<<2, R4, R4
pend:
	LDAXRW	(R4), R5
	ORRW	R3, R5, R5
	STLXRW	R5, (R4), R6
	CBNZ	R6, pend

	MOVD	$const_irqSignal, R0
	MOVD	R0, 8(RSP)
	CALL	os∕signal·Relay(SB)

	// wake the other CPUs (ICC_SGI1R_EL1.IRM), so an idle one services it
	DSB	SY
	MOVD	$(1<<40 | const_sgiWake<<24), R0
	MSR	R0, ICC_SGI1R_EL1
	ISB	SY
	B	done

ppi:
	// a PPI nobody asked for: disable it at this CPU's redistributor
	MRS	TPIDR_EL1, R4
	MOVD	pcpu_gicr(R4), R4
	ADD	$const_gicrICENABLE, R4, R4
	MOVW	R3, (R4)
	DSB	SY
	MSR	R0, ICC_EOIR1_EL1
	B	done

isync:
	// SyncInstructions: the exception entry and an ISB synchronize
	// this CPU's context; say so (as ackSync)
	MOVD	$·syncGen(SB), R4
	LDAR	(R4), R5
	CBZ	R5, isyncEnd
	ISB	SY
	MRS	TPIDR_EL1, R4
	ADD	$pcpu_synced, R4, R4
	STLR	R5, (R4)
isyncEnd:
	MSR	R0, ICC_EOIR1_EL1
	B	done

timer:
	// the idle timer: stop it
	MOVD	$0, R1
	MSR	R1, CNTV_CTL_EL0
	ISB	SY
wake:
	MSR	R0, ICC_EOIR1_EL1
	MRS	TPIDR_EL1, R4
	MOVW	$1, R5
	MOVW	R5, pcpu_woken(R4)

done:
	MOVD	(16*16)(RSP), R0
	MOVD	R0, NZCV
	LDP	(15*16)(RSP), (R29, R30)
	LDP	(14*16)(RSP), (R27, g)
	LDP	(13*16)(RSP), (R25, R26)
	LDP	(12*16)(RSP), (R23, R24)
	LDP	(11*16)(RSP), (R21, R22)
	LDP	(10*16)(RSP), (R19, R20)
	LDP	(9*16)(RSP), (R16, R17)
	LDP	(8*16)(RSP), (R14, R15)
	LDP	(7*16)(RSP), (R12, R13)
	LDP	(6*16)(RSP), (R10, R11)
	LDP	(5*16)(RSP), (R8, R9)
	LDP	(4*16)(RSP), (R6, R7)
	LDP	(3*16)(RSP), (R4, R5)
	LDP	(2*16)(RSP), (R2, R3)
	LDP	(1*16)(RSP), (R0, R1)
	MOVD	(16*16+8)(RSP), R18_PLATFORM
	MOVD	R18_PLATFORM, RSP
	ERET
