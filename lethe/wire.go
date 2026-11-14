package lethe

import (
	"encoding/binary"
	"unicode/utf8"
)

var le = binary.LittleEndian

// reader reads little-endian fields; the first short read sets err.
type reader struct {
	b   []byte
	err bool
}

func (r *reader) bytes(n int) []byte {
	if r.err || n < 0 || n > len(r.b) {
		r.err = true
		return nil
	}
	v := r.b[:n:n]
	r.b = r.b[n:]
	return v
}

func (r *reader) u8() uint8 {
	b := r.bytes(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *reader) u16() uint16 {
	b := r.bytes(2)
	if b == nil {
		return 0
	}
	return le.Uint16(b)
}

func (r *reader) u32() uint32 {
	b := r.bytes(4)
	if b == nil {
		return 0
	}
	return le.Uint32(b)
}

func (r *reader) u64() uint64 {
	b := r.bytes(8)
	if b == nil {
		return 0
	}
	return le.Uint64(b)
}

// data16 is s[2]: a u16 length and that many bytes.
func (r *reader) data16() []byte { return r.bytes(int(r.u16())) }

// data32 is [count u32].
func (r *reader) data32() []byte {
	n := r.u32()
	if uint64(n) > uint64(len(r.b)) {
		r.err = true
		return nil
	}
	return r.bytes(int(n))
}

// str16 is s[2] holding UTF-8.
func (r *reader) str16() string {
	b := r.data16()
	if !utf8.Valid(b) {
		r.err = true
	}
	return string(b)
}

func (r *reader) array(dst []byte) { copy(dst, r.bytes(len(dst))) }

func (r *reader) rest() []byte {
	v := r.b
	r.b = nil
	return v
}

// done reports whether everything was read and nothing failed.
func (r *reader) done() bool { return !r.err && len(r.b) == 0 }

type writer struct{ b []byte }

func (w *writer) u8(v uint8)      { w.b = append(w.b, v) }
func (w *writer) u16(v uint16)    { w.b = le.AppendUint16(w.b, v) }
func (w *writer) u32(v uint32)    { w.b = le.AppendUint32(w.b, v) }
func (w *writer) u64(v uint64)    { w.b = le.AppendUint64(w.b, v) }
func (w *writer) bytes(b []byte)  { w.b = append(w.b, b...) }
func (w *writer) str16(s string)  { w.data16([]byte(s)) }
func (w *writer) data32(b []byte) { w.u32(uint32(len(b))); w.b = append(w.b, b...) }

// data16 cuts at 65535 bytes; callers check lengths first, so that never
// happens on the wire.
func (w *writer) data16(b []byte) {
	if len(b) > 0xffff {
		b = b[:0xffff]
	}
	w.u16(uint16(len(b)))
	w.b = append(w.b, b...)
}
