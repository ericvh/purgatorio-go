package main

import (
	"io"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Redirword is one redirection to make: an fd sh already has open (rfd),
// a file to open (path), or, with neither, a dup.
type Redirword struct {
	rfd  int
	path string
	r    Redir
}

type Redirlist struct {
	r []Redirword
}

func (rl *Redirlist) prepend(w Redirword) {
	rl.r = append([]Redirword{w}, rl.r...)
}

// pipe2cmd turns a background pipeline a|b into {a|b} $*.
func pipe2cmd(n *Node) *Node {
	if n == nil || n.typ != nPIPE {
		return n
	}
	return mk(nADJ, mk(nBLOCK, n, nil), mk(nVAR, &Node{typ: nWORD, word: "*"}, nil))
}

// walk runs a parsed command and returns its status. last is set when
// it is the last thing this shell will do.
func walk(c *Context, n *Node, last bool) string {
	for n != nil && n.typ == nSEQ {
		status := walk(c, n.left, false)
		if c.options()&optERROREXIT != 0 && status != "" {
			raise("fail:" + status)
		}
		setstatus(c, status)
		n = n.right
	}
	if n == nil {
		return ""
	}
	switch n.typ {
	case nPIPE:
		return waitfor(c, walkpipeline(c, n, -1, -1))
	case nASSIGN, nLOCAL:
		assign(c, n)
		return ""
	}
	bg := false
	if n.typ == nNOWAIT {
		bg = true
		n = pipe2cmd(n.left)
	}
	redirs := &Redirlist{}
	line := glob(glom(c, n, redirs, nil))
	if bg {
		j := runasync(c, line, redirs)
		if j.done {
			c.set("apid", nil)
		} else {
			c.set("apid", []*Listnode{wordnode(strconv.Itoa(j.pid))})
		}
		return ""
	}
	return runsync(c, line, redirs, last)
}

func assign(c *Context, n *Node) []*Listnode {
	redirs := &Redirlist{}
	var val []*Listnode
	if n.right != nil && (n.right.typ == nASSIGN || n.right.typ == nLOCAL) {
		val = assign(c, n.right)
	} else {
		val = glob(glom(c, n.right, redirs, nil))
	}
	vars := glom(c, n.left, redirs, nil)
	if len(vars) == 0 {
		c.fail("bad assign", "sh: nil variable name")
	}
	if len(redirs.r) > 0 {
		c.fail("bad assign", "sh: redirections not allowed in assignment")
	}
	tval := val
	for i, v := range vars {
		vname := deglob(v.word)
		if vname == "" {
			c.fail("bad assign", "sh: bad variable name")
		}
		var vv []*Listnode
		if i == len(vars)-1 {
			vv = append(vv, tval...)
		} else if len(tval) > 0 {
			vv = []*Listnode{tval[0]}
		}
		if n.typ == nASSIGN {
			c.set(vname, vv)
		} else {
			c.setlocal(vname, vv)
		}
		if len(tval) > 0 {
			tval = tval[1:]
		}
	}
	return val
}

// walkpipeline starts each command of a pipeline, left to right, and
// returns their jobs in that order.
func walkpipeline(c *Context, n *Node, wrpipe, wfdno int) []job {
	if n == nil {
		if wrpipe >= 0 {
			closefd(wrpipe)
		}
		return nil
	}
	var jobs []job
	rfd, rfdno := -1, -1
	if n.typ == nPIPE {
		p, err := sys.Pipe()
		if err != nil {
			c.fail("no pipe", "sh: cannot make pipe: "+sys.Errstr())
		}
		nwfdno := -1
		if n.redir != nil {
			fd1, fd2 := n.redir.fd2, n.redir.fd1
			if fd2 == -1 {
				fd1, fd2 = fd2, fd1
			}
			nwfdno, rfdno = fd2, fd1
		}
		jobs = walkpipeline(c, n.left, p[1], nwfdno)
		rfd = p[0]
		n = n.right
	}
	r := &Redirlist{}
	rlist := glob(glom(c, n, r, nil))
	if rfd >= 0 {
		if rfdno == -1 {
			rfdno = 0
		}
		r.prepend(Redirword{rfd, "", Redir{oREAD, rfdno, -1}})
	}
	if wrpipe >= 0 {
		if wfdno == -1 {
			wfdno = 1
		}
		r.prepend(Redirword{wrpipe, "", Redir{oWRITE, wfdno, -1}})
	}
	return append(jobs, runasync(c, rlist, r))
}

// glom expands the words of n, collecting its redirections.
func glom(c *Context, n *Node, redirs *Redirlist, onto []*Listnode) []*Listnode {
	if n == nil {
		return nil
	}
	if n.typ != nADJ {
		return listjoin(glomoperation(c, n, redirs), onto)
	}
	nlist := glom(c, n.right, redirs, onto)
	if n.left.typ != nADJ {
		nlist = listjoin(glomoperation(c, n.left, redirs), nlist)
	} else {
		nlist = glom(c, n.left, redirs, nlist)
	}
	return nlist
}

func listjoin(left, right []*Listnode) []*Listnode {
	r := make([]*Listnode, 0, len(left)+len(right))
	r = append(r, left...)
	return append(r, right...)
}

func pipecmd(c *Context, cmd []*Listnode, redir *Redir) int {
	if redir.fd2 != -1 || redir.rtype&oappend != 0 {
		c.fail("bad redir", "sh: bad redirection")
	}
	r := *redir
	switch redir.rtype {
	case oREAD:
		r.rtype = oWRITE
	case oWRITE:
		r.rtype = oREAD
	}
	p, err := sys.Pipe()
	if err != nil {
		c.fail("no pipe", "sh: cannot make pipe: "+sys.Errstr())
	}
	runasync(c, cmd, &Redirlist{[]Redirword{{p[1], "", r}}})
	return p[0]
}

func glomoperation(c *Context, n *Node, redirs *Redirlist) []*Listnode {
	if n == nil {
		return nil
	}
	var nlist []*Listnode
	switch n.typ {
	case nWORD:
		nlist = []*Listnode{wordnode(n.word)}
	case nREDIR:
		wlist := glob(glom(c, n.left, &Redirlist{}, nil))
		if len(wlist) != 1 {
			c.fail("bad redir", "sh: single redirection operand required")
		}
		if wlist[0].cmd != nil {
			fd := pipecmd(c, wlist, n.redir)
			redirs.prepend(Redirword{fd, "", Redir{n.redir.rtype, fd, -1}})
			nlist = []*Listnode{wordnode("/fd/" + strconv.Itoa(fd))}
		} else {
			redirs.prepend(Redirword{-1, wlist[0].word, *n.redir})
		}
	case nDUP:
		redirs.prepend(Redirword{-1, "", *n.redir})
	case nLIST:
		nlist = glom(c, n.left, redirs, nil)
	case nCONCAT:
		nlist = concat(c, glom(c, n.left, redirs, nil), glom(c, n.right, redirs, nil))
	case nVAR, nSQUASH, nCOUNT:
		arg := glom(c, n.left, &Redirlist{}, nil)
		if len(arg) == 1 && arg[0].cmd != nil {
			nlist = subsbuiltin(c, arg[0].cmd.left)
		} else if len(arg) != 1 || arg[0].word == "" {
			c.fail("bad $ arg", "sh: bad variable name")
		} else {
			nlist = c.get(deglob(arg[0].word))
		}
		switch n.typ {
		case nCOUNT:
			nlist = []*Listnode{wordnode(strconv.Itoa(len(nlist)))}
		case nSQUASH:
			nlist = []*Listnode{wordnode(squash(words(nlist), " "))}
		}
	case nBQ, nBQ2:
		arg := glom(c, n.left, &Redirlist{}, nil)
		seps := ""
		if n.typ == nBQ {
			seps = squash(words(c.get("ifs")), "")
			if seps == "" {
				seps = " \t\n\r"
			}
		}
		nlist = bq(c, glob(arg), seps)
	case nBLOCK:
		nlist = []*Listnode{{cmd: n}}
	case nASSIGN, nLOCAL:
		c.fail("bad assign", "sh: assignment in invalid context")
	default:
		shpanic("bad node type " + strconv.Itoa(n.typ) + " in glomop")
	}
	return nlist
}

func subsbuiltin(c *Context, n *Node) []*Listnode {
	if n == nil || n.typ == nSEQ || n.typ == nPIPE || n.typ == nNOWAIT {
		c.fail("bad $ arg", "sh: invalid argument to ${} operator")
	}
	r := &Redirlist{}
	cmd := glob(glom(c, n, r, nil))
	if len(r.r) > 0 {
		c.fail("bad $ arg", "sh: redirection not allowed in substitution")
	}
	if len(cmd) == 0 || cmd[0].word == "" || cmd[0].cmd != nil {
		c.fail("bad $ arg", "sh: bad builtin name")
	}
	_, mods := c.env.sbuiltins.find(cmd[0].word)
	if mods == nil {
		c.fail("builtin not found", "sh: builtin "+cmd[0].word+" not found")
	}
	return mods[0].runsbuiltin(c, cmd)
}

// bq runs cmd with its standard output on a pipe and returns what it
// wrote, split at characters in seps unless seps is empty.
func bq(c *Context, cmd []*Listnode, seps string) []*Listnode {
	p, err := sys.Pipe()
	if err != nil {
		c.fail("no pipe", "sh: cannot make pipe: "+sys.Errstr())
	}
	j := runasync(c, cmd, &Redirlist{[]Redirword{{p[1], "", Redir{oWRITE, 1, -1}}}})
	b, _ := io.ReadAll(fdreader(p[0]))
	closefd(p[0])
	waitfor(c, []job{j})
	if j.exc != "" {
		raise(j.exc)
	}
	if seps != "" {
		return stringlist(tokenize(string(b), seps))
	}
	return []*Listnode{wordnode(string(b))}
}

func concatwords(p1, p2 *Listnode) *Listnode {
	return wordnode(word(p1) + word(p2))
}

func concat(c *Context, nl1, nl2 []*Listnode) []*Listnode {
	if len(nl1) == 0 || len(nl2) == 0 {
		if len(nl1) == 0 && len(nl2) == 0 {
			return nil
		}
		c.fail("bad concatenation", "sh: null list in concatenation")
	}
	var ret []*Listnode
	if len(nl1) == 1 || len(nl2) == 1 {
		for _, p1 := range nl1 {
			for _, p2 := range nl2 {
				ret = append(ret, concatwords(p1, p2))
			}
		}
		return ret
	}
	if len(nl1) != len(nl2) {
		c.fail("bad concatenation", "sh: lists of differing sizes can't be concatenated")
	}
	for i := range nl1 {
		ret = append(ret, concatwords(nl1[i], nl2[i]))
	}
	return ret
}

// runsync runs a command, applying its redirections for its duration.
func runsync(c *Context, argv []*Listnode, redirs *Redirlist, last bool) string {
	if len(redirs.r) == 0 {
		return c.run(argv, last)
	}
	saved := c.fds
	c.fds = saved.clone()
	var owned []int
	defer func() {
		c.fds = saved
		for _, fd := range owned {
			closefd(fd)
		}
	}()
	owned = doredirs(c, redirs)
	return c.run(argv, last)
}

// doredirs applies redirs to c.fds and returns the fds it opened, which
// the caller closes when the command is done.
func doredirs(c *Context, redirs *Redirlist) (owned []int) {
	defer func() {
		if r := recover(); r != nil {
			for _, fd := range owned {
				closefd(fd)
			}
			panic(r)
		}
	}()
	for _, rw := range redirs.r {
		mode, fd1, fd2 := rw.r.rtype, rw.r.fd1, rw.r.fd2
		if rw.path == "" && rw.rfd < 0 {
			if fd1 == -1 || fd2 == -1 {
				c.fail("bad redir", "sh: invalid dup")
			}
			r, ok := c.fds.get(fd2)
			if !ok {
				c.fail("bad redir", "sh: cannot dup: fd out of range or not open")
			}
			c.fds.m[fd1] = r
			continue
		}
		if fd1 == -1 {
			if mode&omask == oWRITE {
				fd1 = 1
			} else {
				fd1 = 0
			}
		}
		rfd := rw.rfd
		if rfd < 0 {
			path := rw.path
			isappend, omode := mode&oappend != 0, mode&^oappend
			err := ""
			var e error
			rfd = -1
			switch mode {
			case oREAD:
				rfd, e = sys.Open(path, omode)
			case oWRITE | oappend, oRDWR:
				rfd, e = sys.Open(path, omode)
				if e != nil {
					err = sys.Errstr()
					if nonexistent(err) {
						rfd, e = sys.Create(path, omode, 0o666)
						err = ""
					}
				}
			case oWRITE:
				rfd, e = sys.Create(path, omode, 0o666)
				if e != nil {
					err = sys.Errstr()
					if err == "permission denied" {
						rfd, e = sys.Open(path, omode)
						if e != nil {
							if nerr := sys.Errstr(); !nonexistent(nerr) {
								err = nerr
							}
						}
					}
				}
			}
			if e != nil || rfd < 0 {
				if err == "" {
					err = sys.Errstr()
				}
				c.fail("bad redir", "sh: cannot open "+path+": "+err)
			}
			if isappend {
				_, _ = sys.Seek(rfd, 0, 2)
			}
		}
		owned = append(owned, rfd)
		c.fds.m[fd1] = rfd
	}
	return owned
}

// run runs a command line: a block, a builtin or an external command.
func (c *Context) run(args []*Listnode, last bool) string {
	if len(args) == 0 || (args[0].cmd == nil && args[0].word == "") {
		return ""
	}
	cmd := args[0]
	if cmd.cmd != nil || cmd.word[0] == '{' {
		return runblock(c, args, last)
	}
	if c.options()&optEXECPRINT != 0 {
		c.eprint(quoted(args, false) + "\n")
	}
	if ok, status := trybuiltin(c, args, last); ok {
		return status
	}
	return runexternal(c, args)
}

func trybuiltin(c *Context, args []*Listnode, last bool) (bool, string) {
	_, mods := c.env.builtins.find(args[0].word)
	if mods == nil {
		return false, ""
	}
	return true, mods[0].runbuiltin(c, args, last)
}

func runblock(c *Context, args []*Listnode, last bool) string {
	cmd := args[0].cmd
	if cmd == nil {
		var err string
		cmd, err = parse(args[0].word)
		if cmd == nil {
			if err == "" {
				// An empty block, such as '{}', parses to nothing.
				return ""
			}
			c.fail("parse error", "sh: "+err)
		}
		args[0].cmd = cmd
	}
	c.push()
	defer c.pop()
	c.setlocal("0", []*Listnode{args[0]})
	c.setlocal("*", append([]*Listnode(nil), args[1:]...))
	if cmd.typ == nBLOCK {
		cmd = cmd.left
	}
	return walk(c, cmd, last)
}

// Globbing.

func containswildchar(s string) bool {
	for i := 0; i < len(s)-1; i++ {
		if s[i] == globc {
			switch s[i+1] {
			case '*', '[', '?', globc:
				return true
			}
		}
	}
	return false
}

// patquote removes glob marks and escapes the wildcards they do not
// mark; a marked [~ becomes [^.
func patquote(word string) string {
	w := []byte(word)
	out := make([]byte, 0, len(w))
	for i := 0; i < len(w); i++ {
		switch w[i] {
		case '[', '*', '?', '\\':
			out = append(out, '\\')
		case globc:
			i++
			if i >= len(w) {
				return string(out)
			}
			if w[i] == '[' && i < len(w)-1 && w[i+1] == '~' {
				w[i+1] = '^'
			}
		}
		out = append(out, w[i])
	}
	return string(out)
}

func deglob(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != globc {
			b = append(b, s[i])
		}
	}
	return string(b)
}

func glob(nl []*Listnode) []*Listnode {
	var r []*Listnode
	for _, n := range nl {
		if containswildchar(n.word) {
			files := expand(patquote(n.word))
			if len(files) == 0 {
				files = []string{deglob(n.word)}
			}
			r = append(r, stringlist(files)...)
		} else {
			r = append(r, n)
		}
	}
	return r
}
