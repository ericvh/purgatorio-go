#include "textflag.h"

TEXT ·growMemory(SB), NOSPLIT, $0
	Get SP
	I32Load pages+0(FP)
	GrowMemory
	I32Store ret+8(FP)
	RET
