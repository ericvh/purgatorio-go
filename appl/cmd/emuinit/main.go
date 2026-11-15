// Command emuinit is the first program the emu runs (Inferno emuinit(8)):
// it binds #e on /env, then runs the command given to the emu in this
// prog, or sh -l when there is none. A command that cannot be loaded is
// given to sh -c.
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	_, _ = sys.Bind("#e", "/env", sys.MREPL|sys.MCREATE)
	args := os.Args[1:]
	path := "/bin/sh.wasm"
	if len(args) == 0 {
		args = []string{"sh", "-l"}
	} else {
		path = args[0]
		if !strings.HasPrefix(path, "/") {
			path = "/bin/" + path
		}
		if !strings.HasSuffix(path, ".wasm") {
			path += ".wasm"
		}
	}
	err := sys.Exec(path, args)
	// Not a loadable command: let the shell run it.
	serr := sys.Exec("/bin/sh.wasm", append([]string{"sh", "-c", "$*"}, args...))
	_ = serr
	os.Stderr.WriteString("emuinit: unable to load " + path + ": " + err.Error() + "\n")
	sys.Exits("fail:error")
}
