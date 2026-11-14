package tk

import (
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// text is a multi-line text editor (text(9)): indices, marks, the sel
// tag and scrolling. Tags other than sel are remembered but drawn
// plainly; embedded windows and images are not supported.
type text struct {
	lines   [][]rune
	marks   map[string]pos
	left    map[string]bool // marks with left gravity
	tags    map[string][]span
	top     int // first line shown
	wrap    string
	yscroll string
	xscroll string
	lastf   string // fractions last sent to yscroll
	shown   int    // lines shown at the last paint
}

type pos struct{ line, col int } // line from 0; col in runes

type span struct{ a, b pos }

func (p pos) less(q pos) bool { return p.line < q.line || p.line == q.line && p.col < q.col }

func newText() *text {
	return &text{lines: [][]rune{nil}, marks: map[string]pos{"insert": {}, "current": {}}, left: map[string]bool{},
		tags: map[string][]span{}, wrap: "char"}
}

func (x *text) defaults(w *Widget) {
	w.bd, w.relief, w.takefocus = 1, reliefSunken, true
}

func (x *text) option(w *Widget, name, val string) (bool, error) {
	switch name {
	case "wrap":
		if val != "none" && val != "char" && val != "word" {
			return true, tkerr(errBadvl, val)
		}
		x.wrap = val
	case "yscrollcommand":
		x.yscroll = val
	case "xscrollcommand":
		x.xscroll = val
	case "spacing1", "spacing2", "spacing3", "tabs", "insertwidth", "setgrid", "exportselection":
	default:
		return false, nil
	}
	return true, nil
}

func (x *text) get(w *Widget, name string) (string, bool) {
	switch name {
	case "wrap":
		return x.wrap, true
	case "yscrollcommand":
		return x.yscroll, true
	case "xscrollcommand":
		return x.xscroll, true
	}
	return "", false
}

func (x *text) natural(w *Widget) draw.Point {
	return draw.Pt(40*w.t.charwidth(), 10*w.t.font.Height)
}

func (x *text) end() pos {
	n := len(x.lines) - 1
	return pos{n, len(x.lines[n])}
}

func (x *text) clamp(p pos) pos {
	if p.line < 0 {
		return pos{}
	}
	if p.line >= len(x.lines) {
		return x.end()
	}
	p.col = max(0, min(p.col, len(x.lines[p.line])))
	return p
}

// fwd moves p n runes on, counting line ends as one.
func (x *text) fwd(p pos, n int) pos {
	for n > 0 {
		if p.col < len(x.lines[p.line]) {
			d := min(n, len(x.lines[p.line])-p.col)
			p.col += d
			n -= d
		} else if p.line+1 < len(x.lines) {
			p = pos{p.line + 1, 0}
			n--
		} else {
			break
		}
	}
	for n < 0 {
		if p.col > 0 {
			d := min(-n, p.col)
			p.col -= d
			n += d
		} else if p.line > 0 {
			p = pos{p.line - 1, len(x.lines[p.line-1])}
			n++
		} else {
			break
		}
	}
	return p
}

func isword(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 0x80
}

// index parses a text index: a base (line.char, @x,y, end, a mark,
// tag.first or tag.last) and modifiers (+n chars, -n lines,
// linestart, lineend, wordstart, wordend).
func (x *text) index(w *Widget, s string) (pos, error) {
	bad := tkerr(errBadix, s)
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] != ' ' && s[i] != '+' && s[i] != '-' {
		i++
	}
	base, mods := s[:i], s[i:]
	var p pos
	switch {
	case base == "":
		return p, bad
	case base == "end":
		p = x.end()
	case base[0] == '@':
		xy := strings.SplitN(base[1:], ",", 2)
		if len(xy) != 2 {
			return p, bad
		}
		px, e1 := strconv.Atoi(xy[0])
		py, e2 := strconv.Atoi(xy[1])
		if e1 != nil || e2 != nil {
			return p, bad
		}
		in := w.inside()
		p = x.at(w, in.Min.Add(draw.Pt(px, py)))
	case base[0] >= '0' && base[0] <= '9':
		lc := strings.SplitN(base, ".", 2)
		if len(lc) != 2 {
			return p, bad
		}
		l, err := strconv.Atoi(lc[0])
		if err != nil {
			return p, bad
		}
		p.line = l - 1
		if lc[1] == "end" {
			p = x.clamp(p)
			p.col = len(x.lines[p.line])
		} else {
			c, err := strconv.Atoi(lc[1])
			if err != nil {
				return p, bad
			}
			p.col = c
		}
		p = x.clamp(p)
	case strings.HasSuffix(base, ".first") || strings.HasSuffix(base, ".last"):
		dot := strings.LastIndexByte(base, '.')
		sp := x.tags[base[:dot]]
		if len(sp) == 0 {
			return p, tkerr(errBadtg, base)
		}
		if base[dot:] == ".first" {
			p = sp[0].a
		} else {
			p = sp[len(sp)-1].b
		}
	default:
		m, ok := x.marks[base]
		if !ok {
			return p, bad
		}
		p = m
	}
	for {
		mods = strings.TrimLeft(mods, " ")
		if mods == "" {
			return x.clamp(p), nil
		}
		if mods[0] == '+' || mods[0] == '-' {
			sign := 1
			if mods[0] == '-' {
				sign = -1
			}
			mods = strings.TrimLeft(mods[1:], " ")
			j := 0
			for j < len(mods) && mods[j] >= '0' && mods[j] <= '9' {
				j++
			}
			n, err := strconv.Atoi(mods[:j])
			if err != nil {
				return p, bad
			}
			mods = strings.TrimLeft(mods[j:], " ")
			k := 0
			for k < len(mods) && mods[k] >= 'a' && mods[k] <= 'z' {
				k++
			}
			unit := mods[:k]
			mods = mods[k:]
			switch {
			case unit != "" && strings.HasPrefix("chars", unit):
				p = x.fwd(p, sign*n)
			case unit != "" && strings.HasPrefix("lines", unit):
				p = x.clamp(pos{p.line + sign*n, p.col})
			default:
				return p, bad
			}
			continue
		}
		k := 0
		for k < len(mods) && mods[k] >= 'a' && mods[k] <= 'z' {
			k++
		}
		word := mods[:k]
		mods = mods[k:]
		line := x.lines[p.line]
		switch word {
		case "linestart":
			p.col = 0
		case "lineend":
			p.col = len(line)
		case "wordstart":
			for p.col > 0 && isword(line[p.col-1]) {
				p.col--
			}
		case "wordend":
			for p.col < len(line) && isword(line[p.col]) {
				p.col++
			}
		default:
			return p, bad
		}
	}
}

func posString(p pos) string { return itoa(p.line+1) + "." + itoa(p.col) }

// textof returns the text from a to b.
func (x *text) textof(a, b pos) string {
	if !a.less(b) {
		return ""
	}
	var sb strings.Builder
	for l := a.line; l <= b.line; l++ {
		line := x.lines[l]
		c0, c1 := 0, len(line)
		if l == a.line {
			c0 = a.col
		}
		if l == b.line {
			c1 = b.col
		}
		sb.WriteString(string(line[c0:c1]))
		if l < b.line {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// adjust moves positions for an insertion of n runes ending at e
// after a (right gravity) or a deletion of a..b.
func adjustIns(p, a, e pos) pos {
	if p.less(a) {
		return p
	}
	if p.line == a.line {
		return pos{e.line, e.col + p.col - a.col}
	}
	return pos{p.line + e.line - a.line, p.col}
}

func adjustDel(p, a, b pos) pos {
	if p.less(a) {
		return p
	}
	if p.less(b) {
		return a
	}
	if p.line == b.line {
		return pos{a.line, a.col + p.col - b.col}
	}
	return pos{p.line - (b.line - a.line), p.col}
}

func (x *text) insert(p pos, s string, tags []string) pos {
	p = x.clamp(p)
	parts := strings.Split(s, "\n")
	line := x.lines[p.line]
	head := append([]rune(nil), line[:p.col]...)
	tail := append([]rune(nil), line[p.col:]...)
	var nl [][]rune
	for i, part := range parts {
		rs := []rune(part)
		if i == 0 {
			rs = append(head, rs...)
		}
		nl = append(nl, rs)
	}
	e := pos{p.line + len(parts) - 1, len(nl[len(nl)-1])}
	nl[len(nl)-1] = append(nl[len(nl)-1], tail...)
	x.lines = append(x.lines[:p.line], append(nl, x.lines[p.line+1:]...)...)
	for k, m := range x.marks {
		if m == p && x.left[k] {
			continue
		}
		x.marks[k] = adjustIns(m, p, e)
	}
	for k, sp := range x.tags {
		for i := range sp {
			sp[i].a = adjustIns(sp[i].a, p, e)
			if sp[i].b != p {
				sp[i].b = adjustIns(sp[i].b, p, e)
			}
		}
		x.tags[k] = sp
	}
	for _, tg := range tags {
		x.tagAdd(tg, p, e)
	}
	return e
}

func (x *text) delete(a, b pos) {
	a, b = x.clamp(a), x.clamp(b)
	if !a.less(b) {
		return
	}
	nl := append(append([]rune(nil), x.lines[a.line][:a.col]...), x.lines[b.line][b.col:]...)
	x.lines = append(x.lines[:a.line], append([][]rune{nl}, x.lines[b.line+1:]...)...)
	for k, m := range x.marks {
		x.marks[k] = adjustDel(m, a, b)
	}
	for k, sp := range x.tags {
		var out []span
		for _, s := range sp {
			s.a, s.b = adjustDel(s.a, a, b), adjustDel(s.b, a, b)
			if s.a.less(s.b) {
				out = append(out, s)
			}
		}
		x.tags[k] = out
	}
	x.top = min(x.top, len(x.lines)-1)
}

func (x *text) tagAdd(name string, a, b pos) {
	if !a.less(b) {
		return
	}
	sp := append(x.tags[name], span{a, b})
	// Merge overlapping spans, kept in order.
	for i := 1; i < len(sp); i++ {
		for j := i; j > 0 && sp[j].a.less(sp[j-1].a); j-- {
			sp[j], sp[j-1] = sp[j-1], sp[j]
		}
	}
	var out []span
	for _, s := range sp {
		if n := len(out); n > 0 && !out[n-1].b.less(s.a) {
			if out[n-1].b.less(s.b) {
				out[n-1].b = s.b
			}
			continue
		}
		out = append(out, s)
	}
	x.tags[name] = out
}

func (x *text) tagRemove(name string, a, b pos) {
	var out []span
	for _, s := range x.tags[name] {
		if !a.less(s.b) || !s.a.less(b) {
			out = append(out, s) // no overlap
			continue
		}
		if s.a.less(a) {
			out = append(out, span{s.a, a})
		}
		if b.less(s.b) {
			out = append(out, span{b, s.b})
		}
	}
	x.tags[name] = out
}

func (x *text) selected(p pos) bool {
	for _, s := range x.tags["sel"] {
		if !p.less(s.a) && p.less(s.b) {
			return true
		}
	}
	return false
}

// segment is one row on the screen: part of a line.
type segment struct {
	line, c0, c1 int
}

// rows lays out lines from top to fill height h at width wd.
func (x *text) rows(w *Widget, wd, h int) []segment {
	f := w.t.font
	var segs []segment
	maxrows := max(1, h/f.Height)
	for l := x.top; l < len(x.lines) && len(segs) < maxrows; l++ {
		line := x.lines[l]
		if x.wrap == "none" || len(line) == 0 {
			segs = append(segs, segment{l, 0, len(line)})
			continue
		}
		c0 := 0
		for c0 < len(line) && len(segs) < maxrows {
			c1, width := c0, 0
			for c1 < len(line) {
				cw := f.Width(string(line[c1]))
				if width+cw > wd && c1 > c0 {
					break
				}
				width += cw
				c1++
			}
			if x.wrap == "word" && c1 < len(line) {
				for k := c1; k > c0; k-- {
					if line[k-1] == ' ' {
						c1 = k
						break
					}
				}
			}
			segs = append(segs, segment{l, c0, c1})
			c0 = c1
		}
	}
	return segs
}

func (x *text) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	t := w.t
	f := t.font
	segs := x.rows(w, r.Dx()-2, r.Dy())
	fg, sbg, sfg := w.col(w.colors[cFg]), w.col(w.colors[cSelectBg]), w.col(w.colors[cSelectFg])
	ins := x.marks["insert"]
	showcursor := t.focus == w && t.focused && w.state != stDisabled
	y := r.Min.Y
	for i, sg := range segs {
		line := x.lines[sg.line]
		px := r.Min.X + 1
		last := i+1 == len(segs) || segs[i+1].line != sg.line
		for c := sg.c0; c < sg.c1; c++ {
			s := string(line[c])
			cw := f.Width(s)
			if x.selected(pos{sg.line, c}) {
				dst.Draw(draw.Rect(px, y, px+cw, y+f.Height), sbg, nil, draw.ZP)
				_, _ = dst.String(draw.Pt(px, y), sfg, draw.ZP, f, s)
			} else {
				_, _ = dst.String(draw.Pt(px, y), fg, draw.ZP, f, s)
			}
			if showcursor && ins.line == sg.line && ins.col == c {
				dst.Draw(draw.Rect(px, y, px+1, y+f.Height), fg, nil, draw.ZP)
			}
			px += cw
		}
		if showcursor && ins.line == sg.line && ins.col == sg.c1 && (last || sg.c1 == sg.c0) {
			dst.Draw(draw.Rect(px, y, px+1, y+f.Height), fg, nil, draw.ZP)
		}
		y += f.Height
	}
	x.shown = 0
	if len(segs) > 0 {
		x.shown = segs[len(segs)-1].line - x.top + 1
	}
	x.scrolled(w)
}

// scrolled tells the scroll command where the view is.
func (x *text) scrolled(w *Widget) {
	if x.yscroll == "" {
		return
	}
	n := float64(len(x.lines))
	f0 := float64(x.top) / n
	f1 := float64(x.top+x.shown) / n
	fs := strconv.FormatFloat(f0, 'f', 4, 64) + " " + strconv.FormatFloat(min(1, f1), 'f', 4, 64)
	if fs != x.lastf {
		x.lastf = fs
		w.t.later(x.yscroll + " " + fs)
	}
}

// at returns the position at point p of the image.
func (x *text) at(w *Widget, p draw.Point) pos {
	r := w.inside()
	f := w.t.font
	segs := x.rows(w, r.Dx()-2, r.Dy())
	if len(segs) == 0 {
		return x.end()
	}
	row := (p.Y - r.Min.Y) / f.Height
	if p.Y < r.Min.Y {
		row = 0
	}
	if row >= len(segs) {
		row = len(segs) - 1
	}
	sg := segs[row]
	line := x.lines[sg.line]
	px := r.Min.X + 1
	for c := sg.c0; c < sg.c1; c++ {
		cw := f.Width(string(line[c]))
		if p.X < px+cw/2 {
			return pos{sg.line, c}
		}
		px += cw
	}
	if sg.c1 < len(line) && sg.c1 > sg.c0 {
		return pos{sg.line, sg.c1 - 1}
	}
	return pos{sg.line, sg.c1}
}

// see scrolls so that p is shown.
func (x *text) see(w *Widget, p pos) {
	r := w.inside()
	rows := max(1, r.Dy()/w.t.font.Height)
	if p.line < x.top {
		x.top = p.line
		return
	}
	for x.top < p.line {
		segs := x.rows(w, r.Dx()-2, r.Dy())
		if len(segs) < rows || segs[len(segs)-1].line > p.line ||
			segs[len(segs)-1].line == p.line && (segs[len(segs)-1].c1 >= p.col || segs[len(segs)-1].c1 == len(x.lines[p.line])) {
			return
		}
		x.top++
	}
}

func (x *text) cmd(w *Widget, args []string) (string, error) {
	w.t.dirty = true
	idx := func(k int) (pos, error) {
		if len(args) <= k {
			return pos{}, tkerr(errBadvl)
		}
		return x.index(w, args[k])
	}
	switch args[0] {
	case "insert":
		p, err := idx(1)
		if err != nil {
			return "", err
		}
		for k := 2; k < len(args); k += 2 {
			var tags []string
			if k+1 < len(args) {
				tags = strings.Fields(args[k+1])
			}
			p = x.insert(p, args[k], tags)
		}
		return "", nil
	case "delete":
		a, err := idx(1)
		if err != nil {
			return "", err
		}
		b := x.fwd(a, 1)
		if len(args) > 2 {
			if b, err = idx(2); err != nil {
				return "", err
			}
		}
		x.delete(a, b)
		return "", nil
	case "get":
		a, err := idx(1)
		if err != nil {
			return "", err
		}
		b := x.fwd(a, 1)
		if len(args) > 2 {
			if b, err = idx(2); err != nil {
				return "", err
			}
		}
		return x.textof(a, b), nil
	case "index":
		p, err := idx(1)
		return posString(p), err
	case "compare":
		if len(args) != 4 {
			return "", tkerr(errBadvl)
		}
		a, err := idx(1)
		if err != nil {
			return "", err
		}
		b, err := idx(3)
		if err != nil {
			return "", err
		}
		var v bool
		switch args[2] {
		case "<":
			v = a.less(b)
		case "<=":
			v = !b.less(a)
		case "==":
			v = a == b
		case "!=":
			v = a != b
		case ">=":
			v = !a.less(b)
		case ">":
			v = b.less(a)
		default:
			return "", tkerr(errBadvl, args[2])
		}
		if v {
			return "1", nil
		}
		return "0", nil
	case "mark":
		return x.mark(w, args[1:])
	case "tag":
		return x.tag(w, args[1:])
	case "see":
		p, err := idx(1)
		if err == nil {
			x.see(w, p)
		}
		return "", err
	case "yview":
		return x.yview(w, args[1:])
	case "xview":
		return "0 1", nil
	case "debug", "scan", "window", "image":
		return "", nil
	}
	return "", tkerr(errBadcm, args[0])
}

func (x *text) mark(w *Widget, args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadvl)
	}
	switch args[0] {
	case "set":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		p, err := x.index(w, args[2])
		if err != nil {
			return "", err
		}
		x.marks[args[1]] = p
	case "unset":
		for _, m := range args[1:] {
			if m != "insert" && m != "current" {
				delete(x.marks, m)
			}
		}
	case "names":
		var n []string
		for m := range x.marks {
			n = append(n, m)
		}
		sortStrings(n)
		return strings.Join(n, " "), nil
	case "gravity":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		if len(args) == 2 {
			if x.left[args[1]] {
				return "left", nil
			}
			return "right", nil
		}
		switch args[2] {
		case "left":
			x.left[args[1]] = true
		case "right":
			delete(x.left, args[1])
		default:
			return "", tkerr(errBadvl, args[2])
		}
	case "next", "previous":
	default:
		return "", tkerr(errBadvl, args[0])
	}
	return "", nil
}

func (x *text) tag(w *Widget, args []string) (string, error) {
	if len(args) < 2 && !(len(args) == 1 && args[0] == "names") {
		return "", tkerr(errBadvl)
	}
	switch args[0] {
	case "add", "remove":
		if len(args) < 3 {
			return "", tkerr(errBadvl)
		}
		for k := 2; k < len(args); k += 2 {
			a, err := x.index(w, args[k])
			if err != nil {
				return "", err
			}
			b := x.fwd(a, 1)
			if k+1 < len(args) {
				if b, err = x.index(w, args[k+1]); err != nil {
					return "", err
				}
			}
			if args[0] == "add" {
				x.tagAdd(args[1], a, b)
			} else {
				x.tagRemove(args[1], a, b)
			}
		}
	case "delete":
		for _, n := range args[1:] {
			delete(x.tags, n)
		}
	case "names":
		var n []string
		for k, sp := range x.tags {
			if len(sp) > 0 || k != "sel" {
				n = append(n, k)
			}
		}
		sortStrings(n)
		return strings.Join(n, " "), nil
	case "ranges":
		var n []string
		for _, s := range x.tags[args[1]] {
			n = append(n, posString(s.a), posString(s.b))
		}
		return strings.Join(n, " "), nil
	case "configure", "cget", "bind", "raise", "lower", "nextrange", "prevrange":
		if _, ok := x.tags[args[1]]; !ok {
			x.tags[args[1]] = nil
		}
	default:
		return "", tkerr(errBadvl, args[0])
	}
	return "", nil
}

func (x *text) yview(w *Widget, args []string) (string, error) {
	if len(args) == 0 {
		n := float64(len(x.lines))
		return strconv.FormatFloat(float64(x.top)/n, 'f', 4, 64) + " " +
			strconv.FormatFloat(min(1, float64(x.top+max(1, x.shown))/n), 'f', 4, 64), nil
	}
	rows := max(1, w.inside().Dy()/w.t.font.Height)
	switch args[0] {
	case "moveto":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return "", tkerr(errBadvl, args[1])
		}
		x.top = max(0, min(len(x.lines)-1, int(f*float64(len(x.lines)))))
	case "scroll":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		n, err := atoi(args[1])
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(args[2], "page") {
			n *= rows
		}
		x.top = max(0, min(len(x.lines)-1, x.top+n))
	case "-pickplace":
		if len(args) == 2 {
			p, err := x.index(w, args[1])
			if err != nil {
				return "", err
			}
			x.see(w, p)
		}
	default:
		p, err := x.index(w, args[0])
		if err != nil {
			return "", err
		}
		x.top = p.line
	}
	return "", nil
}

func (x *text) event(w *Widget, ev *event) {
	t := w.t
	switch ev.kind {
	case evPress:
		switch ev.button {
		case 1:
			p := x.at(w, ev.p)
			x.marks["insert"], x.marks["anchor"] = p, p
			delete(x.tags, "sel")
		case 4:
			x.top = max(0, x.top-3)
		case 5:
			x.top = min(len(x.lines)-1, x.top+3)
		}
		t.dirty = true
	case evMotion:
		x.marks["current"] = x.at(w, ev.p)
		if ev.held&1 != 0 {
			p := x.at(w, ev.p)
			a := x.marks["anchor"]
			delete(x.tags, "sel")
			if p.less(a) {
				x.tagAdd("sel", p, a)
			} else {
				x.tagAdd("sel", a, p)
			}
			x.marks["insert"] = p
			t.dirty = true
		}
	case evKey:
		if w.state != stDisabled {
			x.key(w, ev.key)
		}
		t.dirty = true
	case evFocusIn, evFocusOut:
		t.dirty = true
	}
}

func (x *text) key(w *Widget, k rune) {
	ins := x.marks["insert"]
	delsel := func() bool {
		sp := x.tags["sel"]
		if len(sp) == 0 {
			return false
		}
		a, b := sp[0].a, sp[len(sp)-1].b
		if ins.less(a) || b.less(ins) {
			return false
		}
		x.delete(a, b)
		delete(x.tags, "sel")
		return true
	}
	switch k {
	case '\b':
		if !delsel() {
			x.delete(x.fwd(ins, -1), ins)
		}
	case 0x7F, draw.KeyDel:
		if !delsel() {
			x.delete(ins, x.fwd(ins, 1))
		}
	case 0x15:
		x.delete(pos{ins.line, 0}, ins)
	case 0x17:
		p := ins
		line := x.lines[p.line]
		for p.col > 0 && !isword(line[p.col-1]) {
			p.col--
		}
		for p.col > 0 && isword(line[p.col-1]) {
			p.col--
		}
		x.delete(p, ins)
	case 0x01, draw.KeyHome:
		x.marks["insert"] = pos{ins.line, 0}
	case 0x05, draw.KeyEnd:
		x.marks["insert"] = pos{ins.line, len(x.lines[ins.line])}
	case draw.KeyLeft:
		x.marks["insert"] = x.fwd(ins, -1)
	case draw.KeyRight:
		x.marks["insert"] = x.fwd(ins, 1)
	case draw.KeyUp:
		x.marks["insert"] = x.clamp(pos{ins.line - 1, ins.col})
	case draw.KeyDown:
		x.marks["insert"] = x.clamp(pos{ins.line + 1, ins.col})
	case draw.KeyPgup:
		x.top = max(0, x.top-max(1, x.shown-1))
		return
	case draw.KeyPgdown:
		x.top = min(len(x.lines)-1, x.top+max(1, x.shown-1))
		return
	case draw.KeyEsc:
		return
	default:
		if k != '\n' && k != '\t' && (k < 0x20 || k >= draw.KeySpec && k < 0xF900) {
			return
		}
		delsel()
		x.insert(x.marks["insert"], string(k), nil)
	}
	x.see(w, x.marks["insert"])
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
