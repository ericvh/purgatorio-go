package lethe

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ericvh/purgatorio-go/styx"
)

// The byte vectors here are the Rust codec's (crates/purg-lethe/src/msg.rs
// in Purgatorio), so the two stay wire-compatible.

func round(t *testing.T, id uint32, f Frame) []byte {
	t.Helper()
	b := Encode(id, f)
	gid, g, err := Decode(b)
	if err != nil || gid != id || !reflect.DeepEqual(g, f) {
		t.Fatalf("round trip of %#v: %#v %v", f, g, err)
	}
	return b
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func TestVectors(t *testing.T) {
	e := round(t, 0x04030201, &Rerror{Ename: "file does not exist"})
	if want := cat([]byte{31, 0, 0, 0, 1, 5, 1, 2, 3, 4, 19, 0}, []byte("file does not exist")); !bytes.Equal(e, want) {
		t.Errorf("rerror %v", e)
	}
	r := (&Request{Time: 0x0102, Op: &Read{P: "/a", Offset: 7, Count: 8192, Cond: true, Have: 9}}).Encode()
	want := []byte{2, 1, 0, 0, 0, 0, 0, 0, 0x12, 2, 0, '/', 'a', 7, 0, 0, 0, 0, 0, 0, 0, 0, 0x20, 0, 0, 1, 9, 0, 0, 0}
	if !bytes.Equal(r, want) {
		t.Errorf("request %v", r)
	}
	tm, ok, op, err := DecodeRequest(r)
	if !ok || tm != 0x0102 || err != nil || !reflect.DeepEqual(op, &Read{P: "/a", Offset: 7, Count: 8192, Cond: true, Have: 9}) {
		t.Errorf("decode request %v %v %#v %v", tm, ok, op, err)
	}
	q := styx.Qid{Path: 5, Vers: 6, Type: 0x80}
	d := EncodeReply(&Rreaddir{Qid: q, Cookie: []byte("z")})
	if want := []byte{0x15, 0x80, 6, 0, 0, 0, 5, 0, 0, 0, 0, 0, 0, 0, 1, 0, 'z', 0, 0, 0, 0}; !bytes.Equal(d, want) {
		t.Errorf("readdir %v", d)
	}
}

func TestClearVectors(t *testing.T) {
	op := &Read{P: "/a", Offset: 7, Count: 8192, Cond: true, Have: 9}
	tc := ClearFrame(0x04030201, op)
	want := []byte{32, 0, 0, 0, 1, 9, 1, 2, 3, 4, 0x12, 2, 0, '/', 'a', 7, 0, 0, 0, 0, 0, 0, 0, 0, 0x20, 0, 0, 1, 9, 0, 0, 0}
	if !bytes.Equal(tc, want) {
		t.Errorf("tclear %v", tc)
	}
	_, f, err := Decode(tc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOp(f.(*Tclear).Request)
	if err != nil || !reflect.DeepEqual(got, op) {
		t.Errorf("decode op %#v %v", got, err)
	}
	if !bytes.Equal((&Request{Time: 1, Op: op}).Encode()[8:], f.(*Tclear).Request) {
		t.Error("clear op differs from the sealed plaintext")
	}
	if s := ClearFrame(5, &Stat{P: "/"}); !bytes.Equal(s, []byte{14, 0, 0, 0, 1, 9, 5, 0, 0, 0, 0x10, 1, 0, '/'}) {
		t.Errorf("stat %v", s)
	}
	if s := ClearFrame(6, &Readdir{P: "/d", Cookie: []byte("k"), Count: 256}); !bytes.Equal(s,
		[]byte{22, 0, 0, 0, 1, 9, 6, 0, 0, 0, 0x14, 2, 0, '/', 'd', 1, 0, 'k', 0, 1, 0, 0}) {
		t.Errorf("readdir %v", s)
	}
	reply := EncodeReply(&Rread{Qid: styx.Qid{Path: 5, Vers: 6}, Data: []byte("hi")})
	rc := round(t, 0x04030201, &Rclear{Reply: reply})
	if want := []byte{31, 0, 0, 0, 1, 10, 1, 2, 3, 4, 0x13, 0, 6, 0, 0, 0, 5, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 'h', 'i'}; !bytes.Equal(rc, want) {
		t.Errorf("rclear %v", rc)
	}
	e := Encode(1, &Rclear{Reply: EncodeReply(&RError{Ename: "permission denied"})})
	if want := cat([]byte{30, 0, 0, 0, 1, 10, 1, 0, 0, 0, 0x7f, 17, 0}, []byte("permission denied")); !bytes.Equal(e, want) {
		t.Errorf("rclear error %v", e)
	}
	long := append(EncodeOp(&Stat{P: "/"}), 0)
	for _, c := range []struct {
		b    []byte
		want Undecoded
	}{
		{long, Undecoded{Bad: true}},
		{nil, Undecoded{Bad: true}},
		{[]byte{0x21}, Undecoded{Write: true, Code: 0x21}},
		{[]byte{0x16}, Undecoded{Code: 0x16}},
	} {
		_, err := DecodeOp(c.b)
		if u, ok := err.(*Undecoded); !ok || *u != c.want {
			t.Errorf("DecodeOp(%v) = %v", c.b, err)
		}
	}
}

func TestWalkVectors(t *testing.T) {
	op := &Walk{P: "/a", Names: []string{"b", "cd"}}
	b := EncodeOp(op)
	if want := []byte{0x18, 2, 0, '/', 'a', 2, 0, 1, 0, 'b', 2, 0, 'c', 'd'}; !bytes.Equal(b, want) {
		t.Errorf("twalk %v", b)
	}
	if got, err := DecodeOp(b); err != nil || !reflect.DeepEqual(got, op) {
		t.Errorf("decode twalk %#v %v", got, err)
	}
	q := styx.Qid{Path: 5, Vers: 6, Type: 0x80}
	stopped := &Rwalk{Qids: []styx.Qid{q}, Error: "file does not exist"}
	sb := EncodeReply(stopped)
	want := cat([]byte{0x19, 1, 0, 0x80, 6, 0, 0, 0, 5, 0, 0, 0, 0, 0, 0, 0, 19, 0}, []byte("file does not exist"))
	if !bytes.Equal(sb, want) {
		t.Errorf("rwalk %v", sb)
	}
	if got, err := DecodeReply(sb); err != nil || !reflect.DeepEqual(got, stopped) {
		t.Errorf("decode rwalk %#v %v", got, err)
	}
	d := styx.Dir{Name: "cd"}
	done := &Rwalk{Qids: []styx.Qid{q, q}, Stat: &d}
	db := EncodeReply(done)
	packed := styx.MarshalDir(nil, &d)
	if !bytes.Equal(db[:3], []byte{0x19, 2, 0}) || !bytes.Equal(db[29:31], []byte{0, 0}) || !bytes.Equal(db[31:], packed) {
		t.Errorf("rwalk done %v", db)
	}
	if got, err := DecodeReply(db); err != nil || !reflect.DeepEqual(got, done) {
		t.Errorf("decode rwalk done %#v %v", got, err)
	}
	if _, err := DecodeReply(cat(want, packed)); err == nil {
		t.Error("stat after an error accepted")
	}
	if _, err := DecodeReply([]byte{0x19, 0, 0, 0, 0}); err == nil {
		t.Error("no stat after success accepted")
	}
	if _, err := DecodeOp(append(b, 0)); err == nil {
		t.Error("trailing byte accepted")
	}
	many := []byte{0x18, 1, 0, '/', 65, 0}
	for i := 0; i < 65; i++ {
		many = append(many, 1, 0, 'x')
	}
	if _, err := DecodeOp(many); err == nil {
		t.Error("65 names accepted")
	}
	many[4] = 64
	if _, err := DecodeOp(many[:len(many)-3]); err != nil {
		t.Errorf("64 names: %v", err)
	}
}

func TestFramesRoundTrip(t *testing.T) {
	ta := &Tauth{Rights: 1, Prefix: "/", Uname: "glenda", Body: bytes.Repeat([]byte{2}, 32)}
	for i := range ta.Eph {
		ta.Eph[i] = 1
	}
	for i := range ta.Signature {
		ta.Signature[i] = 3
	}
	b := round(t, 9, ta)
	if n, err := FrameSize([4]byte(b[:4])); err != nil || n != len(b) {
		t.Errorf("frame size %d %v", n, err)
	}
	if !bytes.HasPrefix(ta.Message(), []byte(AuthContext)) {
		t.Error("message context")
	}
	round(t, 1, &Treq{Ticket: bytes.Repeat([]byte{4}, 80), Sealed: bytes.Repeat([]byte{6}, 40)})
	round(t, 1, &Rreq{Sealed: bytes.Repeat([]byte{6}, TagLen)})
	round(t, 2, &Rauth{Sealed: bytes.Repeat([]byte{6}, 30)})
	g := &Grant{Now: 1, Expires: 2, Rights: 1, Prefix: "/lib", Ticket: bytes.Repeat([]byte{9}, 10)}
	if gg, err := DecodeGrant(g.Encode()); err != nil || !reflect.DeepEqual(gg, g) {
		t.Errorf("grant %#v %v", gg, err)
	}
}

func TestStrict(t *testing.T) {
	b := ErrorFrame(1, "x")
	long := append(append([]byte{}, b...), 0)
	long[0]++
	if _, _, err := Decode(long); err == nil || err.Error() != EMessage {
		t.Errorf("long: %v", err)
	}
	if _, _, err := Decode(b[:len(b)-1]); err == nil {
		t.Error("short accepted")
	}
	v := append([]byte{}, b...)
	v[4] = 2
	if _, _, err := Decode(v); err == nil || err.Error() != EVersion {
		t.Errorf("version: %v", err)
	}
	ty := append([]byte{}, b...)
	ty[5] = 6
	if _, _, err := Decode(ty); err == nil || err.Error() != EMessage {
		t.Errorf("type: %v", err)
	}
	if _, err := FrameSize([4]byte{1, 0, 1, 0}); err == nil {
		t.Error("big frame size")
	}
	if _, err := FrameSize([4]byte{9, 0, 0, 0}); err == nil {
		t.Error("small frame size")
	}
	short := Encode(1, &Rreq{Sealed: make([]byte, TagLen-1)})
	if _, _, err := Decode(short); err == nil {
		t.Error("short seal accepted")
	}
	r := (&Request{Time: 1, Op: &Read{P: "/", Count: 1}}).Encode()
	r[len(r)-1] = 1
	if tm, ok, _, err := DecodeRequest(r); !ok || tm != 1 || err == nil || !err.(*Undecoded).Bad {
		t.Errorf("cond 0 with a version: %v", err)
	}
	w := append(le.AppendUint64(nil, 1), 0x20)
	if _, ok, _, err := DecodeRequest(w); !ok || !err.(*Undecoded).Write {
		t.Errorf("write: %v", err)
	}
	w[8] = 0x40
	if _, ok, _, err := DecodeRequest(w); !ok || err.(*Undecoded).Code != 0x40 || err.(*Undecoded).Write {
		t.Errorf("stream: %v", err)
	}
	if _, ok, _, _ := DecodeRequest([]byte{1}); ok {
		t.Error("short request has a time")
	}
}

func TestReplies(t *testing.T) {
	d := styx.Dir{Name: "f", UID: "u"}
	packed := styx.MarshalDir(nil, &d)
	for _, r := range []Reply{
		&Rstat{Dir: d},
		&Rread{Data: []byte("abc")},
		&Rread{Unchanged: true, Data: []byte{}},
		&Rreaddir{Cookie: []byte("f"), Entries: packed},
		&RError{Ename: "permission denied"},
	} {
		got, err := DecodeReply(EncodeReply(r))
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Errorf("reply %#v: %#v %v", r, got, err)
		}
	}
	if _, err := DecodeReply(EncodeReply(&Rread{Unchanged: true, Data: []byte("x")})); err == nil {
		t.Error("unchanged with data accepted")
	}
	if _, err := DecodeReply(EncodeReply(&Rreaddir{Entries: []byte{1, 2, 3}})); err == nil {
		t.Error("bad entries accepted")
	}
}

func TestPaths(t *testing.T) {
	for _, ok := range []string{"/", "/a", "/a/b", "/a.b/..c", "/λ"} {
		if !ValidPath(ok) {
			t.Errorf("%q invalid", ok)
		}
	}
	for _, bad := range []string{"", "a", "//", "/a/", "/a//b", "/.", "/..", "/a/../b", "/a/./b", "/a\x00"} {
		if ValidPath(bad) {
			t.Errorf("%q valid", bad)
		}
	}
	if ValidPath("/" + string(bytes.Repeat([]byte("x"), MaxElem+1))) {
		t.Error("long element")
	}
	if ValidPath(string(bytes.Repeat([]byte("/x"), MaxPath/2+1))) {
		t.Error("long path")
	}
	if !Within("/a/b", "/") || !Within("/a", "/a") || !Within("/a/b", "/a") || Within("/ab", "/a") || Within("/", "/a") {
		t.Error("within")
	}
	if Join("/", "a") != "/a" || Join("/a", "b") != "/a/b" {
		t.Error("join")
	}
	if a := Ancestors("/a/b/c"); !reflect.DeepEqual(a, []string{"/", "/a", "/a/b"}) {
		t.Errorf("ancestors %v", a)
	}
	if len(Ancestors("/")) != 0 || !reflect.DeepEqual(Ancestors("/a"), []string{"/"}) {
		t.Error("ancestors")
	}
}
