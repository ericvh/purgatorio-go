// Command ndb/query looks up entries in the network data base (Inferno
// ndb/query, see ndb(6)): the first entry with attribute attr (or
// attr=value) is printed whole, or with rattr only the value of rattr
// in it. With -a every match is printed.
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/ndb"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var out strings.Builder

func main() {
	dbfile := "/lib/ndb/local"
	a := arg.New(os.Args)
	a.SetUsage("query [-a] [-f dbfile] attr [value [rattr]]")
	all := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'f':
			dbfile = a.EArg()
		case 'a':
			all = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	attr := args[0]
	value, rattr := "", ""
	vflag := len(args) > 1
	if vflag {
		value = args[1]
		if len(args) > 2 {
			rattr = args[2]
		}
	}
	db, err := ndb.Open(dbfile)
	if err != nil {
		os.Stderr.WriteString("query: can't open " + dbfile + ": " + sys.Errstr() + "\n")
		sys.Exits("fail:error")
	}
	defer flush()
	var p *ndb.Ptr
	for {
		var e *ndb.Entry
		switch {
		case rattr != "":
			e, p = db.FindByAttr(p, attr, value, rattr)
		case vflag:
			e, p = db.FindPair(p, attr, value)
		default:
			e, p = db.Find(p, attr)
		}
		if e == nil {
			break
		}
		if rattr != "" {
			for _, m := range e.FindByAttr(attr, value, rattr) {
				for _, v := range m.Attrs {
					out.WriteString(quote.Quote(v.Val) + "\n")
					if !all {
						break
					}
				}
				if !all {
					return
				}
			}
		} else {
			var s []string
			for _, l := range e.Lines {
				for _, v := range l.Pairs {
					s = append(s, quote.Quote(v.Attr)+"="+quote.Quote(v.Val))
				}
			}
			out.WriteString(strings.Join(s, " ") + "\n")
		}
		if !all {
			return
		}
	}
}

func flush() { os.Stdout.WriteString(out.String()) }
