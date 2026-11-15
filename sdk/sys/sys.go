// Package sys is the guest side of the purgatorio import module: the
// Inferno Sys calls WASI preview 1 cannot express. Guests use the Go
// standard library for what WASI covers (os, fmt, time) and this package
// for the rest: Inferno open modes, bind, pctl, spawn, error strings.
package sys

import "strings"

// Open modes, bind flags and pctl flags, as in Inferno's sys.m.
const (
	OREAD   = 0
	OWRITE  = 1
	ORDWR   = 2
	OEXEC   = 3
	OTRUNC  = 0x10
	ORCLOSE = 0x40

	MREPL   = 0
	MBEFORE = 1
	MAFTER  = 2
	MCREATE = 4
	// MCACHE says a mounted tree is disk-like, so its plain files are
	// read ahead.
	MCACHE = 0x10
	// MSRVAUTH requires the server's proof after a version 3 or 4
	// handshake (mount -S).
	MSRVAUTH = 0x4000_0000

	NEWFD   = 1 << 0
	FORKFD  = 1 << 1
	NEWNS   = 1 << 2
	FORKNS  = 1 << 3
	NEWPGRP = 1 << 4
	NODEVS  = 1 << 5
	NEWENV  = 1 << 6
	FORKENV = 1 << 7

	DMDIR    = 0x80000000
	DMAPPEND = 0x40000000
	DMEXCL   = 0x20000000
	DMAUTH   = 0x08000000

	QTDIR    = 0x80
	QTAPPEND = 0x40
	QTEXCL   = 0x20
	QTAUTH   = 0x08

	ERRMAX = 128
)

// Error is an Inferno error string.
type Error string

func (e Error) Error() string { return string(e) }

// lastErr returns the errstr as an error.
func lastErr() error { return Error(Errstr()) }

// Fatal writes msg to standard error and exits with "fail:" and msg as
// the status, as Inferno commands raise "fail:...".
func Fatal(msg string) {
	writeStderr(msg + "\n")
	Exits("fail:" + msg)
}

func argv(args []string) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString(a)
		b.WriteByte(0)
	}
	return b.String()
}
