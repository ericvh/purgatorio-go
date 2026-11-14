//go:build wasip1

package sys

import (
	"os"
	"unsafe"
)

//go:wasmimport purgatorio errstr
func errstr(buf unsafe.Pointer, n uint32) int32

//go:wasmimport purgatorio werrstr
func werrstr(p unsafe.Pointer, n uint32)

//go:wasmimport purgatorio exits
func exits(p unsafe.Pointer, n uint32)

// errmax is Inferno's ERRMAX.
const errmax = 128

// Errstr returns the prog's last error string, in Inferno's wording.
func Errstr() string {
	var b [errmax]byte
	n := errstr(unsafe.Pointer(&b[0]), errmax)
	if n < 0 {
		return ""
	}
	return string(b[:n])
}

// Werrstr sets the prog's error string.
func Werrstr(s string) { werrstr(unsafe.Pointer(unsafe.StringData(s)), uint32(len(s))) }

// Exits ends the prog with an Inferno exit status; empty means success.
func Exits(s string) {
	exits(unsafe.Pointer(unsafe.StringData(s)), uint32(len(s)))
	panic("exits returned")
}

func writeStderr(s string) { _, _ = os.Stderr.WriteString(s) }
