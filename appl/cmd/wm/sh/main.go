// Command sh is a shell in a window (Inferno wm/sh): a Tk text widget
// with a scroll bar, talking to sh -i over pipes. Output goes in before
// the line being typed; each line typed after the output is sent to the
// shell when it is ended with a newline.
//
//	wm/sh [-w width] [-h height] [command ...]
//
// With a command it runs sh -c with it instead of an interactive shell.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/tk"
	"github.com/ericvh/purgatorio-go/sdk/tkclient"
	"github.com/ericvh/purgatorio-go/sdk/wmclient"
)

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "wm/sh: %s\n", s)
	sys.Exits("fail:" + s)
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("wm/sh [-w width] [-h height] [command ...]")
	width, height := "400", "200"
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'w':
			width = a.EArg()
		case 'h':
			height = a.EArg()
		default:
			a.Usage()
		}
	}
	shargs := []string{"sh", "-i"}
	if rest := a.Argv(); len(rest) > 0 {
		shargs = append([]string{"sh", "-c"}, strings.Join(rest, " "))
	}

	d, err := draw.Init("")
	if err != nil {
		fatal(err.Error())
	}
	if _, err := d.GetDefaultFont(); err != nil {
		fatal(err.Error())
	}
	top, wmctl, err := tkclient.Toplevel(d, "", "Shell", tkclient.Appl)
	if err != nil {
		fatal(err.Error())
	}
	cmd := make(chan string, 16)
	tk.NameChan(top, cmd, "cmd")
	for _, c := range []string{
		"scrollbar .s -command {.t yview}",
		"text .t -yscrollcommand {.s set} -bg white -width " + width + " -height " + height,
		"pack .s -side left -fill y",
		"pack .t -side left -fill both -expand 1",
		".t mark set outpoint end",
		".t mark gravity outpoint left",
		"focus .t",
		"update",
	} {
		if e := tk.Cmd(top, c); strings.HasPrefix(e, "!") {
			fatal(fmt.Sprintf("tk: %s: %s", c, e))
		}
	}

	in, err := sys.Pipe()
	if err != nil {
		fatal(err.Error())
	}
	out, err := sys.Pipe()
	if err != nil {
		fatal(err.Error())
	}
	_, err = sys.Spawn("/bin/sh.wasm", shargs, sys.NEWFD,
		[][2]int{{0, in[0]}, {1, out[1]}, {2, out[1]}})
	if err != nil {
		fatal("cannot start sh: " + err.Error())
	}
	syscall.Close(in[0])
	syscall.Close(out[1])

	if err := tkclient.Onscreen(top, ""); err != nil {
		fatal(err.Error())
	}
	if err := tkclient.StartInput(top, "kbd", "ptr"); err != nil {
		fatal(err.Error())
	}

	var partial []byte
	buf := make([]byte, 8192)
	for {
		e, err := tkclient.Next(top, []int{out[0]}, -1)
		if err != nil {
			fatal(err.Error())
		}
		switch e.Kind {
		case wmclient.Fd:
			n, err := syscall.Read(out[0], buf)
			if err != nil || n <= 0 {
				wmclient.Exit()
			}
			partial = append(partial, buf[:n]...)
			// Keep an incomplete UTF-8 sequence for next time.
			k := len(partial)
			for k > 0 && !utf8.Valid(partial[:k]) && len(partial)-k < utf8.UTFMax {
				k--
			}
			if k > 0 {
				o := string(partial[:k])
				tk.Cmd(top, ".t insert outpoint "+tk.Quote(o)+"; .t mark set outpoint {outpoint + "+
					strconv.Itoa(utf8.RuneCountInString(o))+" chars}; .t see end; update")
				partial = partial[k:]
			}
		}
		for drained := false; !drained; {
			select {
			case s := <-wmctl:
				tkclient.Wmctl(top, s)
			case <-cmd:
			default:
				drained = true
			}
		}
		// Send each finished line after the output point to the shell.
		typed := tk.Cmd(top, ".t get outpoint end")
		if i := strings.LastIndexByte(typed, '\n'); i >= 0 && !strings.HasPrefix(typed, "!") {
			line := typed[:i+1]
			if _, err := syscall.Write(in[1], []byte(line)); err != nil {
				wmclient.Exit()
			}
			tk.Cmd(top, ".t mark set outpoint {outpoint + "+strconv.Itoa(utf8.RuneCountInString(line))+" chars}")
		}
	}
}
