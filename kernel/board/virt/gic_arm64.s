#include "textflag.h"

#define SY $0b1111

// func gicSysInit()
TEXT ·gicSysInit(SB),NOSPLIT,$0
	MOVD	$1, R0
	MSR	R0, ICC_SRE_EL1
	ISB	SY
	MOVD	$0xff, R0
	MSR	R0, ICC_PMR_EL1
	MOVD	$0, R0
	MSR	R0, ICC_BPR1_EL1
	MOVD	$1, R0
	MSR	R0, ICC_IGRPEN1_EL1
	ISB	SY
	RET

// func gicAck() uint32
TEXT ·gicAck(SB),NOSPLIT,$0-4
	MRS	ICC_IAR1_EL1, R0
	DSB	SY
	MOVW	R0, ret+0(FP)
	RET

// func gicEOI(id uint32)
TEXT ·gicEOI(SB),NOSPLIT,$0-4
	MOVWU	id+0(FP), R0
	MSR	R0, ICC_EOIR1_EL1
	ISB	SY
	RET

// func readMPIDR() uint64
TEXT ·readMPIDR(SB),NOSPLIT,$0-8
	MRS	MPIDR_EL1, R0
	MOVD	R0, ret+0(FP)
	RET

