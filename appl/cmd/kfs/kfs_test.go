package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/styx"
)

func rpc(t *testing.T, cp *channel, f *styx.Fcall) *styx.Fcall {
	t.Helper()
	r := apply(cp, f)
	if r.Type == styx.Rerror {
		t.Fatalf("%d: %s", f.Type, r.Ename)
	}
	return r
}

func session(t *testing.T, uname string) *channel {
	cp := &channel{fids: map[uint32]*file{}}
	rpc(t, cp, &styx.Fcall{Type: styx.Tversion, Msize: maxrpc, Version: styx.Version})
	rpc(t, cp, &styx.Fcall{Type: styx.Tattach, Fid: 0, Afid: styx.NOFID, Uname: uname})
	return cp
}

func TestKfs(t *testing.T) {
	img := filepath.Join(t.TempDir(), "kfs.img")
	if err := os.WriteFile(img, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if e := start(img, true, 1024); e != "" {
		t.Fatal(e)
	}
	cp := session(t, "inferno")
	rpc(t, cp, &styx.Fcall{Type: styx.Twalk, Fid: 0, Newfid: 1})
	rpc(t, cp, &styx.Fcall{Type: styx.Tcreate, Fid: 1, Name: "dir", Perm: styx.DMDIR | 0o775, Mode: 0})
	rpc(t, cp, &styx.Fcall{Type: styx.Tclunk, Fid: 1})
	rpc(t, cp, &styx.Fcall{Type: styx.Twalk, Fid: 0, Newfid: 1, Wname: []string{"dir"}})
	rpc(t, cp, &styx.Fcall{Type: styx.Tcreate, Fid: 1, Name: "big", Perm: 0o664, Mode: 1})
	// Past the direct and into the indirect blocks.
	data := make([]byte, 8000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	for off := 0; off < 40*len(data); off += len(data) {
		r := rpc(t, cp, &styx.Fcall{Type: styx.Twrite, Fid: 1, Offset: uint64(off), Data: data})
		if r.Count != uint32(len(data)) {
			t.Fatalf("wrote %d", r.Count)
		}
	}
	rpc(t, cp, &styx.Fcall{Type: styx.Tclunk, Fid: 1})
	if r := apply(cp, &styx.Fcall{Type: styx.Twalk, Fid: 0, Newfid: 1, Wname: []string{"dir"}}); r.Type != styx.Rwalk {
		t.Fatal(r.Ename)
	}
	if r := apply(cp, &styx.Fcall{Type: styx.Tcreate, Fid: 1, Name: "big", Perm: 0o664, Mode: 1}); r.Ename != eexist {
		t.Errorf("create of an existing file: %q", r.Ename)
	}
	rpc(t, cp, &styx.Fcall{Type: styx.Tclunk, Fid: 1})

	// Served again, the files are there.
	readonly = false
	if e := start(img, false, 0); e != "" {
		t.Fatal(e)
	}
	wstatallow, writeallow = false, false
	cp = session(t, "inferno")
	r := rpc(t, cp, &styx.Fcall{Type: styx.Twalk, Fid: 0, Newfid: 1, Wname: []string{"dir", "big"}})
	if len(r.Wqid) != 2 {
		t.Fatalf("walk %v", r.Wqid)
	}
	r = rpc(t, cp, &styx.Fcall{Type: styx.Tstat, Fid: 1})
	dir, _ := styx.UnmarshalDir(r.Stat)
	if dir.Length != uint64(40*len(data)) || dir.UID != "inferno" || dir.GID != "adm" || dir.Mode != 0o664 {
		t.Errorf("stat %+v", dir)
	}
	rpc(t, cp, &styx.Fcall{Type: styx.Topen, Fid: 1, Mode: 0})
	r = rpc(t, cp, &styx.Fcall{Type: styx.Tread, Fid: 1, Offset: uint64(39*len(data) + 100), Count: 50})
	if string(r.Data) != string(data[100:150]) {
		t.Errorf("read back %v", r.Data)
	}
	rpc(t, cp, &styx.Fcall{Type: styx.Tclunk, Fid: 1})

	// A directory read lists the entry; none may not remove it.
	rpc(t, cp, &styx.Fcall{Type: styx.Twalk, Fid: 0, Newfid: 2, Wname: []string{"dir"}})
	rpc(t, cp, &styx.Fcall{Type: styx.Topen, Fid: 2, Mode: 0})
	r = rpc(t, cp, &styx.Fcall{Type: styx.Tread, Fid: 2, Count: 8192})
	if d, err := styx.UnmarshalDir(r.Data); err != nil || d.Name != "big" {
		t.Errorf("dir read %v %v", d, err)
	}
	none := session(t, "none")
	rpc(t, none, &styx.Fcall{Type: styx.Twalk, Fid: 0, Newfid: 1, Wname: []string{"dir", "big"}})
	if r := apply(none, &styx.Fcall{Type: styx.Tremove, Fid: 1}); r.Ename != eaccess {
		t.Errorf("remove by none: %q", r.Ename)
	}

	// Removing frees the blocks.
	sb := getsuper(bread)
	free := sb.tfree
	sb.put()
	rpc(t, cp, &styx.Fcall{Type: styx.Twalk, Fid: 0, Newfid: 1, Wname: []string{"dir", "big"}})
	rpc(t, cp, &styx.Fcall{Type: styx.Tremove, Fid: 1})
	sb = getsuper(bread)
	if sb.tfree <= free+300 {
		t.Errorf("free blocks %d, then %d", free, sb.tfree)
	}
	sb.put()
}

// checkout runs the check and returns what it prints.
func checkout(flags int) string {
	consout = &strings.Builder{}
	check(flags)
	s := consout.String()
	consout = nil
	return s
}

func TestKfsCheck(t *testing.T) {
	img := filepath.Join(t.TempDir(), "kfs.img")
	if err := os.WriteFile(img, make([]byte, 400*512), 0o644); err != nil {
		t.Fatal(err)
	}
	readonly = false
	if e := start(img, true, 512); e != "" {
		t.Fatal(e)
	}
	cp := session(t, "inferno")
	rpc(t, cp, &styx.Fcall{Type: styx.Twalk, Fid: 0, Newfid: 1})
	rpc(t, cp, &styx.Fcall{Type: styx.Tcreate, Fid: 1, Name: "f", Perm: 0o664, Mode: 1})
	rpc(t, cp, &styx.Fcall{Type: styx.Twrite, Fid: 1, Data: make([]byte, 5000)})
	rpc(t, cp, &styx.Fcall{Type: styx.Tclunk, Fid: 1})
	want := "checking file system: main\ncheck free list\nlo = 15; hi = 399\n" +
		"       2 files\n     399 blocks in the file system\n      14 used blocks\n" +
		"     385 free blocks\n      11 maximum qid path\n"
	if got := checkout(0); got != want {
		t.Errorf("check:\n%s\nwant\n%s", got, want)
	}
	if got := checkout(cquiet | cpfile); got != "/f\ncheck free list\nlo = 15; hi = 399\n" {
		t.Errorf("check Pq:\n%s", got)
	}

	// Lose a block from the free list: check finds it missing, and
	// check f rebuilds the list.
	sb := getsuper(bread | bmod)
	n := get4(sb.fbuf, 0)
	put4(sb.fbuf, 0, n-1)
	sb.tfree--
	sb.touched()
	sb.put()
	if got := checkout(cquiet); !strings.Contains(got, "       1 missing blocks\n") || !strings.Contains(got, "missing: ") {
		t.Errorf("check q after losing a block:\n%s", got)
	}
	checkout(cquiet | cfree)
	if got := checkout(cquiet); got != "check free list\nlo = 15; hi = 399\n" {
		t.Errorf("check q after check f:\n%s", got)
	}
	if checkflags("cdfpPqrtwv") < 0 || checkflags("z") != -1 {
		t.Error("checkflags")
	}
}
