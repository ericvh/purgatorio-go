package styx

import "encoding/binary"

var le = binary.LittleEndian

type encoder struct{ b []byte }

func (e *encoder) u8(v uint8)   { e.b = append(e.b, v) }
func (e *encoder) u16(v uint16) { e.b = le.AppendUint16(e.b, v) }
func (e *encoder) u32(v uint32) { e.b = le.AppendUint32(e.b, v) }
func (e *encoder) u64(v uint64) { e.b = le.AppendUint64(e.b, v) }
func (e *encoder) str(s string) { e.u16(uint16(len(s))); e.b = append(e.b, s...) }
func (e *encoder) qid(q Qid)    { e.u8(q.Type); e.u32(q.Vers); e.u64(q.Path) }

type decoder struct {
	b   []byte
	err error
}

func (d *decoder) take(n int) []byte {
	if d.err != nil || len(d.b) < n {
		d.err = ErrShort
		return nil
	}
	v := d.b[:n]
	d.b = d.b[n:]
	return v
}

func (d *decoder) u8() uint8 {
	if v := d.take(1); v != nil {
		return v[0]
	}
	return 0
}

func (d *decoder) u16() uint16 {
	if v := d.take(2); v != nil {
		return le.Uint16(v)
	}
	return 0
}

func (d *decoder) u32() uint32 {
	if v := d.take(4); v != nil {
		return le.Uint32(v)
	}
	return 0
}

func (d *decoder) u64() uint64 {
	if v := d.take(8); v != nil {
		return le.Uint64(v)
	}
	return 0
}

func (d *decoder) str() string { return string(d.take(int(d.u16()))) }

func (d *decoder) qid() Qid { return Qid{Type: d.u8(), Vers: d.u32(), Path: d.u64()} }

func (d *decoder) bytes(n int) []byte {
	v := d.take(n)
	if v == nil {
		return nil
	}
	return append([]byte(nil), v...)
}

// Marshal appends the wire form of f to b.
func Marshal(b []byte, f *Fcall) ([]byte, error) {
	start := len(b)
	e := encoder{b: append(b, 0, 0, 0, 0)}
	e.u8(f.Type)
	e.u16(f.Tag)
	switch f.Type {
	case Tversion, Rversion:
		e.u32(f.Msize)
		e.str(f.Version)
	case Tauth:
		e.u32(f.Afid)
		e.str(f.Uname)
		e.str(f.Aname)
	case Rauth, Rattach:
		e.qid(f.Qid)
	case Tattach:
		e.u32(f.Fid)
		e.u32(f.Afid)
		e.str(f.Uname)
		e.str(f.Aname)
	case Rerror:
		e.str(f.Ename)
	case Tflush:
		e.u16(f.Oldtag)
	case Twalk:
		if len(f.Wname) > MAXWELEM {
			return b, ErrBadWalk
		}
		e.u32(f.Fid)
		e.u32(f.Newfid)
		e.u16(uint16(len(f.Wname)))
		for _, n := range f.Wname {
			e.str(n)
		}
	case Rwalk:
		if len(f.Wqid) > MAXWELEM {
			return b, ErrBadWalk
		}
		e.u16(uint16(len(f.Wqid)))
		for _, q := range f.Wqid {
			e.qid(q)
		}
	case Topen:
		e.u32(f.Fid)
		e.u8(f.Mode)
	case Ropen, Rcreate:
		e.qid(f.Qid)
		e.u32(f.Iounit)
	case Tcreate:
		e.u32(f.Fid)
		e.str(f.Name)
		e.u32(f.Perm)
		e.u8(f.Mode)
	case Tread:
		e.u32(f.Fid)
		e.u64(f.Offset)
		e.u32(f.Count)
	case Rread:
		e.u32(uint32(len(f.Data)))
		e.b = append(e.b, f.Data...)
	case Twrite:
		e.u32(f.Fid)
		e.u64(f.Offset)
		e.u32(uint32(len(f.Data)))
		e.b = append(e.b, f.Data...)
	case Rwrite:
		e.u32(f.Count)
	case Tclunk, Tremove, Tstat:
		e.u32(f.Fid)
	case Rstat:
		e.u16(uint16(len(f.Stat)))
		e.b = append(e.b, f.Stat...)
	case Twstat:
		e.u32(f.Fid)
		e.u16(uint16(len(f.Stat)))
		e.b = append(e.b, f.Stat...)
	case Rflush, Rclunk, Rremove, Rwstat:
	default:
		return b, ErrBadType
	}
	le.PutUint32(e.b[start:], uint32(len(e.b)-start))
	return e.b, nil
}

// Size returns the length of the message at the start of b, or 0 if fewer
// than four bytes are available.
func Size(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return le.Uint32(b)
}

// Unmarshal decodes one complete message. b must hold exactly that message.
// A msize of 0 means no limit.
func Unmarshal(b []byte, msize uint32) (*Fcall, error) {
	if len(b) < 7 {
		return nil, ErrShort
	}
	n := le.Uint32(b)
	if msize != 0 && n > msize {
		return nil, ErrLong
	}
	if int64(n) != int64(len(b)) {
		if int64(n) > int64(len(b)) {
			return nil, ErrShort
		}
		return nil, ErrTrail
	}
	d := decoder{b: b[4:]}
	f := &Fcall{Type: d.u8(), Tag: d.u16()}
	switch f.Type {
	case Tversion, Rversion:
		f.Msize = d.u32()
		f.Version = d.str()
	case Tauth:
		f.Afid = d.u32()
		f.Uname = d.str()
		f.Aname = d.str()
	case Rauth, Rattach:
		f.Qid = d.qid()
	case Tattach:
		f.Fid = d.u32()
		f.Afid = d.u32()
		f.Uname = d.str()
		f.Aname = d.str()
	case Rerror:
		f.Ename = d.str()
	case Tflush:
		f.Oldtag = d.u16()
	case Twalk:
		f.Fid = d.u32()
		f.Newfid = d.u32()
		nw := int(d.u16())
		if nw > MAXWELEM {
			return nil, ErrBadWalk
		}
		for i := 0; i < nw && d.err == nil; i++ {
			f.Wname = append(f.Wname, d.str())
		}
	case Rwalk:
		nq := int(d.u16())
		if nq > MAXWELEM {
			return nil, ErrBadWalk
		}
		for i := 0; i < nq && d.err == nil; i++ {
			f.Wqid = append(f.Wqid, d.qid())
		}
	case Topen:
		f.Fid = d.u32()
		f.Mode = d.u8()
	case Ropen, Rcreate:
		f.Qid = d.qid()
		f.Iounit = d.u32()
	case Tcreate:
		f.Fid = d.u32()
		f.Name = d.str()
		f.Perm = d.u32()
		f.Mode = d.u8()
	case Tread:
		f.Fid = d.u32()
		f.Offset = d.u64()
		f.Count = d.u32()
	case Rread:
		f.Data = d.bytes(int(d.u32()))
	case Twrite:
		f.Fid = d.u32()
		f.Offset = d.u64()
		f.Data = d.bytes(int(d.u32()))
	case Rwrite:
		f.Count = d.u32()
	case Tclunk, Tremove, Tstat:
		f.Fid = d.u32()
	case Rstat:
		f.Stat = d.bytes(int(d.u16()))
	case Twstat:
		f.Fid = d.u32()
		f.Stat = d.bytes(int(d.u16()))
	case Rflush, Rclunk, Rremove, Rwstat:
	default:
		return nil, ErrBadType
	}
	if d.err != nil {
		return nil, d.err
	}
	if len(d.b) != 0 {
		return nil, ErrTrail
	}
	return f, nil
}

// MarshalDir appends the packed form of dir (with its leading size) to b.
func MarshalDir(b []byte, dir *Dir) []byte {
	start := len(b)
	e := encoder{b: append(b, 0, 0)}
	e.u16(dir.Type)
	e.u32(dir.Dev)
	e.qid(dir.Qid)
	e.u32(dir.Mode)
	e.u32(dir.Atime)
	e.u32(dir.Mtime)
	e.u64(dir.Length)
	e.str(dir.Name)
	e.str(dir.UID)
	e.str(dir.GID)
	e.str(dir.MUID)
	le.PutUint16(e.b[start:], uint16(len(e.b)-start-2))
	return e.b
}

// UnmarshalDir decodes one packed Dir, which must fill b exactly.
func UnmarshalDir(b []byte) (*Dir, error) {
	d := decoder{b: b}
	if n := d.u16(); d.err != nil || int(n) != len(d.b) {
		return nil, ErrBadStat
	}
	dir := &Dir{
		Type:   d.u16(),
		Dev:    d.u32(),
		Qid:    d.qid(),
		Mode:   d.u32(),
		Atime:  d.u32(),
		Mtime:  d.u32(),
		Length: d.u64(),
		Name:   d.str(),
		UID:    d.str(),
		GID:    d.str(),
		MUID:   d.str(),
	}
	if d.err != nil || len(d.b) != 0 {
		return nil, ErrBadStat
	}
	return dir, nil
}
