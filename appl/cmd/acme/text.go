package main

import (
	"strings"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// What a text is.
const (
	Columntag = iota
	Rowtag
	Tag
	Body
)

// Keys acme treats specially, as Inferno's keyboard sends them.
const (
	Kscrollup   = draw.KeyUp
	Kscrolldown = draw.KeyDown
	Kleft       = draw.KeyLeft
	Kright      = draw.KeyRight
	Khome       = draw.KeyHome
	Kend        = draw.KeyEnd
	Kpgup       = draw.KeyPgup
	Kpgdown     = draw.KeyPgdown
	Kesc        = 0x1B
	Kdel        = 0x7F
	Kbs         = 0x08
)

const (
	scrollwid = 12 // width of a body's scroll bar
	scrollgap = 4  // and the gap after it
)

// A Text is a tag or a body: a File shown in a Frame (acme's text.b).
type Text struct {
	file    *File
	fr      Frame
	what    int
	w       *Window
	col     *Column
	row     *Row
	org     int // the first rune shown
	q0, q1  int // the selection
	eq0     int // where typing began, for Esc
	cq0     int
	all     draw.Rectangle
	scrollr draw.Rectangle
	lastsr  draw.Rectangle
	tabstop int
}

func (t *Text) init(f *File, r draw.Rectangle, cols [NCOL]*draw.Image) {
	t.file = f
	f.text = append(f.text, t)
	t.all = r
	t.scrollr = r
	t.scrollr.Max.X = r.Min.X + scrollwid
	t.lastsr = draw.Rectangle{}
	r.Min.X += scrollwid + scrollgap
	t.eq0 = -1
	t.tabstop = g.tabstop
	t.fr.init(r, g.font, g.screen, cols)
	t.fr.tickcol = cols[TEXT]
	t.fr.maxtab = t.tabstop * max(1, g.font.Width("0"))
}

// resize gives the text new room and returns where its bottom is,
// rounded to whole lines for a body.
func (t *Text) resize(r draw.Rectangle) int {
	if r.Dy() > 0 && t.what == Body {
		r.Max.Y -= r.Dy() % g.font.Height
	}
	t.all = r
	t.scrollr = r
	t.scrollr.Max.X = r.Min.X + scrollwid
	t.lastsr = draw.Rectangle{}
	r.Min.X += scrollwid + scrollgap
	t.fr.r = r
	t.redraw()
	return t.all.Max.Y
}

// close takes t off its file.
func (t *Text) close() {
	f := t.file
	for i, x := range f.text {
		if x == t {
			f.text = append(f.text[:i], f.text[i+1:]...)
			break
		}
	}
}

// visible is the runes the frame can show from org.
func (t *Text) visible() []rune {
	n := t.file.Nc()
	// Enough runes to fill the frame: lines times a generous width.
	lim := t.org + (t.fr.maxlines()+1)*(t.fr.r.Dx()/max(1, g.font.Width("i"))+2)
	return t.file.Read(t.org, min(n, max(lim, t.org)))
}

// redraw draws the text, its selection and, for a body, its scroll
// bar.
func (t *Text) redraw() {
	if t.fr.r.Dx() <= 0 || t.all.Dy() <= 0 {
		return
	}
	if t.what == Body {
		g.screen.Draw(draw.Rect(t.scrollr.Max.X, t.all.Min.Y, t.fr.r.Min.X, t.all.Max.Y), t.fr.cols[BACK], nil, draw.ZP)
	}
	high, htext := t.fr.cols[HIGH], t.fr.cols[HTEXT]
	p0, p1 := t.q0-t.org, t.q1-t.org
	rs := t.visible()
	p0, p1 = max(0, min(p0, len(rs))), max(0, min(p1, len(rs)))
	if t.q1 < t.org {
		p0, p1 = 0, 0
	}
	t.fr.redraw(rs, p0, p1, t.q0 == t.q1 && t.q0 >= t.org, high, htext)
	if t.q0 == t.q1 && (t.q0 < t.org || t.q0 > t.org+t.fr.nchars) {
		t.fr.ticked = false
	}
	if t.what == Body {
		t.scrdraw()
	}
}

// scrdraw draws the scroll bar (scrl.b).
func (t *Text) scrdraw() {
	r := t.scrollr
	if r.Dy() <= 0 {
		return
	}
	g.screen.Draw(r, t.fr.cols[BORD], nil, draw.ZP)
	tot := max(1, t.file.Nc())
	h := r.Dy()
	y0 := r.Min.Y + int(int64(t.org)*int64(h)/int64(tot))
	y1 := r.Min.Y + int(int64(t.org+t.fr.nchars)*int64(h)/int64(tot))
	if y1-y0 < 2 {
		y1 = min(r.Max.Y, y0+2)
	}
	g.screen.Draw(draw.Rect(r.Min.X, y0, r.Max.X-1, y1), t.fr.cols[BACK], nil, draw.ZP)
	g.screen.Draw(draw.Rect(r.Max.X-1, r.Min.Y, r.Max.X, r.Max.Y), g.black, nil, draw.ZP)
}

// inserted adjusts t for n runes put in its file at q0.
func (t *Text) inserted(q0, n int) {
	if q0 < t.q0 {
		t.q0 += n
	}
	if q0 < t.q1 {
		t.q1 += n
	}
	if q0 < t.org {
		t.org += n
	}
	if t.w != nil && t.what == Body {
		t.w.changed()
	}
	t.dirty()
}

// deleted adjusts t for runes q0 to q1 taken from its file.
func (t *Text) deleted(q0, q1 int) {
	n := q1 - q0
	adj := func(q int) int {
		switch {
		case q <= q0:
			return q
		case q >= q1:
			return q - n
		}
		return q0
	}
	t.q0, t.q1, t.org = adj(t.q0), adj(t.q1), adj(t.org)
	if t.w != nil && t.what == Body {
		t.w.changed()
	}
	t.dirty()
}

// dirty notes that t needs drawing.
func (t *Text) dirty() { g.redraw[t] = true }

// insert puts r at q0; with tofile it goes in the file, which tells
// every text showing it.
func (t *Text) insert(q0 int, r []rune, tofile bool) {
	if len(r) == 0 {
		return
	}
	if tofile {
		t.file.Insert(q0, r)
		for _, x := range t.file.text {
			x.inserted(q0, len(r))
		}
		if t.what == Tag && t.w != nil {
			t.w.tagchanged = true
		}
	}
}

// delete removes q0 to q1 from the file.
func (t *Text) delete(q0, q1 int, tofile bool) {
	if q0 >= q1 {
		return
	}
	if tofile {
		t.file.Delete(q0, q1)
		for _, x := range t.file.text {
			x.deleted(q0, q1)
		}
		if t.what == Tag && t.w != nil {
			t.w.tagchanged = true
		}
	}
}

func (t *Text) readc(q int) rune { return t.file.Readc(q) }

// setselect sets the selection.
func (t *Text) setselect(q0, q1 int) {
	t.q0, t.q1 = q0, q1
	t.dirty()
}

// show makes q0 to q1 the selection and scrolls to show it.
func (t *Text) show(q0, q1 int, doselect bool) {
	if t.what != Body {
		if doselect {
			t.setselect(q0, q1)
		}
		return
	}
	if t.w != nil && t.fr.maxlines() == 0 {
		t.col.grow(t.w, 1)
	}
	if doselect {
		t.setselect(q0, q1)
	}
	qe := t.org + t.fr.nchars
	tsd := false
	if t.org+t.fr.nchars == t.file.Nc() && t.fr.nchars > 0 {
		tsd = true
	}
	if t.org <= q0 && (q0 < qe || q0 == qe && qe == t.file.Nc() && !t.fr.lastLineFull() || tsd && q0 == qe) {
		t.dirty()
		return
	}
	t.setorigin(t.backnl(q0, max(1, t.fr.maxlines()/4)), false)
}

// backnl returns the start of the line n lines before p (n == 0 is the
// start of p's line).
func (t *Text) backnl(p, n int) int {
	if n == 0 && p > 0 && t.readc(p-1) != '\n' {
		n = 1
	}
	i := n
	for i > 0 && p > 0 {
		p--
		if p > 0 && t.readc(p-1) == '\n' {
			i--
		}
	}
	for p > 0 && t.readc(p-1) != '\n' {
		p--
	}
	return p
}

// setorigin starts the frame at org; unless exact, at a line start.
func (t *Text) setorigin(org int, exact bool) {
	if org > 0 && !exact {
		// Back up to a line start, but not too far.
		for i := 0; i < 256 && org > 0; i++ {
			if t.readc(org-1) == '\n' {
				break
			}
			org--
		}
	}
	t.org = max(0, min(org, t.file.Nc()))
	t.dirty()
}

// lines counts the lines from q0 to q1.
func (t *Text) scrollLines(n int) {
	if n > 0 {
		// Down: the start of line n of the frame.
		t.redrawIfNeeded()
		if n >= len(t.fr.lines) {
			if t.fr.nchars > 0 {
				t.setorigin(t.org+t.fr.nchars, true)
			}
			return
		}
		t.setorigin(t.org+t.fr.lines[n].c0, true)
		return
	}
	t.setorigin(t.backnl(t.org, -n), true)
}

// redrawIfNeeded brings the frame's layout up to date.
func (t *Text) redrawIfNeeded() {
	if g.redraw[t] {
		t.redraw()
		delete(g.redraw, t)
	}
}

// bswidth is how far ^H, ^U or ^W reach back from the insert point.
func (t *Text) bswidth(c rune) int {
	if c == Kbs {
		return 1
	}
	q := t.q0
	skipping := true
	for q > 0 {
		r := t.readc(q - 1)
		if r == '\n' { // eat at most one more character
			if q == t.q0 {
				q--
			}
			break
		}
		if c == 0x17 {
			eq := isalnum(r)
			if eq && skipping {
				skipping = false
			} else if !eq && !skipping {
				break
			}
		}
		q--
	}
	return t.q0 - q
}

// isalnum is util.b's: anything but blanks, controls and ASCII
// punctuation.
func isalnum(c rune) bool {
	if c <= ' ' || 0x7F <= c && c <= 0xA0 {
		return false
	}
	return !strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^`{|}~", c)
}

// lineAt is the frame's position n lines down, as a file position.
func (t *Text) lineAt(n int) int {
	t.redrawIfNeeded()
	return t.org + t.fr.charofpt(draw.Pt(t.fr.r.Min.X, t.fr.r.Min.Y+n*g.font.Height))
}

// typex handles a key typed into t (Text.typex).
func (t *Text) typex(r rune) {
	if t.what != Body && r == '\n' {
		return
	}
	switch r {
	case Kscrolldown, Kpgdown:
		n := t.fr.maxlines() / 3
		if r == Kpgdown {
			n = 2 * t.fr.maxlines() / 3
		}
		t.setorigin(t.lineAt(max(1, n)), false)
		return
	case Kscrollup, Kpgup:
		n := t.fr.maxlines() / 3
		if r == Kpgup {
			n = 2 * t.fr.maxlines() / 3
		}
		t.setorigin(t.backnl(t.org, max(1, n)), false)
		return
	case Khome:
		t.commitIfTag()
		t.show(0, 0, false)
		return
	case Kend:
		t.commitIfTag()
		t.show(t.file.Nc(), t.file.Nc(), false)
		return
	case Kleft:
		t.commitIfTag()
		if t.q0 != t.q1 {
			t.show(t.q0, t.q0, true)
		} else if t.q0 != 0 {
			t.show(t.q0-1, t.q0-1, true)
		}
		return
	case Kright:
		t.commitIfTag()
		if t.q0 != t.q1 {
			t.show(t.q1, t.q1, true)
		} else if t.q1 != t.file.Nc() {
			t.show(t.q1+1, t.q1+1, true)
		}
		return
	case 0x01: // ^A: where ^U would erase back to
		t.commitIfTag()
		nnb := 0
		if t.q0 > 0 && t.readc(t.q0-1) != '\n' {
			nnb = t.bswidth(0x15)
		}
		t.show(t.q0-nnb, t.q0-nnb, true)
		return
	case 0x05: // ^E: end of line
		t.commitIfTag()
		q := t.q0
		for q < t.file.Nc() && t.readc(q) != '\n' {
			q++
		}
		t.show(q, q, true)
		return
	}
	if t.what == Body {
		g.seq++
		t.file.Mark()
	}
	if t.q1 > t.q0 {
		// Typing replaces the selection, which goes to the snarf.
		cut(t, t, true, true)
		t.eq0 = -1
		if r == Kbs || r == Kdel {
			t.show(t.q0, t.q0, true)
			return
		}
	}
	t.show(t.q0, t.q0, true)
	switch r {
	case Kesc:
		if t.eq0 != -1 {
			t.setselect(t.eq0, t.q0)
		}
		t.commitIfTag()
		return
	case Kbs, 0x15, 0x17: // ^H, ^U, ^W
		if t.q0 == 0 {
			return
		}
		q1 := t.q0
		q0 := q1 - t.bswidth(r)
		if t.eq0 == q1 || t.eq0 == -1 {
			t.eq0 = q0
		}
		if t.w != nil && t.what == Body {
			t.w.event("K", 'D', q0, q1, 0, nil)
		}
		t.delete(q0, q1, true)
		t.setselect(q0, q0)
		return
	case Kdel, draw.KeyDel:
		// Delete forward.
		t.commitIfTag()
		if t.q0 >= t.file.Nc() {
			return
		}
		q0 := t.q0
		if t.eq0 == q0+1 || t.eq0 == -1 {
			t.eq0 = q0
		}
		if t.w != nil && t.what == Body {
			t.w.event("K", 'D', q0, q0+1, 0, nil)
		}
		t.delete(q0, q0+1, true)
		t.setselect(q0, q0)
		return
	}
	if r < 0x20 && r != '\t' && r != '\n' || r >= draw.KeySpec && r < 0xF900 {
		return
	}
	if t.eq0 == -1 {
		t.eq0 = t.q0
	}
	q := t.q0
	t.insert(q, []rune{r}, true)
	if t.w != nil && t.what == Body {
		t.w.event("K", 'I', q, q+1, 0, []rune{r})
	}
	t.setselect(q+1, q+1)
	if t.what == Body {
		t.show(q+1, q+1, true)
	}
}

func (t *Text) commitIfTag() {
	if t.what == Tag && t.w != nil {
		t.w.commit(t)
	}
}

// Brackets for double clicks.
var (
	lefts  = []string{"{[(<«", "\n", "'\"`"}
	rights = []string{"}])>»", "\n", "'\"`"}
)

// doubleclick returns what a double click at q0 selects: from a bracket
// to its match, a line, or a word.
func (t *Text) doubleclick(q0 int) (int, int) {
	nc := t.file.Nc()
	for i := range lefts {
		l, r := []rune(lefts[i]), []rune(rights[i])
		// A left bracket before q0: look right.
		c := '\n'
		if q0 > 0 {
			c = t.readc(q0 - 1)
		}
		if k := runeIndex(l, c); k >= 0 {
			if q, ok := t.matchfwd(c, r[k], q0); ok {
				if c != '\n' {
					q--
				}
				return q0, q
			}
			return q0, q0
		}
		// A right bracket at q0: look left.
		if q0 >= nc {
			continue
		}
		c = t.readc(q0)
		if k := runeIndex(r, c); k >= 0 {
			if q, ok := t.matchback(c, l[k], q0); ok {
				q1 := q0
				if c == '\n' {
					q1++
				}
				return q + 1, q1
			}
			return q0, q0
		}
	}
	q1 := q0
	for q1 < nc && isalnum(t.readc(q1)) {
		q1++
	}
	for q0 > 0 && isalnum(t.readc(q0-1)) {
		q0--
	}
	return q0, q1
}

func runeIndex(rs []rune, c rune) int {
	for i, r := range rs {
		if r == c {
			return i
		}
	}
	return -1
}

// matchfwd finds cr closing cl, looking from q, and returns the position
// just after it. A line runs to the end of the text.
func (t *Text) matchfwd(cl, cr rune, q int) (int, bool) {
	nest := 1
	for ; q < t.file.Nc(); q++ {
		c := t.readc(q)
		if c == cr {
			if nest--; nest == 0 {
				return q + 1, true
			}
		} else if c == cl {
			nest++
		}
	}
	return q, cl == '\n'
}

// matchback finds cl opening the cr at q, looking back, and returns its
// position, or -1 for the start of the text when looking for a line.
func (t *Text) matchback(cr, cl rune, q int) (int, bool) {
	nest := 1
	for q > 0 {
		q--
		c := t.readc(q)
		if c == cl {
			if nest--; nest == 0 {
				return q, true
			}
		} else if c == cr {
			nest++
		}
	}
	return -1, cr == '\n'
}

// selectWord expands an empty selection at q to the text around it
// that a middle or right click means: non-blank runes.
func (t *Text) expand(q0, q1 int, filename bool) (int, int) {
	if q0 != q1 {
		return q0, q1
	}
	ok := func(c rune) bool {
		if filename {
			return isfilec(c)
		}
		return isalnum(c)
	}
	for q1 < t.file.Nc() && ok(t.readc(q1)) {
		q1++
	}
	for q0 > 0 && ok(t.readc(q0-1)) {
		q0--
	}
	return q0, q1
}

func isfilec(c rune) bool {
	return isalnum(c) || c == '.' || c == '-' || c == '+' || c == '/' || c == ':'
}

func isexecc(c rune) bool { return isfilec(c) || c == '<' || c == '|' || c == '>' }

// runelen is the number of runes in s.
func runelen(s string) int { return utf8.RuneCountInString(s) }
