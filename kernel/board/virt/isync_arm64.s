#include "go_asm.h"
#include "textflag.h"

#define SY $0b1111

// func isb()
TEXT ·isb(SB),NOSPLIT,$0
	ISB	SY
	RET

// func ackSync()
//
// The CPU it runs on synchronizes its context for the latest request:
// it reads syncGen, executes an ISB (ordered after the load by the
// branch on its value) and stores what it read in its pcpu's synced.
// irqEntry does the same for sgiSync.
TEXT ·ackSync(SB),NOSPLIT,$0
	MOVD	$·syncGen(SB), R0
	LDAR	(R0), R1
	CBZ	R1, none
	ISB	SY
	MRS	TPIDR_EL1, R0
	ADD	$pcpu_synced, R0, R0
	STLR	R1, (R0)
none:
	RET
