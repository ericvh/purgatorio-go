package sys

import (
	"encoding/binary"
	"errors"
)

// Qid is a server's unique identity for a file.
type Qid struct {
	Path uint64
	Vers uint32
	Type uint8
}

// Dir is Sys->Dir.
type Dir struct {
	Name   string
	UID    string
	GID    string
	MUID   string
	Qid    Qid
	Mode   uint32
	Atime  uint32
	Mtime  uint32
	Length uint64
	Type   uint16
	Dev    uint32
}

// NullDir returns a Dir whose fields all mean "don't change", for wstat.
func NullDir() Dir {
	return Dir{Qid: Qid{^uint64(0), ^uint32(0), ^uint8(0)}, Mode: ^uint32(0),
		Atime: ^uint32(0), Mtime: ^uint32(0), Length: ^uint64(0), Type: ^uint16(0), Dev: ^uint32(0)}
}

var le = binary.LittleEndian

var errStat = errors.New("malformed stat buffer")

// Pack returns the 9P wire form of d.
func (d *Dir) Pack() []byte {
	b := make([]byte, 2, 64)
	b = le.AppendUint16(b, d.Type)
	b = le.AppendUint32(b, d.Dev)
	b = append(b, d.Qid.Type)
	b = le.AppendUint32(b, d.Qid.Vers)
	b = le.AppendUint64(b, d.Qid.Path)
	b = le.AppendUint32(b, d.Mode)
	b = le.AppendUint32(b, d.Atime)
	b = le.AppendUint32(b, d.Mtime)
	b = le.AppendUint64(b, d.Length)
	for _, s := range []string{d.Name, d.UID, d.GID, d.MUID} {
		b = le.AppendUint16(b, uint16(len(s)))
		b = append(b, s...)
	}
	le.PutUint16(b, uint16(len(b)-2))
	return b
}

// UnpackDir decodes one packed Dir.
func UnpackDir(b []byte) (*Dir, error) {
	if len(b) < 2+39+8 || int(le.Uint16(b))+2 != len(b) {
		return nil, errStat
	}
	d := &Dir{Type: le.Uint16(b[2:]), Dev: le.Uint32(b[4:]),
		Qid:  Qid{Type: b[8], Vers: le.Uint32(b[9:]), Path: le.Uint64(b[13:])},
		Mode: le.Uint32(b[21:]), Atime: le.Uint32(b[25:]), Mtime: le.Uint32(b[29:]), Length: le.Uint64(b[33:])}
	p := b[41:]
	var ss [4]string
	for i := range ss {
		if len(p) < 2 || len(p) < 2+int(le.Uint16(p)) {
			return nil, errStat
		}
		n := int(le.Uint16(p))
		ss[i] = string(p[2 : 2+n])
		p = p[2+n:]
	}
	d.Name, d.UID, d.GID, d.MUID = ss[0], ss[1], ss[2], ss[3]
	return d, nil
}

// UnpackDirs decodes consecutive packed Dirs.
func UnpackDirs(b []byte) ([]Dir, error) {
	var out []Dir
	for len(b) > 0 {
		if len(b) < 2 {
			return out, errStat
		}
		n := int(le.Uint16(b)) + 2
		if n > len(b) {
			return out, errStat
		}
		d, err := UnpackDir(b[:n])
		if err != nil {
			return out, err
		}
		out = append(out, *d)
		b = b[n:]
	}
	return out, nil
}
