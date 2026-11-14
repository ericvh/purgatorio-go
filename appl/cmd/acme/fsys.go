package main

// The kinds of acme's files; the file server comes later.
const (
	qwEvent = iota
	nqid
)

const srvfd = 5

type fsys struct{ fd int }

func (fs *fsys) one() {}

func startfsys() {}

func fsysEvents(w *Window) {}

func fsysWindowGone(w *Window) {}
