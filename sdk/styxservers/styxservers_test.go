package styxservers

import (
	"io"
	"testing"

	"github.com/ericvh/purgatorio-go/styx"
)

type pipeRW struct {
	io.Reader
	io.Writer
}

type client struct {
	t   *testing.T
	w   io.Writer
	r   io.Reader
	buf []byte
}

func (c *client) rpc(f *styx.Fcall) *styx.Fcall {
	c.t.Helper()
	b, err := styx.Marshal(nil, f)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.w.Write(b); err != nil {
		c.t.Fatal(err)
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c.r, hdr); err != nil {
		c.t.Fatal(err)
	}
	msg := make([]byte, styx.Size(hdr))
	copy(msg, hdr)
	if _, err := io.ReadFull(c.r, msg[4:]); err != nil {
		c.t.Fatal(err)
	}
	r, err := styx.Unmarshal(msg, 0)
	if err != nil {
		c.t.Fatal(err)
	}
	if r.Tag != f.Tag {
		c.t.Fatalf("tag %d, want %d", r.Tag, f.Tag)
	}
	return r
}

func dir(name string, perm uint32, path uint64) styx.Dir {
	d := styx.Dir{Name: name, UID: "me", GID: "me", Mode: perm, Qid: styx.Qid{Path: path}}
	if perm&styx.DMDIR != 0 {
		d.Qid.Type = styx.QTDIR
	}
	return d
}

func setup(t *testing.T) (*client, *Tree) {
	tree := NewTree()
	tree.Create(0, dir(".", styx.DMDIR|0o555, 0))
	tree.Create(0, dir("a", 0o444, 1))
	tree.Create(0, dir("sub", styx.DMDIR|0o555, 2))
	tree.Create(2, dir("b", 0o444, 3))
	cr, cw := io.Pipe()
	sr, sw := io.Pipe()
	srv := New(pipeRW{cr, sw}, tree, 0)
	go func() {
		for {
			m, err := srv.Next()
			if err != nil {
				sw.Close()
				return
			}
			if m.Type == styx.Tread {
				if c := srv.GetFid(m.Fid); c != nil && c.Path == 1 && c.IsOpen {
					srv.Reply(ReadStr(m, "hello"))
					continue
				}
			}
			srv.Default(m)
		}
	}()
	t.Cleanup(func() { cw.Close() })
	return &client{t: t, w: cw, r: sr}, tree
}

func TestServer(t *testing.T) {
	c, tree := setup(t)
	r := c.rpc(&styx.Fcall{Type: styx.Tattach, Tag: 1, Fid: 0, Afid: styx.NOFID, Uname: "me"})
	if r.Type != styx.Rerror || r.Ename != "Tversion not seen" {
		t.Fatalf("attach before version: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Tversion, Tag: styx.NOTAG, Msize: 16384, Version: "9P2000.u"})
	if r.Type != styx.Rversion || r.Msize != MAXRPC || r.Version != "9P2000" {
		t.Fatalf("version: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Tauth, Tag: 1, Afid: 9})
	if r.Ename != "authentication not required" {
		t.Fatalf("auth: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Tattach, Tag: 1, Fid: 0, Afid: styx.NOFID, Uname: "me"})
	if r.Type != styx.Rattach || r.Qid.Type != styx.QTDIR {
		t.Fatalf("attach: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Twalk, Tag: 1, Fid: 0, Newfid: 1, Wname: []string{"sub", "b"}})
	if r.Type != styx.Rwalk || len(r.Wqid) != 2 || r.Wqid[1].Path != 3 {
		t.Fatalf("walk: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Twalk, Tag: 1, Fid: 0, Newfid: 2, Wname: []string{"sub", "nope"}})
	if r.Type != styx.Rwalk || len(r.Wqid) != 1 {
		t.Fatalf("partial walk: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Twalk, Tag: 1, Fid: 0, Newfid: 2, Wname: []string{"nope"}})
	if r.Type != styx.Rerror || r.Ename != string(Enotfound) {
		t.Fatalf("failed walk: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Twalk, Tag: 1, Fid: 0, Newfid: 1})
	if r.Ename != string(Einuse) {
		t.Fatalf("walk to fid in use: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Twalk, Tag: 1, Fid: 0, Newfid: 2, Wname: []string{"a"}})
	r = c.rpc(&styx.Fcall{Type: styx.Topen, Tag: 1, Fid: 2, Mode: OWRITE})
	if r.Ename != string(Eperm) {
		t.Fatalf("open for write: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Topen, Tag: 1, Fid: 2, Mode: OREAD})
	if r.Type != styx.Ropen || r.Iounit != MAXRPC-styx.IOHDRSZ {
		t.Fatalf("open: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Tread, Tag: 1, Fid: 2, Offset: 1, Count: 100})
	if string(r.Data) != "ello" {
		t.Fatalf("read: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Tstat, Tag: 1, Fid: 2})
	d, err := styx.UnmarshalDir(r.Stat)
	if err != nil || d.Name != "a" {
		t.Fatalf("stat: %+v %v", d, err)
	}

	// Directory reads, in pieces small enough to need the saved offset.
	tree.Create(0, dir("c", 0o444, 4))
	r = c.rpc(&styx.Fcall{Type: styx.Twalk, Tag: 1, Fid: 0, Newfid: 3})
	r = c.rpc(&styx.Fcall{Type: styx.Topen, Tag: 1, Fid: 3, Mode: OREAD})
	var names []string
	off := uint64(0)
	for {
		r = c.rpc(&styx.Fcall{Type: styx.Tread, Tag: 1, Fid: 3, Offset: off, Count: 70})
		if r.Type != styx.Rread {
			t.Fatalf("dir read: %+v", r)
		}
		if len(r.Data) == 0 {
			break
		}
		off += uint64(len(r.Data))
		for b := r.Data; len(b) > 0; {
			n := int(b[0]) | int(b[1])<<8 + 2
			d, err := styx.UnmarshalDir(b[:n])
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, d.Name)
			b = b[n:]
		}
	}
	if len(names) != 3 || names[0] != "c" || names[1] != "sub" || names[2] != "a" {
		t.Fatalf("dir entries %q", names)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Twrite, Tag: 1, Fid: 2, Data: []byte("x")})
	if r.Ename != string(Eperm) {
		t.Fatalf("write: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Tclunk, Tag: 1, Fid: 2})
	if r.Type != styx.Rclunk {
		t.Fatalf("clunk: %+v", r)
	}
	r = c.rpc(&styx.Fcall{Type: styx.Tclunk, Tag: 1, Fid: 2})
	if r.Ename != string(Ebadfid) {
		t.Fatalf("second clunk: %+v", r)
	}
}

func TestTree(t *testing.T) {
	tree := NewTree()
	if err := tree.Create(0, dir(".", styx.DMDIR|0o555, 0)); err != nil {
		t.Fatal(err)
	}
	tree.Create(0, dir("d", styx.DMDIR|0o555, 1))
	tree.Create(1, dir("f", 0o444, 2))
	if err := tree.Create(1, dir("f", 0o444, 3)); err != Eexists {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := tree.Create(9, dir("g", 0o444, 4)); err != Enotfound {
		t.Fatalf("missing parent: %v", err)
	}
	if p, ok := tree.Getpath(2); !ok || p != "./d/f" {
		t.Fatalf("getpath %q", p)
	}
	nd := styx.Dir{Qid: styx.Qid{Path: ^uint64(0), Vers: ^uint32(0), Type: ^uint8(0)}, Mode: ^uint32(0),
		Atime: ^uint32(0), Mtime: ^uint32(0), Length: ^uint64(0), Type: ^uint16(0), Dev: ^uint32(0), Name: "g"}
	if err := tree.Wstat(2, nd); err != nil {
		t.Fatal(err)
	}
	if d, _ := tree.Walk(1, "g"); d == nil || d.Qid.Path != 2 || d.Mode != 0o444 {
		t.Fatalf("after wstat %+v", d)
	}
	if d, _ := tree.Walk(2, ".."); d == nil || d.Qid.Path != 1 {
		t.Fatalf(".. of a file: %+v", d)
	}
	tree.Remove(1)
	if _, err := tree.Stat(2); err != Enotfound {
		t.Fatalf("child survived its parent: %v", err)
	}
	if ds := tree.Readdir(0, 0, 10); len(ds) != 0 {
		t.Fatalf("root still lists %d entries", len(ds))
	}
}

func TestOpenOK(t *testing.T) {
	cases := []struct {
		u     string
		mode  int
		perm  uint32
		allow bool
	}{
		{"me", OREAD, 0o400, true},
		{"you", OREAD, 0o400, false},
		{"you", OREAD, 0o404, true},
		{"g", ORDWR, 0o060, true},
		{"me", OREAD | OTRUNC, 0o600, true},
		{"me", OREAD | OTRUNC, styx.DMDIR | 0o700, false},
		{"me", OEXEC, 0o600, false},
	}
	for _, c := range cases {
		if got := OpenOK(c.u, c.mode, c.perm, "me", "g"); got != c.allow {
			t.Errorf("OpenOK(%q, %d, %o) = %v", c.u, c.mode, c.perm, got)
		}
	}
	if OpenMode(OREAD|OTRUNC|ORCLOSE) != OREAD || OpenMode(OEXEC) != -1 {
		t.Error("OpenMode")
	}
}
