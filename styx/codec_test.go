package styx

import (
	"bytes"
	"reflect"
	"testing"
)

var q = Qid{Type: QTDIR, Vers: 7, Path: 0x0102030405060708}

var samples = []Fcall{
	{Type: Tversion, Tag: NOTAG, Msize: 8192, Version: Version},
	{Type: Rversion, Tag: NOTAG, Msize: 8192, Version: Version},
	{Type: Tauth, Tag: 1, Afid: 9, Uname: "inferno", Aname: ""},
	{Type: Rauth, Tag: 1, Qid: Qid{Type: QTAUTH, Path: 1}},
	{Type: Tattach, Tag: 2, Fid: 0, Afid: NOFID, Uname: "inferno", Aname: "/"},
	{Type: Rattach, Tag: 2, Qid: q},
	{Type: Rerror, Tag: 3, Ename: "file does not exist"},
	{Type: Tflush, Tag: 4, Oldtag: 3},
	{Type: Rflush, Tag: 4},
	{Type: Twalk, Tag: 5, Fid: 0, Newfid: 1, Wname: []string{"dev", "cons"}},
	{Type: Twalk, Tag: 5, Fid: 0, Newfid: 1},
	{Type: Rwalk, Tag: 5, Wqid: []Qid{q, {Path: 2}}},
	{Type: Topen, Tag: 6, Fid: 1, Mode: 2},
	{Type: Ropen, Tag: 6, Qid: q, Iounit: 8168},
	{Type: Tcreate, Tag: 7, Fid: 1, Name: "f", Perm: 0o644, Mode: 1},
	{Type: Rcreate, Tag: 7, Qid: q, Iounit: 8168},
	{Type: Tread, Tag: 8, Fid: 1, Offset: 1 << 40, Count: 512},
	{Type: Rread, Tag: 8, Data: []byte("hello")},
	{Type: Twrite, Tag: 9, Fid: 1, Offset: 3, Data: []byte("world")},
	{Type: Rwrite, Tag: 9, Count: 5},
	{Type: Tclunk, Tag: 10, Fid: 1},
	{Type: Rclunk, Tag: 10},
	{Type: Tremove, Tag: 11, Fid: 1},
	{Type: Rremove, Tag: 11},
	{Type: Tstat, Tag: 12, Fid: 1},
	{Type: Rstat, Tag: 12, Stat: MarshalDir(nil, &Dir{Name: "cons", Qid: q})},
	{Type: Twstat, Tag: 13, Fid: 1, Stat: MarshalDir(nil, &Dir{Name: "x"})},
	{Type: Rwstat, Tag: 13},
}

func TestRoundTrip(t *testing.T) {
	for _, f := range samples {
		b, err := Marshal(nil, &f)
		if err != nil {
			t.Fatalf("Marshal %d: %v", f.Type, err)
		}
		if Size(b) != uint32(len(b)) {
			t.Errorf("type %d: size %d, len %d", f.Type, Size(b), len(b))
		}
		g, err := Unmarshal(b, 0)
		if err != nil {
			t.Fatalf("Unmarshal %d: %v", f.Type, err)
		}
		if !reflect.DeepEqual(&f, g) {
			t.Errorf("type %d: got %+v, want %+v", f.Type, g, f)
		}
	}
}

// Every proper prefix of a message, with its size field fixed up, must be
// rejected rather than decoded or panicking.
func TestTruncated(t *testing.T) {
	for _, f := range samples {
		b, _ := Marshal(nil, &f)
		for n := 0; n < len(b); n++ {
			c := append([]byte(nil), b[:n]...)
			if n >= 4 {
				le.PutUint32(c, uint32(n))
			}
			if _, err := Unmarshal(c, 0); err == nil {
				t.Errorf("type %d cut to %d bytes decoded", f.Type, n)
			}
		}
	}
}

func TestMsize(t *testing.T) {
	b, _ := Marshal(nil, &Fcall{Type: Rread, Data: make([]byte, 100)})
	if _, err := Unmarshal(b, 50); err != ErrLong {
		t.Errorf("got %v, want ErrLong", err)
	}
}

// Tversion as Inferno's emu sends it.
func TestKnownBytes(t *testing.T) {
	want := []byte{19, 0, 0, 0, 100, 0xff, 0xff, 0x00, 0x20, 0, 0, 6, 0, '9', 'P', '2', '0', '0', '0'}
	b, _ := Marshal(nil, &Fcall{Type: Tversion, Tag: NOTAG, Msize: 8192, Version: Version})
	if !bytes.Equal(b, want) {
		t.Errorf("got % x, want % x", b, want)
	}
}

func TestDir(t *testing.T) {
	d := Dir{Type: 'U', Dev: 1, Qid: q, Mode: DMDIR | 0o755, Atime: 1, Mtime: 2,
		Length: 0, Name: "lib", UID: "inferno", GID: "inferno", MUID: ""}
	b := MarshalDir(nil, &d)
	g, err := UnmarshalDir(b)
	if err != nil || !reflect.DeepEqual(&d, g) {
		t.Fatalf("got %+v, %v", g, err)
	}
	if _, err := UnmarshalDir(b[:len(b)-1]); err == nil {
		t.Error("short Dir decoded")
	}
}

func FuzzUnmarshal(f *testing.F) {
	for _, s := range samples {
		b, _ := Marshal(nil, &s)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := Unmarshal(b, 0)
		if err != nil {
			return
		}
		c, err := Marshal(nil, m)
		if err != nil || !bytes.Equal(b, c) {
			t.Fatalf("re-encode differs: % x vs % x (%v)", b, c, err)
		}
	})
}
