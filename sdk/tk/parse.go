package tk

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// The errors of libtk (xdata.c). Each begins with '!', which is how
// callers of Cmd tell an error from a value.
const (
	errNomem    = "!out of memory"
	errBadop    = "!bad option"
	errOparg    = "!arg requires option"
	errBadvl    = "!bad value"
	errBadwp    = "!bad window path"
	errWpack    = "!window is already packed"
	errNotop    = "!no toplevel"
	errDupli    = "!window path already exists"
	errNotpk    = "!window not packed"
	errBadcm    = "!bad command"
	errIstop    = "!can't pack top level"
	errBadbm    = "!failed to load bitmap"
	errBadft    = "!failed to open font"
	errBadit    = "!bad item type"
	errBadtg    = "!bad/no matching tag"
	errFewpt    = "!wrong number of points"
	errBadsq    = "!bad event sequence"
	errBadix    = "!bad index"
	errNotwm    = "!not a window"
	errBadvr    = "!variable does not exist"
	errNotvt    = "!variable is wrong type"
	errMovfw    = "!too many events buffered"
	errSyntx    = "!bad [] or {} syntax"
	errRecur    = "!cannot pack recursively"
	errDepth    = "!execution stack too big"
	errNomaster = "!no master given"
	errNotgrid  = "!not a grid"
)

// tkError is a libtk error with the item it is about (libtk's errx).
type tkError struct{ msg, item string }

func (e *tkError) Error() string {
	if e.item != "" {
		return e.msg + " " + e.item
	}
	return e.msg
}

func tkerr(msg string, item ...string) error {
	return &tkError{msg, strings.Join(item, " ")}
}

// match returns the index just past the bracket that closes the one
// before s[i], skipping backslashed characters, or -1 (tkmatch).
func match(s string, i int, inc, dec byte) int {
	depth, esc := 1, false
	for ; i < len(s); i++ {
		c := s[i]
		if !esc {
			if c == inc {
				depth++
			}
			if c == dec {
				depth--
			}
			if depth == 0 {
				return i + 1
			}
		}
		esc = c == '\\' && !esc
	}
	return -1
}

// splitCmds splits a script into commands at ';' outside brackets and
// braces. A quote outside them makes the rest one command (tkexec).
func splitCmds(s string) ([]string, error) {
	var cmds []string
	start := 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '[':
			if i = match(s, i+1, '[', ']'); i < 0 {
				return nil, tkerr(errSyntx)
			}
		case '{':
			if i = match(s, i+1, '{', '}'); i < 0 {
				return nil, tkerr(errSyntx)
			}
		case ';':
			cmds = append(cmds, s[start:i])
			i++
			start = i
		case '\'':
			return append(cmds, s[start:]), nil
		default:
			i++
		}
	}
	return append(cmds, s[start:]), nil
}

func isspace(c byte) bool { return c == ' ' || c == '\t' }

func skipspace(s string) string {
	for len(s) > 0 && isspace(s[0]) {
		s = s[1:]
	}
	return s
}

// word takes the next word of s (tkword): a braced word, whose
// backslashed braces and backslashes lose the backslash; a bracketed
// command, replaced by its value; a quote and the rest of s; or
// characters up to white space. ok is false at the end of s.
func (t *Toplevel) word(s string) (w, rest string, ok bool, err error) {
	s = skipspace(s)
	if s == "" {
		return "", "", false, nil
	}
	switch s[0] {
	case '{', '[':
		open, close := s[0], byte('}')
		if open == '[' {
			close = ']'
		}
		var b strings.Builder
		lev := 1
		i := 1
		for ; i < len(s); i++ {
			c := s[i]
			if c == '\\' && i+1 < len(s) && (s[i+1] == open || s[i+1] == close || s[i+1] == '\\') {
				i++
				c = s[i]
			} else if c == close {
				lev--
				if lev == 0 {
					break
				}
			} else if c == open {
				lev++
			}
			b.WriteByte(c)
		}
		if lev != 0 {
			return "", "", false, tkerr(errSyntx)
		}
		rest = s[i+1:]
		if open == '[' {
			v, err := t.exec(b.String())
			if err != nil {
				return "", "", false, err
			}
			return v, rest, true, nil
		}
		return b.String(), rest, true, nil
	case '\'':
		return s[1:], "", true, nil
	}
	i := 0
	for i < len(s) && !isspace(s[i]) {
		i++
	}
	return s[:i], s[i:], true, nil
}

// words splits s into all its words.
func (t *Toplevel) words(s string) ([]string, error) {
	var ws []string
	for {
		w, rest, ok, err := t.word(s)
		if err != nil {
			return nil, err
		}
		if !ok {
			return ws, nil
		}
		ws = append(ws, w)
		s = rest
	}
}

// Quote returns s quoted so that Cmd takes it as one word.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '{' || c == '}' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('}')
	return b.String()
}

// Tkdpi is libtk's idea of the screen's resolution, for distances in
// centimetres, millimetres, inches and points.
const Tkdpi = 100

// dist parses a distance (tkfrac): a number with an optional fraction
// and unit, c, m, i, p, w (the width of the font's '0') or h (its
// height). w may be nil when those units make no sense.
func (t *Toplevel) dist(s string, w *Widget) (int, error) {
	s = strings.TrimSpace(s)
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	j := i
	for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
		j++
	}
	if j == i {
		return 0, tkerr(errBadvl, s)
	}
	f, err := strconv.ParseFloat(s[:j], 64)
	if err != nil {
		return 0, tkerr(errBadvl, s)
	}
	unit := s[j:]
	switch unit {
	case "":
	case "c":
		f *= Tkdpi / 2.54
	case "m":
		f *= Tkdpi / 25.4
	case "i":
		f *= Tkdpi
	case "p":
		f = f * Tkdpi / 72
	case "w":
		f *= float64(t.charwidth())
	case "h":
		f *= float64(t.font.Height)
	default:
		return 0, tkerr(errBadvl, s)
	}
	if f < 0 {
		return int(f - 0.5), nil
	}
	return int(f + 0.5), nil
}

func (t *Toplevel) charwidth() int {
	if w := t.font.Width("0"); w > 0 {
		return w
	}
	return 1
}

// nndist is a distance that must not be negative.
func (t *Toplevel) nndist(s string) (int, error) {
	d, err := t.dist(s, nil)
	if err == nil && d < 0 {
		err = tkerr(errBadvl, s)
	}
	return d, err
}

// The colours libtk knows by name, as 0xRRGGBBAA.
var colornames = map[string]uint32{
	"black": 0x000000FF, "blue": 0x0000CCFF, "darkblue": 0x5D00BBFF, "red": 0xFF0000FF,
	"yellow": 0xFFFF00FF, "green": 0x008000FF, "white": 0xFFFFFFFF, "orange": 0xFFAA00FF,
	"aqua": 0x00FFFFFF, "fuchsia": 0xFF00FFFF, "gray": 0x808080FF, "grey": 0x808080FF,
	"lime": 0x00FF00FF, "maroon": 0x800000FF, "navy": 0x000080FF, "olive": 0x808000FF,
	"purple": 0x800080FF, "silver": 0xC0C0C0FF, "teal": 0x008080FF, "transparent": 0x00000000,
}

// DNotacolor is what Color returns for a string that is not a colour.
const DNotacolor = 0xFFFFFF00

func hexval(s string) (uint32, bool) {
	v, err := strconv.ParseUint(s, 16, 32)
	return uint32(v), err == nil
}

// parseColor parses a colour (tkparsecolor): a name, #rgb, #rrggbb or
// #rrggbbaa, optionally followed by *#aa or *f, an alpha to apply.
func parseColor(s string) (uint32, error) {
	alpha := uint32(0xFF)
	if i := strings.IndexByte(s, '*'); i >= 0 {
		a := s[i+1:]
		s = s[:i]
		if strings.HasPrefix(a, "#") {
			v, ok := hexval(a[1:])
			if !ok || len(a) != 3 {
				return 0, tkerr(errBadvl, a)
			}
			alpha = v
		} else {
			f, err := strconv.ParseFloat(a, 64)
			if err != nil || f < 0 || f > 1 {
				return 0, tkerr(errBadvl, a)
			}
			alpha = uint32(f*255 + 0.5)
		}
	}
	var rgba uint32
	if strings.HasPrefix(s, "#") {
		v, ok := hexval(s[1:])
		if !ok {
			return 0, tkerr(errBadvl, s)
		}
		switch len(s) {
		case 4:
			r, g, b := v>>8&0xF, v>>4&0xF, v&0xF
			rgba = (r*0x11)<<24 | (g*0x11)<<16 | (b*0x11)<<8 | 0xFF
		case 7:
			rgba = v<<8 | 0xFF
		case 9:
			rgba = v
		default:
			return 0, tkerr(errBadvl, s)
		}
	} else {
		v, ok := colornames[s]
		if !ok {
			return 0, tkerr(errBadvl, s)
		}
		rgba = v
	}
	if alpha != 0xFF {
		a := (rgba & 0xFF) * alpha / 0xFF
		rgba = rgba&^0xFF | a
	}
	return rgba, nil
}

// Color returns the colour s names as 0xRRGGBBAA, or DNotacolor.
func Color(s string) uint32 {
	c, err := parseColor(s)
	if err != nil {
		return DNotacolor
	}
	return c
}

// premultiply returns rgba with its colour scaled by its alpha, as draw
// images hold it.
func premultiply(rgba uint32) uint32 {
	a := rgba & 0xFF
	if a == 0xFF {
		return rgba
	}
	r, g, b := rgba>>24, rgba>>16&0xFF, rgba>>8&0xFF
	return (r*a/255)<<24 | (g*a/255)<<16 | (b*a/255)<<8 | a
}

func colorString(c uint32) string {
	if c&0xFF == 0xFF {
		return "#" + hex6(c>>8)
	}
	return "#" + hex6(c>>8) + hex2(c&0xFF)
}

func hex6(v uint32) string {
	s := strconv.FormatUint(uint64(v), 16)
	return strings.Repeat("0", 6-len(s)) + s
}

func hex2(v uint32) string {
	s := strconv.FormatUint(uint64(v), 16)
	return strings.Repeat("0", 2-len(s)) + s
}

func parseBool(s string) (bool, error) {
	switch s {
	case "0", "no", "off", "false":
		return false, nil
	case "1", "yes", "on", "true":
		return true, nil
	}
	return false, tkerr(errBadvl, s)
}

// Anchors and sides.
const (
	anCenter = 0
	anN      = 1 << iota
	anS
	anE
	anW
)

var anchors = map[string]int{
	"center": anCenter, "c": anCenter, "n": anN, "ne": anN | anE, "e": anE, "se": anS | anE,
	"s": anS, "sw": anS | anW, "w": anW, "nw": anN | anW,
}

func anchorName(a int) string {
	for _, n := range []string{"center", "n", "ne", "e", "se", "s", "sw", "w", "nw"} {
		if anchors[n] == a {
			return n
		}
	}
	return "center"
}

// Reliefs.
const (
	reliefFlat = iota
	reliefRaised
	reliefSunken
	reliefGroove
	reliefRidge
)

var reliefs = []string{"flat", "raised", "sunken", "groove", "ridge"}

func lookup(tab []string, s string) (int, error) {
	for i, n := range tab {
		if n == s {
			return i, nil
		}
	}
	return 0, tkerr(errBadvl, s)
}

// runelen is the number of runes in s.
func runelen(s string) int { return utf8.RuneCountInString(s) }

func itoa(i int) string { return strconv.Itoa(i) }

func atoi(s string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, tkerr(errBadvl, s)
	}
	return v, nil
}
