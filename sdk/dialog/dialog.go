// Package dialog puts up modal dialog windows (Inferno's dialog(2)):
// Prompt asks a question with a choice of buttons, GetString asks for a
// line of text. Each makes a window of its own near the parent's image
// and waits for the answer, while the caller's other windows wait.
package dialog

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/tk"
	"github.com/ericvh/purgatorio-go/sdk/tkclient"
	"github.com/ericvh/purgatorio-go/sdk/wmclient"
)

// step is how far down and right of its parent a dialog goes.
const step = 20

func localgeom(im *draw.Image) string {
	if im == nil {
		return ""
	}
	return fmt.Sprintf("-x %d -y %d", im.R.Min.X+step, im.R.Min.Y+step)
}

func cmd(t *tk.Toplevel, s string) string {
	e := tk.Cmd(t, s)
	if strings.HasPrefix(e, "!") {
		fmt.Fprintf(os.Stderr, "dialog: tk error %s on '%s'\n", e, s)
	}
	return e
}

var promptcfg = []string{
	"label .top.ico",
	"label .top.msg",
	"frame .top -relief raised -bd 1",
	"frame .bot -relief raised -bd 1",
	"pack .top.ico -side left -padx 10 -pady 10",
	"pack .top.msg -side left -expand 1 -fill both -padx 10 -pady 10",
	"pack .Wm_t .top .bot -side top -fill both",
	"focus .",
}

// Prompt shows msg with a button for each of labs and returns the
// index of the one pressed. ico names a bitmap; dflt, if not -1, is the
// button that Enter presses and that a closed window answers.
func Prompt(d *draw.Display, parent *draw.Image, ico, title, msg string, dflt int, labs []string) int {
	t, tc, err := tkclient.Toplevel(d, localgeom(parent), title, 0)
	if err != nil {
		return dflt
	}
	defer closeWindow(t)
	c := make(chan string, 4)
	tk.NameChan(t, c, "d")
	for _, s := range promptcfg {
		cmd(t, s)
	}
	cmd(t, ".top.msg configure -text "+tk.Quote(msg))
	if ico != "" {
		tk.Cmd(t, ".top.ico configure -bitmap "+tk.Quote(ico))
	}
	for i, l := range labs {
		b := ".bot.button" + strconv.Itoa(i)
		cmd(t, "button "+b+" -command {send d "+strconv.Itoa(i)+"} -text "+tk.Quote(l))
		if i == dflt {
			cmd(t, "frame .bot.default -relief sunken -bd 1")
			cmd(t, "pack .bot.default -side left -expand 1 -padx 10 -pady 8")
			cmd(t, "pack "+b+" -in .bot.default -side left -padx 10 -pady 8 -ipadx 8 -ipady 4")
		} else {
			cmd(t, "pack "+b+" -side left -expand 1 -padx 10 -pady 10 -ipadx 8 -ipady 4")
		}
	}
	if dflt >= 0 {
		cmd(t, "bind . <Key-\n> {send d "+strconv.Itoa(dflt)+"}")
	}
	if e := tk.Cmd(t, "variable lasterror"); e != "" {
		fmt.Fprintf(os.Stderr, "Dialog error: %s\n", e)
		return dflt
	}
	if tkclient.Onscreen(t, "") != nil || tkclient.StartInput(t, "kbd", "ptr") != nil {
		return dflt
	}
	cmd(t, "update")
	for {
		if _, err := tkclient.Next(t, nil, -1); err != nil {
			return dflt
		}
		for {
			select {
			case s := <-c:
				n, _ := strconv.Atoi(s)
				return n
			case s := <-tc:
				if s == "exit" {
					return dflt
				}
				tkclient.Wmctl(t, s)
				continue
			default:
			}
			break
		}
	}
}

var getstringcfg = []string{
	"label .lab",
	"entry .ent -relief sunken -bd 2 -width 200",
	"pack .lab .ent -side left",
	"bind .ent <Key-\n> {send f 1}",
	"focus .ent",
}

// GetString asks for a string with prompt msg and returns what was
// typed, or "" if the window was closed.
func GetString(d *draw.Display, parent *draw.Image, msg string) string {
	t, wmctl, err := tkclient.Toplevel(d, localgeom(parent)+" -borderwidth 2 -relief raised", "", 0)
	if err != nil {
		return ""
	}
	defer closeWindow(t)
	f := make(chan string, 4)
	tk.NameChan(t, f, "f")
	for _, s := range getstringcfg {
		cmd(t, s)
	}
	cmd(t, ".lab configure -text "+tk.Quote(msg+":   "))
	if tkclient.Onscreen(t, "") != nil || tkclient.StartInput(t, "kbd", "ptr") != nil {
		return ""
	}
	if e := tk.Cmd(t, "variable lasterror"); e != "" {
		fmt.Fprintf(os.Stderr, "getstring error: %s\n", e)
		return ""
	}
	cmd(t, "update")
	for {
		if _, err := tkclient.Next(t, nil, -1); err != nil {
			return ""
		}
		for {
			select {
			case <-f:
				return tk.Cmd(t, ".ent get")
			case s := <-wmctl:
				if s == "exit" {
					return ""
				}
				tkclient.Wmctl(t, s)
				continue
			default:
			}
			break
		}
	}
}

// closeWindow takes the dialog's window off the screen.
func closeWindow(t *tk.Toplevel) {
	if w, ok := t.Ctxt.(*wmclient.Window); ok {
		w.Close()
	}
}
