// Command dbm/fetch prints the contents stored under key in the dbm(2)
// data base dbfile (Inferno's dbm/fetch).
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/dbm"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) != 3 {
		os.Stderr.WriteString("usage: dbm/fetch dbfile key\n")
		sys.Exits("fail:usage")
	}
	db := dbm.OpenCmd("dbm/fetch", os.Args[1], sys.OREAD)
	data := dbm.Must(db.Fetch([]byte(os.Args[2])))
	if data == nil {
		os.Stderr.WriteString("not found\n")
	} else {
		os.Stdout.Write(data)
	}
}
