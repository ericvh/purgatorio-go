// Command dbm/store stores data under key in the dbm(2) data base
// dbfile, which must exist (Inferno's dbm/store). With no key it reads
// lines from standard input, each a key, a space or tab, and the data.
// A key already there keeps its contents.
package main

import (
	"bufio"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/dbm"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) != 2 && len(os.Args) != 4 {
		os.Stderr.WriteString("usage: dbm/store dbfile [key data]\n")
		sys.Exits("fail:usage")
	}
	db := dbm.OpenCmd("dbm/store", os.Args[1], sys.ORDWR)
	if len(os.Args) == 4 {
		if store(db, os.Args[2], os.Args[3]) != 0 {
			sys.Exits("fail:store")
		}
		return
	}
	failed := false
	in := bufio.NewReader(os.Stdin)
	for {
		s, err := in.ReadString('\n')
		if s == "" && err != nil {
			break
		}
		s = strings.TrimSuffix(s, "\n")
		i := strings.IndexAny(s, " \t")
		if i <= 0 {
			os.Stderr.WriteString("dbm/store: bad input\n")
			sys.Exits("fail:error")
		}
		if store(db, s[:i], s[i+1:]) != 0 {
			failed = true
		}
	}
	if failed {
		sys.Exits("fail:store")
	}
}

func store(db *dbm.Dbf, key, dat string) int {
	r := dbm.Must(db.Store([]byte(key), []byte(dat), false))
	if r < 0 {
		os.Stderr.WriteString("bad store\n")
	} else if r > 0 {
		os.Stderr.WriteString(quote.Quote(key) + " exists\n")
	}
	return r
}
