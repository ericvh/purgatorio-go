// Command mergelog combines a replica log and a newer section of it, read
// from standard input, into one log with the most recent state of each
// file (Inferno install/mergelog). The entries of oldlog, then the new
// ones, are replayed, and an entry for each file is printed in the order
// of its last change. Entries that make no sense in order are noted on
// standard error and left out.
//
// Inferno's mergelog wanted three arguments yet refused a second, so it
// never ran; this one takes the old log alone, and prints the entries in
// the log format that applylog reads rather than Inferno's debugging
// format. -v and -d are accepted.
package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/logs"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var nerror int

func main() {
	a := arg.New(os.Args)
	a.SetUsage("mergelog [-vd] oldlog <newlog")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd', 'v':
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) < 1 {
		a.Usage()
	}
	if len(args) > 1 {
		errorf("restriction by path not yet done")
	}
	client := logs.New("existing log")
	f, err := os.Open(args[0])
	if err != nil {
		errorf(fmt.Sprintf("can't open %s: %s", args[0], sys.Errstr()))
	}
	replay(client, bufio.NewReader(f))
	f.Close()
	replay(client, bufio.NewReader(os.Stdin))
	client.Sort(logs.Byseq)
	out := bufio.NewWriter(os.Stdout)
	for _, e := range client.State {
		out.WriteString(e.Logtext() + "\n")
	}
	out.Flush()
	if nerror > 0 {
		sys.Exits(fmt.Sprintf("fail:%d errors", nerror))
	}
}

func replay(db *logs.Db, in *bufio.Reader) {
	for {
		log, err := logs.Read(in)
		if err != "" {
			errorf(err)
		}
		if log == nil {
			return
		}
		replaylog(db, log)
	}
}

func replaylog(db *logs.Db, log *logs.Entry) {
	e := db.Look(log.Path)
	indb := e != nil && !e.Removed()
	switch log.Action {
	case 'a':
		if indb {
			note(quote.Quote(log.Path) + " duplicate create")
			return
		}
	case 'c':
		if !indb {
			note(quote.Quote(log.Path) + " contents but no entry")
			return
		}
	case 'd':
		if !indb {
			note(quote.Quote(log.Path) + " deleted but no entry")
			return
		}
		if e.D.Mtime > log.D.Mtime {
			note(quote.Quote(log.Path) + " deleted but it's newer")
			return
		}
	case 'm':
		if !indb {
			note(quote.Quote(log.Path) + " metadata but no entry")
			return
		}
	default:
		errorf(fmt.Sprintf("bad log entry: %d %d", log.Seq>>32, log.Seq&0xFFFFFFFF))
	}
	if e == nil {
		e = db.Entry(log.Seq, log.Path, log.D)
	}
	e.Update(log)
}

func errorf(s string) {
	fmt.Fprintf(os.Stderr, "mergelog: %s\n", s)
	sys.Exits("fail:error")
}

func note(s string) { fmt.Fprintf(os.Stderr, "mergelog: note: %s\n", s) }
