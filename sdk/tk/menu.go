package tk

import (
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// menu is a column of entries posted over the toplevel (menu(9)):
// command, checkbutton, radiobutton and separator entries. A posted
// menu is drawn inside the toplevel's window, not in a window of its
// own as in Inferno, so it is clipped by it. Cascades are not
// supported.
type menu struct {
	entries []*mentry
	active  int
}

type mentry struct {
	typ         string
	label       string
	command     string
	variable    string
	value       string
	onvalue     string
	offvalue    string
	accelerator string
	underline   int
	state       int
}

const (
	menupad = 2 // around each entry's text
	sepht   = 6
)

func (m *menu) defaults(w *Widget) {
	w.bd, w.relief = 1, reliefRaised
	m.active = -1
}

func (m *menu) option(w *Widget, name, val string) (bool, error) {
	switch name {
	case "tearoff", "postcommand", "selectcolor", "type":
		return true, nil
	}
	return false, nil
}

func (m *menu) get(w *Widget, name string) (string, bool) { return "", false }

func (m *menu) height(w *Widget, e *mentry) int {
	if e.typ == "separator" {
		return sepht
	}
	return w.t.font.Height + 2*menupad
}

func (m *menu) natural(w *Widget) draw.Point {
	f := w.t.font
	wd, ht := 0, 0
	for _, e := range m.entries {
		ew := f.Width(e.label) + 2*textpadx + checksz + checkgap
		if e.accelerator != "" {
			ew += 2*textpadx + f.Width(e.accelerator)
		}
		wd = max(wd, ew)
		ht += m.height(w, e)
	}
	return draw.Pt(wd, ht)
}

func (m *menu) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	t := w.t
	f := t.font
	y := r.Min.Y
	for i, e := range m.entries {
		h := m.height(w, e)
		er := draw.Rect(r.Min.X, y, r.Max.X, y+h)
		y += h
		if e.typ == "separator" {
			mid := er.Min.Y + h/2 - 1
			bevel(dst, t, draw.Rect(er.Min.X+2, mid, er.Max.X-2, mid+2), 1, reliefSunken, w.colors[cBg])
			continue
		}
		fg := w.colors[cFg]
		if e.state == stDisabled {
			fg = w.colors[cDisabled]
		} else if i == m.active {
			dst.Draw(er, w.col(w.colors[cActiveBg]), nil, draw.ZP)
			bevel(dst, t, er, 1, reliefRaised, w.colors[cActiveBg])
			fg = w.colors[cActiveFg]
		}
		if (e.typ == "checkbutton" || e.typ == "radiobutton") && m.on(w, e) {
			c := draw.Pt(er.Min.X+2+checksz/2, er.Min.Y+h/2)
			dst.FillEllipse(c, 3, 3, w.col(w.colors[cSelect]), draw.ZP)
		}
		tx := er.Min.X + checksz + checkgap + textpadx
		_, _ = dst.String(draw.Pt(tx, er.Min.Y+menupad), w.col(fg), draw.ZP, f, e.label)
		if e.accelerator != "" {
			ax := er.Max.X - textpadx - f.Width(e.accelerator)
			_, _ = dst.String(draw.Pt(ax, er.Min.Y+menupad), w.col(fg), draw.ZP, f, e.accelerator)
		}
	}
}

func (m *menu) on(w *Widget, e *mentry) bool {
	v := w.t.vars[e.variable]
	if e.typ == "radiobutton" {
		return v == e.value
	}
	return v == e.onvalue
}

// entryAt is the entry at y in the image, or -1.
func (m *menu) entryAt(w *Widget, y int) int {
	in := w.inside()
	yy := in.Min.Y
	for i, e := range m.entries {
		h := m.height(w, e)
		if y >= yy && y < yy+h {
			if e.typ == "separator" {
				return -1
			}
			return i
		}
		yy += h
	}
	return -1
}

func (m *menu) index(w *Widget, s string) (int, error) {
	switch {
	case s == "end", s == "last":
		return len(m.entries) - 1, nil
	case s == "active":
		return m.active, nil
	case s == "none":
		return -1, nil
	case strings.HasPrefix(s, "@"):
		y, err := strconv.Atoi(s[1:])
		if err != nil {
			return 0, tkerr(errBadix, s)
		}
		return m.entryAt(w, w.inside().Min.Y+y), nil
	}
	if i, err := strconv.Atoi(s); err == nil {
		if i < 0 || i >= len(m.entries) {
			return 0, tkerr(errBadix, s)
		}
		return i, nil
	}
	for i, e := range m.entries {
		if e.label == s {
			return i, nil
		}
	}
	return 0, tkerr(errBadix, s)
}

func (m *menu) configure(w *Widget, e *mentry, opts []string) error {
	if len(opts)%2 != 0 {
		return tkerr(errOparg, opts[len(opts)-1])
	}
	for k := 0; k < len(opts); k += 2 {
		v := opts[k+1]
		var err error
		switch opts[k] {
		case "-label", "-text":
			e.label = v
		case "-command":
			e.command = v
		case "-variable":
			e.variable = v
		case "-value":
			e.value = v
		case "-onvalue":
			e.onvalue = v
		case "-offvalue":
			e.offvalue = v
		case "-accelerator":
			e.accelerator = v
		case "-underline":
			e.underline, err = atoi(v)
		case "-state":
			e.state, err = lookup(states, v)
		case "-menu", "-bitmap", "-image", "-font", "-background", "-foreground", "-bg", "-fg",
			"-activebackground", "-activeforeground", "-selectcolor", "-indicatoron":
		default:
			err = tkerr(errBadop, opts[k])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *menu) invoke(w *Widget, i int) (string, error) {
	if i < 0 || i >= len(m.entries) {
		return "", nil
	}
	e := m.entries[i]
	if e.state == stDisabled {
		return "", nil
	}
	t := w.t
	switch e.typ {
	case "checkbutton":
		if m.on(w, e) {
			t.setvar(e.variable, e.offvalue)
		} else {
			t.setvar(e.variable, e.onvalue)
		}
	case "radiobutton":
		t.setvar(e.variable, e.value)
	case "separator", "cascade":
		return "", nil
	}
	if e.command != "" {
		return t.exec(e.command)
	}
	return "", nil
}

func (m *menu) cmd(w *Widget, args []string) (string, error) {
	t := w.t
	t.dirty = true
	switch args[0] {
	case "add", "insert":
		at := len(m.entries)
		if args[0] == "insert" {
			if len(args) < 3 {
				return "", tkerr(errBadvl)
			}
			i, err := m.index(w, args[1])
			if err != nil {
				return "", err
			}
			at = max(0, min(i, len(m.entries)))
			args = args[1:]
		}
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		switch args[1] {
		case "command", "checkbutton", "radiobutton", "separator", "cascade":
		default:
			return "", tkerr(errBadit, args[1])
		}
		e := &mentry{typ: args[1], underline: -1, onvalue: "1", offvalue: "0"}
		if e.typ == "checkbutton" || e.typ == "radiobutton" {
			e.variable = "selectedButton"
		}
		if err := m.configure(w, e, args[2:]); err != nil {
			return "", err
		}
		if e.typ == "checkbutton" && e.variable == "selectedButton" {
			e.variable = e.label
		}
		if e.typ == "radiobutton" && e.value == "" {
			e.value = e.label
		}
		m.entries = append(m.entries, nil)
		copy(m.entries[at+1:], m.entries[at:])
		m.entries[at] = e
		return "", nil
	case "delete":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		a, err := m.index(w, args[1])
		if err != nil {
			return "", err
		}
		b := a
		if len(args) > 2 {
			if b, err = m.index(w, args[2]); err != nil {
				return "", err
			}
		}
		if a >= 0 && b >= a && b < len(m.entries) {
			m.entries = append(m.entries[:a], m.entries[b+1:]...)
		}
		m.active = -1
		return "", nil
	case "entryconfigure":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		i, err := m.index(w, args[1])
		if err != nil || i < 0 {
			return "", tkerr(errBadix, args[1])
		}
		return "", m.configure(w, m.entries[i], args[2:])
	case "entrycget":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		i, err := m.index(w, args[1])
		if err != nil || i < 0 {
			return "", tkerr(errBadix, args[1])
		}
		e := m.entries[i]
		switch args[2] {
		case "-label", "-text":
			return e.label, nil
		case "-command":
			return e.command, nil
		case "-state":
			return states[e.state], nil
		case "-variable":
			return e.variable, nil
		case "-value":
			return e.value, nil
		}
		return "", tkerr(errBadop, args[2])
	case "invoke":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		i, err := m.index(w, args[1])
		if err != nil {
			return "", err
		}
		return m.invoke(w, i)
	case "activate":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		i, err := m.index(w, args[1])
		if err == nil {
			m.active = i
		}
		return "", err
	case "index":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		i, err := m.index(w, args[1])
		if i < 0 {
			return "none", err
		}
		return itoa(i), err
	case "type":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		i, err := m.index(w, args[1])
		if err != nil || i < 0 {
			return "", tkerr(errBadix, args[1])
		}
		return m.entries[i].typ, nil
	case "yposition":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		i, err := m.index(w, args[1])
		if err != nil {
			return "", err
		}
		y := 0
		for k := 0; k < i && k < len(m.entries); k++ {
			y += m.height(w, m.entries[k])
		}
		return itoa(y), nil
	case "post":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		x, err := t.dist(args[1], w)
		if err != nil {
			return "", err
		}
		y, err := t.dist(args[2], w)
		if err != nil {
			return "", err
		}
		t.post(w, draw.Pt(x, y))
		return "", nil
	case "unpost":
		if t.menu == w {
			t.unpost()
		}
		return "", nil
	}
	return "", tkerr(errBadcm, args[0])
}

// post shows menu m with its top left at p in the image.
func (t *Toplevel) post(m *Widget, p draw.Point) {
	if t.menu != nil && t.menu != m {
		t.unpost()
	}
	t.layout()
	sz := m.req()
	root := t.root.rect()
	p.X = max(root.Min.X, min(p.X, root.Max.X-sz.X))
	p.Y = max(root.Min.Y, min(p.Y, root.Max.Y-sz.Y))
	o := t.root.abs()
	m.x, m.y, m.w, m.h = p.X-o.X, p.Y-o.Y, sz.X, sz.Y
	m.mapped = true
	m.impl.(*menu).active = -1
	t.menu = m
	t.dirty = true
}

func (t *Toplevel) unpost() {
	if t.menu != nil {
		t.menu.mapped = false
		t.menu = nil
		t.dirty = true
	}
}

// menuPointer gives a pointer event to the posted menu; it takes all
// pointer events while one is posted.
func (t *Toplevel) menuPointer(p draw.Pointer) bool {
	w := t.menu
	m := w.impl.(*menu)
	inside := p.XY.In(w.rect())
	act := -1
	if inside {
		act = m.entryAt(w, p.XY.Y)
	}
	if act != m.active {
		m.active = act
		t.dirty = true
	}
	pressed := p.Buttons &^ t.buttons
	released := t.buttons &^ p.Buttons
	switch {
	case released != 0 && inside:
		t.unpost()
		_, _ = m.invoke(w, act)
	case pressed != 0 && !inside:
		t.unpost()
	}
	if p.Buttons == 0 {
		t.press = nil
	}
	return true
}
