// Command cd changes the current directory of the name space it shares
// with the shell that runs it (Inferno cd(1)).
package main

import (
	"os"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"/usr/" + user()}
	}
	if len(args) > 1 {
		os.Stderr.WriteString("Usage: cd [directory]\n")
		sys.Exits("fail:usage")
	}
	if err := sys.Chdir(args[0]); err != nil {
		os.Stderr.WriteString("cd: " + args[0] + ": " + err.Error() + "\n")
		sys.Exits("fail:failed")
	}
}

func user() string {
	fd, err := sys.Open("/dev/user", sys.OREAD)
	if err != nil {
		return "inferno"
	}
	buf := make([]byte, 256)
	n, err := syscall.Read(fd, buf)
	syscall.Close(fd)
	if err != nil || n <= 0 {
		return "inferno"
	}
	return string(buf[:n])
}
