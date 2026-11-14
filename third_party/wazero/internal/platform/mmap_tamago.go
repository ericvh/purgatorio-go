// Purgatorio-Go patch: executable memory on GOOS=tamago comes from the
// kernel, through experimental.SetCodeMemory.

//go:build tamago

package platform

import (
	"errors"

	"github.com/tetratelabs/wazero/experimental"
)

var errNoCodeMemory = errors.New("no code memory: experimental.SetCodeMemory was not called")

func mmapCodeSegment(size int) ([]byte, error) {
	m := experimental.GetCodeMemory()
	if m == nil {
		return nil, errNoCodeMemory
	}
	return m.Map(size)
}

func munmapCodeSegment(code []byte) error {
	m := experimental.GetCodeMemory()
	if m == nil {
		return errNoCodeMemory
	}
	return m.Unmap(code)
}

// MprotectCodeSegment makes code executable and read-only.
func MprotectCodeSegment(code []byte) error {
	m := experimental.GetCodeMemory()
	if m == nil {
		return errNoCodeMemory
	}
	return m.Protect(code)
}
