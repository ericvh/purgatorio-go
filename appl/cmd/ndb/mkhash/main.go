// Command ndb/mkhash writes the hash file file.attr for attribute attr
// of the data base file (Inferno ndb/mkhash, after Plan 9's), in the
// format ndb(6) describes.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/ndb"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) != 3 {
		fail("usage: mkhash file attr")
	}
	dbname, attr := os.Args[1], os.Args[2]
	f, err := ndb.OpenFile(dbname)
	if err != nil {
		fail("can't open " + dbname + ": " + sys.Errstr())
	}
	b := ndb.Mkhash(f, attr)
	hashfile := dbname + "." + attr
	fd, err := sys.Create(hashfile, sys.OWRITE, 0o666)
	if err != nil {
		fail("can't create " + hashfile + ": " + sys.Errstr())
	}
	hf := os.NewFile(uintptr(fd), hashfile)
	if n, err := hf.Write(b); err != nil || n != len(b) {
		fail("error writing " + hashfile + ": " + sys.Errstr())
	}
	hf.Close()
}

func fail(s string) {
	os.Stderr.WriteString("mkhash: " + s + "\n")
	sys.Exits("fail:error")
}
