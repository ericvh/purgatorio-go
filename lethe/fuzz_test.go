package lethe

import (
	"bytes"
	"testing"

	"github.com/ericvh/purgatorio-go/pgauth"
)

// FuzzHandle feeds arbitrary frames to a server that takes everything:
// nothing may panic, every answer is one frame, and what decodes
// re-encodes to the same bytes.
func FuzzHandle(f *testing.F) {
	f.Add(ClearFrame(1, &Stat{P: "/"}))
	f.Add(ClearFrame(2, &Walk{P: "/", Names: []string{"lib", "a"}}))
	f.Add(ErrorFrame(3, "x"))
	f.Add(Encode(4, &Treq{Ticket: make([]byte, 60), Sealed: make([]byte, 40)}))
	f.Add(Encode(5, &Tauth{Prefix: "/", Uname: "glenda", Body: make([]byte, 32)}))
	f.Add(Encode(6, &Tauth2{Prefix: "/", Uname: "glenda", Body: make([]byte, 1984), Signature: make([]byte, 3373)}))
	s := sealedServer(pgauth.AcceptHybrid|pgauth.AcceptClassical, 9)
	s.Clear = true
	tr := testTree()
	f.Fuzz(func(t *testing.T, b []byte) {
		r := &rig{s: s, tree: tr, now: t0}
		out := r.send(b)
		if _, _, err := Decode(out); err != nil {
			t.Fatalf("answer does not decode: %v", err)
		}
		if id, fr, err := Decode(b); err == nil && !bytes.Equal(Encode(id, fr), b) {
			t.Fatalf("re-encoding differs")
		}
		if op, err := DecodeOp(b); err == nil && !bytes.Equal(EncodeOp(op), b) {
			t.Fatalf("op re-encoding differs")
		}
		if x, err := DecodeReply(b); err == nil && !bytes.Equal(EncodeReply(x), b) {
			t.Fatalf("reply re-encoding differs")
		}
	})
}
