// Command dbm/delete removes key and its contents from the dbm(2) data
// base dbfile (Inferno's dbm/delete).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/dbm"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) != 3 {
		os.Stderr.WriteString("usage: dbm/delete dbfile key\n")
		sys.Exits("fail:usage")
	}
	db := dbm.OpenCmd("dbm/delete", os.Args[1], sys.ORDWR)
	if dbm.Must(db.Delete([]byte(os.Args[2]))) < 0 {
		os.Stderr.WriteString("not found\n")
	}
}
