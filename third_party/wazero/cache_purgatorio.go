// Purgatorio-Go patch: a compilation cache over a store the caller
// supplies, so code compiled ahead of time can be loaded without a file
// system.

package wazero

import (
	"io"

	"github.com/tetratelabs/wazero/internal/engine/wazevo"
)

// CacheStore holds compiled modules for a compilation cache, by key. Get
// returns a module's entry, Add stores one the compiler made, and Delete
// drops one the compiler found stale.
type CacheStore interface {
	Get(key [32]byte) (content io.ReadCloser, ok bool, err error)
	Add(key [32]byte, content io.Reader) error
	Delete(key [32]byte) error
}

// NewCompilationCacheWithStore returns a compilation cache kept in s.
func NewCompilationCacheWithStore(s CacheStore) CompilationCache {
	return &cache{fileCache: s}
}

// CacheKey is the key under which the compiler looks up code in a
// CacheStore, for a runtime with WithCloseOnContextDone(closeOnDone) and
// no function listeners.
func CacheKey(code []byte, closeOnDone bool) [32]byte {
	return wazevo.ImageKey(code, closeOnDone)
}
