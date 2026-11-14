package virt

import _ "unsafe"

// The runtime seeds its own generator from GetRandomData. QEMU virt has no
// RNG device without virtio-rng, so this is splitmix64 seeded from the
// counter: fine for hash seeds, not for keys (virtio-rng comes with the
// device drivers).
var rngState uint64

//go:linkname initRNG runtime/goos.InitRNG
func initRNG() { rngState = uint64(ARM64.Counter()) | 1 }

//go:linkname getRandomData runtime/goos.GetRandomData
func getRandomData(b []byte) {
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
