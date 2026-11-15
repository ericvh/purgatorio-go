// Command dbm/keys prints the keys of the dbm(2) data base dbfile, one
// per line, in the data base's order (Inferno's dbm/keys).
package main

import (
	"bufio"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/dbm"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) != 2 {
		os.Stderr.WriteString("usage: dbm/keys dbfile\n")
		sys.Exits("fail:usage")
	}
	db := dbm.OpenCmd("dbm/keys", os.Args[1], sys.OREAD)
	out := bufio.NewWriter(os.Stdout)
	for key := dbm.Must(db.Firstkey()); key != nil; {
		out.Write(key)
		out.WriteByte('\n')
		next, err := db.Nextkey(key)
		if err != nil {
			out.Flush()
		}
		key = dbm.Must(next, err)
	}
	out.Flush()
}
