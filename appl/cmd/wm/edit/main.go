// Command edit is a simple text editor in a window (Inferno's wm/edit):
// a Tk text with menus to open and save files, cut, copy and paste
// through the snarf buffer, find and replace, and go to a line, and a
// Limbo mode that colours keywords and comments and indents.
//
//	wm/edit [file]
//
// Open and Save As ask for a file name with a dialog where Inferno has
// selectfile(2).
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/dialog"
	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/tk"
	"github.com/ericvh/purgatorio-go/sdk/tkclient"
	"github.com/ericvh/purgatorio-go/sdk/wmclient"
)

// winui is the editor's world when it runs in a window.
type winui struct {
	d   *draw.Display
	top *tk.Toplevel
}

func (u *winui) Prompt(title, msg string, dflt int, labs []string) int {
	return dialog.Prompt(u.d, u.top.Image, errico, title, msg, dflt, labs)
}

func (u *winui) GetString(msg string) string { return dialog.GetString(u.d, u.top.Image, msg) }

func (u *winui) SetTitle(t string) {
	tkclient.SetTitle(u.top, t)
	tk.Cmd(u.top, "update")
}

func (u *winui) SnarfGet() string  { return tkclient.SnarfGet() }
func (u *winui) SnarfPut(s string) { tkclient.SnarfPut(s) }

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "wm/edit: %s\n", s)
	sys.Exits("fail:" + s)
}

func main() {
	sys.Pctl(sys.NEWPGRP, nil)
	d, err := draw.Init("")
	if err != nil {
		fatal(err.Error())
	}
	if _, err := d.GetDefaultFont(); err != nil {
		fatal(err.Error())
	}
	top, wmctl, err := tkclient.Toplevel(d, "", "Edit", tkclient.Appl)
	if err != nil {
		fatal(err.Error())
	}
	c := make(chan string, 64)
	tk.NameChan(top, c, "c")
	u := &winui{d: d, top: top}
	e := newEditor(top, u)
	e.setup()
	if len(os.Args) > 1 {
		if msg := e.loadtfile(os.Args[1]); msg != "" {
			u.Prompt("Open file", msg, 0, []string{"Ok"})
		}
	}
	e.settitle()
	if err := tkclient.Onscreen(top, ""); err != nil {
		fatal(err.Error())
	}
	if err := tkclient.StartInput(top, "ptr", "kbd"); err != nil {
		fatal(err.Error())
	}
	e.cmd("update")
	if v := tk.Cmd(top, "variable lasterror"); v != "" {
		fmt.Printf("edit error: %s\n", v)
		return
	}
	for {
		if _, err := tkclient.Next(top, nil, -1); err != nil {
			fatal(err.Error())
		}
		for drained := false; !drained; {
			select {
			case s := <-wmctl:
				if s == "exit" {
					if e.checkDirty() {
						wmclient.Exit()
					}
					break
				}
				tkclient.Wmctl(top, s)
			case s := <-c:
				if !e.do(s) {
					wmclient.Exit()
				}
			default:
				drained = true
			}
		}
		e.cmd("update")
		if v := tk.Cmd(top, "variable lasterror"); v != "" {
			fmt.Printf("edit error: %s\n", v)
		}
	}
}
