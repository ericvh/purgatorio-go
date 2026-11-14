// Command pwd prints the current working directory (Inferno pwd(1)).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	fd, err := sys.Open(".", sys.OREAD)
	var wd string
	if err == nil {
		wd, err = sys.Fd2path(fd)
	}
	if err != nil {
		os.Stderr.WriteString("pwd: " + sys.Errstr() + "\n")
		sys.Exits("fail:error")
	}
	os.Stdout.WriteString(wd + "\n")
}
