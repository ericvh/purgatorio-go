// Command runas runs a command as another user (Inferno runas(1)): it
// writes the user name to /dev/user, which only the host owner may do,
// and runs the command with sh only if that succeeds.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) < 3 {
		os.Stderr.WriteString("usage: runas user cmd [args...]\n")
		sys.Exits("fail:usage")
	}
	user, argv := os.Args[1], os.Args[2:]
	fd, err := sys.Open("/dev/user", sys.OWRITE)
	if err != nil {
		fail("cannot open /dev/user: " + sys.Errstr())
	}
	f := os.NewFile(uintptr(fd), "/dev/user")
	if n, err := f.Write([]byte(user)); err != nil || n != len(user) {
		fail("cannot set user: " + sys.Errstr())
	}
	f.Close()
	// sh runs the words of $* as one simple command, as Sh->run does.
	sys.Exec("/bin/sh.wasm", append([]string{"sh", "-c", "$*"}, argv...))
	os.Stderr.WriteString("runas: cannot load /bin/sh.wasm: " + sys.Errstr() + "\n")
	sys.Exits("fail:bad module")
}

func fail(e string) {
	os.Stderr.WriteString("runas: " + e + "\n")
	sys.Exits("fail:error")
}
