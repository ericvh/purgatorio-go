package virt

import (
	"sync/atomic"
	"unsafe"
)

// Device registers are read and written with atomic loads and stores,
// which the compiler neither elides nor reorders.

func mmio(addr uint32) *uint32 { return (*uint32)(unsafe.Add(nil, uintptr(addr))) }

func read32(addr uint32) uint32     { return atomic.LoadUint32(mmio(addr)) }
func write32(addr uint32, v uint32) { atomic.StoreUint32(mmio(addr), v) }
