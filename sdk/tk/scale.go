package tk

import (
	"math"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// scale (scale(9)): a slider over a range of values, with the value
// shown above it. Moving the slider, or pressing button 1 in the
// trough beside it, runs its command with the new value appended.
// Values are whole numbers here: -resolution is rounded to one.
type scale struct {
	from, to   int
	value      int
	res        int
	bigi       int
	bigiset    bool
	length     int
	sl         int // the slider's length
	vertical   bool
	showvalue  bool
	jump       bool
	label      string
	command    string
	tick       int
	sliderrel  int
	dragging   bool
	dragpix    int // where the drag started, along the scale
	dragbase   int // and the value then
	pixmin     int // the trough's extent along the scale, set by paint
	pixmax     int
	pixpos     int // the slider's start
	digitwidth int
}

// libtk's scale.c sizes.
const (
	scalewidth  = 18
	scalepad    = 2
	scalebw     = 1
	scaleslider = 16
	scalelen    = 80
)

func (s *scale) defaults(w *Widget) {
	s.res = 1
	s.to = 100
	s.length = scalelen
	s.vertical = true
	s.sliderrel = reliefRaised
	s.sl = scaleslider
	s.showvalue = true
	w.takefocus = true
}

func (s *scale) option(w *Widget, name, val string) (bool, error) {
	var err error
	num := func(p *int) {
		var f float64
		if f, err = strconv.ParseFloat(val, 64); err != nil {
			err = tkerr(errBadvl, val)
			return
		}
		*p = int(math.Round(f))
	}
	switch name {
	case "from":
		num(&s.from)
	case "to":
		num(&s.to)
	case "resolution":
		num(&s.res)
	case "bigincrement":
		num(&s.bigi)
		s.bigiset = s.bigi != 0
	case "tickinterval", "tick":
		num(&s.tick)
	case "length":
		s.length, err = w.t.nndist(val)
	case "sliderlength":
		s.sl, err = w.t.nndist(val)
	case "digits":
	case "showrange":
	case "showvalue":
		s.showvalue, err = parseBool(val)
	case "jump":
		s.jump, err = parseBool(val)
	case "sliderrelief":
		s.sliderrel, err = lookup(reliefs, val)
	case "label":
		s.label = val
	case "command":
		s.command = val
	case "orient":
		switch val {
		case "vertical":
			s.vertical = true
		case "horizontal":
			s.vertical = false
		default:
			err = tkerr(errBadvl, val)
		}
	default:
		return false, nil
	}
	if err == nil {
		s.check()
	}
	return true, err
}

func (s *scale) get(w *Widget, name string) (string, bool) {
	switch name {
	case "from":
		return itoa(s.from), true
	case "to":
		return itoa(s.to), true
	case "resolution":
		return itoa(s.res), true
	case "bigincrement":
		return itoa(s.increment()), true
	case "length":
		return itoa(s.length), true
	case "sliderlength":
		return itoa(s.sl), true
	case "showvalue":
		return boolString(s.showvalue), true
	case "jump":
		return boolString(s.jump), true
	case "label":
		return s.label, true
	case "command":
		return s.command, true
	case "orient":
		if s.vertical {
			return "vertical", true
		}
		return "horizontal", true
	}
	return "", false
}

func (s *scale) increment() int {
	if s.bigiset {
		return s.bigi
	}
	return (s.to - s.from) / 10
}

// check keeps the value a multiple of the resolution and in range
// (tkscalecheckvalue); it reports whether the value was at a limit.
func (s *scale) check() bool {
	v := s.value
	if s.res > 0 {
		v = v / s.res * s.res
	}
	lo, hi := min(s.from, s.to), max(s.from, s.to)
	limit := true
	switch {
	case v < lo:
		v = lo
	case v > hi:
		v = hi
	default:
		limit = false
	}
	s.value = v
	return limit
}

func (s *scale) natural(w *Widget) draw.Point {
	digits := 0
	if s.showvalue {
		digits = max(len(itoa(s.from)), len(itoa(s.to))) * w.charwidth()
	}
	s.digitwidth = digits
	lp := draw.Point{}
	if s.label != "" {
		lp = w.textsize(s.label)
	}
	fh := w.font.Height
	if s.vertical {
		ww := scalewidth + 2*scalepad + 2*scalebw
		if lp.X > 0 {
			ww += lp.X + scalepad
		}
		if s.showvalue {
			ww += digits + scalepad
		}
		return draw.Pt(ww, s.length+2*scalebw+2*scalepad)
	}
	h := scalewidth + 2*scalepad + 2*scalebw
	if s.label != "" {
		h += fh + scalepad
	}
	if s.showvalue {
		h += fh + scalepad
	}
	return draw.Pt(max(lp.X, s.length+scalebw+2*scalepad), h)
}

// trough returns the trough's rectangle in the widget's inside r.
func (s *scale) trough(w *Widget, r draw.Rectangle) draw.Rectangle {
	r = r.Inset(scalepad)
	fh := w.font.Height
	if s.vertical {
		if s.label != "" {
			r.Min.X += w.textsize(s.label).X + scalepad
		}
		if s.showvalue {
			r.Min.X += s.digitwidth + scalepad
		}
		r.Max.X = min(r.Max.X, r.Min.X+scalewidth+2*scalebw)
		return r
	}
	if s.label != "" {
		r.Min.Y += fh + scalepad
	}
	if s.showvalue {
		r.Min.Y += fh + scalepad
	}
	r.Max.Y = min(r.Max.Y, r.Min.Y+scalewidth+2*scalebw)
	return r
}

// along is a point's offset along the scale.
func (s *scale) along(p draw.Point) int {
	if s.vertical {
		return p.Y
	}
	return p.X
}

func (s *scale) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	fg := w.col(w.fg())
	bg := w.colors[cBg]
	if s.label != "" {
		w.drawText(dst, r.Inset(scalepad), s.label, anN|anW, w.fg())
	}
	tr := s.trough(w, r)
	bevel(dst, w.t, tr, scalebw, reliefSunken, bg)
	sr := tr.Inset(scalebw)
	sl := s.sl + 2*scalebw
	span := s.along(sr.Max) - s.along(sr.Min) - sl
	if span <= 0 {
		span = 1
	}
	s.pixmin, s.pixmax = s.along(sr.Min), s.along(sr.Max)
	off := 0
	if n := s.to - s.from; n != 0 {
		off = (s.value - s.from) * span / n
	}
	s.pixpos = s.pixmin + off
	var slider draw.Rectangle
	if s.vertical {
		slider = draw.Rect(sr.Min.X, s.pixpos, sr.Max.X, s.pixpos+sl)
	} else {
		slider = draw.Rect(s.pixpos, sr.Min.Y, s.pixpos+sl, sr.Max.Y)
	}
	rel := s.sliderrel
	if s.dragging {
		rel = reliefSunken
	}
	dst.Draw(slider, w.col(w.bg()), nil, draw.ZP)
	bevel(dst, w.t, slider, scalebw, rel, bg)
	// The groove across the middle of the slider, in the other relief.
	groove := reliefSunken
	if rel == reliefSunken {
		groove = reliefRaised
	}
	c := slider.Min.Add(slider.Max).Div(2)
	if s.vertical {
		bevel(dst, w.t, draw.Rect(slider.Min.X+1, c.Y-1, slider.Max.X-1, c.Y+1), 1, groove, bg)
	} else {
		bevel(dst, w.t, draw.Rect(c.X-1, slider.Min.Y+1, c.X+1, slider.Max.Y-1), 1, groove, bg)
	}
	if !s.showvalue {
		return
	}
	v := itoa(s.value)
	vw := w.font.Width(v)
	if s.vertical {
		p := draw.Pt(tr.Min.X-scalepad-vw, c.Y-w.font.Height/2)
		_, _ = dst.String(p, fg, draw.ZP, w.font, v)
		return
	}
	p := draw.Pt(c.X-vw/2, tr.Min.Y-scalepad-w.font.Height)
	p.X = max(s.pixmin, min(p.X, s.pixmax-vw))
	_, _ = dst.String(p, fg, draw.ZP, w.font, v)
}

// run runs the command with the value.
func (s *scale) run(w *Widget) {
	if s.command != "" {
		_, _ = w.t.exec(s.command + " " + itoa(s.value))
	}
}

func (s *scale) event(w *Widget, e *event) {
	switch e.kind {
	case evPress:
		if e.button != 1 {
			return
		}
		a := s.along(e.p)
		switch {
		case a < s.pixpos:
			s.step(w, -1)
		case a >= s.pixpos+s.sl+2*scalebw:
			s.step(w, 1)
		default:
			s.dragging, s.dragpix, s.dragbase = true, a, s.value
		}
	case evMotion:
		if !s.dragging || e.held&1 == 0 {
			return
		}
		s.drag(w, s.along(e.p))
	case evRelease:
		if e.button != 1 || !s.dragging {
			return
		}
		s.drag(w, s.along(e.p))
		s.dragging = false
		if s.jump {
			s.run(w)
		}
	default:
		return
	}
	w.t.dirty = true
}

// step moves the value by the big increment toward one end
// (stepscale).
func (s *scale) step(w *Widget, dir int) {
	sg := 1
	if s.to < s.from {
		sg = -1
	}
	s.value += dir * sg * max(1, s.increment())
	s.check()
	s.run(w)
}

// drag follows the pointer at pix along the scale (tkscaledrag).
func (s *scale) drag(w *Widget, pix int) {
	v := s.dragbase
	if span := s.pixmax - s.pixmin - s.sl - 2*scalebw; span > 0 {
		v = s.dragbase + (pix-s.dragpix)*(s.to-s.from)/span
	}
	if s.res > 0 {
		a := s.res / 2
		if v < 0 {
			a = -a
		}
		v = (v + a) / s.res * s.res
	}
	old := s.value
	s.value = v
	s.check()
	if !s.jump && s.value != old {
		s.run(w)
	}
}

func (s *scale) cmd(w *Widget, args []string) (string, error) {
	switch args[0] {
	case "get":
		return itoa(s.value), nil
	case "set":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return "", tkerr(errBadvl, args[1])
		}
		s.value = int(math.Round(f))
		s.check()
		w.t.dirty = true
		return "", nil
	case "identify":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		x, _ := atoi(args[1])
		y, _ := atoi(args[2])
		a := s.along(w.abs().Add(draw.Pt(x, y)))
		switch {
		case a < s.pixmin || a >= s.pixmax:
			return "", nil
		case a < s.pixpos:
			return "trough1", nil
		case a >= s.pixpos+s.sl+2*scalebw:
			return "trough2", nil
		}
		return "slider", nil
	}
	return "", tkerr(errBadcm, args[0])
}

// panel (panel(9)) shows an image given to it with tk->putimage. Its
// size, unless set, is the image's; "dirty" says the image has changed.
type panel struct {
	img, mask *draw.Image
	anchor    int
}

func (p *panel) defaults(w *Widget) { p.anchor = anCenter }

func (p *panel) option(w *Widget, name, val string) (bool, error) {
	switch name {
	case "anchor":
		a, ok := anchors[val]
		if !ok {
			return true, tkerr(errBadvl, val)
		}
		p.anchor = a
		return true, nil
	}
	return false, nil
}

func (p *panel) get(w *Widget, name string) (string, bool) {
	if name == "anchor" {
		return anchorName(p.anchor), true
	}
	return "", false
}

func (p *panel) natural(w *Widget) draw.Point {
	if p.img == nil {
		return draw.Point{}
	}
	return p.img.R.Size()
}

func (p *panel) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	if p.img == nil {
		return
	}
	at := place(r, p.img.R.Size(), p.anchor)
	dst.Draw(draw.Rectangle{Min: at, Max: at.Add(p.img.R.Size())}, p.img, p.mask, p.img.R.Min)
}

func (p *panel) cmd(w *Widget, args []string) (string, error) {
	switch args[0] {
	case "dirty":
		w.t.dirty = true
		return "", nil
	case "origin":
		return "0 0", nil
	}
	return "", tkerr(errBadcm, args[0])
}
