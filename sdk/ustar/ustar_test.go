package ustar

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	long := strings.Repeat("d/", 60) + "file"
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, h := range []*Header{
		{Name: "dir", Type: '5', Mode: 0o755, Mtime: 7},
		{Name: long, Type: '0', Mode: 0o644, Size: 3, Mtime: 1 << 40, Uname: "u", Gname: "g"},
	} {
		b, err := h.Pack()
		if err != nil {
			t.Fatal(err)
		}
		w.Block(b)
	}
	w.Block([]byte("abc"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len()%(NBlock*BlockSize) != 0 {
		t.Fatalf("archive of %d bytes", buf.Len())
	}
	// Go's reader agrees.
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	if h, err := tr.Next(); err != nil || h.Name != "dir/" || h.Typeflag != tar.TypeDir {
		t.Fatalf("%+v %v", h, err)
	}
	if h, err := tr.Next(); err != nil || h.Name != long || h.ModTime.Unix() != 1<<40 || h.Uname != "u" {
		t.Fatalf("%+v %v", h, err)
	}
	a := NewArchive(NewFullReader(iotest(buf.Bytes())))
	h, err := a.Next()
	if err != nil || h.Name != "dir/" || h.Mode != 0o755 || h.Mtime != 7 {
		t.Fatalf("%+v %v", h, err)
	}
	h, err = a.Next()
	if err != nil || h.Name != long || h.Size != 3 || h.Mtime != 1<<40 {
		t.Fatalf("%+v %v", h, err)
	}
	if d, _ := io.ReadAll(a); string(d) != "abc" {
		t.Fatalf("data %q", d)
	}
	if h, err := a.Next(); h != nil || err != nil {
		t.Fatalf("after the end: %+v %v", h, err)
	}
}

// iotest returns a reader of b that returns 7 bytes at a time.
func iotest(b []byte) io.Reader { return &slow{b} }

type slow struct{ b []byte }

func (s *slow) Read(p []byte) (int, error) {
	if len(s.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), 7)], s.b)
	s.b = s.b[n:]
	return n, nil
}

func TestErrors(t *testing.T) {
	b := make([]byte, BlockSize)
	copy(b, "x")
	copy(b[257:], "bogus")
	if _, err := Parse(b); err == nil || err.Error() != "bad magic x" {
		t.Fatalf("got %v", err)
	}
	copy(b[257:], "ustar")
	if _, err := Parse(b); err == nil || err.Error() != "directory checksum error x" {
		t.Fatalf("got %v", err)
	}
	if err := PackName(make([]byte, BlockSize), strings.Repeat("x", 101)); err != ErrNoSplit {
		t.Fatalf("got %v", err)
	}
	r := NewReader(bytes.NewReader(make([]byte, 100)))
	if _, err := r.Block(); err != ErrBlocksize {
		t.Fatalf("got %v", err)
	}
}
