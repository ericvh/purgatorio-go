//go:build !wasip1

package sys

// Host stubs, so guest packages build and vet on the host. Guests only run
// under wasip1.

func Errstr() string     { return "" }
func Werrstr(string)     {}
func Exits(s string)     { panic("sys.Exits outside a guest: " + s) }
func writeStderr(string) {}
