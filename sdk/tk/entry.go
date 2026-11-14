package tk

import (
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// entry is a one-line text editor (entry(9)).
type entry struct {
	text       []rune
	icursor    int
	sel0, sel1 int // selection, empty when equal
	anchor     int
	xoff       int // first rune shown
	show       string
	justify    string
	xscroll    string
}

func (e *entry) defaults(w *Widget) {
	w.bd, w.relief, w.takefocus = 1, reliefSunken, true
}

func (e *entry) option(w *Widget, name, val string) (bool, error) {
	switch name {
	case "show":
		e.show = val
	case "justify":
		e.justify = val
	case "xscrollcommand":
		e.xscroll = val
	case "textvariable":
	default:
		return false, nil
	}
	return true, nil
}

func (e *entry) get(w *Widget, name string) (string, bool) {
	switch name {
	case "show":
		return e.show, true
	case "justify":
		return e.justify, true
	case "xscrollcommand":
		return e.xscroll, true
	}
	return "", false
}

func (e *entry) natural(w *Widget) draw.Point {
	return draw.Pt(20*w.charwidth(), w.font.Height+2)
}

// shown is the text as displayed.
func (e *entry) shown() []rune {
	if e.show == "" {
		return e.text
	}
	s := []rune(e.show)[0]
	r := make([]rune, len(e.text))
	for i := range r {
		r[i] = s
	}
	return r
}

func (e *entry) width(w *Widget, rs []rune) int { return w.font.Width(string(rs)) }

func (e *entry) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	t := w.t
	f := w.font
	rs := e.shown()
	// Keep the cursor in view.
	e.xoff = min(e.xoff, e.icursor, len(rs))
	for e.xoff < e.icursor && e.width(w, rs[e.xoff:e.icursor]) > r.Dx()-2 {
		e.xoff++
	}
	x := r.Min.X + 1
	y := r.Min.Y + (r.Dy()-f.Height)/2
	fg, sbg, sfg := w.col(w.colors[cFg]), w.col(w.colors[cSelectBg]), w.col(w.colors[cSelectFg])
	if w.state == stDisabled {
		fg = w.col(w.colors[cDisabled])
	}
	for i := e.xoff; i < len(rs) && x < r.Max.X; i++ {
		s := string(rs[i])
		cw := f.Width(s)
		if i >= e.sel0 && i < e.sel1 {
			dst.Draw(draw.Rect(x, y, x+cw, y+f.Height), sbg, nil, draw.ZP)
			_, _ = dst.String(draw.Pt(x, y), sfg, draw.ZP, f, s)
		} else {
			_, _ = dst.String(draw.Pt(x, y), fg, draw.ZP, f, s)
		}
		x += cw
	}
	if t.focus == w && t.focused && w.state != stDisabled {
		cx := r.Min.X + 1 + e.width(w, rs[e.xoff:e.icursor])
		dst.Draw(draw.Rect(cx, y, cx+1, y+f.Height), fg, nil, draw.ZP)
	}
}

// at returns the index of the character at x in the image.
func (e *entry) at(w *Widget, x int) int {
	x -= w.inside().Min.X + 1
	rs := e.shown()
	for i := e.xoff; i < len(rs); i++ {
		cw := w.font.Width(string(rs[i]))
		if x < cw/2 {
			return i
		}
		x -= cw
	}
	return len(rs)
}

// index parses an entry index.
func (e *entry) index(w *Widget, s string) (int, error) {
	switch {
	case s == "end":
		return len(e.text), nil
	case s == "insert":
		return e.icursor, nil
	case s == "anchor":
		return e.anchor, nil
	case s == "sel.first", s == "sel.last":
		if e.sel0 == e.sel1 {
			return 0, tkerr(errBadix, s)
		}
		if s == "sel.first" {
			return e.sel0, nil
		}
		return e.sel1, nil
	case strings.HasPrefix(s, "@"):
		x, err := strconv.Atoi(s[1:])
		if err != nil {
			return 0, tkerr(errBadix, s)
		}
		return e.at(w, w.inside().Min.X+x), nil
	}
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0, tkerr(errBadix, s)
	}
	return max(0, min(i, len(e.text))), nil
}

func (e *entry) delete(a, b int) {
	if a >= b {
		return
	}
	e.text = append(e.text[:a], e.text[b:]...)
	adj := func(i int) int {
		switch {
		case i >= b:
			return i - (b - a)
		case i > a:
			return a
		}
		return i
	}
	e.icursor, e.anchor = adj(e.icursor), adj(e.anchor)
	e.sel0, e.sel1 = adj(e.sel0), adj(e.sel1)
}

func (e *entry) insert(i int, s string) {
	rs := []rune(s)
	e.text = append(e.text[:i], append(rs, e.text[i:]...)...)
	if e.icursor >= i {
		e.icursor += len(rs)
	}
	if e.sel0 >= i {
		e.sel0 += len(rs)
	}
	if e.sel1 >= i {
		e.sel1 += len(rs)
	}
}

func (e *entry) cmd(w *Widget, args []string) (string, error) {
	w.t.dirty = true
	switch args[0] {
	case "get":
		return string(e.text), nil
	case "insert":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		i, err := e.index(w, args[1])
		if err != nil {
			return "", err
		}
		e.insert(i, args[2])
		return "", nil
	case "delete":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		a, err := e.index(w, args[1])
		if err != nil {
			return "", err
		}
		b := a + 1
		if len(args) > 2 {
			if b, err = e.index(w, args[2]); err != nil {
				return "", err
			}
		}
		e.delete(a, min(b, len(e.text)))
		return "", nil
	case "icursor":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		i, err := e.index(w, args[1])
		if err == nil {
			e.icursor = i
		}
		return "", err
	case "index":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		i, err := e.index(w, args[1])
		return itoa(i), err
	case "see":
		if len(args) == 2 {
			if i, err := e.index(w, args[1]); err == nil && i < e.xoff {
				e.xoff = i
			}
		}
		return "", nil
	case "xview":
		if len(args) == 3 && args[1] == "moveto" {
			if f, err := strconv.ParseFloat(args[2], 64); err == nil {
				e.xoff = max(0, min(len(e.text), int(f*float64(len(e.text)))))
			}
		} else if len(args) == 2 {
			if i, err := e.index(w, args[1]); err == nil {
				e.xoff = i
			}
		}
		return "", nil
	case "selection":
		return e.selection(w, args[1:])
	}
	return "", tkerr(errBadcm, args[0])
}

func (e *entry) selection(w *Widget, args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadvl)
	}
	idx := func(k int) (int, error) {
		if len(args) <= k {
			return 0, tkerr(errBadvl)
		}
		return e.index(w, args[k])
	}
	switch args[0] {
	case "clear":
		e.sel0, e.sel1 = 0, 0
	case "present":
		if e.sel0 != e.sel1 {
			return "1", nil
		}
		return "0", nil
	case "from":
		i, err := idx(1)
		if err != nil {
			return "", err
		}
		e.anchor = i
	case "to", "adjust":
		i, err := idx(1)
		if err != nil {
			return "", err
		}
		e.sel0, e.sel1 = min(e.anchor, i), max(e.anchor, i)
	case "range":
		a, err := idx(1)
		if err != nil {
			return "", err
		}
		b, err := idx(2)
		if err != nil {
			return "", err
		}
		e.sel0, e.sel1 = a, max(a, b)
	default:
		return "", tkerr(errBadvl, args[0])
	}
	return "", nil
}

func (e *entry) event(w *Widget, ev *event) {
	t := w.t
	switch ev.kind {
	case evPress:
		if ev.button == 1 {
			i := e.at(w, ev.p.X)
			e.icursor, e.anchor = i, i
			e.sel0, e.sel1 = 0, 0
			t.dirty = true
		}
	case evMotion:
		if ev.held&1 != 0 {
			i := e.at(w, ev.p.X)
			e.sel0, e.sel1 = min(e.anchor, i), max(e.anchor, i)
			e.icursor = i
			t.dirty = true
		}
	case evKey:
		e.key(ev.key)
		t.dirty = true
	case evFocusIn, evFocusOut:
		t.dirty = true
	}
}

func (e *entry) key(k rune) {
	delsel := func() bool {
		if e.sel0 == e.sel1 {
			return false
		}
		e.icursor = e.sel0
		e.delete(e.sel0, e.sel1)
		e.sel0, e.sel1 = 0, 0
		return true
	}
	switch k {
	case '\b':
		if !delsel() && e.icursor > 0 {
			e.delete(e.icursor-1, e.icursor)
		}
	case 0x7F, draw.KeyDel:
		if !delsel() && e.icursor < len(e.text) {
			e.delete(e.icursor, e.icursor+1)
		}
	case 0x15: // ^U: delete to the start
		e.delete(0, e.icursor)
	case 0x17: // ^W: delete a word
		i := e.icursor
		for i > 0 && e.text[i-1] == ' ' {
			i--
		}
		for i > 0 && e.text[i-1] != ' ' {
			i--
		}
		e.delete(i, e.icursor)
	case 0x01, draw.KeyHome:
		e.icursor = 0
	case 0x05, draw.KeyEnd:
		e.icursor = len(e.text)
	case draw.KeyLeft:
		e.icursor = max(0, e.icursor-1)
	case draw.KeyRight:
		e.icursor = min(len(e.text), e.icursor+1)
	case '\n', '\r', '\t', draw.KeyEsc:
	default:
		if k < 0x20 || k >= draw.KeySpec && k < 0xF900 {
			return
		}
		delsel()
		e.insert(e.icursor, string(k))
	}
}
