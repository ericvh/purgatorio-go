package tk

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// Event kinds.
const (
	evPress = iota + 1
	evRelease
	evMotion
	evKey
	evEnter
	evLeave
	evFocusIn
	evFocusOut
	evConfigure
	evDestroy
	evMap
	evUnmap
)

// event is something delivered to a widget.
type event struct {
	kind   int
	button int // 1 to 6 for presses and releases
	double bool
	held   int        // buttons down, for motion
	key    rune       // for keys
	p      draw.Point // pointer position in the image
}

// pattern is a parsed event sequence (bind(9)).
type pattern struct {
	kind   int
	button int  // 0 for any
	key    rune // 0 for any
	double bool
	held   int // Motion-Button-n
}

type binding struct {
	pat    pattern
	script string
	seq    string // as given
}

// setbinding sets the script for p in bs; a leading + adds to the old
// script and a leading - takes it out.
func setbinding(bs []binding, p pattern, seq, script string) []binding {
	for i := range bs {
		if bs[i].pat != p {
			continue
		}
		switch {
		case strings.HasPrefix(script, "+"):
			if bs[i].script == "" {
				bs[i].script = script[1:]
			} else {
				bs[i].script += ";" + script[1:]
			}
		case strings.HasPrefix(script, "-"):
			bs[i].script = strings.Replace(bs[i].script, script[1:], "", 1)
		default:
			bs[i].script = script
		}
		return bs
	}
	if strings.HasPrefix(script, "-") {
		return bs
	}
	return append(bs, binding{p, strings.TrimPrefix(script, "+"), seq})
}

// parseSeq parses an event sequence such as <Double-Button-1>,
// <Key-a>, <Control-c> or a single character, a key (tkseqparse).
func parseSeq(seq string) (pattern, error) {
	var p pattern
	bad := tkerr(errBadsq, seq)
	if !strings.HasPrefix(seq, "<") {
		r, n := utf8.DecodeRuneInString(seq)
		if n == 0 || n != len(seq) {
			return p, bad
		}
		return pattern{kind: evKey, key: r}, nil
	}
	s := strings.TrimPrefix(seq, "<")
	item := func() string {
		s = strings.TrimLeft(s, " -")
		i := 0
		for i < len(s) && s[i] != ' ' && s[i] != '-' && s[i] != '>' {
			i++
		}
		it := s[:i]
		s = s[i:]
		return it
	}
	key := func() rune {
		s = strings.TrimLeft(s, " -")
		if strings.HasPrefix(s, "\\") {
			s = s[1:]
		} else if s == "" || s[0] == '>' || s[0] == '-' {
			return 0
		}
		r, n := utf8.DecodeRuneInString(s)
		s = s[n:]
		return r
	}
	button := func() (int, bool) {
		it := item()
		if it == "" {
			return 0, true
		}
		if len(it) == 1 && it[0] >= '1' && it[0] <= '6' {
			return int(it[0] - '0'), true
		}
		return 0, false
	}
	motion := false
	for s != "" && s[0] != '>' {
		it := item()
		switch it {
		case "":
			continue
		case "Motion":
			motion = true
		case "Double":
			p.double = true
		case "Map":
			p.kind = evMap
		case "Unmap":
			p.kind = evUnmap
		case "Destroy":
			p.kind = evDestroy
		case "Enter":
			p.kind = evEnter
		case "Leave":
			p.kind = evLeave
		case "FocusIn":
			p.kind = evFocusIn
		case "FocusOut":
			p.kind = evFocusOut
		case "Configure":
			p.kind = evConfigure
		case "Control":
			r := key()
			if r == 0 {
				return p, bad
			}
			if r <= '~' {
				r &= 0x1F
			}
			p.kind, p.key = evKey, r
		case "Key", "KeyPress":
			p.kind, p.key = evKey, key()
		case "Button", "ButtonPress":
			b, ok := button()
			if !ok {
				return p, bad
			}
			p.kind, p.button = evPress, b
		case "ButtonRelease":
			b, ok := button()
			if !ok {
				return p, bad
			}
			p.kind, p.button = evRelease, b
		default:
			return p, bad
		}
	}
	if motion {
		if p.kind == evPress {
			p.held = 1 << (p.button - 1)
			if p.button == 0 {
				p.held = 0x1F
			}
			p.button = 0
		}
		p.kind = evMotion
	}
	if p.kind == 0 {
		return p, bad
	}
	return p, nil
}

// matches reports how well pattern p matches e: 0 for not at all.
func (p *pattern) matches(e *event) int {
	if p.kind != e.kind {
		return 0
	}
	score := 1
	switch e.kind {
	case evPress, evRelease:
		if p.button != 0 {
			if p.button != e.button {
				return 0
			}
			score++
		}
		if p.double {
			if !e.double {
				return 0
			}
			score += 2
		}
	case evMotion:
		if p.held != 0 {
			if e.held&p.held == 0 {
				return 0
			}
			score++
		}
	case evKey:
		if p.key != 0 {
			if p.key != e.key {
				return 0
			}
			score++
		}
	}
	return score
}

func cmdBind(t *Toplevel, args []string) (string, error) {
	if len(args) < 2 {
		return "", tkerr(errBadvl)
	}
	w := t.widgets[args[0]]
	if w == nil {
		return "", tkerr(errBadwp, args[0])
	}
	p, err := parseSeq(args[1])
	if err != nil {
		return "", err
	}
	if len(args) == 2 {
		for _, b := range w.binds {
			if b.pat == p {
				return b.script, nil
			}
		}
		return "", nil
	}
	w.binds = setbinding(w.binds, p, args[1], args[2])
	return "", nil
}

// deliver gives e to w: its built-in behaviour, then its binding.
func (w *Widget) deliver(e *event) {
	if w.destroyed {
		return
	}
	if ev, ok := w.impl.(eventer); ok && w.state != stDisabled {
		ev.event(w, e)
	}
	if w.destroyed {
		return
	}
	var best *binding
	bs := 0
	for i := range w.binds {
		if s := w.binds[i].pat.matches(e); s > bs {
			best, bs = &w.binds[i], s
		}
	}
	if best != nil && best.script != "" {
		_, _ = w.t.exec(w.substitute(best.script, e))
	}
}

// substitute replaces the % sequences of a bound script (tkcmdbind).
func (w *Widget) substitute(s string, e *event) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		pos := w.abs()
		scr := e.p.Sub(w.t.root.abs()).Add(w.t.root.screenpos())
		switch s[i] {
		case '%':
			b.WriteByte('%')
		case 'b':
			b.WriteString(itoa(e.button))
		case 's':
			switch e.kind {
			case evKey:
				b.WriteString(itoa(int(e.key)))
			default:
				b.WriteString(itoa(e.held))
			}
		case 'x':
			b.WriteString(itoa(e.p.X - pos.X))
		case 'y':
			b.WriteString(itoa(e.p.Y - pos.Y))
		case 'X':
			b.WriteString(itoa(scr.X))
		case 'Y':
			b.WriteString(itoa(scr.Y))
		case 'A':
			if e.key == '{' || e.key == '}' || e.key == '\\' {
				b.WriteByte('\\')
			}
			if e.key != 0 {
				b.WriteRune(e.key)
			}
		case 'K':
			fmt.Fprintf(&b, "%.4X", e.key)
		case 'W':
			b.WriteString(w.name)
		default:
			b.WriteByte('%')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// screenpos is where "." is on the screen: the image's coordinates are
// the screen's.
func (w *Widget) screenpos() draw.Point { return w.abs() }

// hit returns the deepest mapped widget at p.
func (w *Widget) hit(p draw.Point) *Widget {
	if !w.mapped || !p.In(w.rect()) {
		return nil
	}
	for i := len(w.slaves) - 1; i >= 0; i-- {
		if h := w.slaves[i].hit(p); h != nil {
			return h
		}
	}
	return w
}

// Pointer is tk->pointer.
func (t *Toplevel) Pointer(p draw.Pointer) {
	defer t.update()
	t.layout()
	if t.menu != nil && t.menuPointer(p) {
		t.last, t.buttons = p, p.Buttons
		return
	}
	var target *Widget
	switch {
	case t.press != nil:
		target = t.press
	default:
		target = t.root.hit(p.XY)
		if t.grab != nil && (target == nil || !target.within(t.grab)) {
			target = t.grab
		}
	}
	if t.buttons == 0 && target != t.under {
		if t.under != nil {
			t.under.deliver(&event{kind: evLeave, p: p.XY})
		}
		t.under = target
		if target != nil {
			target.deliver(&event{kind: evEnter, p: p.XY})
		}
	}
	changed := p.Buttons ^ t.buttons
	if target != nil {
		for b := 0; b < 6; b++ {
			bit := 1 << b
			if changed&bit == 0 {
				continue
			}
			if p.Buttons&bit != 0 {
				if t.buttons == 0 {
					t.press = target
				}
				e := &event{kind: evPress, button: b + 1, held: p.Buttons, p: p.XY}
				if t.click.w == target && t.click.b == b+1 && p.Msec-t.click.msec < 500 {
					e.double = true
					t.click.w = nil
				} else {
					t.click.w, t.click.b, t.click.msec = target, b+1, p.Msec
				}
				if b == 0 && target.takefocus {
					t.setfocus(target)
				}
				target.deliver(e)
			} else {
				target.deliver(&event{kind: evRelease, button: b + 1, held: p.Buttons, p: p.XY})
			}
		}
		if changed == 0 && p.XY != t.last.XY {
			target.deliver(&event{kind: evMotion, held: p.Buttons, p: p.XY})
		}
	}
	t.last, t.buttons = p, p.Buttons
	if p.Buttons == 0 && t.press != nil {
		t.press = nil
		if h := t.root.hit(p.XY); h != t.under {
			if t.under != nil {
				t.under.deliver(&event{kind: evLeave, p: p.XY})
			}
			t.under = h
			if h != nil {
				h.deliver(&event{kind: evEnter, p: p.XY})
			}
		}
	}
}

// within reports whether w is a or inside it.
func (w *Widget) within(a *Widget) bool {
	return w == a || strings.HasPrefix(w.name, a.name+".") || a.name == "."
}

// Keyboard is tk->keyboard.
func (t *Toplevel) Keyboard(key int) {
	defer t.update()
	e := &event{kind: evKey, key: rune(key)}
	if t.menu != nil {
		if key == draw.KeyEsc {
			t.unpost()
		}
		return
	}
	w := t.focus
	if w == nil {
		w = t.root
	}
	w.deliver(e)
}
