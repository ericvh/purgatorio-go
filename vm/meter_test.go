package vm

import "testing"

func TestInitialSizes(t *testing.T) {
	mod := []byte("\x00asm\x01\x00\x00\x00")
	// A type section to skip, a table of 300 funcrefs with a maximum, and
	// two memories: 2 pages, and 1 page with a maximum.
	mod = append(mod, 1, 4, 1, 0x60, 0, 0)
	mod = append(mod, 4, 7, 1, 0x70, 1, 0xac, 0x02, 0x90, 0x03)
	mod = append(mod, 5, 6, 2, 0, 2, 1, 1, 9)
	mem, elems := InitialSizes(mod)
	if mem != 3*65536 || elems != 300 {
		t.Fatalf("got %d bytes, %d elements", mem, elems)
	}
	if mem, elems := InitialSizes(mod[:len(mod)-3]); mem != 0 || elems != 300 {
		t.Fatalf("truncated: %d %d", mem, elems)
	}
}
