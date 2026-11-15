package dbm

import (
	"bytes"
	"fmt"
	"sort"
	"testing"
)

// mem is a File in memory.
type mem struct{ b []byte }

func (m *mem) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.b)) {
		return 0, nil
	}
	return copy(p, m.b[off:]), nil
}

func (m *mem) WriteAt(p []byte, off int64) (int, error) {
	if end := int(off) + len(p); end > len(m.b) {
		m.b = append(m.b, make([]byte, end-len(m.b))...)
	}
	return copy(m.b[off:], p), nil
}

func TestLayout(t *testing.T) {
	pag, dir := &mem{}, &mem{}
	db := New(pag, dir, 0, false)
	if r, err := db.Store([]byte("key"), []byte("value"), false); r != 0 || err != nil {
		t.Fatal(r, err)
	}
	if len(pag.b) != PBLKSIZ || len(dir.b) != 0 {
		t.Fatalf("pag %d dir %d bytes", len(pag.b), len(dir.b))
	}
	want := []byte{0, 2, 0x01, 0xfd, 0x01, 0xf8}
	if !bytes.Equal(pag.b[:6], want) || string(pag.b[PBLKSIZ-8:]) != "valuekey" {
		t.Fatalf("page %x ... %q", pag.b[:6], pag.b[PBLKSIZ-8:])
	}
}

func TestMany(t *testing.T) {
	pag, dir := &mem{}, &mem{}
	db := New(pag, dir, 0, false)
	want := map[string]string{}
	for i := range 2000 {
		k, v := fmt.Sprintf("key%d", i), fmt.Sprintf("value of %d, long enough to split pages", i*i)
		want[k] = v
		if r, err := db.Store([]byte(k), []byte(v), false); r != 0 || err != nil {
			t.Fatal(k, r, err)
		}
	}
	if r, _ := db.Store([]byte("key7"), []byte("x"), false); r != 1 {
		t.Fatalf("store of existing key gave %d", r)
	}
	if len(dir.b) == 0 {
		t.Fatal("no split")
	}
	// Reopen, as from the files.
	db = New(pag, dir, int64(len(dir.b)), true)
	var keys []string
	for k, err := db.Firstkey(); k != nil; k, err = db.Nextkey(k) {
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, string(k))
	}
	if len(keys) != len(want) {
		t.Fatalf("%d keys, want %d", len(keys), len(want))
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 && keys[i-1] == k {
			t.Fatalf("%s twice", k)
		}
		v, err := db.Fetch([]byte(k))
		if err != nil || string(v) != want[k] {
			t.Fatalf("%s: %q %v", k, v, err)
		}
	}
	if r, _ := db.Delete([]byte("key1")); r != -1 {
		t.Fatal("deleted from a read-only data base")
	}
	db = New(pag, dir, int64(len(dir.b)), false)
	if r, _ := db.Delete([]byte("key1")); r != 0 {
		t.Fatal("delete failed")
	}
	if v, _ := db.Fetch([]byte("key1")); v != nil {
		t.Fatalf("deleted key gives %q", v)
	}
	if r, _ := db.Delete([]byte("key1")); r != -1 {
		t.Fatal("deleted twice")
	}
	if r, _ := db.Store([]byte("big"), make([]byte, PBLKSIZ), false); r != -1 {
		t.Fatal("stored an item bigger than a page")
	}
}

func TestBadBlock(t *testing.T) {
	pag := &mem{b: make([]byte, PBLKSIZ)}
	pag.b[1] = 200
	db := New(pag, &mem{}, 0, true)
	if _, err := db.Fetch([]byte("x")); err == nil || err.Error() != "dbm: bad block" {
		t.Fatalf("got %v", err)
	}
}
