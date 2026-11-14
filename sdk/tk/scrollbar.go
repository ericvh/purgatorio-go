package tk

import (
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// scrollbar (scrollbar(9)): arrows at each end and a slider showing
// the part in view. It runs its command with "scroll n units", "scroll
// n pages" or "moveto f" appended.
type scrollbar struct {
	vertical    bool
	command     string
	first, last float64
	dragging    bool
	dragoff     int
}

const scrollw = 12 // the bar's thickness

func (s *scrollbar) defaults(w *Widget) {
	w.bd, w.relief = 1, reliefSunken
	s.vertical = true
	s.last = 1
}

func (s *scrollbar) option(w *Widget, name, val string) (bool, error) {
	switch name {
	case "orient":
		switch val {
		case "vertical":
			s.vertical = true
		case "horizontal":
			s.vertical = false
		default:
			return true, tkerr(errBadvl, val)
		}
	case "command":
		s.command = val
	case "jump", "elementborderwidth", "activerelief":
	default:
		return false, nil
	}
	return true, nil
}

func (s *scrollbar) get(w *Widget, name string) (string, bool) {
	switch name {
	case "orient":
		if s.vertical {
			return "vertical", true
		}
		return "horizontal", true
	case "command":
		return s.command, true
	}
	return "", false
}

func (s *scrollbar) natural(w *Widget) draw.Point {
	if s.vertical {
		return draw.Pt(scrollw, 4*scrollw)
	}
	return draw.Pt(4*scrollw, scrollw)
}

// geometry returns the length along the bar, the arrow size, and the
// slider's extent along the trough.
func (s *scrollbar) geometry(w *Widget) (length, arrow, s0, s1 int) {
	r := w.inside()
	length, thick := r.Dy(), r.Dx()
	if !s.vertical {
		length, thick = r.Dx(), r.Dy()
	}
	arrow = min(thick, length/3)
	trough := max(1, length-2*arrow)
	s0 = arrow + int(s.first*float64(trough))
	s1 = arrow + int(s.last*float64(trough)+0.5)
	if s1-s0 < 4 {
		s1 = min(length-arrow, s0+4)
	}
	return
}

func (s *scrollbar) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	ln, arrow, s0, s1 := s.geometry(w)
	bg := w.colors[cBg]
	fg := w.col(w.colors[cFg])
	seg := func(a, b int) draw.Rectangle {
		if s.vertical {
			return draw.Rect(r.Min.X, r.Min.Y+a, r.Max.X, r.Min.Y+b)
		}
		return draw.Rect(r.Min.X+a, r.Min.Y, r.Min.X+b, r.Max.Y)
	}
	slider := seg(s0, s1)
	dst.Draw(slider, w.col(bg), nil, draw.ZP)
	bevel(dst, w.t, slider, 1, reliefRaised, bg)
	// Arrows: triangles pointing out.
	for k, rr := range []draw.Rectangle{seg(0, arrow), seg(ln-arrow, ln)} {
		in := rr.Inset(2)
		if in.Empty() {
			continue
		}
		c := in.Min.Add(in.Max).Div(2)
		var pts []draw.Point
		switch {
		case s.vertical && k == 0:
			pts = []draw.Point{draw.Pt(c.X, in.Min.Y), draw.Pt(in.Max.X, in.Max.Y), draw.Pt(in.Min.X, in.Max.Y)}
		case s.vertical:
			pts = []draw.Point{draw.Pt(in.Min.X, in.Min.Y), draw.Pt(in.Max.X, in.Min.Y), draw.Pt(c.X, in.Max.Y)}
		case k == 0:
			pts = []draw.Point{draw.Pt(in.Min.X, c.Y), draw.Pt(in.Max.X, in.Min.Y), draw.Pt(in.Max.X, in.Max.Y)}
		default:
			pts = []draw.Point{draw.Pt(in.Min.X, in.Min.Y), draw.Pt(in.Max.X, c.Y), draw.Pt(in.Min.X, in.Max.Y)}
		}
		dst.FillPoly(pts, 0, fg, draw.ZP)
	}
}

func (s *scrollbar) run(w *Widget, args string) {
	if s.command != "" {
		_, _ = w.t.exec(s.command + " " + args)
	}
}

// along is p's offset along the bar.
func (s *scrollbar) along(w *Widget, p draw.Point) int {
	r := w.inside()
	if s.vertical {
		return p.Y - r.Min.Y
	}
	return p.X - r.Min.X
}

func (s *scrollbar) fraction(w *Widget, off int) float64 {
	length, arrow, _, _ := s.geometry(w)
	trough := max(1, length-2*arrow)
	return max(0, min(1, float64(off-arrow)/float64(trough)))
}

func (s *scrollbar) event(w *Widget, e *event) {
	switch e.kind {
	case evPress:
		if e.button != 1 {
			return
		}
		length, arrow, s0, s1 := s.geometry(w)
		a := s.along(w, e.p)
		switch {
		case a < arrow:
			s.run(w, "scroll -1 unit")
		case a >= length-arrow:
			s.run(w, "scroll 1 unit")
		case a < s0:
			s.run(w, "scroll -1 page")
		case a >= s1:
			s.run(w, "scroll 1 page")
		default:
			s.dragging, s.dragoff = true, a-s0
		}
	case evMotion:
		if s.dragging && e.held&1 != 0 {
			s.run(w, "moveto "+strconv.FormatFloat(s.fraction(w, s.along(w, e.p)-s.dragoff), 'f', 4, 64))
		}
	case evRelease:
		s.dragging = false
	}
	w.t.dirty = true
}

func (s *scrollbar) cmd(w *Widget, args []string) (string, error) {
	switch args[0] {
	case "set":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		f0, e0 := strconv.ParseFloat(args[1], 64)
		f1, e1 := strconv.ParseFloat(args[2], 64)
		if e0 != nil || e1 != nil {
			return "", tkerr(errBadvl, args[1], args[2])
		}
		s.first, s.last = max(0, min(1, f0)), max(0, min(1, f1))
		w.t.dirty = true
		return "", nil
	case "get":
		return strconv.FormatFloat(s.first, 'f', 4, 64) + " " + strconv.FormatFloat(s.last, 'f', 4, 64), nil
	case "fraction":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		x, err := atoi(args[1])
		if err != nil {
			return "", err
		}
		y, err := atoi(args[2])
		if err != nil {
			return "", err
		}
		p := w.inside().Min.Add(draw.Pt(x, y))
		return strconv.FormatFloat(s.fraction(w, s.along(w, p)), 'f', 4, 64), nil
	case "identify":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		x, _ := atoi(args[1])
		y, _ := atoi(args[2])
		length, arrow, s0, s1 := s.geometry(w)
		a := s.along(w, w.inside().Min.Add(draw.Pt(x, y)))
		switch {
		case a < arrow:
			return "arrow1", nil
		case a >= length-arrow:
			return "arrow2", nil
		case a < s0:
			return "trough1", nil
		case a >= s1:
			return "trough2", nil
		}
		return "slider", nil
	case "activate":
		return "", nil
	}
	return "", tkerr(errBadcm, args[0])
}
