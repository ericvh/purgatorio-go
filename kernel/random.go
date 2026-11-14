package kernel

import "crypto/rand"

// fillRandom uses the runtime's generator, which the board seeds.
func fillRandom(b []byte) { _, _ = rand.Read(b) }
