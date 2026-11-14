#include "textflag.h"

// Cache and TLB maintenance for codemem.go. The instructions the Go
// assembler does not name are written as words: DC CVAU, IC IVAU and
// TLBI VAAE1IS on R0, DSB ISH and ISB.

#define DC_CVAU_R0	WORD $0xd50b7b20
#define IC_IVAU_R0	WORD $0xd50b7520
#define TLBI_VAAE1IS_R0	WORD $0xd5088360
#define DSB_ISH		WORD $0xd5033b9f
#define ISB_SY		WORD $0xd5033fdf

// func ttbr0() uint64
TEXT ·ttbr0(SB),NOSPLIT,$0-8
	MRS	TTBR0_EL1, R0
	MOVD	R0, ret+0(FP)
	RET

// func ctr() uint64
TEXT ·ctr(SB),NOSPLIT,$0-8
	MRS	CTR_EL0, R0
	MOVD	R0, ret+0(FP)
	RET

// func cleanDcache(start, end, line uintptr)
TEXT ·cleanDcache(SB),NOSPLIT,$0-24
	MOVD	start+0(FP), R0
	MOVD	end+8(FP), R1
	MOVD	line+16(FP), R2
	SUB	$1, R2, R3
	BIC	R3, R0, R0
clean:
	DC_CVAU_R0
	ADD	R2, R0, R0
	CMP	R1, R0
	BLO	clean
	DSB_ISH
	RET

// func invalidateIcache(start, end, line uintptr)
TEXT ·invalidateIcache(SB),NOSPLIT,$0-24
	MOVD	start+0(FP), R0
	MOVD	end+8(FP), R1
	MOVD	line+16(FP), R2
	SUB	$1, R2, R3
	BIC	R3, R0, R0
inval:
	IC_IVAU_R0
	ADD	R2, R0, R0
	CMP	R1, R0
	BLO	inval
	DSB_ISH
	ISB_SY
	RET

// func tlbiPage(va uintptr)
TEXT ·tlbiPage(SB),NOSPLIT,$0-8
	MOVD	va+0(FP), R0
	LSR	$12, R0, R0
	TLBI_VAAE1IS_R0
	RET

// func barrier()
TEXT ·barrier(SB),NOSPLIT,$0-0
	DSB_ISH
	ISB_SY
	RET
