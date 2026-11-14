package rstyx

import (
	"encoding/binary"
	"io"

	"github.com/ericvh/purgatorio-go/pgauth"
)

// Link is one end of the connection after the opening: each frame sealed
// as one pgauth record once the exchange agreed keys, or clear frames.
type Link struct {
	seal *pgauth.Sealer
	open *pgauth.Opener
	buf  []byte
}

// ClientLink is the client's end, sealed when keys is not nil.
func ClientLink(keys *pgauth.Keys) *Link {
	if keys == nil {
		return &Link{}
	}
	s, o := keys.Client()
	return &Link{seal: s, open: o}
}

// ServerLink is the server's end.
func ServerLink(keys *pgauth.Keys) *Link {
	if keys == nil {
		return &Link{}
	}
	s, o := keys.Server()
	return &Link{seal: s, open: o}
}

// Wire is a frame as it goes on the wire.
func (l *Link) Wire(kind byte, data []byte) ([]byte, error) {
	f := Frame(kind, data)
	if l.seal == nil {
		return f, nil
	}
	return l.seal.Seal(nil, f)
}

// Push takes bytes that arrived.
func (l *Link) Push(b []byte) { l.buf = append(l.buf, b...) }

// Take is the next whole frame; ok is false when more bytes are needed.
func (l *Link) Take() (byte, []byte, bool, error) {
	if l.open == nil {
		if len(l.buf) < Hdr {
			return 0, nil, false, nil
		}
		n := int(binary.LittleEndian.Uint32(l.buf[1:]))
		if n > Max {
			return 0, nil, false, ErrProto
		}
		if len(l.buf) < Hdr+n {
			return 0, nil, false, nil
		}
		k, d := l.buf[0], append([]byte(nil), l.buf[Hdr:Hdr+n]...)
		l.buf = l.buf[Hdr+n:]
		return k, d, true, nil
	}
	if len(l.buf) < pgauth.RecordHeader {
		return 0, nil, false, nil
	}
	n, err := pgauth.RecordSize(l.buf, Hdr+Max)
	if err != nil {
		return 0, nil, false, err
	}
	if len(l.buf) < n {
		return 0, nil, false, nil
	}
	rec := append([]byte(nil), l.buf[:n]...)
	l.buf = l.buf[n:]
	p, err := l.open.Open(rec)
	if err != nil {
		return 0, nil, false, err
	}
	k, d, err := unframe(p)
	return k, d, err == nil, err
}

// Read reads exactly one frame from r (only for the opening, before
// anything was pushed).
func (l *Link) Read(r io.Reader) (byte, []byte, error) {
	if l.open == nil {
		return ReadFrame(r)
	}
	var h [pgauth.RecordHeader]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, ErrHungup
	}
	n, err := pgauth.RecordSize(h[:], Hdr+Max)
	if err != nil {
		return 0, nil, err
	}
	rec := make([]byte, n)
	copy(rec, h[:])
	if _, err := io.ReadFull(r, rec[pgauth.RecordHeader:]); err != nil {
		return 0, nil, ErrHungup
	}
	p, err := l.open.Open(rec)
	if err != nil {
		return 0, nil, err
	}
	return unframe(p)
}

func unframe(p []byte) (byte, []byte, error) {
	if len(p) < Hdr {
		return 0, nil, ErrProto
	}
	n := int(binary.LittleEndian.Uint32(p[1:]))
	if n > Max || n != len(p)-Hdr {
		return 0, nil, ErrProto
	}
	return p[0], append([]byte(nil), p[Hdr:]...), nil
}
