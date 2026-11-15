// Command B sends files to the editor (Inferno's B, appl/cmd/B.b).
//
//	B [-line] file ...
//
// Each file, made absolute from the current directory, goes to the
// plumber as a message for the edit port, from B, of kind text. A
// -line argument before a file adds :line to it, so the editor shows
// that line. acme reads the edit port and opens the file in a window.
//
// Inferno's B ran the host's B through /cmd, to reach an editor on the
// host. This one reaches acme through the plumber, as Plan 9's does;
// the plumber must be running, with a rule that sends files to the
// edit port, as /lib/plumbing has.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/plumbmsg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	args := os.Args
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: B file ...\n")
		return
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: can't get working dir: %s\n", sys.Errstr())
		sys.Exits("wd")
	}
	if err := plumbmsg.Init(true, "", 0); err != nil {
		fmt.Fprintf(os.Stderr, "B: open /chan/plumb.input: %v\n", err)
		sys.Exits("plumb")
	}
	line := ""
	for _, f := range args[1:] {
		if len(f) > 1 && f[0] == '-' {
			if _, err := strconv.Atoi(f[1:]); err == nil {
				line = ":" + f[1:]
				continue
			}
		}
		if f != "" && f[0] != '/' {
			f = strings.TrimSuffix(wd, "/") + "/" + f
		}
		m := &plumbmsg.Msg{Src: "B", Dst: "edit", Dir: wd, Kind: "text", Data: []byte(f + line)}
		if err := m.Send(); err != nil {
			fmt.Fprintf(os.Stderr, "B: %s: %v\n", f, err)
			sys.Exits("plumb")
		}
	}
}
