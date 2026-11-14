// Package tk is Inferno's Tk for Go guests: a toolkit that draws
// through sdk/draw and is driven by the same command strings as Limbo's
// Tk module (tk(2), and the widget pages of section 9).
//
//	t := tk.New(display, "")
//	cmd := make(chan string, 8)
//	tk.NameChan(t, cmd, "cmd")
//	tk.Cmd(t, "button .b -text Hello -command {send cmd hi}; pack .b; update")
//
// As in Inferno the toolkit runs inside the program: Pointer and
// Keyboard deliver input, Cmd changes the widgets, and "update" draws
// them on the toplevel's image, which the window manager supplies (see
// sdk/tkclient). Commands bound to widgets send strings on channels
// named with NameChan; tk sends without blocking, so those channels
// should be buffered.
//
// The widgets are frame, label, button, checkbutton, radiobutton,
// menubutton, entry, text, listbox, scrollbar, canvas and menu, with the
// packer. The comment on each widget's type says what it leaves out.
package tk

import (
	"sort"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// Flags for Rect.
const (
	Border   = 1 << iota // include the border
	Required             // the requested size, not the actual one
	Local                // relative to the toplevel, not the screen
)

// Toplevel is a tree of widgets with "." at its root (Tk->Toplevel).
type Toplevel struct {
	Display *draw.Display
	// Wreq carries requests for the window manager: "!reshape . -1 x0 y0
	// x1 y1" when the toplevel wants a new size, "raise ." and "lower .".
	Wreq chan string
	// Image is what "." is drawn on, set by PutImage.
	Image *draw.Image
	// Screenr is the screen's rectangle, for placing windows.
	Screenr draw.Rectangle
	// Ctxt is for the program's window manager connection; tk does not
	// use it (Tk->Toplevel.ctxt).
	Ctxt any

	root    *Widget
	widgets map[string]*Widget
	chans   map[string]chan<- string
	vars    map[string]string
	font    *draw.Font
	colors  map[uint32]*draw.Image
	buf     *draw.Image // where updates are drawn before going to Image

	focus   *Widget // keyboard focus
	focused bool    // the window has the keyboard
	grab    *Widget
	under   *Widget // the widget the pointer is in
	press   *Widget // the widget that took the press while buttons are down
	buttons int
	last    draw.Pointer
	click   struct {
		w    *Widget
		b    int
		msec int
	}
	menu *Widget // the posted menu

	dirty    bool
	gen      int
	inlayout bool
	reqsize  draw.Point // the size last asked of the window manager
	depth    int
	lasterr  string
	pending  []string // commands to run after drawing (scroll commands)
}

// New makes a toplevel (tk->toplevel) on display d. arg holds options
// for ".", such as "-borderwidth 1 -relief raised", and -x and -y, its
// position on the screen.
func New(d *draw.Display, arg string) *Toplevel {
	t := &Toplevel{Display: d, Wreq: make(chan string, 64), widgets: map[string]*Widget{},
		chans: map[string]chan<- string{}, vars: map[string]string{}, colors: map[uint32]*draw.Image{}}
	t.font = d.DefaultFont
	if t.font == nil {
		t.font, _ = d.GetDefaultFont()
	}
	if d.Image != nil {
		t.Screenr = d.Image.R
	}
	t.root = t.newWidget(".", "frame", nil, &frame{})
	t.root.mapped = true
	if arg != "" {
		ws, err := t.words(arg)
		if err == nil {
			var opts []string
			for i := 0; i+1 < len(ws); i += 2 {
				switch ws[i] {
				case "-x":
					t.root.x, _ = t.dist(ws[i+1], nil)
				case "-y":
					t.root.y, _ = t.dist(ws[i+1], nil)
				default:
					opts = append(opts, ws[i], ws[i+1])
				}
			}
			_ = t.root.configure(opts)
		}
	}
	return t
}

// Cmd runs the commands in arg (tk->cmd) and returns the value of the
// last, or an error string beginning with '!'.
func Cmd(t *Toplevel, arg string) string { return t.Cmd(arg) }

// Cmd is tk->cmd.
func (t *Toplevel) Cmd(arg string) string {
	t.depth = 0
	v, err := t.exec(arg)
	if err != nil {
		return err.Error()
	}
	return v
}

// exec runs a script.
func (t *Toplevel) exec(arg string) (string, error) {
	t.depth++
	defer func() { t.depth-- }()
	if t.depth > 128 {
		return "", tkerr(errDepth)
	}
	cmds, err := splitCmds(arg)
	if err != nil {
		return "", err
	}
	var v string
	for _, c := range cmds {
		if v, err = t.single(c); err != nil {
			t.lasterr = strings.TrimSpace(c) + ": " + err.Error()
			return "", err
		}
	}
	return v, nil
}

type cmdfn func(t *Toplevel, args []string) (string, error)

var cmdtab map[string]cmdfn

func init() {
	cmdtab = map[string]cmdfn{
		"bind": cmdBind, "button": creator("button"), "canvas": creator("canvas"),
		"checkbutton": creator("checkbutton"), "cursor": cmdCursor, "destroy": cmdDestroy,
		"entry": creator("entry"), "focus": cmdFocus, "frame": creator("frame"), "grab": cmdGrab,
		"image": cmdImage, "label": creator("label"), "listbox": creator("listbox"),
		"lower": cmdLower, "menu": creator("menu"), "menubutton": creator("menubutton"),
		"pack": cmdPack, "puts": cmdPuts, "radiobutton": creator("radiobutton"), "raise": cmdRaise,
		"scrollbar": creator("scrollbar"), "see": cmdSee, "text": creator("text"),
		"update": cmdUpdate, "variable": cmdVariable, "winfo": cmdWinfo,
	}
}

// single runs one command.
func (t *Toplevel) single(c string) (string, error) {
	name, rest, ok, err := t.word(c)
	if err != nil || !ok {
		return "", err
	}
	if name == "send" {
		return t.send(rest)
	}
	args, err := t.words(rest)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(name, ".") {
		w := t.widgets[name]
		if w == nil {
			return "", tkerr(errBadwp, name)
		}
		return w.command(args)
	}
	fn := cmdtab[name]
	if fn == nil {
		return "", tkerr(errBadcm, name)
	}
	return fn(t, args)
}

// send sends the rest of the command on a named channel (tksend).
func (t *Toplevel) send(rest string) (string, error) {
	name, rest, ok, err := t.word(rest)
	if err != nil {
		return "", err
	}
	c := t.chans[name]
	if !ok || c == nil {
		return "", tkerr(errBadvr, name)
	}
	v := skipspace(rest)
	select {
	case c <- v:
	default:
		go func() { c <- v }()
	}
	return "", nil
}

// NameChan names channel c n for "send n ..." (tk->namechan).
func NameChan(t *Toplevel, c chan<- string, n string) string { return t.NameChan(c, n) }

func (t *Toplevel) NameChan(c chan<- string, n string) string {
	if n == "" || c == nil {
		return errBadvl
	}
	t.chans[n] = c
	return ""
}

// Pointer delivers a pointer event (tk->pointer) and draws what changed.
func Pointer(t *Toplevel, p draw.Pointer) { t.Pointer(p) }

// Keyboard delivers a key (tk->keyboard) and draws what changed.
func Keyboard(t *Toplevel, key int) { t.Keyboard(key) }

// PutImage gives a widget its image (tk->putimage). For "." it is the
// image the toplevel is drawn on; name may be followed by a request id.
func PutImage(t *Toplevel, name string, i, m *draw.Image) string { return t.PutImage(name, i, m) }

func (t *Toplevel) PutImage(name string, i, m *draw.Image) string {
	if f := strings.Fields(name); len(f) > 0 {
		name = f[0]
	}
	if name != "." {
		if t.widgets[name] == nil {
			return errBadwp
		}
		return errNotwm
	}
	t.Image = i
	if t.buf != nil {
		t.buf.Free()
		t.buf = nil
	}
	if i != nil {
		t.root.x, t.root.y = i.R.Min.X, i.R.Min.Y
		t.root.w, t.root.h = i.R.Dx(), i.R.Dy()
		t.root.fixed = true
	}
	t.dirty = true
	t.update()
	return ""
}

// GetImage returns a widget's image (tk->getimage).
func GetImage(t *Toplevel, name string) (*draw.Image, *draw.Image, string) {
	if name != "." {
		if t.widgets[name] == nil {
			return nil, nil, errBadwp
		}
		return nil, nil, errNotwm
	}
	return t.Image, nil, ""
}

// Rect returns a widget's rectangle on the screen (tk->rect).
func Rect(t *Toplevel, name string, flags int) draw.Rectangle { return t.Rect(name, flags) }

func (t *Toplevel) Rect(name string, flags int) draw.Rectangle {
	w := t.widgets[name]
	if w == nil {
		return draw.Rectangle{}
	}
	t.layout()
	var p draw.Point
	if w != t.root || flags&Local == 0 {
		p = w.abs()
	}
	if flags&Local != 0 {
		p = p.Sub(t.root.abs())
	}
	sz := draw.Pt(w.w, w.h)
	if flags&Required != 0 {
		sz = w.req()
	}
	r := draw.Rectangle{Min: p, Max: p.Add(sz)}
	if flags&Border == 0 {
		r = r.Inset(w.bd)
	}
	return r
}

// colour returns an image of colour c, cached.
func (t *Toplevel) colour(c uint32) *draw.Image {
	if i := t.colors[c]; i != nil {
		return i
	}
	i, err := t.Display.Color(premultiply(c))
	if err != nil {
		return t.Display.Black
	}
	t.colors[c] = i
	return i
}

// update lays out and draws the widgets if anything has changed.
func (t *Toplevel) update() {
	for range 3 {
		t.update1()
		p := t.pending
		t.pending = nil
		for _, c := range p {
			_, _ = t.exec(c)
		}
		if !t.dirty {
			return
		}
	}
}

// later runs c after the next drawing.
func (t *Toplevel) later(c string) { t.pending = append(t.pending, c) }

func (t *Toplevel) update1() {
	t.layout()
	if !t.dirty || t.Image == nil {
		return
	}
	t.dirty = false
	r := t.Image.R
	if t.buf == nil || t.buf.R != r {
		if t.buf != nil {
			t.buf.Free()
		}
		var err error
		t.buf, err = t.Display.AllocImage(r, t.Image.Chan, false, draw.White)
		if err != nil {
			t.buf = nil
			return
		}
	}
	t.buf.Draw(r, t.colour(t.root.colors[cBg]), nil, draw.ZP)
	t.root.paintTree(t.buf, r)
	if t.menu != nil {
		t.menu.paintTree(t.buf, r)
	}
	t.Image.Draw(r, t.buf, nil, r.Min)
	_ = t.Display.Flush(true)
}

func cmdUpdate(t *Toplevel, args []string) (string, error) {
	if len(args) > 0 && args[0] == "-onscreen" {
		args = args[1:]
	}
	t.update()
	return "", nil
}

func cmdVariable(t *Toplevel, args []string) (string, error) {
	switch len(args) {
	case 0:
		return "", tkerr(errBadvr)
	case 1:
		if args[0] == "lasterror" {
			e := t.lasterr
			t.lasterr = ""
			return e, nil
		}
		v, ok := t.vars[args[0]]
		if !ok {
			if _, isch := t.chans[args[0]]; isch {
				return "", tkerr(errNotvt, args[0])
			}
			return "", nil
		}
		return v, nil
	}
	t.setvar(args[0], args[1])
	return "", nil
}

// setvar sets a variable and redraws the buttons that show it.
func (t *Toplevel) setvar(name, val string) {
	t.vars[name] = val
	t.dirty = true
}

func cmdPuts(t *Toplevel, args []string) (string, error) { return "", nil }

func cmdCursor(t *Toplevel, args []string) (string, error) { return "", nil }

func cmdImage(t *Toplevel, args []string) (string, error) {
	return "", tkerr(errBadbm, strings.Join(args, " "))
}

func cmdSee(t *Toplevel, args []string) (string, error) { return "", nil }

func cmdWinfo(t *Toplevel, args []string) (string, error) {
	if len(args) < 2 {
		return "", tkerr(errBadvl)
	}
	w := t.widgets[args[1]]
	if w == nil {
		return "", tkerr(errBadwp, args[1])
	}
	switch args[0] {
	case "class":
		return w.kind, nil
	}
	return "", tkerr(errBadvl, args[0])
}

func cmdRaise(t *Toplevel, args []string) (string, error) {
	if len(args) == 0 || args[0] == "." {
		t.Wreq <- "raise ."
	}
	return "", nil
}

func cmdLower(t *Toplevel, args []string) (string, error) {
	if len(args) == 0 || args[0] == "." {
		t.Wreq <- "lower ."
	}
	return "", nil
}

func cmdDestroy(t *Toplevel, args []string) (string, error) {
	for _, n := range args {
		w := t.widgets[n]
		if w == nil {
			return "", tkerr(errBadwp, n)
		}
		if w == t.root {
			continue
		}
		w.destroy()
	}
	return "", nil
}

// focus sets the keyboard focus (focus(9)).
func cmdFocus(t *Toplevel, args []string) (string, error) {
	if len(args) == 0 {
		if t.focus == nil {
			return "", nil
		}
		return t.focus.name, nil
	}
	if args[0] == "-global" {
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		in, err := parseBool(args[1])
		if err != nil {
			return "", err
		}
		if in != t.focused {
			t.focused = in
			ev := evFocusOut
			if in {
				ev = evFocusIn
			}
			t.root.deliver(&event{kind: ev})
			if t.focus != nil {
				t.focus.deliver(&event{kind: ev})
			}
			t.dirty = true
		}
		return "", nil
	}
	w := t.widgets[args[0]]
	if w == nil {
		return "", tkerr(errBadwp, args[0])
	}
	t.setfocus(w)
	return "", nil
}

func (t *Toplevel) setfocus(w *Widget) {
	if t.focus == w {
		return
	}
	if old := t.focus; old != nil {
		t.focus = nil
		old.deliver(&event{kind: evFocusOut})
	}
	t.focus = w
	if w != nil {
		w.deliver(&event{kind: evFocusIn})
	}
	t.dirty = true
}

func cmdGrab(t *Toplevel, args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadvl)
	}
	switch args[0] {
	case "status":
		if t.grab == nil {
			return "", nil
		}
		return t.grab.name, nil
	case "release":
		t.grab = nil
		return "", nil
	case "set", "ifunset":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		w := t.widgets[args[1]]
		if w == nil {
			return "", tkerr(errBadwp, args[1])
		}
		if args[0] == "set" || t.grab == nil {
			t.grab = w
		}
		return "", nil
	}
	return "", tkerr(errBadvl, args[0])
}

// creator returns the command that makes a widget of kind k.
func creator(k string) cmdfn {
	return func(t *Toplevel, args []string) (string, error) {
		if len(args) == 0 {
			return "", tkerr(errBadwp)
		}
		name := args[0]
		if t.widgets[name] != nil {
			return "", tkerr(errDupli, name)
		}
		parent := t.parentof(name)
		if parent == nil {
			return "", tkerr(errBadwp, name)
		}
		w := t.newWidget(name, k, parent, newImpl(k))
		if err := w.configure(args[1:]); err != nil {
			w.destroy()
			return "", err
		}
		return name, nil
	}
}

// parentof returns the widget whose child name would be.
func (t *Toplevel) parentof(name string) *Widget {
	if !strings.HasPrefix(name, ".") || name == "." || strings.HasSuffix(name, ".") {
		return nil
	}
	i := strings.LastIndexByte(name, '.')
	if i == 0 {
		return t.root
	}
	return t.widgets[name[:i]]
}

// sortedNames returns the widget names in order, for tests.
func (t *Toplevel) sortedNames() []string {
	var n []string
	for k := range t.widgets {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}
