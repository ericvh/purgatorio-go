package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// The console, kfs.b's consinit: with -n name, kfs serves the file
// /chan/kfs.name.cmd, to which kfscmd(8) writes commands. A command's
// output (only check has any) is read back from the same file; a read
// with no output pending is end of file.
//
// kfs.b runs a check in a process of its own and passes its output to
// readers as it comes; a guest is single-threaded, so here the check
// runs when it is written and its output waits for the reads.

var (
	kfsname  string
	nocheck  bool
	consfile *sys.FileIO
	consout  *strings.Builder // the output of a check in progress
	cdata    []byte           // output not yet read
	cfid     = -1             // the fid using the console
)

// cprint prints a line of console output.
func cprint(s string) {
	if consout != nil {
		consout.WriteString(s + "\n")
		return
	}
	eprint(s)
}

// consinit makes the console's file, if kfs was given a name.
func consinit() {
	if kfsname == "" {
		return
	}
	cfname := "kfs." + kfsname + ".cmd"
	sys.Bind("#s", "/chan", sys.MBEFORE)
	f, err := sys.File2chan("/chan", cfname)
	if err != nil {
		fatal("can't create /chan/" + cfname + ": " + sys.Errstr())
	}
	consfile = f
}

// consreq answers one read or write of the console.
func consreq(req any) {
	switch r := req.(type) {
	case *sys.ReadReq:
		if r.Closed() {
			return
		}
		if cfid == -1 {
			cfid = r.Fid
		}
		if r.Fid != cfid {
			r.Reply(nil, "kfs.cmd is busy")
			return
		}
		if len(cdata) == 0 || r.Count <= 0 {
			r.Reply(nil, "")
			return
		}
		n := r.Count
		if n > len(cdata) {
			n = len(cdata)
		}
		r.Reply(cdata[:n], "")
		cdata = cdata[n:]
	case *sys.WriteReq:
		if cfid == -1 {
			cfid = r.Fid
		}
		if r.Closed() {
			if r.Fid == cfid {
				cfid = -1
				cdata = nil // discard unread output of the last command
			}
			return
		}
		if r.Fid != cfid {
			r.Reply(0, "kfs.cmd is busy")
			return
		}
		f := strings.FieldsFunc(string(r.Data), func(c rune) bool { return strings.ContainsRune(" \t\n\r", c) })
		if len(f) < 1 {
			r.Reply(0, "illegal kfs request")
			return
		}
		switch f[0] {
		case "check":
			fl := 0
			if len(f) > 1 {
				if fl = checkflags(f[1]); fl < 0 {
					r.Reply(0, "illegal check flag: "+f[1])
					return
				}
			}
			r.Reply(len(r.Data), "")
			consout = &strings.Builder{}
			check(fl)
			cdata = append(cdata, consout.String()...)
			consout = nil
		case "users", "user":
			cmdusers()
			r.Reply(len(r.Data), "")
		case "sync":
			// Writes are not buffered: nothing to do.
			r.Reply(len(r.Data), "")
		case "allow":
			wstatallow, writeallow = true, true
			r.Reply(len(r.Data), "")
		case "allowoff", "disallow":
			wstatallow, writeallow = false, false
			r.Reply(len(r.Data), "")
		default:
			r.Reply(0, "unknown kfs request")
		}
	}
}

// waitany waits for Styx on standard input or a console request; it
// returns true for Styx.
func waitany() (bool, any) {
	if consfile == nil {
		return true, nil
	}
	ops := [][2]int{{sys.WaitRead, 0}, {sys.WaitChanRecv, int(consfile.Read)}, {sys.WaitChanRecv, int(consfile.Write)}}
	for {
		i, err := sys.Wait(ops, -1)
		if err != nil {
			return true, nil
		}
		switch i {
		case 0:
			return true, nil
		case 1, 2:
			req, err := consfile.Next()
			if err != nil {
				os.Stderr.WriteString("kfs: console: " + err.Error() + "\n")
				consfile = nil
				return true, nil
			}
			return false, req
		}
	}
}
