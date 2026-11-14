package virt

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
	_ "unsafe"
)

// The runtime, crypto/rand included, draws random bytes from
// GetRandomData. Until the kernel has found a virtio entropy device the
// bytes come from splitmix64 seeded from the counter, which is fine for
// hash seeds but not for keys; Seed then switches to ChaCha8 keyed from
// the device.
var (
	rngState uint64
	chacha   atomic.Pointer[rand.ChaCha8]
	chachaMu sync.Mutex
)

//go:linkname initRNG runtime/goos.InitRNG
func initRNG() { rngState = uint64(ARM64.Counter()) | 1 }

//go:linkname getRandomData runtime/goos.GetRandomData
func getRandomData(b []byte) {
	if c := chacha.Load(); c != nil {
		chachaMu.Lock()
		_, _ = c.Read(b)
		chachaMu.Unlock()
		return
	}
	for i := range b {
		if i%8 == 0 {
			rngState += 0x9e3779b97f4a7c15
		}
		z := rngState
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		b[i] = byte(z >> (8 * (i % 8)))
	}
}

// Seed keys the runtime's generator with 32 bytes from an entropy
// source.
func Seed(seed [32]byte) { chacha.Store(rand.NewChaCha8(seed)) }
