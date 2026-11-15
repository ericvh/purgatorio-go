package main

// Gates (and, or, not, match, depth, path, mode), the verbs that use
// them (filter, select, chstat) and compose.

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/filepat"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/regex"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func gates(args []value) []gatechan {
	var sub []gatechan
	for _, a := range args {
		sub = append(sub, vp(a))
	}
	return sub
}

func andrun(_ *Report, _ []option, args []value) value {
	c := make(gatechan)
	sub := gates(args)
	go func() {
		myreply := make(chan bool)
		for q := <-c; q.reply != nil; q = <-c {
			ok := true
			for _, g := range sub {
				g <- gatequery{q.e, myreply}
				if !<-myreply {
					ok = false
					break
				}
			}
			q.reply <- ok
		}
		for _, g := range sub {
			g <- gatequery{}
		}
	}()
	return vP{c}
}

func orrun(_ *Report, _ []option, args []value) value {
	c := make(gatechan)
	sub := gates(args)
	go func() {
		myreply := make(chan bool)
		for q := <-c; q.reply != nil; q = <-c {
			ok := false
			for _, g := range sub {
				g <- gatequery{q.e, myreply}
				if <-myreply {
					ok = true
					break
				}
			}
			q.reply <- ok
		}
		for _, g := range sub {
			g <- gatequery{}
		}
	}()
	return vP{c}
}

func notrun(_ *Report, _ []option, args []value) value {
	c := make(gatechan)
	sub := vp(args[0])
	go func() {
		myreply := make(chan bool)
		for q := <-c; q.reply != nil; q = <-c {
			sub <- gatequery{q.e, myreply}
			q.reply <- !<-myreply
		}
		sub <- gatequery{}
	}()
	return vP{c}
}

func matchrun(_ *Report, opts []option, args []value) value {
	pat := vs(args[0])
	aflag, rflag := false, false
	for _, o := range opts {
		switch o.opt {
		case 'a':
			aflag = true
		case 'r':
			rflag = true
		}
	}
	var re *regex.Re
	if rflag {
		var err string
		re, err = regex.Compile(pat, 0)
		if re == nil {
			fmt.Fprintf(os.Stderr, "fs: match: regex error on %s: %s\n", quote.Quote(pat), err)
			return nil
		}
	}
	c := make(gatechan)
	go func() {
		for q := <-c; q.reply != nil; q = <-c {
			name := q.e.path
			if !aflag {
				name = q.e.dir.Name
			}
			if re != nil {
				q.reply <- re.Execute([]rune(name)) != nil
			} else {
				q.reply <- filepat.Match(pat, name)
			}
		}
	}()
	return vP{c}
}

func depthrun(_ *Report, _ []option, args []value) value {
	d, _ := strconv.Atoi(vs(args[0]))
	if d <= 0 {
		fmt.Fprintf(os.Stderr, "fs: depth: invalid depth\n")
		return nil
	}
	c := make(gatechan)
	go func() {
		for q := <-c; q.reply != nil; q = <-c {
			q.reply <- q.e.depth <= d
		}
	}()
	return vP{c}
}

func pathrun(_ *Report, opts []option, args []value) value {
	c := make(gatechan)
	var paths []string
	for _, a := range args {
		paths = append([]string{vs(a)}, paths...)
	}
	xflag := opts != nil
	go func() {
		for q := <-c; q.reply != nil; q = <-c {
			path := q.e.path
			found := false
			for _, p := range paths {
				var r bool
				if xflag {
					r = true
					if len(path) > len(p) {
						r = path[len(p)] != '/' || path[:len(p)] != p
					} else if len(path) == len(p) {
						r = path != p
					}
					if !r {
						found = true
						break
					}
				} else {
					if len(path) > len(p) {
						r = path[len(p)] == '/' && path[:len(p)] == p
					} else if len(path) == len(p) {
						r = path == p
					} else {
						r = p[len(path)] == '/' && p[:len(path)] == path
					}
					if r {
						found = true
						break
					}
				}
			}
			q.reply <- found != xflag
		}
	}()
	return vP{c}
}

// Permission classes, from chmod.
const (
	pUser  = 0o700
	pGroup = 0o070
	pOther = 0o007
	pAll   = pUser | pGroup | pOther
	pRead  = 0o444
	pWrite = 0o222
	pExec  = 0o111
)

// parsemode parses a chmod-style mode into a mask and the bits.
func parsemode(spec string) (bool, uint32, uint32) {
	mask := uint32(sys.DMAPPEND | sys.DMEXCL | sys.DMDIR | sys.DMAUTH)
	i := 0
loop:
	for ; i < len(spec); i++ {
		switch spec[i] {
		case 'u':
			mask |= pUser
		case 'g':
			mask |= pGroup
		case 'o':
			mask |= pOther
		case 'a':
			mask |= pAll
		default:
			break loop
		}
	}
	if i == len(spec) {
		return false, 0, 0
	}
	if i == 0 {
		mask |= pAll
	}
	op := spec[i]
	i++
	if op != '+' && op != '-' && op != '=' {
		return false, 0, 0
	}
	var mode uint32
	for ; i < len(spec); i++ {
		switch spec[i] {
		case 'r':
			mode |= pRead
		case 'w':
			mode |= pWrite
		case 'x':
			mode |= pExec
		case 'a':
			mode |= sys.DMAPPEND
		case 'l':
			mode |= sys.DMEXCL
		case 'd':
			mode |= sys.DMDIR
		case 'A':
			mode |= sys.DMAUTH
		default:
			return false, 0, 0
		}
	}
	if op == '+' || op == '-' {
		mask &= mode
	}
	if op == '-' {
		mode = ^mode
	}
	return true, mask, mode
}

func moderun(_ *Report, _ []option, args []value) value {
	spec := vs(args[0])
	ok, mask, mode := parsemode(spec)
	if !ok {
		fmt.Fprintf(os.Stderr, "fs: mode: bad mode %s\n", quote.Quote(spec))
		return nil
	}
	c := make(gatechan)
	m := mode & mask
	go func() {
		for q := <-c; q.reply != nil; q = <-c {
			q.reply <- (q.e.dir.Mode&mask)^m == 0
		}
	}()
	return vP{c}
}

// filter passes src to dst, leaving out the entries query refuses and
// their contents (appl/lib/fsfilter.b).
func filter(query func(d *sys.Dir, name string, depth int) bool, src, dst fschan) {
	var names []string
	name := ""
	indent := 0
	myreply := make(chan int)
	for {
		m := <-src
		d := m.d
		if d.dir != nil {
			p := name
			if indent > 0 && p != "" && p[len(p)-1] != '/' {
				p += "/"
			}
			if !query(d.dir, p+d.dir.Name, indent) && indent > 0 {
				m.reply <- Next
				continue
			}
		}
		dst <- fsmsg{d, myreply}
		r := <-myreply
		m.reply <- r
		switch r {
		case Quit:
			return
		case Next:
			if d.dir == nil && d.data == nil {
				indent--
				if indent == 0 {
					return
				}
				name, names = names[len(names)-1], names[:len(names)-1]
			}
		case Skip:
			indent--
			if indent == 0 {
				return
			}
			name, names = names[len(names)-1], names[:len(names)-1]
		case Down:
			if d.dir != nil {
				names = append(names, name)
				if d.dir.Mode&sys.DMDIR != 0 {
					if indent == 0 {
						name = d.dir.Name
					} else {
						if name[len(name)-1] != '/' {
							name += "/"
						}
						name += d.dir.Name
					}
				}
				indent++
			}
		}
	}
}

func filterrun(_ *Report, opts []option, args []value) value {
	dst := make(fschan)
	gate := vp(args[0])
	src := vx(args[1])
	dflag := opts != nil
	reply := make(chan bool)
	go func() {
		filter(func(d *sys.Dir, name string, depth int) bool {
			if depth == 0 || (dflag && d.Mode&sys.DMDIR != 0) {
				return true
			}
			gate <- gatequery{entry{d, name, depth}, reply}
			return <-reply
		}, src, dst)
		gate <- gatequery{}
	}()
	return vX{dst}
}

func selectrun(_ *Report, _ []option, args []value) value {
	dst := entrychan{make(chan int), make(chan entry)}
	query := vp(args[0])
	src := vt(args[1])
	go func() {
		if <-dst.sync == 0 {
			query <- gatequery{}
			src.sync <- 0
			return
		}
		src.sync <- 1
		reply := make(chan bool)
		for d := <-src.c; d.dir != nil; d = <-src.c {
			query <- gatequery{d, reply}
			if <-reply {
				dst.c <- d
			}
		}
		dst.c <- entry{}
		query <- gatequery{}
	}()
	return vT{dst}
}

func chstatrun(_ *Report, opts []option, args []value) value {
	ws := sys.NullDir()
	var mask uint32
	var gate value
	cflag := false
	for _, o := range opts {
		switch o.opt {
		case 'p':
			if gate != nil {
				discard(gate)
			}
			gate = o.args[0]
		case 'm':
			m := vs(o.args[0])
			var ok bool
			ok, mask, ws.Mode = parsemode(m)
			mask &^= sys.DMDIR
			if !ok {
				fmt.Fprintf(os.Stderr, "fs: chstat: bad mode %s\n", quote.Quote(m))
				if gate != nil {
					discard(gate)
				}
				return nil
			}
		case 'u':
			ws.UID = vs(o.args[0])
		case 'g':
			ws.GID = vs(o.args[0])
		case 't':
			t, _ := strconv.Atoi(vs(o.args[0]))
			ws.Mtime = uint32(t)
		case 'a':
			t, _ := strconv.Atoi(vs(o.args[0]))
			ws.Atime = uint32(t)
		case 'c':
			cflag = true
		}
	}
	dst := make(fschan)
	var g gatechan
	if gate != nil {
		g = vp(gate)
	}
	src := vx(args[0])
	reply := make(chan bool)
	go func() {
		filter(func(d *sys.Dir, name string, depth int) bool {
			c := true
			if g != nil {
				g <- gatequery{entry{d, name, depth}, reply}
				c = <-reply
			}
			if c {
				if cflag {
					m := d.Mode & 0o700
					d.Mode = (d.Mode &^ 0o77) | (m >> 3) | (m >> 6)
				}
				d.Mode = (d.Mode &^ mask) | (ws.Mode & mask)
				if ws.UID != "" {
					d.UID = ws.UID
				}
				if ws.GID != "" {
					d.GID = ws.GID
				}
				if ws.Mtime != ^uint32(0) {
					d.Mtime = ws.Mtime
				}
				if ws.Atime != ^uint32(0) {
					d.Atime = ws.Atime
				}
			}
			return true
		}, src, dst)
		if g != nil {
			g <- gatequery{}
		}
	}()
	return vX{dst}
}

// Compositing operators: which of A, B or both to take where both
// exist (in) or only one does (out).
const (
	AinB  = 1 << 3
	BinA  = 1 << 2
	AoutB = 1 << 1
	BoutA = 1 << 0
)

var composeops = [...]string{
	AinB:                 "AinB",
	BinA:                 "BinA",
	AoutB:                "AoutB",
	BoutA:                "BoutA",
	AinB | AoutB:         "A",
	AinB | AoutB | BoutA: "AoverB",
	AinB | BoutA:         "AatopB",
	AoutB | BoutA:        "AxorB",
	BinA | BoutA:         "B",
	BinA | BoutA | AoutB: "BoverA",
	BinA | AoutB:         "BatopA",
}

func composerun(_ *Report, opts []option, args []value) value {
	s := vs(args[0])
	i := 0
	for ; i < len(composeops); i++ {
		if composeops[i] == s {
			break
		}
	}
	if i == len(composeops) {
		fmt.Fprintf(os.Stderr, "fs: join: bad op %s\n", quote.Quote(s))
		return nil
	}
	c := make(cmpchan)
	go compose(c, i, opts != nil)
	return vM{c}
}

func compose(c cmpchan, op int, dflag bool) {
	var t [4]int
	if op&AinB != 0 {
		t[3] = 1
	}
	if op&BinA != 0 {
		t[3] = 2
	}
	if op&AoutB != 0 {
		t[1] = 1
	}
	if op&BoutA != 0 {
		t[2] = 2
	}
	for q := <-c; q.reply != nil; q = <-c {
		x := 0
		if q.d0 != nil {
			x |= 1
		}
		if q.d1 != nil {
			x |= 2
		}
		r := t[x]
		if dflag && r == 0 && x == 3 && q.d0.Mode&q.d1.Mode&sys.DMDIR != 0 {
			r = 3
		}
		q.reply <- r
	}
}
