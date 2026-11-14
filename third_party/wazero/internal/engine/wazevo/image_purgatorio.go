// Purgatorio-Go patch: the key under which the compiler looks up a
// module in its compilation cache, so a cache can serve code compiled
// ahead of time.

package wazevo

import "github.com/tetratelabs/wazero/internal/wasm"

// ImageKey is the compilation cache key of the module code, compiled
// with no function listeners.
func ImageKey(code []byte, ensureTermination bool) [32]byte {
	m := &wasm.Module{}
	m.AssignModuleID(code, nil, ensureTermination)
	return fileCacheKey(m)
}
