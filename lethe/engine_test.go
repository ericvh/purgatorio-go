package lethe

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ericvh/purgatorio-go/styx"
)

// memTree is a tree in memory, by path, for the engine tests (the Rust
// engine tests' Mem).
type memTree struct {
	mu    sync.Mutex
	files map[string]*memEntry
}

type memEntry struct {
	mode  uint32
	owner string
	data  []byte // nil for a directory
	vers  uint32
}

func (m *memTree) add(p string, mode uint32, owner string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string]*memEntry{}
	}
	m.files[p] = &memEntry{mode: mode, owner: owner, data: data, vers: 1}
}

func (m *memTree) change(p string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.files[p]
	e.data = data
	e.vers++
}

func (m *memTree) dir(p string) (styx.Dir, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.files[p]
	if !ok {
		return styx.Dir{}, false
	}
	name := p[strings.LastIndexByte(p, '/')+1:]
	if name == "" {
		name = "/"
	}
	d := styx.Dir{Qid: styx.Qid{Path: uint64(len(p)), Vers: e.vers}, Mode: e.mode,
		Length: uint64(len(e.data)), Name: name, UID: e.owner, GID: e.owner, MUID: e.owner}
	if e.data == nil {
		d.Qid.Type = styx.QTDIR
		d.Mode |= styx.DMDIR
	}
	return d, true
}

func (m *memTree) Stat(p string) (styx.Dir, error) {
	d, ok := m.dir(p)
	if !ok {
		return d, Error("file does not exist")
	}
	return d, nil
}

func (m *memTree) Read(p string, off uint64, buf []byte) (styx.Qid, int, error) {
	d, err := m.Stat(p)
	if err != nil {
		return styx.Qid{}, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data := m.files[p].data
	if off > uint64(len(data)) {
		off = uint64(len(data))
	}
	return d.Qid, copy(buf, data[off:]), nil
}

func (m *memTree) List(p string, after []byte, limit int) (styx.Qid, []styx.Dir, error) {
	d, err := m.Stat(p)
	if err != nil {
		return styx.Qid{}, nil, err
	}
	prefix := p + "/"
	if p == "/" {
		prefix = "/"
	}
	var names []string
	m.mu.Lock()
	for k := range m.files {
		if strings.HasPrefix(k, prefix) && len(k) > len(prefix) && !strings.Contains(k[len(prefix):], "/") {
			names = append(names, k)
		}
	}
	m.mu.Unlock()
	var all []styx.Dir
	for _, n := range names {
		if e, ok := m.dir(n); ok && bytes.Compare([]byte(e.Name), after) > 0 {
			all = append(all, e)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	if len(all) > limit {
		all = all[:limit]
	}
	return d.Qid, all, nil
}

func testTree() *memTree {
	m := &memTree{}
	m.add("/", 0o755, "sys", nil)
	m.add("/hello", 0o644, "sys", []byte("hello, lethe\n"))
	m.add("/secret", 0o600, "sys", []byte("no"))
	m.add("/mine", 0o600, "glenda", []byte("yes"))
	m.add("/lib", 0o755, "sys", nil)
	m.add("/lib/a", 0o644, "sys", []byte("a"))
	m.add("/locked", 0o700, "sys", nil)
	m.add("/locked/x", 0o644, "sys", []byte("x"))
	m.add("/many", 0o755, "sys", nil)
	for i := 0; i < 200; i++ {
		m.add(fmt.Sprintf("/many/f%03d", i), 0o644, "sys", []byte{})
	}
	return m
}

const t0 = 1_700_000_000

// rig is a server and a tree behind an in-memory transport.
type rig struct {
	s      *Server
	tree   Tree
	trust  Trust
	now    uint64
	events []Event
	sent   [][]byte
	n      byte
}

func (r *rig) env() *Env {
	return &Env{Now: r.now, Random: func(b []byte) {
		for i := range b {
			r.n++
			b[i] = r.n
		}
	}, Trust: func(string) Trust { return r.trust }}
}

func (r *rig) send(f []byte) []byte {
	r.sent = append(r.sent, f)
	out, ev := r.s.Handle(r.tree, r.env(), f)
	r.events = append(r.events, ev)
	return out
}

func (r *rig) clear(op Op) (Reply, error) { return ClearReply(7, r.send(ClearFrame(7, op))) }

func (r *rig) clearRead(p string, cond bool, have uint32) (Reply, error) {
	return r.clear(&Read{P: p, Count: 8192, Cond: cond, Have: have})
}

func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}

// A server with -A and no trust answers clear requests as none.
func TestClearRequests(t *testing.T) {
	tr := testTree()
	tr.add("/nones", 0o600, "none", []byte("owned by a user called none"))
	r := &rig{s: &Server{Config: DefaultConfig(), Clear: true}, tree: tr, now: t0}
	x, err := r.clear(&Stat{P: "/hello"})
	if err != nil {
		t.Fatal(err)
	}
	if d := x.(*Rstat).Dir; d.Name != "hello" || d.Length != 13 {
		t.Errorf("stat %+v", d)
	}
	if ev := r.events[0]; ev.Kind != "answered" || ev.Uname != "none" || ev.Op != "stat" || ev.Path != "/hello" || ev.Error != "" {
		t.Errorf("event %+v", ev)
	}
	if s := r.sent[0]; s[5] != FTclear || !bytes.Equal(s[10:], EncodeOp(&Stat{P: "/hello"})) {
		t.Errorf("sent %v", s)
	}
	x, err = r.clearRead("/hello", false, 0)
	if err != nil || string(x.(*Rread).Data) != "hello, lethe\n" {
		t.Fatalf("read %v %v", x, err)
	}
	qid := x.(*Rread).Qid
	reply := r.send(r.sent[len(r.sent)-1])
	if reply[5] != FRclear || !bytes.Contains(reply, []byte("hello, lethe")) {
		t.Errorf("reply %v", reply)
	}
	x, err = r.clearRead("/hello", true, qid.Vers)
	if err != nil || !x.(*Rread).Unchanged || len(x.(*Rread).Data) != 0 {
		t.Errorf("conditional %v %v", x, err)
	}
	tr.change("/hello", []byte("changed\n"))
	x, err = r.clearRead("/hello", true, qid.Vers)
	if err != nil || x.(*Rread).Unchanged || string(x.(*Rread).Data) != "changed\n" {
		t.Errorf("changed %v %v", x, err)
	}
	var cookie []byte
	var names []string
	for {
		x, err := r.clear(&Readdir{P: "/many", Cookie: cookie, Count: 1000})
		if err != nil {
			t.Fatal(err)
		}
		rd := x.(*Rreaddir)
		ds, _ := Dirs(rd.Entries)
		for _, d := range ds {
			names = append(names, d.Name)
		}
		if len(rd.Cookie) == 0 {
			break
		}
		cookie = rd.Cookie
	}
	if len(names) != 200 || names[199] != "f199" {
		t.Errorf("paging: %d names", len(names))
	}
	for _, p := range []string{"/secret", "/mine", "/nones"} {
		_, err := r.clearRead(p, false, 0)
		wantErr(t, err, "permission denied")
	}
	_, err = r.clear(&Stat{P: "/locked/x"})
	wantErr(t, err, "permission denied")
	_, err = r.clear(&Readdir{P: "/locked", Count: 100})
	wantErr(t, err, "permission denied")
	_, err = r.clear(&Stat{P: "/nope"})
	wantErr(t, err, "file does not exist")
	_, err = r.clearRead("/lib", false, 0)
	wantErr(t, err, "file is a directory")
	for _, p := range []string{"/a/../b", "/..", "lib", "/lib/", ""} {
		_, err := r.clear(&Stat{P: p})
		wantErr(t, err, "file name syntax")
	}
	for _, c := range []struct {
		body []byte
		want string
	}{
		{[]byte{0x20}, "permission denied"},
		{[]byte{0x40}, "not implemented"},
		{[]byte{0x16}, "not implemented"},
		{[]byte{}, EMessage},
		{[]byte{0x10, 9}, EMessage},
	} {
		_, f, err := Decode(r.send(Encode(3, &Tclear{Request: c.body})))
		if err != nil {
			t.Fatal(err)
		}
		x, err := DecodeReply(f.(*Rclear).Reply)
		if e, ok := x.(*RError); err != nil || !ok || e.Ename != c.want {
			t.Errorf("%v: %#v %v", c.body, x, err)
		}
	}
	cfg := DefaultConfig()
	cfg.Allow = 0
	r = &rig{s: &Server{Config: cfg, Clear: true}, tree: tr, now: t0}
	_, err = r.clearRead("/hello", false, 0)
	wantErr(t, err, "permission denied")
}

// Neither end falls back: each mismatch is an error with a name.
func TestClearAndSealedDoNotMix(t *testing.T) {
	tr := testTree()
	r := &rig{s: &Server{Config: DefaultConfig()}, tree: tr, now: t0}
	_, err := r.clearRead("/hello", false, 0)
	wantErr(t, err, EAuthRequired)
	if f, ok := err.(*Failure); !ok || f.Kind != FailClear {
		t.Errorf("failure %#v", err)
	}
	if len(r.events) != 1 || r.events[0].Kind != "refused" || r.events[0].Error != EAuthRequired {
		t.Errorf("events %+v", r.events)
	}
	_, f, _ := Decode(r.send(Encode(1, &Tclear{Request: []byte{0xff}})))
	if e, ok := f.(*Rerror); !ok || e.Ename != EAuthRequired {
		t.Errorf("junk: %#v", f)
	}
	// A Tauth to a clear-only server.
	r = &rig{s: &Server{Config: DefaultConfig(), Clear: true}, tree: tr, now: t0}
	_, f, _ = Decode(r.send(Encode(1, &Tauth{Prefix: "/", Uname: "glenda"})))
	if e, ok := f.(*Rerror); !ok || e.Ename != EAuthNotRequired {
		t.Errorf("tauth: %#v", f)
	}
	// A clear client takes no sealed reply, and none for another id.
	op := &Stat{P: "/hello"}
	plain := r.send(ClearFrame(7, op))
	if _, err := ClearReply(7, plain); err != nil {
		t.Error(err)
	}
	_, err = ClearReply(8, plain)
	wantErr(t, err, EMessage)
	_, err = ClearReply(7, Encode(7, &Rreq{Sealed: make([]byte, TagLen)}))
	wantErr(t, err, EMessage)
}

func TestClearWalks(t *testing.T) {
	tr := testTree()
	tr.add("/lib/d", 0o755, "sys", nil)
	tr.add("/lib/d/e", 0o644, "sys", []byte("deep"))
	r := &rig{s: &Server{Config: DefaultConfig(), Clear: true}, tree: tr, now: t0}
	q := func(p string) styx.Qid { d, _ := tr.dir(p); return d.Qid }
	x, err := r.clear(&Walk{P: "/", Names: []string{"lib", "d", "e"}})
	if err != nil {
		t.Fatal(err)
	}
	w := x.(*Rwalk)
	if len(w.Qids) != 3 || w.Qids[0] != q("/lib") || w.Qids[2] != q("/lib/d/e") || w.Error != "" || w.Stat.Name != "e" {
		t.Errorf("walk %+v", w)
	}
	x, _ = r.clear(&Walk{P: "/lib"})
	if w := x.(*Rwalk); len(w.Qids) != 0 || w.Stat.Name != "lib" {
		t.Errorf("empty walk %+v", w)
	}
	for _, c := range []struct {
		names []string
		n     int
		why   string
	}{
		{[]string{"lib", "nope", "x"}, 1, "file does not exist"},
		{[]string{"hello", "x"}, 1, "not a directory"},
		{[]string{"locked", "x"}, 1, "permission denied"},
		{[]string{"lib", "..", "x"}, 1, "file name syntax"},
		{[]string{"nope"}, 0, "file does not exist"},
	} {
		x, err := r.clear(&Walk{P: "/", Names: c.names})
		if err != nil {
			t.Fatal(err)
		}
		if w := x.(*Rwalk); len(w.Qids) != c.n || w.Error != c.why || w.Stat != nil {
			t.Errorf("%v: %+v", c.names, w)
		}
	}
	_, err = r.clear(&Walk{P: "/locked/x", Names: []string{"x"}})
	wantErr(t, err, "permission denied")
	_, err = r.clear(&Walk{P: "/nope", Names: []string{"x"}})
	wantErr(t, err, "file does not exist")
}
