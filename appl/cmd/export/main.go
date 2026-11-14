// Command export serves the name space below dir as Styx on a connection
// (Inferno export(4)): the file named by connection, or standard input.
// With -a it returns at once and the export goes on in the background.
// With -r (not Inferno; the Rust sibling's) the export refuses every
// request that would change a file.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("Usage: export [-ar] dir [connection]\n")
	sys.Exits("fail:usage")
}

func main() {
	argv := os.Args[1:]
	flag, rdonly := sys.EXPWAIT, 0
	for ; len(argv) > 0 && len(argv[0]) > 0 && argv[0][0] == '-'; argv = argv[1:] {
		for _, c := range argv[0][1:] {
			switch c {
			case 'a':
				flag = sys.EXPASYNC
			case 'r':
				rdonly = sys.EXPRDONLY
			default:
				usage()
			}
		}
	}
	if len(argv) < 1 || len(argv) > 2 {
		usage()
	}
	fd := 0
	if len(argv) == 2 {
		var err error
		if fd, err = sys.Open(argv[1], sys.ORDWR); err != nil {
			os.Stderr.WriteString("export: can't open " + argv[1] + ": " + sys.Errstr() + "\n")
			sys.Exits("fail:open")
		}
	}
	if err := sys.Export(fd, argv[0], flag|rdonly); err != nil {
		os.Stderr.WriteString("export: can't export: " + sys.Errstr() + "\n")
		sys.Exits("fail:export")
	}
}
