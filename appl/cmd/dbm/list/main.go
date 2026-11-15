// Command dbm/list prints each key of the dbm(2) data base dbfile with
// its contents, separated by a tab (Inferno's dbm/list).
package main

import (
	"bufio"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/dbm"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) != 2 {
		os.Stderr.WriteString("usage: dbm/list dbfile\n")
		sys.Exits("fail:usage")
	}
	db := dbm.OpenCmd("dbm/list", os.Args[1], sys.OREAD)
	out := bufio.NewWriter(os.Stdout)
	key, err := db.Firstkey()
	for key != nil && err == nil {
		var d []byte
		if d, err = db.Fetch(key); err != nil {
			break
		}
		out.Write(key)
		out.WriteByte('\t')
		out.Write(d)
		out.WriteByte('\n')
		key, err = db.Nextkey(key)
	}
	out.Flush()
	dbm.Must(0, err)
}
