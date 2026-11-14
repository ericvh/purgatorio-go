package tk

import (
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// listbox is a list of lines to choose from (listbox(9)).
type listbox struct {
	items   []string
	sel     map[int]bool
	active  int
	anchor  int
	top     int
	mode    string
	yscroll string
	xscroll string
	lastf   string
	shown   int
}

func (l *listbox) defaults(w *Widget) {
	w.bd, w.relief, w.takefocus = 1, reliefSunken, true
	l.mode = "browse"
	l.sel = map[int]bool{}
}

func (l *listbox) option(w *Widget, name, val string) (bool, error) {
	switch name {
	case "selectmode":
		switch val {
		case "single", "browse", "multiple", "extended":
			l.mode = val
		default:
			return true, tkerr(errBadvl, val)
		}
	case "yscrollcommand":
		l.yscroll = val
	case "xscrollcommand":
		l.xscroll = val
	case "selectborderwidth", "exportselection", "setgrid":
	default:
		return false, nil
	}
	return true, nil
}

func (l *listbox) get(w *Widget, name string) (string, bool) {
	switch name {
	case "selectmode":
		return l.mode, true
	case "yscrollcommand":
		return l.yscroll, true
	case "xscrollcommand":
		return l.xscroll, true
	}
	return "", false
}

func (l *listbox) natural(w *Widget) draw.Point {
	return draw.Pt(20*w.t.charwidth(), 10*w.t.font.Height)
}

func (l *listbox) rows(w *Widget) int { return max(1, w.inside().Dy()/w.t.font.Height) }

func (l *listbox) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	t := w.t
	f := t.font
	y := r.Min.Y
	n := 0
	for i := l.top; i < len(l.items) && y < r.Max.Y; i++ {
		fg := w.col(w.colors[cFg])
		if l.sel[i] {
			dst.Draw(draw.Rect(r.Min.X, y, r.Max.X, y+f.Height), w.col(w.colors[cSelectBg]), nil, draw.ZP)
			fg = w.col(w.colors[cSelectFg])
		}
		_, _ = dst.String(draw.Pt(r.Min.X+2, y), fg, draw.ZP, f, l.items[i])
		if i == l.active && t.focus == w && t.focused {
			dst.Border(draw.Rect(r.Min.X, y, r.Max.X, y+f.Height), 1, fg, draw.ZP)
		}
		y += f.Height
		n++
	}
	l.shown = n
	if l.yscroll != "" {
		total := float64(max(1, len(l.items)))
		fs := strconv.FormatFloat(float64(l.top)/total, 'f', 4, 64) + " " +
			strconv.FormatFloat(min(1, float64(l.top+l.rows(w))/total), 'f', 4, 64)
		if fs != l.lastf {
			l.lastf = fs
			t.later(l.yscroll + " " + fs)
		}
	}
}

// index parses a listbox index; end is one past the last item.
func (l *listbox) index(w *Widget, s string) (int, error) {
	switch {
	case s == "end":
		return len(l.items), nil
	case s == "active":
		return l.active, nil
	case s == "anchor":
		return l.anchor, nil
	case strings.HasPrefix(s, "@"):
		xy := strings.SplitN(s[1:], ",", 2)
		if len(xy) != 2 {
			return 0, tkerr(errBadix, s)
		}
		y, err := strconv.Atoi(xy[1])
		if err != nil {
			return 0, tkerr(errBadix, s)
		}
		return l.nearest(w, w.inside().Min.Y+y), nil
	}
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0, tkerr(errBadix, s)
	}
	return i, nil
}

// nearest is the item at y in the image.
func (l *listbox) nearest(w *Widget, y int) int {
	if len(l.items) == 0 {
		return 0
	}
	i := l.top + (y-w.inside().Min.Y)/w.t.font.Height
	return max(0, min(len(l.items)-1, i))
}

func (l *listbox) see(w *Widget, i int) {
	if i < l.top {
		l.top = max(0, i)
	} else if rows := l.rows(w); i >= l.top+rows {
		l.top = i - rows + 1
	}
}

func (l *listbox) cmd(w *Widget, args []string) (string, error) {
	w.t.dirty = true
	idx := func(k int) (int, error) {
		if len(args) <= k {
			return 0, tkerr(errBadvl)
		}
		return l.index(w, args[k])
	}
	span := func() (int, int, error) {
		a, err := idx(1)
		if err != nil {
			return 0, 0, err
		}
		b := a
		if len(args) > 2 {
			if b, err = idx(2); err != nil {
				return 0, 0, err
			}
			if args[2] == "end" {
				b--
			}
		}
		return a, b, nil
	}
	switch args[0] {
	case "insert":
		i, err := idx(1)
		if err != nil {
			return "", err
		}
		i = max(0, min(i, len(l.items)))
		add := args[2:]
		l.items = append(l.items[:i], append(append([]string(nil), add...), l.items[i:]...)...)
		nsel := map[int]bool{}
		for k := range l.sel {
			if k >= i {
				k += len(add)
			}
			nsel[k] = true
		}
		l.sel = nsel
		return "", nil
	case "delete":
		a, b, err := span()
		if err != nil {
			return "", err
		}
		a, b = max(0, a), min(b, len(l.items)-1)
		if a > b {
			return "", nil
		}
		l.items = append(l.items[:a], l.items[b+1:]...)
		nsel := map[int]bool{}
		for k := range l.sel {
			switch {
			case k < a:
				nsel[k] = true
			case k > b:
				nsel[k-(b-a+1)] = true
			}
		}
		l.sel = nsel
		l.top = max(0, min(l.top, len(l.items)-1))
		return "", nil
	case "get":
		a, b, err := span()
		if err != nil {
			return "", err
		}
		if len(args) == 2 {
			if a < 0 || a >= len(l.items) {
				return "", nil
			}
			return l.items[a], nil
		}
		var out []string
		for i := max(0, a); i <= b && i < len(l.items); i++ {
			out = append(out, l.items[i])
		}
		return strings.Join(out, " "), nil
	case "size":
		return itoa(len(l.items)), nil
	case "curselection":
		var out []string
		for i := range l.items {
			if l.sel[i] {
				out = append(out, itoa(i))
			}
		}
		return strings.Join(out, " "), nil
	case "selection":
		if len(args) < 3 {
			return "", tkerr(errBadvl)
		}
		args = args[1:]
		a, b, err := span()
		if err != nil {
			return "", err
		}
		switch args[0] {
		case "set":
			for i := a; i <= b; i++ {
				l.sel[i] = true
			}
		case "clear":
			for i := a; i <= b; i++ {
				delete(l.sel, i)
			}
		case "includes":
			if l.sel[a] {
				return "1", nil
			}
			return "0", nil
		case "anchor":
			l.anchor = a
		default:
			return "", tkerr(errBadvl, args[0])
		}
		return "", nil
	case "see":
		i, err := idx(1)
		if err == nil {
			l.see(w, i)
		}
		return "", err
	case "activate":
		i, err := idx(1)
		if err == nil {
			l.active = max(0, min(i, len(l.items)-1))
		}
		return "", err
	case "index":
		i, err := idx(1)
		return itoa(i), err
	case "nearest":
		y, err := idx(1)
		if err != nil {
			return "", err
		}
		return itoa(l.nearest(w, w.inside().Min.Y+y)), nil
	case "yview":
		return l.yview(w, args[1:])
	case "xview":
		return "0 1", nil
	}
	return "", tkerr(errBadcm, args[0])
}

func (l *listbox) yview(w *Widget, args []string) (string, error) {
	total := max(1, len(l.items))
	if len(args) == 0 {
		return strconv.FormatFloat(float64(l.top)/float64(total), 'f', 4, 64) + " " +
			strconv.FormatFloat(min(1, float64(l.top+l.rows(w))/float64(total)), 'f', 4, 64), nil
	}
	switch args[0] {
	case "moveto":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return "", tkerr(errBadvl, args[1])
		}
		l.top = int(f * float64(total))
	case "scroll":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		n, err := atoi(args[1])
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(args[2], "page") {
			n *= l.rows(w)
		}
		l.top += n
	default:
		i, err := l.index(w, args[0])
		if err != nil {
			return "", err
		}
		l.top = i
	}
	l.top = max(0, min(l.top, len(l.items)-1))
	return "", nil
}

func (l *listbox) choose(i int, toggle bool) {
	if l.mode == "multiple" || toggle {
		if l.sel[i] {
			delete(l.sel, i)
		} else {
			l.sel[i] = true
		}
	} else {
		l.sel = map[int]bool{i: true}
	}
	l.active, l.anchor = i, i
}

func (l *listbox) event(w *Widget, e *event) {
	t := w.t
	switch e.kind {
	case evPress:
		switch e.button {
		case 1:
			if len(l.items) > 0 {
				l.choose(l.nearest(w, e.p.Y), false)
			}
		case 4:
			l.top = max(0, l.top-3)
		case 5:
			l.top = max(0, min(len(l.items)-1, l.top+3))
		}
		t.dirty = true
	case evMotion:
		if e.held&1 != 0 && l.mode == "browse" && len(l.items) > 0 {
			if i := l.nearest(w, e.p.Y); !l.sel[i] {
				l.choose(i, false)
				t.dirty = true
			}
		}
	case evKey:
		n := len(l.items)
		if n == 0 {
			return
		}
		switch e.key {
		case draw.KeyUp:
			l.active = max(0, l.active-1)
		case draw.KeyDown:
			l.active = min(n-1, l.active+1)
		case ' ':
			l.choose(l.active, false)
		default:
			return
		}
		if l.mode == "browse" {
			l.sel = map[int]bool{l.active: true}
		}
		l.see(w, l.active)
		t.dirty = true
	case evFocusIn, evFocusOut:
		t.dirty = true
	}
}
