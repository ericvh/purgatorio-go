package core

import (
	"fmt"
	"runtime"
)

// memoryText is #c/memory in Inferno's format, one line per pool:
// cursize maxsize hw nalloc nfree nbrk max name. Go has one heap; it is
// reported as the main pool, with the heap and image pools empty.
func memoryText() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	line := func(cur, max, hw, nalloc, nfree uint64, name string) string {
		return fmt.Sprintf("%11d %11d %11d %11d %11d %11d %11d %s\n", cur, max, hw, nalloc, nfree, 0, max, name)
	}
	return line(m.HeapAlloc, m.Sys, m.HeapSys, m.Mallocs, m.Frees, "main") +
		line(0, 0, 0, 0, 0, "heap") + line(0, 0, 0, 0, 0, "image")
}
