//go:build wasip1

package main

import "unsafe"

//go:wasmimport wasi_snapshot_preview1 fd_read
func fdRead(fd int32, iovs uint32, n uint32, nread unsafe.Pointer) uint32

// badRead calls fd_read with an iovec far outside memory.
func badRead() uint32 {
	var nread uint32
	return fdRead(0, 0xfffffff0, 1, unsafe.Pointer(&nread))
}
