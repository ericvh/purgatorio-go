//go:build wasip1

package module

import "unsafe"

// Buffers handed to the host stay referenced until the next call.
var (
	inbufs [][]byte
	result []byte
)

func addr(b []byte) uint32 { return uint32(uintptr(unsafe.Pointer(&b[0]))) }

//go:wasmexport purgatorio_alloc
func alloc(n int32) uint32 {
	b := make([]byte, max(1, n))
	inbufs = append(inbufs, b)
	return addr(b)
}

// buf finds a buffer alloc handed out by its address.
func buf(p, n uint32) []byte {
	for _, b := range inbufs {
		if addr(b) == p {
			return b[:n]
		}
	}
	return nil
}

//go:wasmexport purgatorio_call
func call(np, nl, ip, il uint32) uint64 {
	name := string(buf(np, nl))
	in := append([]byte(nil), buf(ip, il)...)
	inbufs = nil
	result = dispatch(name, in)
	return uint64(addr(result))<<32 | uint64(len(result))
}
