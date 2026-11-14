// Command cs is the connection server (Inferno cs(8)). It binds #s before
// /net and serves /net/cs, which translates a symbolic address such as
// tcp!host!styx, written to it, into the clone files and addresses to
// dial, read back one per read:
//
//	/net/tcp/clone 127.0.0.1!6666
//
// Service names come from /lib/ndb/local and the files its database
// entry names; host names from the data base, then /net/dns when there
// is one, then the host's resolver (the hosted Srv module).
//
// Requests are answered one at a time: a guest is single-threaded.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

type reply struct {
	fid   int
	addrs []string
	err   string
}

type cached struct {
	expire int64
	query  string
	addrs  []string
}

var (
	cache     [16]*cached
	nextcache int
	rlist     []*reply
	ndbfile   = "/lib/ndb/local"
	ndb       *db
	mntpt     = "/net"
	myname    string
	verbose   int
	nets      = map[string]bool{}
)

func main() {
	svcname := "#scs"
	a := arg.New(os.Args)
	a.SetUsage("cs [-v] [-x mntpt] [-f database] [-d dialmod]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'v', 'D':
			verbose++
		case 'd':
			if f := a.Arg(); f != "" {
				fatal("cannot load " + f + ": dial-on-demand modules are not supported")
			}
		case 'f':
			ndbfile = a.EArg()
		case 'x':
			mntpt = a.EArg()
			svcname = "#scs" + strings.ReplaceAll(mntpt, "/", "_")
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	sys.Remove(svcname + "/cs")
	sys.Unmount(svcname, mntpt)
	publish(svcname)
	if err := sys.Bind(svcname, mntpt, sys.MBEFORE); err != nil {
		fatal("can't bind #s on " + mntpt + ": " + sys.Errstr())
	}
	file, err := sys.File2chan(mntpt, "cs")
	if err != nil {
		fatal("can't make " + mntpt + "/cs: " + sys.Errstr())
	}
	sys.Pctl(sys.FORKFD|sys.NEWPGRP, nil)
	refresh()
	sys.Detach()
	cs(file)
}

func fatal(s string) {
	os.Stderr.WriteString("cs: " + s + "\n")
	sys.Exits("fail:error")
}

// publish makes the service directory accessible to all. #s does not
// allow that yet, so the failure is only reported with -v.
func publish(dir string) {
	d := sys.NullDir()
	d.Mode = 0o777
	if err := sys.Wstat(dir, &d); err != nil && verbose > 0 {
		os.Stderr.WriteString("cs: can't publish " + dir + ": " + sys.Errstr() + "\n")
	}
}

func refresh() {
	myname = sysname()
	d, err := opendb(ndbfile)
	if err != nil {
		e := sys.Errstr()
		if d, err = opendb("/lib/ndb/inferno"); err != nil {
			os.Stderr.WriteString("cs: warning: can't open " + ndbfile + ": " + e + "\n")
		}
	}
	if nd, err := opendb(mntpt + "/ndb"); err == nil {
		d = nd.append(d)
	}
	ndb = d
}

func sysname() string {
	if t := rf("/dev/sysname"); t != "" {
		return t
	}
	return rf("#e/sysname")
}

func rf(name string) string {
	b, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(b)
}

func cs(file *sys.FileIO) {
	for {
		req, err := file.Next()
		if err != nil {
			return
		}
		switch r := req.(type) {
		case *sys.WriteReq:
			cleanfid(r.Fid) // each write cancels earlier requests
			if r.Closed() {
				continue
			}
			query := string(r.Data)
			if query == "refresh" {
				refresh()
				r.Reply(len(r.Data), "")
				continue
			}
			rp := &reply{fid: r.Fid}
			if query != "" && query[0] == '!' {
				rp.addrs, rp.err = genquery(query[1:])
			} else {
				rp.addrs, rp.err = xlate(query, time.Now().Unix())
				if rp.addrs == nil && rp.err == "" {
					rp.err = "cs: can't translate address"
				}
			}
			rlist = append(rlist, rp)
			if rp.err != "" {
				if verbose > 0 {
					os.Stderr.WriteString("cs: " + query + ": " + rp.err + "\n")
				}
				r.Reply(0, rp.err)
			} else {
				r.Reply(len(r.Data), "")
			}
		case *sys.ReadReq:
			if r.Closed() {
				cleanfid(r.Fid)
				continue
			}
			rp := findfid(r.Fid)
			if rp == nil {
				r.Reply(nil, "unknown request")
				continue
			}
			if rp.err != "" {
				r.Reply(nil, rp.err)
				continue
			}
			addr := ""
			if len(rp.addrs) > 0 {
				addr, rp.addrs = rp.addrs[0], rp.addrs[1:]
			}
			// The offset is ignored: each read returns the next address.
			b := []byte(addr)
			r.Reply(b[:min(len(b), r.Count)], "")
		}
	}
}

func findfid(fid int) *reply {
	for _, r := range rlist {
		if r.fid == fid {
			return r
		}
	}
	return nil
}

func cleanfid(fid int) {
	l := rlist[:0]
	for _, r := range rlist {
		if r.fid != fid {
			l = append(l, r)
		}
	}
	clear(rlist[len(l):])
	rlist = l
}

func lookcache(query string, now int64) *cached {
	for _, c := range cache {
		if c != nil && c.query == query && now < c.expire {
			if verbose > 0 {
				fmt.Printf("cache: %s -> %s\n", query, c.addrs[0])
			}
			return c
		}
	}
	return nil
}

func putcache(query string, addrs []string, now int64) {
	cache[nextcache] = &cached{expire: now + 120, query: query, addrs: addrs}
	nextcache = (nextcache + 1) % len(cache)
}

func xlate(address string, now int64) ([]string, string) {
	if ce := lookcache(address, now); ce != nil && ce.addrs != nil {
		return append([]string(nil), ce.addrs...), ""
	}
	l := strings.FieldsFunc(address, func(r rune) bool { return r == '!' || r == '\n' })
	if len(l) < 2 {
		return nil, "bad format request"
	}
	netw := l[0]
	if netw == "net" {
		netw = "tcp"
	}
	if !isnetwork(netw) {
		return nil, "network unavailable " + netw
	}
	if !isipnet(netw) {
		return []string{mntpt + "/" + netw + "/clone " + strings.Join(l[1:], "!")}, ""
	}
	if len(l) != 3 {
		return nil, "bad format request"
	}
	mach, service := l[1], l[2]
	if !isnumeric(service) {
		s := xlatesvc(netw, service)
		if s == "" {
			s, _ = sys.Ipn2p(netw, service)
			if s == "" {
				return nil, "cs: can't translate service"
			}
		}
		service = s
	}
	attr := dbattr(mach)
	var hosts []string
	switch {
	case mach == "*":
		hosts = []string{""}
	case attr != "ip":
		if mach[0] == '$' && len(mach) > 1 && ndb != nil {
			s := ""
			if v := ndb.lookup("sys", myname, mach[1:]); len(v) > 0 {
				s = v[0]
			} else if names := ndb.lookup("infernosite", "", mach[1:]); len(names) > 0 {
				s = names[0]
			} else {
				return nil, "cs: can't translate " + mach
			}
			mach = s
			attr = dbattr(mach)
		}
		var results []string
		if attr == "sys" {
			if results = ndb.lookup("sys", mach, "ip"); results != nil {
				attr = "ip"
			}
		}
		if attr != "ip" {
			var err string
			if results, err = querydns(mach, "ip"); err != "" {
				return nil, err
			}
		} else if results == nil {
			results = []string{mach}
		}
		hosts = results
		if hosts == nil {
			hosts, _ = sys.Iph2a(mach)
			if hosts == nil {
				return nil, "cs: unknown host"
			}
		}
	default:
		hosts = []string{mach}
	}
	var rl []string
	for _, s := range hosts {
		dnetw := netw
		if s != "" {
			if v := ndb.lookup("ip", s, "divert-"+netw); len(v) > 0 && v[0] != "" {
				dnetw = v[0]
				if !isnetwork(dnetw) {
					return nil, "network unavailable " + dnetw
				}
			}
			s += "!"
		}
		repl := mntpt + "/" + dnetw + "/clone " + s + service
		if verbose > 0 {
			os.Stderr.WriteString("cs: " + netw + "!" + mach + "!" + service + " -> " + repl + "\n")
		}
		rl = append(rl, repl)
	}
	putcache(address, append([]string(nil), rl...), now)
	return rl, ""
}

func querydns(name, rtype string) ([]string, string) {
	fd, err := sys.Open(mntpt+"/dns", sys.ORDWR)
	if err != nil {
		return nil, ""
	}
	f := os.NewFile(uintptr(fd), "dns")
	defer f.Close()
	if _, err := f.Write([]byte(name + " " + rtype)); err != nil {
		return nil, sys.Errstr()
	}
	f.Seek(0, 0)
	var rl []string
	buf := make([]byte, 256)
	for {
		n, _ := f.Read(buf)
		if n <= 0 {
			break
		}
		fld := strings.Fields(string(buf[:n]))
		if len(fld) != 3 {
			os.Stderr.WriteString("cs: odd result from dns: " + string(buf[:n]) + "\n")
			continue
		}
		rl = append(rl, fld[2])
	}
	return rl, ""
}

func isnumeric(a string) bool {
	for i := 0; i < len(a); i++ {
		if a[i] < '0' || a[i] > '9' {
			return false
		}
	}
	return true
}

func isnetwork(s string) bool {
	if nets[s] {
		return true
	}
	if _, err := sys.Stat(mntpt + "/" + s + "/clone"); err == nil {
		nets[s] = true
		return true
	}
	return false
}

func isipnet(s string) bool { return s == "net" || s == "tcp" || s == "udp" || s == "il" }

func xlatesvc(proto, s string) string {
	if ndb == nil || s == "" || isnumeric(s) {
		return s
	}
	port, restricted, ok := ndb.first(proto, s, "port", "restricted")
	if !ok {
		return ""
	}
	if restricted {
		port += "!r"
	}
	if verbose > 1 {
		fmt.Printf("%s=%s port=%s\n", proto, quote(s), port)
	}
	return port
}

// dbattr guesses what kind of name s is, as IPattr->dbattr does.
func dbattr(s string) string {
	var dot, alpha, hex, colon bool
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F':
			hex = true
		case c == '.':
			dot = true
		case c == ':':
			colon = true
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '&':
			alpha = true
		}
	}
	switch {
	case alpha:
		if dot {
			return "dom"
		}
		return "sys"
	case colon:
		return "ip"
	case dot && !hex:
		return "ip"
	case dot:
		return "dom"
	}
	return "sys"
}

// genquery answers !attr1=val1 attr2=val2 ...: the entries with
// attr1=val1 that have a line matching the rest.
func genquery(query string) ([]string, string) {
	pairs, err := parseline(query)
	if err != "" {
		return nil, "bad query: " + err
	}
	if len(pairs) == 0 {
		return nil, "bad query"
	}
	a0, v0 := pairs[0].attr, pairs[0].val
	if a0 == "ipinfo" {
		return nil, "ipinfo not yet supported"
	}
	if ndb == nil {
		return nil, "no match"
	}
	for _, e := range ndb.entries {
		if !e.has(a0, v0) {
			continue
		}
		for _, t := range e {
			if qmatch(t, pairs[1:]) {
				var ls []string
				for _, t := range e {
					ls = append(ls, tuptext(t))
				}
				return ls, ""
			}
		}
	}
	return nil, "no match"
}

func qmatch(t tuples, av []pair) bool {
next:
	for _, a := range av {
		for _, p := range t {
			if p.attr == a.attr && (a.val == "*" || a.val == p.val) {
				continue next
			}
		}
		return false
	}
	return true
}

func tuptext(t tuples) string {
	var b strings.Builder
	for i, p := range t {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p.attr + "=" + quote(p.val))
	}
	return b.String()
}

// quote quotes s as Limbo's %q does: only when it must.
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'=") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
