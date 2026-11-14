// Package platform is the seam between the portable core and whatever it
// runs on: the hosted emu on Linux or macOS, or the bare-metal kernel.
//
// In Go, goroutines, sync.Mutex and sync.Cond already give the core its
// kprocs, locks and Rendez on every target that has a Go runtime, so this
// interface is smaller than Inferno's: it covers what the runtime does not.
package platform

import "io"

// Platform is implemented once per target.
type Platform interface {
	// Now returns the time in nanoseconds since the Unix epoch.
	Now() int64
	// Entropy fills b with random bytes (#c/random).
	Entropy(b []byte)
	// Console is the raw byte stream behind #c/cons.
	Console() Console
	// User and Sysname back #c/user and #c/sysname.
	User() string
	Sysname() string
}

// Console is the host terminal or serial line.
type Console interface {
	io.Reader
	io.Writer
	// SetRaw switches raw mode on or off (#c/consctl rawon, rawoff).
	SetRaw(on bool) error
}
