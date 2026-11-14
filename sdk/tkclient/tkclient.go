// Package tkclient puts a Tk toplevel in a window (Inferno's tkclient(2)):
// it makes the toplevel with a title bar of Tk widgets, as Inferno's
// titlebar module does, gets its image from the window manager and
// passes the window's input to Tk.
//
//	top, wmctl, err := tkclient.Toplevel(display, "", "Hello", tkclient.Appl)
//	cmd := make(chan string, 8)
//	tk.NameChan(top, cmd, "cmd")
//	tk.Cmd(top, "button .b -text Hi -command {send cmd hi}; pack .b; update")
//	tkclient.Onscreen(top, "")
//	tkclient.StartInput(top, "kbd", "ptr")
//	for {
//		e, _ := tkclient.Next(top, nil, -1)
//		...	// e is wmclient's event, already given to Tk
//		for drained := false; !drained; {
//			select {
//			case s := <-wmctl:
//				tkclient.Wmctl(top, s)
//			case s := <-cmd:
//				...
//			default:
//				drained = true
//			}
//		}
//	}
package tkclient

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/tk"
	"github.com/ericvh/purgatorio-go/sdk/wmclient"
)

// Title bar buttons, as in tkclient.m.
const (
	Resize = wmclient.Resize
	Hide   = wmclient.Hide
	Help   = wmclient.Help
	OK     = wmclient.OK
	Plain  = wmclient.Plain
	Appl   = wmclient.Appl
)

// The title bar (titlebar.b), with text where Inferno has bitmaps.
var titlecfg = []string{
	"frame .Wm_t -bg #aaaaaa -borderwidth 1",
	"label .Wm_t.title -anchor w -bg #aaaaaa -fg white",
	"button .Wm_t.e -text X -command {send wm_title exit} -takefocus 0",
	"pack .Wm_t.e -side right",
	"bind .Wm_t <Button-1> {send wm_title move %X %Y}",
	"bind .Wm_t <Double-Button-1> {send wm_title lower .}",
	"bind .Wm_t.title <Button-1> {send wm_title move %X %Y}",
	"bind .Wm_t.title <Double-Button-1> {send wm_title lower .}",
	"bind . <FocusIn> {.Wm_t configure -bg blue; .Wm_t.title configure -bg blue; update}",
	"bind . <FocusOut> {.Wm_t configure -bg #aaaaaa; .Wm_t.title configure -bg #aaaaaa; update}",
}

func win(top *tk.Toplevel) *wmclient.Window {
	w, _ := top.Ctxt.(*wmclient.Window)
	return w
}

// Toplevel makes a toplevel in a new window (tkclient->toplevel) and
// returns it with the channel on which its title bar sends requests
// for Wmctl. topconfig holds options for ".".
func Toplevel(d *draw.Display, topconfig, title string, buts int) (*tk.Toplevel, chan string, error) {
	w, err := wmclient.New(d, title, wmclient.Plain)
	if err != nil {
		return nil, nil, err
	}
	opts := ""
	if buts&Plain == 0 {
		opts = "-borderwidth 1 -relief raised "
	}
	top := tk.New(d, opts+topconfig)
	top.Ctxt = w
	top.Screenr = w.Displayr
	ctl := make(chan string, 16)
	tk.NameChan(top, ctl, "wm_title")
	if buts&Plain != 0 {
		return top, ctl, nil
	}
	for _, c := range titlecfg {
		cmd(top, c)
	}
	if buts&OK != 0 {
		cmd(top, "button .Wm_t.ok -text OK -command {send wm_title ok} -takefocus 0; pack .Wm_t.ok -side right")
	}
	if buts&Resize != 0 {
		cmd(top, "button .Wm_t.m -text + -command {send wm_title size} -takefocus 0; pack .Wm_t.m -side right")
	}
	if buts&Help != 0 {
		cmd(top, "button .Wm_t.h -text ? -command {send wm_title help} -takefocus 0; pack .Wm_t.h -side right")
	}
	cmd(top, "pack .Wm_t.title -side left")
	cmd(top, "pack .Wm_t -fill x")
	SetTitle(top, title)
	return top, ctl, nil
}

func cmd(top *tk.Toplevel, s string) string {
	e := tk.Cmd(top, s)
	if strings.HasPrefix(e, "!") {
		fmt.Printf("tkclient: tk error %s on '%s'\n", e, s)
	}
	return e
}

// SetTitle sets the title and returns the old one.
func SetTitle(top *tk.Toplevel, title string) string {
	old := tk.Cmd(top, ".Wm_t.title cget -text")
	if strings.HasPrefix(old, "!") {
		return ""
	}
	tk.Cmd(top, ".Wm_t.title configure -text "+tk.Quote(title))
	return old
}

// Onscreen puts the toplevel on the screen at its requested size; how
// is as for wmclient's Onscreen.
func Onscreen(top *tk.Toplevel, how string) error {
	if how == "" {
		how = "place"
	}
	r := tk.Rect(top, ".", tk.Border|tk.Required)
	return wmctl(top, "!reshape . -1 "+r2s(r)+" "+quote.Quote(how))
}

// StartInput starts the given inputs, ptr and kbd.
func StartInput(top *tk.Toplevel, devs ...string) error {
	for _, d := range devs {
		if err := wmctl(top, "start "+quote.Quote(d)); err != nil {
			return err
		}
	}
	// Without a window manager nothing will say the window has the
	// keyboard: it has it once it reads it.
	if w := win(top); w != nil && w.Focused {
		tk.Cmd(top, "focus -global 1; update")
	}
	return nil
}

func r2s(r draw.Rectangle) string {
	return fmt.Sprintf("%d %d %d %d", r.Min.X, r.Min.Y, r.Max.X, r.Max.Y)
}

// Wmctl carries out a request from the title bar or the window manager
// (tkclient->wmctl) and returns an error string.
func Wmctl(top *tk.Toplevel, req string) string {
	if err := wmctl(top, req); err != nil {
		return err.Error()
	}
	return ""
}

func wmctl(top *tk.Toplevel, req string) error {
	w := win(top)
	args := quote.Unquoted(req)
	if w == nil || len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "exit":
		wmclient.Exit()
	case "move":
		if len(args) != 3 {
			return errors.New("bad arg count")
		}
		return wmctl(top, "!move . -1 "+args[1]+" "+args[2])
	case "size":
		r := tk.Rect(top, ".Wm_t", tk.Border)
		return wmctl(top, fmt.Sprintf("!size . -1 %d %d", r.Dx()/2, 2*r.Dy()))
	case "ok", "help":
		return nil
	case "rect":
		_ = w.Wmctl(req)
		top.Screenr = w.Displayr
		return nil
	case "haskbdfocus":
		in := len(args) > 1 && args[1] != "0"
		_ = w.Wmctl(req)
		if in {
			tk.Cmd(top, "focus -global 1; update")
		} else {
			tk.Cmd(top, "focus -global 0; update")
		}
		return nil
	}
	if strings.HasPrefix(req, "!") && len(args) > 1 && args[1] != "." {
		return errors.New("only . has a window")
	}
	if err := w.Wmctl(req); err != nil {
		return err
	}
	if strings.HasPrefix(req, "!") && w.Image != nil {
		tk.PutImage(top, ".", w.Image, nil)
	}
	return nil
}

// Next waits for the window's next event or for one of fds to be
// readable, as wmclient's Next, and returns it after giving pointer and
// keyboard events to Tk and carrying out the window manager's control
// messages and Tk's requests for a new size.
func Next(top *tk.Toplevel, fds []int, timeout int) (wmclient.Event, error) {
	w := win(top)
	wreq(top)
	e, err := w.Next(fds, timeout)
	if err != nil {
		return e, err
	}
	switch e.Kind {
	case wmclient.Ptr:
		tk.Pointer(top, e.Ptr)
	case wmclient.Kbd:
		tk.Keyboard(top, int(e.Key))
	case wmclient.Ctl:
		args := quote.Unquoted(e.Ctl)
		if len(args) > 0 && args[0] == "haskbdfocus" {
			if len(args) > 1 && args[1] != "0" {
				tk.Cmd(top, "focus -global 1; update")
			} else {
				tk.Cmd(top, "focus -global 0; update")
			}
		}
	case wmclient.Reshape:
		if w.Image != top.Image {
			tk.PutImage(top, ".", w.Image, nil)
		}
	}
	wreq(top)
	return e, nil
}

// wreq passes on Tk's requests to the window manager.
func wreq(top *tk.Toplevel) {
	for {
		select {
		case r := <-top.Wreq:
			_ = wmctl(top, r)
		default:
			return
		}
	}
}

// snarfbuf holds the snarf when there is no snarf device.
var snarfbuf string

// snarfOpen opens /chan/snarf, where Inferno's emu binds #^, or #^
// itself, which the emu does not bind.
func snarfOpen(mode int) (int, error) {
	fd, err := sys.Open("/chan/snarf", mode)
	if err != nil {
		fd, err = sys.Open("#^/snarf", mode)
	}
	return fd, err
}

// SnarfGet returns the contents of the snarf buffer, /chan/snarf
// (tkclient->snarfget).
func SnarfGet() string {
	fd, err := snarfOpen(sys.OREAD)
	if err != nil {
		return snarfbuf
	}
	defer syscall.Close(fd)
	var b []byte
	buf := make([]byte, 8192)
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil || n <= 0 {
			break
		}
		b = append(b, buf[:n]...)
	}
	return string(b)
}

// SnarfPut puts s in the snarf buffer (tkclient->snarfput).
func SnarfPut(s string) {
	fd, err := snarfOpen(sys.OWRITE)
	if err != nil {
		snarfbuf = s
		return
	}
	defer syscall.Close(fd)
	_, _ = syscall.Write(fd, []byte(s))
}
