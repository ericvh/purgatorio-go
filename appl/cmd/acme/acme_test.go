package main

import "testing"

func textOf(s string) *Text {
	t := &Text{what: Body}
	t.file = &File{b: []rune(s)}
	return t
}

func TestAddress(t *testing.T) {
	tx := textOf("one\ntwo\nthree\nfour\n")
	for _, c := range []struct {
		addr   string
		q0, q1 int
		err    string
	}{
		{"1", 0, 4, ""},
		{"2", 4, 8, ""},
		{"4", 14, 19, ""},
		{"0", 0, 0, ""},
		{"$", 19, 19, ""},
		{"#5", 5, 5, ""},
		{"2,3", 4, 14, ""},
		{",", 0, 19, ""},
		{"/thr/", 8, 11, ""},
		{"/o/", 0, 1, ""},
		{"3-1", 4, 8, ""},
		{"1+1", 4, 8, ""},
		{"#4,#7", 4, 7, ""},
		{"2;+1", 4, 14, ""},
		{"9", 0, 0, "address out of range"},
		{"/zz/", 0, 0, "no match for regexp"},
	} {
		q0, q1, err := address(tx, 0, 0, c.addr)
		if err != c.err || err == "" && (q0 != c.q0 || q1 != c.q1) {
			t.Errorf("%q: got %d,%d %q, want %d,%d %q", c.addr, q0, q1, err, c.q0, c.q1, c.err)
		}
	}
	// Relative to dot: the line after line 2.
	if q0, q1, _ := address(tx, 4, 8, "+1"); q0 != 8 || q1 != 14 {
		t.Errorf("+1 from line 2: %d,%d", q0, q1)
	}
	if q0, q1, _ := address(tx, 4, 8, "-"); q0 != 0 || q1 != 4 {
		t.Errorf("- from line 2: %d,%d", q0, q1)
	}
	if q0, q1, _ := address(tx, 8, 11, "?o?"); q0 != 6 || q1 != 7 {
		t.Errorf("?o? back: %d,%d", q0, q1)
	}
}

func TestUndo(t *testing.T) {
	f := &File{b: []rune("hello")}
	f.Mark()
	f.Insert(5, []rune(" world"))
	f.Insert(0, []rune(">"))
	if string(f.b) != ">hello world" || !f.Dirty() {
		t.Fatalf("after inserts %q dirty %v", string(f.b), f.Dirty())
	}
	f.Mark()
	f.Delete(0, 1)
	if _, _, ok := f.UndoRedo(true); !ok || string(f.b) != ">hello world" {
		t.Errorf("undo delete: %q", string(f.b))
	}
	if _, _, ok := f.UndoRedo(true); !ok || string(f.b) != "hello" || f.Dirty() {
		t.Errorf("undo inserts: %q dirty %v", string(f.b), f.Dirty())
	}
	if _, _, ok := f.UndoRedo(true); ok {
		t.Error("undo with nothing to undo")
	}
	f.UndoRedo(false)
	if string(f.b) != ">hello world" || !f.Dirty() {
		t.Errorf("redo: %q", string(f.b))
	}
	f.Clean()
	if f.Dirty() {
		t.Error("dirty after Clean")
	}
}

func TestDoubleclick(t *testing.T) {
	tx := textOf("f(a, (b)) x\nline two\n")
	for _, c := range []struct{ q, q0, q1 int }{
		{2, 2, 8},    // after (: up to its )
		{8, 2, 8},    // at the closing )
		{6, 6, 7},    // inside the inner ()
		{12, 12, 21}, // start of a line: the line
		{10, 10, 11}, // a word
	} {
		q0, q1 := tx.doubleclick(c.q)
		if q0 != c.q0 || q1 != c.q1 {
			t.Errorf("doubleclick %d: %d,%d, want %d,%d", c.q, q0, q1, c.q0, c.q1)
		}
	}
}
