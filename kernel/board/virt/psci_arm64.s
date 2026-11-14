#include "textflag.h"

// func hvc(fn uint64)
TEXT ·hvc(SB),NOSPLIT,$0-8
	MOVD	fn+0(FP), R0
	HVC	$0
	RET
