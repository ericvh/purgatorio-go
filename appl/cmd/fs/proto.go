package main

// proto: a tree described by an mkfs(8) proto file.

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

type pfile struct {
	name  string
	mode  uint32
	owner string
	group string
	old   string
	flags int
	sub   []*pfile
}

type proto struct {
	indent   int
	lastline string
	haveline bool
	iob      *bufio.Reader
}

const (
	pStar = 1 << iota
	pPlus
	pEmpty
)

const nomode = ^uint32(0)

func protorun(r *Report, opts []option, args []value) value {
	protofile := vs(args[0])
	rootpath := ""
	if opts != nil {
		rootpath = vs(opts[0].args[0])
	}
	if rootpath == "" {
		rootpath = "/"
	}
	fd, err := sys.Open(protofile, sys.OREAD)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fs: proto: cannot open %s: %s\n", quote.Quote(protofile), err)
		return nil
	}
	p := &proto{iob: bufio.NewReader(fdreader(fd))}
	root := &pfile{name: rootpath, mode: nomode}
	root.flags, root.sub = readproto(p, -1)
	sys.Close(fd)
	c := make(fschan)
	errorc := r.start("proto")
	proc(func() {
		protowalk1(c, root.flags, root.name, file2dir(root, nil), root.sub, errorc)
		quit(errorc)
	})
	return vX{c}
}

func pathconcat(p, name string) string {
	if p != "" && p[len(p)-1] != '/' {
		p += "/"
	}
	return p + name
}

func protowalk1(c fschan, flags int, path string, d *sys.Dir, sub []*pfile, errorc chan string) int {
	reply := make(chan int)
	c <- fsmsg{fsdata{d, nil}, reply}
	switch r := <-reply; r {
	case Quit:
		quit(errorc)
	case Next, Skip:
		return r
	}
	var a []*sys.Dir
	if flags&pEmpty == 0 {
		a, _ = readdir(path)
	}
	n := len(a)
	i, j := 0, 0
	prevsub := ""
	for i < n || j < len(sub) {
		for ; j < len(sub); j++ {
			s := sub[j].name
			if s == prevsub {
				report(errorc, "duplicate entry "+pathconcat(path, s))
				continue
			}
			if sub[j].old != "" && i < n && s == a[i].Name {
				i++
			}
			if sub[j].old != "" || i < n && s >= a[i].Name {
				break
			}
			report(errorc, pathconcat(path, s)+" not found")
		}
		foundsub := j < len(sub) && (sub[j].old != "" || i < n && sub[j].name == a[i].Name)
		if !foundsub && flags&(pPlus|pStar) == 0 {
			i++
			continue
		}
		var f *pfile
		if foundsub {
			f = sub[j]
			j++
			prevsub = f.name
		}
		var p string
		var dd *sys.Dir
		if foundsub && f.old != "" {
			p = f.old
			xd, err := sys.Stat(p)
			if err != nil {
				report(errorc, fmt.Sprintf("cannot stat %s: %s", quote.Quote(p), err))
				continue
			}
			dd = xd
		} else {
			if i >= n {
				break
			}
			p = pathconcat(path, a[i].Name)
			dd = a[i]
			i++
		}
		dd = file2dir(f, dd)
		var r int
		switch {
		case dd.Mode&sys.DMDIR == 0:
			r = sendfile(p, dd, c, atomicio, errorc)
		case flags&pPlus != 0:
			r = protowalk1(c, pPlus, p, dd, nil, errorc)
		case flags&pStar != 0 && !foundsub:
			r = protowalk1(c, pEmpty, p, dd, nil, errorc)
		default:
			r = protowalk1(c, f.flags, p, dd, f.sub, errorc)
		}
		if r == Skip {
			return Next
		}
	}
	c <- fsmsg{fsdata{}, reply}
	if <-reply == Quit {
		quit(errorc)
	}
	return Next
}

func readproto(p *proto, indent int) (int, []*pfile) {
	var a []*pfile
	flags := 0
	for {
		f := readline(p, indent)
		if f == nil {
			break
		}
		switch f.name {
		case "*":
			flags |= pStar
		case "+":
			flags |= pPlus
		default:
			f.flags, f.sub = readproto(p, p.indent)
			a = append(a, f)
		}
	}
	sort.SliceStable(a, func(i, j int) bool { return a[i].name < a[j].name })
	return flags, a
}

func readline(p *proto, indent int) *pfile {
	for {
		var s string
		if p.haveline {
			s = p.lastline
			p.haveline = false
		} else if p.indent == -1 {
			return nil
		} else {
			var err error
			s, err = p.iob.ReadString('\n')
			if s == "" && err != nil {
				p.indent = -1
				return nil
			}
		}
		spc := 0
		i := 0
		for ; i < len(s); i++ {
			if s[i] == ' ' {
				spc++
			} else if s[i] == '\t' {
				spc += 8
			} else {
				break
			}
		}
		if i == len(s) || s[i] == '#' || s[i] == '\n' {
			continue
		}
		if spc <= indent {
			p.lastline = s
			p.haveline = true
			return nil
		}
		p.indent = spc
		toks := tokenize(s, " \t\n")
		f := &pfile{mode: nomode}
		f.name, toks = getname(toks[0], false), toks[1:]
		if len(toks) == 0 {
			return f
		}
		f.mode, toks = getmode(toks[0]), toks[1:]
		if len(toks) == 0 {
			return f
		}
		f.owner, toks = getname(toks[0], true), toks[1:]
		if len(toks) == 0 {
			return f
		}
		f.group, toks = getname(toks[0], true), toks[1:]
		if len(toks) == 0 {
			return f
		}
		f.old = toks[0]
		return f
	}
}

// getname: $var is meant to be an environment variable, which
// Inferno's fs leaves unimplemented: it reads as nothing.
func getname(s string, allowminus bool) string {
	if s == "" || allowminus && s == "-" {
		return ""
	}
	if s[0] == '$' {
		return ""
	}
	return s
}

func getmode(s string) uint32 {
	s = getname(s, true)
	if s == "" {
		return nomode
	}
	var m uint32
	i := 0
	if s[i] == 'd' {
		m |= sys.DMDIR
		i++
	}
	if i < len(s) && s[i] == 'a' {
		m |= sys.DMAPPEND
		i++
	}
	if i < len(s) && s[i] == 'l' {
		m |= sys.DMEXCL
		i++
	}
	x, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return nomode
	}
	return uint32(x) | m
}

func file2dir(f *pfile, old *sys.Dir) *sys.Dir {
	d := nulldir()
	if old != nil {
		if old.Type != 'M' {
			d.UID = "sys"
			d.GID = "sys"
			xmode := (old.Mode >> 6) & 7
			d.Mode = old.Mode | xmode | xmode<<3
		} else {
			d.UID = old.UID
			d.GID = old.GID
			d.Mode = old.Mode
		}
		d.Length = old.Length
		d.Mtime = old.Mtime
		d.Atime = old.Atime
		d.MUID = old.MUID
		d.Name = old.Name
	}
	if f != nil {
		d.Name = f.name
		if f.owner != "" {
			d.UID = f.owner
		}
		if f.group != "" {
			d.GID = f.group
		}
		if f.mode != nomode {
			d.Mode = f.mode
		}
	}
	return d
}
