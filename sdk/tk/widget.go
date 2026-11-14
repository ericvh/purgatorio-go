package tk

import (
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// Colour slots of a widget's environment (libtk's TkC* indices).
const (
	cFg = iota
	cBg
	cActiveBg
	cActiveFg
	cSelectBg
	cSelectFg
	cSelect
	cDisabled
	cHighlight
	ncolors
)

// The default environment (colrs.c).
var defcolors = [ncolors]uint32{
	cFg: 0x000000FF, cBg: 0xDDDDDDFF, cActiveBg: 0xEDEDEDFF, cActiveFg: 0x000000FF,
	cSelectBg: 0x404040FF, cSelectFg: 0xFFFFFFFF, cSelect: 0xB03060FF,
	cDisabled: 0x888888FF, cHighlight: 0x000000FF,
}

var coloropts = map[string]int{
	"foreground": cFg, "fg": cFg, "background": cBg, "bg": cBg,
	"activebackground": cActiveBg, "activeforeground": cActiveFg,
	"selectbackground": cSelectBg, "selectforeground": cSelectFg, "selectcolor": cSelect,
	"disabledcolor": cDisabled, "highlightcolor": cHighlight,
}

// Widget states.
const (
	stNormal = iota
	stActive
	stDisabled
)

var states = []string{"normal", "active", "disabled"}

// impl is what makes a widget a label, an entry, a canvas...
type impl interface {
	// option sets a kind-specific option; ok is false if there is none.
	option(w *Widget, name, val string) (ok bool, err error)
	// get returns a kind-specific option.
	get(w *Widget, name string) (string, bool)
	// natural is the size of the contents, without border or padding.
	natural(w *Widget) draw.Point
	// paint draws the contents in r, the inside of the border.
	paint(w *Widget, dst *draw.Image, r draw.Rectangle)
	// cmd runs a widget command other than configure and cget.
	cmd(w *Widget, args []string) (string, error)
}

// eventer is implemented by widgets with built-in behaviour.
type eventer interface {
	event(w *Widget, e *event)
}

// Widget is one widget.
type Widget struct {
	t      *Toplevel
	name   string
	kind   string
	parent *Widget
	impl   impl

	bd, hl     int
	relief     int
	padx, pady int
	width      int // -width and -height, when set
	height     int
	setw, seth bool
	state      int
	takefocus  bool
	colors     [ncolors]uint32

	// Geometry: the position relative to the master (or for "." and
	// menus, the toplevel) and the actual size, border included.
	x, y, w, h int
	fixed      bool // "." with an image: its size is the image's
	mapped     bool
	pk         *packing
	slaves     []*Widget
	propagate  bool

	binds     []binding
	destroyed bool

	reqgen int // the layout pass reqsz was computed in
	reqsz  draw.Point
}

func (t *Toplevel) newWidget(name, kind string, parent *Widget, im impl) *Widget {
	w := &Widget{t: t, name: name, kind: kind, parent: parent, impl: im, propagate: true}
	w.colors = defcolors
	if parent != nil {
		w.colors = parent.colors
	}
	if d, ok := im.(interface{ defaults(*Widget) }); ok {
		d.defaults(w)
	}
	t.widgets[name] = w
	t.dirty = true
	return w
}

// configure sets options from "-name value" pairs.
func (w *Widget) configure(args []string) error {
	if len(args)%2 != 0 {
		return tkerr(errOparg, args[len(args)-1])
	}
	for i := 0; i < len(args); i += 2 {
		if !strings.HasPrefix(args[i], "-") {
			return tkerr(errBadop, args[i])
		}
		name, val := args[i][1:], args[i+1]
		ok, err := w.generic(name, val)
		if !ok && err == nil {
			ok, err = w.impl.option(w, name, val)
		}
		if err != nil {
			return err
		}
		if !ok {
			return tkerr(errBadop, args[i])
		}
	}
	w.t.dirty = true
	w.deliver(&event{kind: evConfigure})
	return nil
}

// generic sets the options every widget has (tkgeneric).
func (w *Widget) generic(name, val string) (bool, error) {
	t := w.t
	var err error
	if c, ok := coloropts[name]; ok {
		var col uint32
		if col, err = parseColor(val); err == nil {
			w.colors[c] = col
		}
		return true, err
	}
	switch name {
	case "bd", "borderwidth":
		w.bd, err = t.nndist(val)
	case "highlightthickness":
		w.hl, err = t.nndist(val)
	case "relief":
		w.relief, err = lookup(reliefs, val)
	case "padx":
		w.padx, err = t.nndist(val)
	case "pady":
		w.pady, err = t.nndist(val)
	case "width":
		if val == "" {
			w.setw = false
			return true, nil
		}
		w.width, err = t.nndist(val)
		w.setw = err == nil
	case "height":
		if val == "" {
			w.seth = false
			return true, nil
		}
		w.height, err = t.nndist(val)
		w.seth = err == nil
	case "state":
		w.state, err = lookup(states, val)
	case "takefocus":
		w.takefocus, err = parseBool(val)
	case "font":
		if f, ferr := t.Display.OpenFont(val); ferr == nil {
			t.font = f
		}
	default:
		return false, nil
	}
	return true, err
}

// cget returns an option's value.
func (w *Widget) cget(opt string) (string, error) {
	name := strings.TrimPrefix(opt, "-")
	if c, ok := coloropts[name]; ok {
		return colorString(w.colors[c]), nil
	}
	switch name {
	case "bd", "borderwidth":
		return itoa(w.bd), nil
	case "highlightthickness":
		return itoa(w.hl), nil
	case "relief":
		return reliefs[w.relief], nil
	case "padx":
		return itoa(w.padx), nil
	case "pady":
		return itoa(w.pady), nil
	case "width":
		if w.setw {
			return itoa(w.width), nil
		}
		return itoa(w.req().X - 2*w.bd - 2*w.hl), nil
	case "height":
		if w.seth {
			return itoa(w.height), nil
		}
		return itoa(w.req().Y - 2*w.bd - 2*w.hl), nil
	case "actwidth":
		w.t.layout()
		return itoa(w.w - 2*w.bd), nil
	case "actheight":
		w.t.layout()
		return itoa(w.h - 2*w.bd), nil
	case "actx", "acty":
		w.t.layout()
		p := w.abs().Sub(w.t.root.abs())
		if name == "actx" {
			return itoa(p.X), nil
		}
		return itoa(p.Y), nil
	case "state":
		return states[w.state], nil
	case "takefocus":
		if w.takefocus {
			return "1", nil
		}
		return "0", nil
	}
	if v, ok := w.impl.get(w, name); ok {
		return v, nil
	}
	return "", tkerr(errBadop, opt)
}

// command runs a widget command (".w configure ...").
func (w *Widget) command(args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadcm, w.name)
	}
	switch args[0] {
	case "configure":
		return "", w.configure(args[1:])
	case "cget":
		if len(args) != 2 {
			return "", tkerr(errBadop)
		}
		return w.cget(args[1])
	}
	return w.impl.cmd(w, args)
}

// destroy removes w and its children.
func (w *Widget) destroy() {
	t := w.t
	prefix := w.name + "."
	for n, c := range t.widgets {
		if strings.HasPrefix(n, prefix) && !c.destroyed {
			c.destroy()
		}
	}
	w.deliver(&event{kind: evDestroy})
	w.destroyed = true
	w.unpack()
	for _, s := range append([]*Widget(nil), w.slaves...) {
		s.unpack()
	}
	delete(t.widgets, w.name)
	for _, p := range []**Widget{&t.focus, &t.grab, &t.under, &t.press, &t.menu} {
		if *p == w {
			*p = nil
		}
	}
	t.dirty = true
}

// abs is the widget's top left in the toplevel's image.
func (w *Widget) abs() draw.Point {
	t := w.t
	if w == t.root {
		return draw.Pt(w.x, w.y)
	}
	if w.pk != nil {
		return w.pk.master.abs().Add(draw.Pt(w.x, w.y))
	}
	return t.root.abs().Add(draw.Pt(w.x, w.y))
}

// rect is the widget's rectangle, border included.
func (w *Widget) rect() draw.Rectangle {
	p := w.abs()
	return draw.Rectangle{Min: p, Max: p.Add(draw.Pt(w.w, w.h))}
}

// inside is the rectangle within the border and highlight.
func (w *Widget) inside() draw.Rectangle { return w.rect().Inset(w.bd + w.hl) }

// req is the size the widget asks for, border included.
func (w *Widget) req() draw.Point {
	if w.t.inlayout && w.reqgen == w.t.gen {
		return w.reqsz
	}
	var n draw.Point
	if len(w.slaves) > 0 && w.propagate {
		n = w.packsize()
	} else {
		n = w.impl.natural(w)
	}
	iw, ih := n.X+2*w.padx, n.Y+2*w.pady
	if w.setw {
		iw = w.width
	}
	if w.seth {
		ih = w.height
	}
	e := 2 * (w.bd + w.hl)
	w.reqgen, w.reqsz = w.t.gen, draw.Pt(iw+e, ih+e)
	return w.reqsz
}

// fg and bg are the colours the widget paints with in its state.
func (w *Widget) fg() uint32 {
	switch w.state {
	case stDisabled:
		return w.colors[cDisabled]
	case stActive:
		return w.colors[cActiveFg]
	}
	return w.colors[cFg]
}

func (w *Widget) bg() uint32 {
	if w.state == stActive {
		return w.colors[cActiveBg]
	}
	return w.colors[cBg]
}

func (w *Widget) col(c uint32) *draw.Image { return w.t.colour(c) }

// paintTree draws w and the widgets packed in it.
func (w *Widget) paintTree(dst *draw.Image, clip draw.Rectangle) {
	if !w.mapped || w.w <= 0 || w.h <= 0 {
		return
	}
	r := w.rect()
	c, ok := r.Clip(clip)
	if !ok {
		return
	}
	_ = dst.ReplClipr(false, c)
	if w.colors[cBg]&0xFF != 0 {
		dst.Draw(r, w.col(w.bg()), nil, draw.ZP)
	}
	rel := w.relief
	if rf, ok := w.impl.(interface{ curRelief(*Widget) int }); ok {
		rel = rf.curRelief(w)
	}
	w.drawRelief(dst, r, rel, w.bg())
	in := r.Inset(w.bd + w.hl)
	if !in.Empty() {
		w.impl.paint(w, dst, in)
	}
	for _, s := range w.slaves {
		s.paintTree(dst, c)
	}
	_ = dst.ReplClipr(false, dst.R)
}

// drawRelief draws a border of width w.bd inside r (tkdrawrelief).
func (w *Widget) drawRelief(dst *draw.Image, r draw.Rectangle, relief int, bg uint32) {
	bevel(dst, w.t, r, w.bd, relief, bg)
}

func bevel(dst *draw.Image, t *Toplevel, r draw.Rectangle, bd, relief int, bg uint32) {
	if bd <= 0 || relief == reliefFlat {
		return
	}
	l, d := t.colour(shade(bg, true)), t.colour(shade(bg, false))
	switch relief {
	case reliefSunken:
		bevelrect(dst, r, bd, d, l)
	case reliefRaised:
		bevelrect(dst, r, bd, l, d)
	case reliefGroove, reliefRidge:
		if relief == reliefGroove {
			l, d = d, l
		}
		b1 := bd / 2
		b2 := bd - b1
		if b1 > 0 {
			bevelrect(dst, r, b1, l, d)
		}
		bevelrect(dst, r.Inset(b1), b2, d, l)
	}
}

// bevelrect draws top and left in top, bottom and right in bottom
// (tkbevel).
func bevelrect(dst *draw.Image, r draw.Rectangle, bw int, top, bottom *draw.Image) {
	dst.Draw(draw.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+bw), top, nil, draw.ZP)
	dst.Draw(draw.Rect(r.Min.X, r.Min.Y, r.Min.X+bw, r.Max.Y), top, nil, draw.ZP)
	for i := 0; i < bw; i++ {
		dst.Draw(draw.Rect(r.Min.X+bw-i, r.Max.Y-bw+i, r.Max.X, r.Max.Y-bw+i+1), bottom, nil, draw.ZP)
		dst.Draw(draw.Rect(r.Max.X-bw+i, r.Min.Y+bw-i, r.Max.X-bw+i+1, r.Max.Y), bottom, nil, draw.ZP)
	}
}

// shade returns a lighter or darker version of rgba (tkrgbashade).
func shade(rgba uint32, light bool) uint32 {
	const mindelta, delta = 0x10, 0x30
	r, g, b, a := int(rgba>>24), int(rgba>>16&0xFF), int(rgba>>8&0xFF), rgba&0xFF
	h, s, v := rgb2hsv(r, g, b)
	var vl, vd int
	switch {
	case v < mindelta:
		vd = v + delta
		vl = vd + delta
	case v > 255-mindelta:
		vl = v - delta
		vd = vl - delta
	default:
		vl = v + delta
		vd = v - delta
	}
	v = vd
	if light {
		v = vl
	}
	v = max(0, min(255, v))
	r, g, b = hsv2rgb(h, s, v)
	return uint32(r)<<24 | uint32(g)<<16 | uint32(b)<<8 | a
}

func rgb2hsv(r, g, b int) (h, s, v int) {
	mx, mn := max(r, g, b), min(r, g, b)
	v = mx
	if mx != 0 {
		s = (mx - mn) * 255 / mx
	}
	if s == 0 {
		return 0, 0, v
	}
	d := mx - mn
	switch mx {
	case r:
		h = (g - b) * 255 / d
	case g:
		h = 2*255 + (b-r)*255/d
	default:
		h = 4*255 + (r-g)*255/d
	}
	h *= 60
	if h < 0 {
		h += 360 * 255
	}
	return h / 255, s, v
}

func hsv2rgb(h, s, v int) (r, g, b int) {
	if s == 0 && h == 0 {
		return v, v, v
	}
	if h >= 360 {
		h = 0
	}
	i := h / 60
	h = h * 255 / 60
	f := h % 255
	p := v * (255 - s) / 255
	q := v * (255 - s*f/255) / 255
	t := v * (255 - s*(255-f)/255) / 255
	switch i {
	case 0:
		return v, t, p
	case 1:
		return q, v, p
	case 2:
		return p, v, t
	case 3:
		return p, q, v
	case 4:
		return t, p, v
	}
	return v, p, q
}

// drawText draws lines of text in r, placed by anchor.
func (w *Widget) drawText(dst *draw.Image, r draw.Rectangle, text string, anchor int, col uint32) {
	f := w.t.font
	lines := strings.Split(text, "\n")
	tw := 0
	for _, l := range lines {
		tw = max(tw, f.Width(l))
	}
	th := len(lines) * f.Height
	p := place(r, draw.Pt(tw, th), anchor)
	src := w.col(col)
	for _, l := range lines {
		_, _ = dst.String(p, src, draw.ZP, f, l)
		p.Y += f.Height
	}
}

// textsize is the size of lines of text.
func (t *Toplevel) textsize(text string) draw.Point {
	lines := strings.Split(text, "\n")
	tw := 0
	for _, l := range lines {
		tw = max(tw, t.font.Width(l))
	}
	return draw.Pt(tw, len(lines)*t.font.Height)
}

// place returns where something of size sz goes in r by anchor.
func place(r draw.Rectangle, sz draw.Point, anchor int) draw.Point {
	p := r.Min
	dx, dy := r.Dx()-sz.X, r.Dy()-sz.Y
	switch {
	case anchor&anW != 0:
	case anchor&anE != 0:
		p.X += dx
	default:
		p.X += dx / 2
	}
	switch {
	case anchor&anN != 0:
	case anchor&anS != 0:
		p.Y += dy
	default:
		p.Y += dy / 2
	}
	return p
}

// frame is a container with nothing of its own (frame(9)).
type frame struct{}

func (*frame) option(w *Widget, name, val string) (bool, error) { return false, nil }
func (*frame) get(w *Widget, name string) (string, bool)        { return "", false }
func (*frame) natural(w *Widget) draw.Point                     { return draw.Point{} }
func (*frame) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
}
func (*frame) cmd(w *Widget, args []string) (string, error) {
	switch args[0] {
	case "map", "unmap", "suspend":
		return "", nil
	}
	return "", tkerr(errBadcm, args[0])
}

// newImpl returns the implementation of a widget kind.
func newImpl(kind string) impl {
	switch kind {
	case "frame":
		return &frame{}
	case "label", "button", "checkbutton", "radiobutton", "menubutton":
		return &label{}
	case "entry":
		return &entry{}
	case "text":
		return newText()
	case "listbox":
		return &listbox{}
	case "scrollbar":
		return &scrollbar{}
	case "canvas":
		return &canvas{}
	case "menu":
		return &menu{}
	}
	return &frame{}
}
