// Command about shows what system this is in a window (Inferno
// wm/about), with Tk: labels and a button to close it.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/tk"
	"github.com/ericvh/purgatorio-go/sdk/tkclient"
	"github.com/ericvh/purgatorio-go/sdk/wmclient"
)

var cfg = []string{
	"frame .f -bg white -borderwidth 1 -relief sunken",
	"label .f.title -text {Purgatorio-Go} -bg white -fg #0000cc",
	"label .f.l1 -text {Inferno, rebuilt in Go} -bg white",
	"label .f.l2 -bg white",
	"label .f.l3 -text {Applications are WebAssembly on wazero} -bg white",
	"pack .f.title .f.l1 .f.l2 .f.l3 -pady 2",
	"button .ok -text OK -command {send cmd ok}",
	"pack .f -fill both -expand 1 -padx 4 -pady 4",
	"pack .ok -pady 4",
	"pack propagate . 1",
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "about: %v\n", err)
	sys.Exits("fail:" + err.Error())
}

func main() {
	d, err := draw.Init("")
	if err != nil {
		fatal(err)
	}
	if _, err := d.GetDefaultFont(); err != nil {
		fatal(err)
	}
	top, wmctl, err := tkclient.Toplevel(d, "", "About Purgatorio-Go", tkclient.Appl)
	if err != nil {
		fatal(err)
	}
	cmd := make(chan string, 8)
	tk.NameChan(top, cmd, "cmd")
	for _, c := range cfg {
		tk.Cmd(top, c)
	}
	version := "Purgatorio-Go"
	if b, err := os.ReadFile("/dev/sysctl"); err == nil {
		version = strings.TrimSpace(string(b))
	}
	tk.Cmd(top, ".f.l2 configure -text "+tk.Quote(version)+"; update")
	if err := tkclient.Onscreen(top, ""); err != nil {
		fatal(err)
	}
	if err := tkclient.StartInput(top, "kbd", "ptr"); err != nil {
		fatal(err)
	}
	for {
		if _, err := tkclient.Next(top, nil, -1); err != nil {
			fatal(err)
		}
		for drained := false; !drained; {
			select {
			case s := <-wmctl:
				tkclient.Wmctl(top, s)
			case <-cmd:
				wmclient.Exit()
			default:
				drained = true
			}
		}
	}
}
