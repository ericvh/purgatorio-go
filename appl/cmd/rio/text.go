package main

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// A text window's text: what programs in it have written and what has
// been typed for them to read.
//
// The text is one run of runes. The output point qh divides it, as in
// Plan 9's rio: before it is what was written to cons and what programs
// have read; after it is typing nobody has read yet. A write goes in at
// qh, before any such typing, and a read takes runes from qh up to
// ready: the end of the last line ended by a newline, or where ^D was
// typed. ^D with nothing waiting makes the next read return nothing, an
// end of file. Typing after ready can be erased.
//
// The selection q0..q1 is where typing goes: a rune typed replaces the
// selection and the selection becomes the point after it. When the
// selection is in text typing may not change, typing goes at the end.

const (
	maxtext = 256 * 1024 // runes kept
	trimlen = 64 * 1024  // runes dropped at a time when there are too many
	tabstop = 4          // tab stops are this many widths of 0 apart
)

type text struct {
	s     []rune
	qh    int // the output point
	ready int // typing before this may be read
	eof   int // ends of file typed and not yet read
	org   int // the first rune shown
	q0    int // the selection
	q1    int
}

// widths measures runes in the window's font, remembering them.
type widths struct {
	font  *draw.Font
	known map[rune]int
	tab   int
}

func newWidths(f *draw.Font) *widths {
	w := &widths{font: f, known: map[rune]int{}}
	w.tab = tabstop * max(w.of('0'), 1)
	return w
}

func (w *widths) of(c rune) int {
	if v, ok := w.known[c]; ok {
		return v
	}
	v := 8
	if w.font != nil {
		v = w.font.Width(string(c))
	}
	w.known[c] = v
	return v
}

// advance is how far rune c moves a point at x.
func (w *widths) advance(c rune, x int) int {
	if c == '\t' {
		return w.tab - x%w.tab
	}
	return w.of(c)
}

func wordch(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' }

// fixed is the first rune typing may still change.
func (t *text) fixed() int { return max(t.qh, t.ready) }

func (t *text) marks() []*int { return []*int{&t.qh, &t.ready, &t.org, &t.q0, &t.q1} }

// insert puts add at q; marks after q move on.
func (t *text) insert(q int, add []rune) {
	q = min(q, len(t.s))
	n := len(add)
	t.s = append(t.s[:q], append(append([]rune(nil), add...), t.s[q:]...)...)
	for _, m := range t.marks() {
		if *m > q {
			*m += n
		}
	}
}

// delete takes out q0..q1; marks inside it go to its start.
func (t *text) delete(q0, q1 int) {
	q1 = min(q1, len(t.s))
	if q0 >= q1 {
		return
	}
	n := q1 - q0
	t.s = append(t.s[:q0], t.s[q1:]...)
	for _, m := range t.marks() {
		if *m >= q1 {
			*m -= n
		} else if *m > q0 {
			*m = q0
		}
	}
}

func (t *text) selected() string {
	n := len(t.s)
	return string(t.s[min(t.q0, n):min(t.q1, n)])
}

// sel selects from a to b, either way round.
func (t *text) sel(a, b int) {
	n := len(t.s)
	t.q0, t.q1 = min(min(a, b), n), min(max(a, b), n)
}

// double is what a double click at q selects: the line when q is at its
// start or end, else the word round q.
func (t *text) double(q int) (int, int) {
	q = min(q, len(t.s))
	ls, le := t.lineStart(q), t.lineEnd(q)
	if q == ls || q == le {
		return ls, min(le+1, len(t.s))
	}
	a, b := q, q
	for a > ls && wordch(t.s[a-1]) {
		a--
	}
	for b < le && wordch(t.s[b]) {
		b++
	}
	return a, b
}

// output inserts a program's output at the output point, before any
// typing; a selection at or after the output point moves on.
func (t *text) output(s string) {
	add := []rune(strings.ReplaceAll(s, "\x00", ""))
	n, q := len(add), t.qh
	t.s = append(t.s[:q], append(add, t.s[q:]...)...)
	t.qh += n
	t.ready += n
	if t.org > q {
		t.org += n
	}
	if t.q0 >= q {
		t.q0 += n
	}
	if t.q1 >= q {
		t.q1 += n
	}
	t.trim()
}

// send adds s at the end as if typed, with a newline if it has none,
// and makes it readable: rio's send.
func (t *text) send(s string) {
	add := []rune(s)
	if len(add) == 0 || add[len(add)-1] != '\n' {
		add = append(add, '\n')
	}
	t.insert(len(t.s), add)
	t.ready = len(t.s)
	t.q0, t.q1 = len(t.s), len(t.s)
	t.trim()
}

// paste puts s in place of the selection and selects it: rio's paste.
// Pasted into typing, it may be read up to its last newline.
func (t *text) paste(s string) {
	add := []rune(s)
	t.delete(t.q0, t.q1)
	q := t.q0
	typing := q >= t.fixed()
	t.insert(q, add)
	if typing {
		for i := len(add) - 1; i >= 0; i-- {
			if add[i] == '\n' {
				t.ready = max(t.ready, q+i+1)
				break
			}
		}
	}
	t.q0, t.q1 = q, q+len(add)
	t.trim()
}

// typed handles a rune typed at the selection, or at the end when the
// selection is in text typing may not change.
func (t *text) typed(c rune) {
	floor := t.fixed()
	if t.q0 < floor {
		t.q0, t.q1 = len(t.s), len(t.s)
	}
	erase := c == '\b' || c == 0x15 || c == 0x17
	if t.q1 > t.q0 && c != 0x04 {
		t.delete(t.q0, t.q1)
		if erase {
			return
		}
	}
	q := t.q0
	switch c {
	case '\b':
		if q > floor {
			t.delete(q-1, q)
		}
	case 0x15: // ^U: back to the line's start or what can be erased
		t.delete(max(t.lineStart(q), floor), q)
	case 0x17: // ^W: the word before
		a := q
		for a > floor && unicode.IsSpace(t.s[a-1]) {
			a--
		}
		for a > floor && !unicode.IsSpace(t.s[a-1]) {
			a--
		}
		t.delete(a, q)
	case 0x04: // ^D: readable without a newline; alone, an end of file
		if len(t.s) == t.qh {
			t.eof++
		}
		t.ready = len(t.s)
	case '\n', '\r':
		t.insert(q, []rune{'\n'})
		t.ready = max(t.ready, q+1)
		t.q0, t.q1 = q+1, q+1
	default:
		t.insert(q, []rune{c})
		t.q0, t.q1 = q+1, q+1
	}
	t.trim()
}

// readable reports whether a read of cons would return now.
func (t *text) readable() bool { return t.ready > t.qh || t.eof > 0 }

// read takes at most count bytes, whole runes from the output point up
// to ready and at most one line, or an end of file; false when the read
// must wait.
func (t *text) read(count int) ([]byte, bool) {
	if t.ready > t.qh {
		var out []byte
		q := t.qh
		for ; q < t.ready; q++ {
			if len(out)+utf8.RuneLen(t.s[q]) > count {
				break
			}
			out = utf8.AppendRune(out, t.s[q])
			if t.s[q] == '\n' {
				q++
				break
			}
		}
		if q == t.qh {
			return nil, false
		}
		t.qh = q
		return out, true
	}
	if t.eof > 0 {
		t.eof--
		return []byte{}, true
	}
	return nil, false
}

// flushInput throws away typing nobody has read.
func (t *text) flushInput() {
	t.s = t.s[:t.qh]
	t.ready, t.eof = t.qh, 0
	t.org, t.q0, t.q1 = min(t.org, len(t.s)), min(t.q0, len(t.s)), min(t.q1, len(t.s))
}

// trim drops the oldest runes once there are too many.
func (t *text) trim() {
	if len(t.s) <= maxtext {
		return
	}
	n := min(trimlen, t.qh)
	t.s = append([]rune(nil), t.s[n:]...)
	for _, m := range t.marks() {
		*m = max(*m-n, 0)
	}
}

func (t *text) all() string { return string(t.s) }

// lineEnd is the end of the line from ls: its newline, or the end.
func (t *text) lineEnd(ls int) int {
	for i := ls; i < len(t.s); i++ {
		if t.s[i] == '\n' {
			return i
		}
	}
	return len(t.s)
}

// lineStart is the start of the line holding q.
func (t *text) lineStart(q int) int {
	for i := q - 1; i >= 0; i-- {
		if t.s[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

// wraps gives where the lines that the line [ls, le) wraps into start,
// at width w.
func (t *text) wraps(ls, le, w int, f *widths) []int {
	out := []int{ls}
	x := 0
	for i := ls; i < le; i++ {
		if x+f.advance(t.s[i], x) > w && x > 0 {
			out = append(out, i)
			x = 0
		}
		x += f.advance(t.s[i], x)
	}
	return out
}

// span is a line on the screen: its first rune and its end, past its
// newline if it has one.
type span struct{ s, e int }

// layout gives the lines from org, at most n.
func (t *text) layout(org, n, w int, f *widths) []span {
	var lines []span
	q := org
	for len(lines) < n {
		le := t.lineEnd(q)
		starts := t.wraps(q, le, w, f)
		for i, s := range starts {
			e := le
			if i+1 < len(starts) {
				e = starts[i+1]
			}
			lines = append(lines, span{s, e})
			if len(lines) == n {
				break
			}
		}
		if le >= len(t.s) {
			break
		}
		if last := &lines[len(lines)-1]; last.e == le {
			last.e = le + 1
		}
		q = le + 1
	}
	return lines
}

// startsBefore gives line starts before q, the nearest last, as far
// back as n.
func (t *text) startsBefore(q, n, w int, f *widths) []int {
	var out []int
	end := q
	for {
		ls := t.lineStart(end)
		var starts []int
		for _, s := range t.wraps(ls, min(t.lineEnd(ls), max(end, ls)), w, f) {
			if s < q || s == q && q == len(t.s) && ls == q {
				starts = append(starts, s)
			}
		}
		out = append(starts, out...)
		if len(out) >= n || ls == 0 {
			return out
		}
		end = ls - 1
	}
}

// orgForEnd is the first rune to show so that the end shows on the
// last of n lines.
func (t *text) orgForEnd(n, w int, f *widths) int {
	starts := t.startsBefore(len(t.s), n, w, f)
	return starts[max(len(starts)-max(n, 1), 0)]
}

// endShows reports whether the end shows with the text from org on n
// lines.
func (t *text) endShows(n, w int, f *widths) bool {
	_, ok := t.pointOf(len(t.s), t.layout(t.org, n, w, f), 1, f)
	return ok
}

// scroll moves the view n lines, down for positive.
func (t *text) scroll(n, w int, f *widths) {
	if n > 0 {
		lines := t.layout(t.org, n+1, w, f)
		if n < len(lines) {
			t.org = lines[n].s
		} else if len(lines) > 0 {
			t.org = min(lines[len(lines)-1].e, len(t.s))
		}
	} else if n < 0 && t.org > 0 {
		starts := t.startsBefore(t.org, -n, w, f)
		t.org = starts[max(len(starts)+n, 0)]
	}
}

// piece is a stretch of a line between tabs, and the x it starts at.
type piece struct {
	s string
	x int
}

func (t *text) pieces(l span, f *widths) []piece {
	var out []piece
	var cur []rune
	x, x0 := 0, 0
	for _, c := range t.s[l.s:l.e] {
		if c == '\n' {
			break
		}
		if c == '\t' {
			if len(cur) > 0 {
				out = append(out, piece{string(cur), x0})
				cur = nil
			}
			x += f.advance(c, x)
			x0 = x
			continue
		}
		cur = append(cur, c)
		x += f.advance(c, x)
	}
	if len(cur) > 0 {
		out = append(out, piece{string(cur), x0})
	}
	return out
}

// pointOf is where the point before rune q is drawn, from the top left
// of lines h high, when it shows.
func (t *text) pointOf(q int, lines []span, h int, f *widths) (draw.Point, bool) {
	for i, l := range lines {
		if l.s <= q && q < l.e {
			return draw.Pt(t.xOf(l.s, q, f), i*h), true
		}
	}
	for i, l := range lines {
		if l.e == q && (l.s == l.e || t.s[l.e-1] != '\n') {
			return draw.Pt(t.xOf(l.s, q, f), i*h), true
		}
	}
	return draw.Point{}, false
}

// xOf is how far from the start of the line at s rune q is drawn.
func (t *text) xOf(s, q int, f *widths) int {
	x := 0
	for _, c := range t.s[s:min(q, len(t.s))] {
		if c == '\n' {
			break
		}
		x += f.advance(c, x)
	}
	return x
}

// atX is the rune at x in line l, or the line's end past the last.
func (t *text) atX(l span, x int, f *widths) int {
	cx := 0
	for q := l.s; q < l.e; q++ {
		c := t.s[q]
		if c == '\n' {
			return q
		}
		a := f.advance(c, cx)
		if x < cx+a {
			return q
		}
		cx += a
	}
	return l.e
}

// textArea is where a window r's text is drawn: a margin inside its
// border.
func textArea(r draw.Rectangle) draw.Rectangle {
	b := draw.Borderwidth
	return draw.Rect(r.Min.X+b+4, r.Min.Y+b+2, r.Max.X-b-4, r.Max.Y-b-2)
}
