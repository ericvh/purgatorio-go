// Package abi holds the constants shared by the host and guests: the ABI
// version, the names of the purgatorio import module, Inferno's open, bind
// and pctl flags, and the exact Inferno error strings.
//
// It is a portable package: it imports nothing but the Go core packages
// allowed by the portability check (see portable_test.go at the root).
package abi

// Version is the guest ABI version, carried in the purgatorio.abi custom
// section and checked at load. Bump it when the ABI changes and update
// docs/abi.md.
const Version = "0.1"

// Section is the name of the custom section holding Version.
const Section = "purgatorio.abi"

// Module is the name of the Purgatorio import module. Non-standard calls
// never go in the WASI namespace.
const Module = "purgatorio"

// WASIModule is the WASI preview 1 import module.
const WASIModule = "wasi_snapshot_preview1"

// Preopened directory fds, as wasi-libc and the Go runtime see them.
const (
	PreopenRootFD = 3 // "/", the prog's namespace root
	PreopenDotFD  = 4 // ".", always the prog's current dot
)

// Open modes (Sys->OREAD ...).
const (
	OREAD   = 0
	OWRITE  = 1
	ORDWR   = 2
	OEXEC   = 3
	OTRUNC  = 0x10
	ORCLOSE = 0x40
	OACCESS = 3 // mask of the access bits
)

// Bind and mount flags (Sys->MREPL ...).
const (
	MREPL   = 0x0000
	MBEFORE = 0x0001
	MAFTER  = 0x0002
	MCREATE = 0x0004
	MORDER  = 0x0003
)

// pctl and spawn_prog flags (Sys->NEWFD ...).
const (
	NEWFD   = 1 << 0
	FORKFD  = 1 << 1
	NEWNS   = 1 << 2
	FORKNS  = 1 << 3
	NEWPGRP = 1 << 4
	NODEVS  = 1 << 5
	NEWENV  = 1 << 6
	FORKENV = 1 << 7
)
