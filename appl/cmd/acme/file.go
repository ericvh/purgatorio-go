package main

// A File is the text of a window, shared by the windows Zerox makes,
// with its undo and redo logs (acme's file.b and elog.b).
type File struct {
	b       []rune
	text    []*Text // the texts showing it
	name    string
	isdir   bool
	seq     int // the change the text is at
	putseq  int // the change when last written or read
	maxseq  int // the last change number given out
	pending bool
	mod     bool
	undo    []delta
	redo    []delta
	unread  bool // not read from disk yet
	editing bool
}

// A delta is one change, recorded so that it can be undone: an insert
// of r at q0, or a delete of r from q0. seq groups the deltas of one
// user action.
type delta struct {
	insert bool
	q0     int
	r      []rune
	seq    int
}

func (f *File) Nc() int { return len(f.b) }

func (f *File) Read(q0, q1 int) []rune {
	q0, q1 = max(0, min(q0, len(f.b))), max(0, min(q1, len(f.b)))
	if q0 >= q1 {
		return nil
	}
	return append([]rune(nil), f.b[q0:q1]...)
}

func (f *File) Readc(q int) rune {
	if q < 0 || q >= len(f.b) {
		return 0
	}
	return f.b[q]
}

// Mark starts a new change, which Undo takes back in one go. The
// change gets its number when something is changed.
func (f *File) Mark() { f.pending = true }

func (f *File) change() {
	if f.pending || f.seq == 0 {
		f.maxseq++
		f.seq = f.maxseq
		f.pending = false
		f.redo = nil
	}
}

// Insert puts r in the file at q0 and logs it.
func (f *File) Insert(q0 int, r []rune) {
	if len(r) == 0 {
		return
	}
	f.change()
	f.insert(q0, r)
	f.undo = append(f.undo, delta{insert: true, q0: q0, r: append([]rune(nil), r...), seq: f.seq})
	f.mod = true
}

func (f *File) insert(q0 int, r []rune) {
	q0 = max(0, min(q0, len(f.b)))
	nb := make([]rune, 0, len(f.b)+len(r))
	nb = append(nb, f.b[:q0]...)
	nb = append(nb, r...)
	f.b = append(nb, f.b[q0:]...)
}

// Delete removes q0 to q1 and logs it.
func (f *File) Delete(q0, q1 int) {
	q0, q1 = max(0, min(q0, len(f.b))), max(0, min(q1, len(f.b)))
	if q0 >= q1 {
		return
	}
	f.change()
	f.undo = append(f.undo, delta{q0: q0, r: append([]rune(nil), f.b[q0:q1]...), seq: f.seq})
	f.delete(q0, q1)
	f.mod = true
}

func (f *File) delete(q0, q1 int) { f.b = append(f.b[:q0], f.b[q1:]...) }

// Reset forgets the logs, as after a Get.
func (f *File) Reset() {
	f.undo, f.redo = nil, nil
	f.seq, f.putseq = 0, 0
	f.mod, f.pending = false, false
}

// Clean says the file is as written.
func (f *File) Clean() { f.putseq = f.seq }

// Dirty reports whether the file differs from what was last written.
func (f *File) Dirty() bool { return f.seq != f.putseq }

// UndoRedo takes back the last change (or with !isundo puts back the
// last undone one), telling each text to update itself. It returns the
// range changed last, for the selection.
func (f *File) UndoRedo(isundo bool) (q0, q1 int, ok bool) {
	from, to := &f.undo, &f.redo
	if !isundo {
		from, to = &f.redo, &f.undo
	}
	if len(*from) == 0 {
		return 0, 0, false
	}
	seq := (*from)[len(*from)-1].seq
	for len(*from) > 0 && (*from)[len(*from)-1].seq == seq {
		d := (*from)[len(*from)-1]
		*from = (*from)[:len(*from)-1]
		inv := d
		if d.insert == isundo {
			// Undo an insert or redo a delete: delete.
			f.delete(d.q0, d.q0+len(d.r))
			for _, t := range f.text {
				t.deleted(d.q0, d.q0+len(d.r))
			}
			q0, q1 = d.q0, d.q0
		} else {
			f.insert(d.q0, d.r)
			for _, t := range f.text {
				t.inserted(d.q0, len(d.r))
			}
			q0, q1 = d.q0, d.q0+len(d.r)
		}
		*to = append(*to, inv)
		ok = true
	}
	if isundo {
		f.seq = 0
		if len(f.undo) > 0 {
			f.seq = f.undo[len(f.undo)-1].seq
		}
	} else {
		f.seq = seq
	}
	f.pending = false
	f.mod = true
	return q0, q1, ok
}
