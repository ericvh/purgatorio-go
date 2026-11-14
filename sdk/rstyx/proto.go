// Package rstyx is the protocol of rcmd, cpu and auxi/rstyxd with pgauth
// (docs/rcmd.md): the Rust sibling's pgrstyx2, and pgrstyx3, the same
// with the hybrid handshake. Everything on a connection is a frame: one
// byte of kind, a u32 LE length and that many bytes.
//
//	C -> S  HELLO   "pgrstyx2 <user>" (classical) or "pgrstyx3 <user>" (hybrid)
//	S -> C  CHAL    the offer (64 or 1248 bytes), or empty when no proof is wanted
//	C -> S  PROOF   a version 3 or 4 mount proof for attach name "rstyx", or empty
//	S -> C  OK (the server's proof, or empty) | ERR <reason>
//	        after a proof every later frame is sealed as one record
//	C -> S  EXEC    flags u8, then the command line for sh -c
//	then, both ways, in any order: STYX, IN, OUT, ERRS, WIN
//	S -> C  STATUS  the exit status; the last frame
package rstyx

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode"
)

const (
	Magic2 = "pgrstyx2" // classical, the Rust sibling's
	Magic3 = "pgrstyx3" // hybrid
	// Aname is the attach name a proof is made for, so that a proof for
	// a mount is not a proof for a command.
	Aname     = "rstyx"
	Service   = "rstyx"
	Hdr       = 5
	Max       = 64 * 1024
	StyxChunk = 16 * 1024
	Window    = 16 * 1024
	Chunk     = 8 * 1024
	// NoAuth is EXEC's flag: the client's export asks no proof.
	NoAuth = 1
)

// Frame kinds.
const (
	Hello  = 1
	Chal   = 2
	Proof  = 3
	OK     = 4
	Err    = 5
	Exec   = 6
	Styx   = 7
	In     = 8
	Out    = 9
	Errs   = 10
	Win    = 11
	Status = 12
)

var (
	ErrProto  = errors.New("rstyx protocol botch")
	ErrHungup = errors.New("connection closed")
)

// Frame is one frame, ready to write in one write.
func Frame(kind byte, data []byte) []byte {
	f := make([]byte, 0, Hdr+len(data))
	f = append(f, kind)
	f = binary.LittleEndian.AppendUint32(f, uint32(len(data)))
	return append(f, data...)
}

// WinData is a WIN frame's data.
func WinData(stream byte, n int) []byte {
	return binary.LittleEndian.AppendUint32([]byte{stream}, uint32(n))
}

// ParseWin reads a WIN frame's data.
func ParseWin(d []byte) (byte, int, bool) {
	if len(d) != 5 {
		return 0, 0, false
	}
	return d[0], int(binary.LittleEndian.Uint32(d[1:])), true
}

// ReadFrame reads exactly one clear frame, and no byte past it.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var h [Hdr]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, ErrHungup
	}
	n := binary.LittleEndian.Uint32(h[1:])
	if n > Max {
		return 0, nil, ErrProto
	}
	d := make([]byte, n)
	if _, err := io.ReadFull(r, d); err != nil {
		return 0, nil, ErrHungup
	}
	return h[0], d, nil
}

// MkAddr is the dial string for host: host is tcp!host!rstyx, net!host
// is net!host!rstyx, and a full address is itself.
func MkAddr(host string) string {
	switch strings.Count(host, "!") {
	case 0:
		return "tcp!" + host + "!" + Service
	case 1:
		return host + "!" + Service
	}
	return host
}

// Quote is one word as sh reads it back.
func Quote(w string) string {
	plain := w != ""
	for _, c := range w {
		if !(unicode.IsLetter(c) || unicode.IsDigit(c) || strings.ContainsRune("_-+./:,%@!", c)) {
			plain = false
		}
	}
	if plain {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", "''") + "'"
}

// Command is the command line for args: sh for none; one argument is a
// command line already; several are the words of one command.
func Command(args []string) string {
	switch len(args) {
	case 0:
		return "sh"
	case 1:
		return args[0]
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}
