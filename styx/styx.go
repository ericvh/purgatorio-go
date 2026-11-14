// Package styx is the Styx (9P2000) message codec: every T and R message,
// Qid and Dir, with msize checks. It decodes with explicit little-endian
// reads and no unsafe casts, so it behaves the same on every architecture.
//
// It is a portable package; the kernel will reuse it unchanged.
package styx

import (
	"errors"
	"fmt"
)

// Version is the only protocol version spoken.
const Version = "9P2000"

// Message types.
const (
	Tversion uint8 = 100 + iota
	Rversion
	Tauth
	Rauth
	Tattach
	Rattach
	Terror // never sent
	Rerror
	Tflush
	Rflush
	Twalk
	Rwalk
	Topen
	Ropen
	Tcreate
	Rcreate
	Tread
	Rread
	Twrite
	Rwrite
	Tclunk
	Rclunk
	Tremove
	Rremove
	Tstat
	Rstat
	Twstat
	Rwstat
)

const (
	NOTAG    uint16 = 0xffff
	NOFID    uint32 = 0xffffffff
	MAXWELEM        = 16 // most names in one Twalk
	// IOHDRSZ is the room a Tread/Twrite header needs inside msize.
	IOHDRSZ = 24
)

// Qid types and Dir mode bits.
const (
	QTDIR    uint8 = 0x80
	QTAPPEND uint8 = 0x40
	QTEXCL   uint8 = 0x20
	QTAUTH   uint8 = 0x08
	QTFILE   uint8 = 0x00

	DMDIR    uint32 = 0x80000000
	DMAPPEND uint32 = 0x40000000
	DMEXCL   uint32 = 0x20000000
	DMAUTH   uint32 = 0x08000000
)

// Qid is a server's unique identity for a file.
type Qid struct {
	Type uint8
	Vers uint32
	Path uint64
}

func (q Qid) String() string {
	return fmt.Sprintf("(%.16x %d %.2x)", q.Path, q.Vers, q.Type)
}

// Dir is the machine-independent directory entry carried by stat and wstat.
type Dir struct {
	Type   uint16
	Dev    uint32
	Qid    Qid
	Mode   uint32
	Atime  uint32
	Mtime  uint32
	Length uint64
	Name   string
	UID    string
	GID    string
	MUID   string
}

// Fcall is one Styx message. Which fields are meaningful depends on Type,
// as in Plan 9's fcall(2).
type Fcall struct {
	Type    uint8
	Tag     uint16
	Fid     uint32
	Msize   uint32   // Tversion, Rversion
	Version string   // Tversion, Rversion
	Oldtag  uint16   // Tflush
	Ename   string   // Rerror
	Qid     Qid      // Rattach, Ropen, Rcreate, Rauth (aqid)
	Iounit  uint32   // Ropen, Rcreate
	Afid    uint32   // Tauth, Tattach
	Uname   string   // Tauth, Tattach
	Aname   string   // Tauth, Tattach
	Perm    uint32   // Tcreate
	Name    string   // Tcreate
	Mode    uint8    // Topen, Tcreate
	Newfid  uint32   // Twalk
	Wname   []string // Twalk
	Wqid    []Qid    // Rwalk
	Offset  uint64   // Tread, Twrite
	Count   uint32   // Tread, Rwrite
	Data    []byte   // Twrite, Rread
	Stat    []byte   // Twstat, Rstat: a packed Dir
}

var (
	ErrShort   = errors.New("styx: short message")
	ErrBadType = errors.New("styx: bad message type")
	ErrLong    = errors.New("styx: message longer than msize")
	ErrTrail   = errors.New("styx: trailing bytes in message")
	ErrBadWalk = errors.New("styx: too many names in walk")
	ErrBadStat = errors.New("styx: malformed stat")
)
