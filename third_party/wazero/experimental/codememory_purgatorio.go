// Purgatorio-Go patch: a hook for executable memory, for systems without
// mmap (GOOS=tamago).

package experimental

import "sync/atomic"

// CodeMemory supplies the compiler's executable memory where the
// platform has no mmap. Map returns size bytes of writable memory, not
// executable; Protect makes memory Map returned executable and
// read-only, with the caches made coherent; Unmap gives it back.
type CodeMemory interface {
	Map(size int) ([]byte, error)
	Protect(code []byte) error
	Unmap(code []byte) error
}

var codeMemory atomic.Value // CodeMemory

// SetCodeMemory sets the executable memory for the compiler. It must be
// called before a compiler runtime is made.
func SetCodeMemory(m CodeMemory) { codeMemory.Store(&m) }

// GetCodeMemory returns what SetCodeMemory set, or nil.
func GetCodeMemory() CodeMemory {
	if p, ok := codeMemory.Load().(*CodeMemory); ok {
		return *p
	}
	return nil
}
