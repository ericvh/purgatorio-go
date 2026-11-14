package tk

import (
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// label is the label, button, checkbutton, radiobutton and menubutton
// widgets (label(9), button(9), checkbutton(9), radiobutton(9),
// menubutton(9)), which libtk also shares one implementation for.
// Bitmaps and images are not supported: a button shows text only.
type label struct {
	text      string
	anchor    int
	justify   string
	underline int
	command   string
	variable  string
	value     string
	onvalue   string
	offvalue  string
	menu      string
	pressed   bool
	inside    bool
}

const (
	textpadx = 3 // libtk's Textpadx
	checksz  = 10
	checkgap = 4
)

func (l *label) defaults(w *Widget) {
	l.anchor = anCenter
	l.underline = -1
	l.onvalue, l.offvalue = "1", "0"
	switch w.kind {
	case "button":
		w.bd, w.relief = 1, reliefRaised
	case "menubutton":
		w.bd, w.relief = 1, reliefRaised
	case "checkbutton":
		l.variable = w.name
		l.anchor = anW
	case "radiobutton":
		l.variable = "selectedButton"
		l.value = w.name[strings.LastIndexByte(w.name, '.')+1:]
		l.anchor = anW
	}
}

func (l *label) option(w *Widget, name, val string) (bool, error) {
	var err error
	switch name {
	case "text", "label":
		l.text = val
	case "anchor":
		var ok bool
		if l.anchor, ok = anchors[val]; !ok {
			err = tkerr(errBadvl, val)
		}
	case "justify":
		l.justify = val
	case "underline":
		l.underline, err = atoi(val)
	case "bitmap", "image", "indicatoron":
	case "command":
		if w.kind == "label" {
			return false, nil
		}
		l.command = val
	case "variable":
		if w.kind != "checkbutton" && w.kind != "radiobutton" {
			return false, nil
		}
		l.variable = val
	case "value":
		if w.kind != "radiobutton" {
			return false, nil
		}
		l.value = val
	case "onvalue":
		l.onvalue = val
	case "offvalue":
		l.offvalue = val
	case "menu":
		if w.kind != "menubutton" {
			return false, nil
		}
		l.menu = val
	default:
		return false, nil
	}
	return true, err
}

func (l *label) get(w *Widget, name string) (string, bool) {
	switch name {
	case "text", "label":
		return l.text, true
	case "anchor":
		return anchorName(l.anchor), true
	case "justify":
		return l.justify, true
	case "underline":
		return itoa(l.underline), true
	case "command":
		return l.command, true
	case "variable":
		return l.variable, true
	case "value":
		return l.value, true
	case "onvalue":
		return l.onvalue, true
	case "offvalue":
		return l.offvalue, true
	case "menu":
		return l.menu, true
	}
	return "", false
}

func (l *label) indicator(w *Widget) bool {
	return w.kind == "checkbutton" || w.kind == "radiobutton"
}

func (l *label) natural(w *Widget) draw.Point {
	sz := w.t.textsize(l.text)
	if l.text == "" {
		sz.X = 0
	}
	sz.X += 2 * textpadx
	if l.indicator(w) {
		sz.X += checksz + checkgap
		sz.Y = max(sz.Y, checksz+2)
	}
	return sz
}

// on reports whether a check or radio button is selected.
func (l *label) on(w *Widget) bool {
	v := w.t.vars[l.variable]
	if w.kind == "radiobutton" {
		return v == l.value
	}
	return v == l.onvalue
}

func (l *label) curRelief(w *Widget) int {
	if l.pressed && l.inside && (w.kind == "button" || w.kind == "menubutton") {
		return reliefSunken
	}
	return w.relief
}

func (l *label) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	if l.indicator(w) {
		y := r.Min.Y + (r.Dy()-checksz)/2
		box := draw.Rect(r.Min.X+2, y, r.Min.X+2+checksz, y+checksz)
		fill := w.colors[cBg]
		if l.on(w) {
			fill = w.colors[cSelect]
		}
		if w.kind == "radiobutton" {
			c := box.Min.Add(draw.Pt(checksz/2, checksz/2))
			pts := []draw.Point{draw.Pt(c.X, box.Min.Y), draw.Pt(box.Max.X, c.Y), draw.Pt(c.X, box.Max.Y), draw.Pt(box.Min.X, c.Y)}
			dst.FillPoly(pts, 0, w.col(fill), draw.ZP)
			dst.Poly(append(pts, pts[0]), draw.Endsquare, draw.Endsquare, 0, w.col(w.colors[cFg]), draw.ZP)
		} else {
			dst.Draw(box, w.col(fill), nil, draw.ZP)
			bevel(dst, w.t, box, 1, reliefSunken, w.colors[cBg])
		}
		r.Min.X += checksz + checkgap
	}
	if l.text == "" {
		return
	}
	in := draw.Rect(r.Min.X+textpadx, r.Min.Y, r.Max.X-textpadx, r.Max.Y)
	w.drawText(dst, in, l.text, l.anchor, w.fg())
	if l.underline >= 0 && l.underline < runelen(l.text) && !strings.Contains(l.text, "\n") {
		f := w.t.font
		sz := w.t.textsize(l.text)
		p := place(in, sz, l.anchor)
		rs := []rune(l.text)
		x0 := p.X + f.Width(string(rs[:l.underline]))
		x1 := x0 + f.Width(string(rs[l.underline]))
		dst.Draw(draw.Rect(x0, p.Y+f.Ascent+1, x1, p.Y+f.Ascent+2), w.col(w.fg()), nil, draw.ZP)
	}
}

// invoke does what a click does.
func (l *label) invoke(w *Widget) (string, error) {
	t := w.t
	if w.state == stDisabled {
		return "", nil
	}
	switch w.kind {
	case "checkbutton":
		if l.on(w) {
			t.setvar(l.variable, l.offvalue)
		} else {
			t.setvar(l.variable, l.onvalue)
		}
	case "radiobutton":
		t.setvar(l.variable, l.value)
	case "menubutton":
		l.post(w)
		return "", nil
	case "label":
		return "", nil
	}
	if l.command != "" {
		return t.exec(l.command)
	}
	return "", nil
}

// post shows a menubutton's menu below it.
func (l *label) post(w *Widget) {
	m := w.t.widgets[l.menu]
	if m == nil || m.kind != "menu" {
		return
	}
	r := w.rect()
	w.t.post(m, draw.Pt(r.Min.X, r.Max.Y))
}

func (l *label) event(w *Widget, e *event) {
	if w.kind == "label" {
		return
	}
	switch e.kind {
	case evEnter:
		l.inside = true
		w.state = stActive
	case evLeave:
		l.inside = false
		w.state = stNormal
	case evPress:
		if e.button == 1 {
			l.pressed = true
			l.inside = true
			if w.kind == "menubutton" {
				l.pressed = false
				l.post(w)
			}
		}
	case evMotion:
		l.inside = e.p.In(w.rect())
	case evRelease:
		if e.button == 1 && l.pressed {
			l.pressed = false
			if e.p.In(w.rect()) {
				_, _ = l.invoke(w)
			}
		}
	default:
		return
	}
	w.t.dirty = true
}

func (l *label) cmd(w *Widget, args []string) (string, error) {
	t := w.t
	switch args[0] {
	case "invoke":
		if w.kind != "label" {
			return l.invoke(w)
		}
	case "flash":
		return "", nil
	case "select":
		if w.kind == "checkbutton" {
			t.setvar(l.variable, l.onvalue)
			return "", nil
		}
		if w.kind == "radiobutton" {
			t.setvar(l.variable, l.value)
			return "", nil
		}
	case "deselect":
		if w.kind == "checkbutton" {
			t.setvar(l.variable, l.offvalue)
			return "", nil
		}
		if w.kind == "radiobutton" {
			if l.on(w) {
				t.setvar(l.variable, "")
			}
			return "", nil
		}
	case "toggle":
		if w.kind == "checkbutton" {
			if l.on(w) {
				t.setvar(l.variable, l.offvalue)
			} else {
				t.setvar(l.variable, l.onvalue)
			}
			return "", nil
		}
	}
	return "", tkerr(errBadcm, args[0])
}
