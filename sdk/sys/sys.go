// Package sys is the guest side of the purgatorio import module: Inferno
// calls that WASI preview 1 cannot express. Guests use the Go standard
// library for everything WASI covers and this package for the rest.
package sys

// Fatal writes its arguments and the error string to standard error and
// exits with them as the status, like Inferno's raise of a "fail:" status.
func Fatal(msg string) {
	writeStderr(msg + "\n")
	Exits("fail:" + msg)
}
