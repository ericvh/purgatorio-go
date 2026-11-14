package main

import (
	"bufio"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// stdModule is Inferno's std shell module (sh-std(1)).
type stdModule struct{}

var stdmod Module = &stdModule{}

func (m *stdModule) initbuiltin(c *Context) string {
	for _, n := range []string{"if", "while", "~", "!", "apply", "for", "status", "pctl", "fn", "subfn",
		"and", "or", "raise", "rescue", "flag", "getlines", "no"} {
		c.addbuiltin(n, m)
	}
	for _, n := range []string{"hd", "tl", "index", "split", "join", "pid", "parse", "env", "pipe"} {
		c.addsbuiltin(n, m)
	}
	for _, e := range c.envlist() {
		if len(e.name) > 3 && e.name[:3] == "fn-" {
			fndef(c, e.name[3:], e.val, false)
		}
		if len(e.name) > 4 && e.name[:4] == "sfn-" {
			fndef(c, e.name[4:], e.val, true)
		}
	}
	return ""
}

func (m *stdModule) whatis(c *Context, name string, wtype int) string {
	var ename, fname string
	switch wtype {
	case wBUILTIN:
		ename, fname = "fn-", "fn "
	case wSBUILTIN:
		ename, fname = "sfn-", "subfn "
	default:
		return ""
	}
	if val := c.get(ename + name); val != nil {
		return fname + name + " " + quoted(val[:1], false)
	}
	return ""
}

func (m *stdModule) runbuiltin(c *Context, cmd []*Listnode, last bool) string {
	name := cmd[0].word
	if val := c.get("fn-" + name); val != nil {
		return c.run(append([]*Listnode{val[0]}, cmd[1:]...), last)
	}
	switch name {
	case "if":
		return builtinIf(c, cmd)
	case "while":
		return builtinWhile(c, cmd)
	case "and":
		return builtinAnd(c, cmd)
	case "apply":
		return builtinApply(c, cmd)
	case "for":
		return builtinFor(c, cmd)
	case "or":
		return builtinOr(c, cmd)
	case "!":
		args := cmd[1:]
		if len(args) == 0 || c.run(args, last) == "" {
			return "false"
		}
		return ""
	case "fn":
		return builtinFn(c, cmd, false)
	case "subfn":
		return builtinFn(c, cmd, true)
	case "~":
		return builtinTwiddle(c, cmd)
	case "status":
		if len(cmd) > 1 {
			return cmd[1].word
		}
		return ""
	case "pctl":
		return builtinPctl(c, cmd)
	case "raise":
		return builtinRaise(c, cmd)
	case "rescue":
		return builtinRescue(c, cmd, last)
	case "flag":
		return builtinFlag(c, cmd)
	case "getlines":
		return builtinGetlines(c, cmd)
	case "no":
		if len(cmd) > 1 {
			return "yes"
		}
		return ""
	}
	return ""
}

func (m *stdModule) runsbuiltin(c *Context, cmd []*Listnode) []*Listnode {
	name := cmd[0].word
	if val := c.get("sfn-" + name); val != nil {
		return runsubfn(c, val, cmd[1:])
	}
	switch name {
	case "pid":
		pid, _ := sys.Pctl(0, nil)
		return []*Listnode{wordnode(strconv.Itoa(pid))}
	case "hd":
		if len(cmd) < 2 {
			return nil
		}
		return cmd[1:2]
	case "tl":
		if len(cmd) < 2 {
			return nil
		}
		return cmd[2:]
	case "index":
		if len(cmd) < 2 || cmd[1].word == "" {
			builtinusage2(c, "index num list")
		}
		return index(cmd[2:], limboAtoi(cmd[1].word)-1)
	case "split":
		return sbuiltinSplit(c, cmd)
	case "join":
		args := cmd[1:]
		if len(args) == 0 {
			builtinusage2(c, "join separator [arg...]")
		}
		return []*Listnode{wordnode(squash(words(args[1:]), word(args[0])))}
	case "parse":
		return sbuiltinParse(c, cmd)
	case "env":
		var vl []*Listnode
		for _, e := range c.envlist() {
			if e.val != nil {
				vl = append([]*Listnode{wordnode(e.name)}, vl...)
			}
		}
		return vl
	case "pipe":
		return sbuiltinPipe(c, cmd)
	}
	return nil
}

func runsubfn(c *Context, body, args []*Listnode) []*Listnode {
	c.push()
	defer c.pop()
	c.setlocal("result", nil)
	c.run(append([]*Listnode{body[0]}, args...), false)
	return c.get("result")
}

// builtinusage2 is std's usage exception, whose message lacks sh's "sh: ".
func builtinusage2(c *Context, s string) {
	c.fail("usage", "usage: "+s)
}

func sbuiltinParse(c *Context, args []*Listnode) []*Listnode {
	if len(args) != 2 {
		builtinusage2(c, "parse arg")
	}
	a := args[1]
	if a.cmd != nil {
		return []*Listnode{{cmd: a.cmd}}
	}
	w := a.word
	if w == "" || w[0] != '{' {
		c.fail("parse error", "parse: argument must be a braced block")
	}
	n, err := parse(w)
	if err != "" {
		c.fail("parse error", "parse: "+err)
	}
	return []*Listnode{{cmd: n}}
}

func sbuiltinSplit(c *Context, args []*Listnode) []*Listnode {
	n := len(args)
	if n < 2 || n > 3 {
		builtinusage2(c, "split [separators] value")
	}
	var seps string
	if n == 2 {
		ifs := c.get("ifs")
		if ifs == nil {
			c.fail("usage", "split: $ifs not set")
		}
		seps = word(ifs[0])
	} else {
		args = args[1:]
		seps = word(args[0])
	}
	return stringlist(tokenize(word(args[1]), seps))
}

func builtinFn(c *Context, args []*Listnode, issub bool) string {
	title := args[0].word
	if len(args) < 2 {
		builtinusage2(c, title+" [name...] [{body}]")
	}
	names := args[1 : len(args)-1]
	for _, a := range names {
		if a.cmd != nil {
			builtinusage2(c, title+" [name...] [{body}]")
		}
	}
	last := args[len(args)-1]
	if last.cmd != nil {
		for _, a := range names {
			fndef(c, a.word, []*Listnode{last}, issub)
		}
	} else {
		for _, a := range args[1:] {
			fnundef(c, a.word, issub)
		}
	}
	return ""
}

func fndef(c *Context, name string, cmd []*Listnode, issub bool) {
	if cmd == nil {
		return
	}
	if issub {
		c.set("sfn-"+name, cmd)
		c.addsbuiltin(name, stdmod)
	} else {
		c.set("fn-"+name, cmd)
		c.addbuiltin(name, stdmod)
	}
}

func fnundef(c *Context, name string, issub bool) {
	if issub {
		c.set("sfn-"+name, nil)
		c.removesbuiltin(name, stdmod)
	} else {
		c.set("fn-"+name, nil)
		c.removebuiltin(name, stdmod)
	}
}

func builtinFlag(c *Context, args []*Listnode) string {
	n := len(args)
	if n < 2 || n > 3 || len([]rune(args[1].word)) != 1 {
		builtinusage2(c, "flag [vxei] [+-]")
	}
	p := ""
	if n == 3 {
		p = args[2].word
	}
	var mask int
	switch args[1].word {
	case "v":
		mask = optVERBOSE
	case "x":
		mask = optEXECPRINT
	case "e":
		mask = optERROREXIT
	case "i":
		mask = optINTERACTIVE
	default:
		builtinusage2(c, "flag [vxei] [+-]")
	}
	switch p {
	case "":
		if c.options()&mask != 0 {
			return ""
		}
		return "not set"
	case "-":
		c.setoptions(mask, false)
	case "+":
		c.setoptions(mask, true)
	default:
		builtinusage2(c, "flag [vxei] [+-]")
	}
	return ""
}

func iscmd(n *Listnode) bool {
	return n.cmd != nil || (n.word != "" && n.word[0] == '{')
}

func runwith(c *Context, cmd *Listnode, args []*Listnode) string {
	return c.run(append([]*Listnode{cmd}, args...), false)
}

func builtinIf(c *Context, args []*Listnode) string {
	args = args[1:]
	if len(args) < 2 {
		builtinusage2(c, "if {cond} {action} [{cond} {action}]... [{elseaction}]")
	}
	status := ""
	dolstar := c.get("*")
	for len(args) > 0 {
		var cmd *Listnode
		if len(args) == 1 {
			cmd = args[0]
			args = nil
		} else {
			if !iscmd(args[0]) {
				builtinusage2(c, "if [{cond} {action}]... [{elseaction}]")
			}
			status = runwith(c, args[0], dolstar)
			if status == "" {
				cmd = args[1]
				args = nil
			} else {
				args = args[2:]
			}
			setstatus(c, status)
		}
		if cmd != nil {
			if !iscmd(cmd) {
				builtinusage2(c, "if [{cond} {action}]... [{elseaction}]")
			}
			status = runwith(c, cmd, dolstar)
		}
	}
	return status
}

func builtinOr(c *Context, args []*Listnode) string {
	s := ""
	dolstar := c.get("*")
	for _, a := range args[1:] {
		if !iscmd(a) {
			builtinusage2(c, "or [{cmd} ...]")
		}
		if s = runwith(c, a, dolstar); s == "" {
			return ""
		}
		setstatus(c, s)
	}
	return s
}

func builtinAnd(c *Context, args []*Listnode) string {
	dolstar := c.get("*")
	for _, a := range args[1:] {
		if !iscmd(a) {
			builtinusage2(c, "and [{cmd} ...]")
		}
		if s := runwith(c, a, dolstar); s != "" {
			return s
		}
		setstatus(c, "")
	}
	return ""
}

const (
	loopContinue = iota
	loopBreak
)

// loopexcept classifies a loop's exception; any other is raised again.
func loopexcept(e string) int {
	switch {
	case isfail(e) && e[5:] == "break":
		return loopBreak
	case isfail(e) && e[5:] == "continue":
		return loopContinue
	}
	raise(e)
	return 0
}

func builtinWhile(c *Context, args []*Listnode) string {
	args = args[1:]
	if len(args) != 2 || !iscmd(args[0]) || !iscmd(args[1]) {
		builtinusage2(c, "while {condition} {cmd}")
	}
	dolstar := c.get("*")
	status := ""
	for {
		e, raised := catch(func() {
			for runwith(c, args[0], dolstar) == "" {
				status = setstatus(c, runwith(c, args[1], dolstar))
			}
		})
		if !raised || loopexcept(e) == loopBreak {
			return status
		}
	}
}

func builtinGetlines(c *Context, argv []*Listnode) string {
	n := len(argv)
	if n < 2 || n > 3 {
		builtinusage2(c, "getlines [separators] {cmd}")
	}
	argv = argv[1:]
	seps := "\n"
	if n == 3 {
		seps = word(argv[0])
		argv = argv[1:]
	}
	if seps == "" || !iscmd(argv[0]) {
		builtinusage2(c, "getlines [separators] {cmd}")
	}
	cmd := argv[0]
	dolstar := c.get("*")
	fd, ok := c.fds.get(0)
	if !ok {
		c.fail("bad input", "getlines: cannot open stdin: fd out of range or not open")
	}
	in := bufio.NewReader(fdreader(fd))
	sepr := []rune(seps)
	status := ""
	c.push()
	defer c.pop()
	for {
		e, raised := catch(func() {
			for {
				s := gett(in, sepr)
				if s == "" {
					break
				}
				r := []rune(s)
				if last := r[len(r)-1]; strings.ContainsRune(seps, last) {
					s = string(r[:len(r)-1])
				}
				c.setlocal("line", []*Listnode{wordnode(s)})
				status = setstatus(c, runwith(c, cmd, dolstar))
			}
		})
		if !raised || loopexcept(e) == loopBreak {
			return status
		}
	}
}

// gett reads up to and including a character in seps, or to the end.
func gett(in *bufio.Reader, seps []rune) string {
	var b []rune
	for {
		r, _, err := in.ReadRune()
		if err != nil {
			return string(b)
		}
		b = append(b, r)
		for _, s := range seps {
			if r == s {
				return string(b)
			}
		}
	}
}

func builtinRaise(c *Context, args []*Listnode) string {
	var ename *Listnode
	if len(args) < 2 {
		e := c.get("exception")
		if e == nil {
			c.fail("bad raise context", "raise: no exception found")
		}
		ename = e[0]
	} else {
		ename = args[1]
	}
	if ename.word == "" && ename.cmd != nil {
		c.fail("bad raise context", "raise: bad exception name")
	}
	s := "fail:" + ename.word
	if len(s) > waitlen {
		s = s[:waitlen]
	}
	raise(s)
	return ""
}

// waitlen is Sys->WAITLEN, the longest exception raise allows.
const waitlen = 192

func builtinRescue(c *Context, args []*Listnode, last bool) string {
	args = args[1:]
	if len(args) != 3 || !iscmd(args[1]) || !iscmd(args[2]) {
		builtinusage2(c, "rescue pattern {rescuecmd} {cmd}")
	}
	if args[0].word == "" && args[0].cmd != nil {
		c.fail("usage", "rescue: bad pattern")
	}
	dolstar := c.get("*")
	var status string
	e, raised := catch(func() { status = runwith(c, args[2], dolstar) })
	if !raised {
		return status
	}
	if !isfail(e) {
		raise(e)
	}
	c.push()
	c.set("exception", []*Listnode{wordnode(e[5:])})
	_, raised2 := catch(func() { status = c.run(append([]*Listnode{args[1]}, dolstar...), last) })
	c.pop()
	if raised2 {
		raise(e)
	}
	return status
}

func builtinFor(c *Context, args []*Listnode) string {
	const usage = "for var in [item...] {cmd}"
	args = args[1:]
	if len(args) == 0 {
		builtinusage2(c, usage)
	}
	v := args[0].word
	if v == "" {
		c.fail("bad assign", "for: bad variable name")
	}
	args = args[1:]
	if len(args) == 0 || args[0].word != "in" {
		builtinusage2(c, usage)
	}
	args = args[1:]
	if len(args) == 0 {
		builtinusage2(c, usage)
	}
	cmd := args[len(args)-1]
	if !iscmd(cmd) {
		builtinusage2(c, usage)
	}
	items := args[:len(args)-1]
	status := ""
	dolstar := c.get("*")
	for {
		e, raised := catch(func() {
			for len(items) > 0 {
				c.setlocal(v, []*Listnode{items[0]})
				status = setstatus(c, runwith(c, cmd, dolstar))
				items = items[1:]
			}
		})
		if !raised || loopexcept(e) == loopBreak {
			return status
		}
		items = items[1:]
	}
}

func builtinApply(c *Context, args []*Listnode) string {
	args = args[1:]
	if len(args) == 0 || !iscmd(args[0]) {
		builtinusage2(c, "apply {cmd} [val...]")
	}
	status := ""
	cmd := args[0]
	args = args[1:]
	for {
		e, raised := catch(func() {
			for len(args) > 0 {
				a := args[0]
				args = args[1:]
				status = setstatus(c, c.run([]*Listnode{cmd, a}, false))
			}
		})
		if !raised || loopexcept(e) == loopBreak {
			return status
		}
	}
}

var pctlnames = []struct {
	name string
	mask int
}{
	{"newfd", sys.NEWFD},
	{"forkfd", sys.FORKFD},
	{"newns", sys.NEWNS},
	{"forkns", sys.FORKNS},
	{"newpgrp", sys.NEWPGRP},
	{"nodevs", sys.NODEVS},
}

func isnum(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func builtinPctl(c *Context, argv []*Listnode) string {
	if len(argv) < 2 {
		builtinusage2(c, "pctl option... [fdnum...]")
	}
	mask := 0
	var fdlist []int
	for _, a := range argv[1:] {
		w := a.word
		if isnum(w) {
			fdlist = append(fdlist, limboAtoi(w))
			continue
		}
		found := false
		for _, p := range pctlnames {
			if p.name == w {
				mask |= p.mask
				found = true
				break
			}
		}
		if !found {
			c.fail("usage", "pctl: unknown flag "+w)
		}
	}
	dopctl(c, mask, fdlist)
	return ""
}

// dopctl does Sys->pctl for the shell. The fds named are the ones
// commands see, so newfd keeps the fds behind them, and sh's wait file.
func dopctl(c *Context, mask int, fdlist []int) {
	if mask&sys.NEWFD == 0 {
		_, _ = sys.Pctl(mask, nil)
		return
	}
	keep := []int{waitfd}
	nt := &fdtab{m: map[int]int{}}
	for _, fd := range fdlist {
		if r, ok := c.fds.get(fd); ok {
			keep = append(keep, r)
			nt.m[fd] = r
		}
	}
	_, _ = sys.Pctl(mask, keep)
	c.fds.m = nt.m
}

func builtinTwiddle(c *Context, argv []*Listnode) string {
	argv = argv[1:]
	if len(argv) == 0 {
		builtinusage2(c, "~ word [pattern...]")
	}
	if len(argv) == 1 {
		return "no match"
	}
	w := word(argv[0])
	for _, p := range argv[1:] {
		if match(word(p), w) {
			return ""
		}
	}
	return "no match"
}

// sbuiltinPipe runs cmd with one of its fds on a pipe and yields a name
// for the other end. Inferno names a file in /tmp/pipes; here it is
// /fd/n, an fd sh keeps open and gives to the commands it runs.
func sbuiltinPipe(c *Context, argv []*Listnode) []*Listnode {
	if len(argv) != 3 || !iscmd(argv[2]) {
		builtinusage2(c, "pipe (from|to|fdnum) {cmd}")
	}
	var n int
	switch s := argv[1].word; s {
	case "from":
		n = 1
	case "to":
		n = 0
	default:
		if !isnum(s) {
			builtinusage2(c, "pipe (from|to|fdnum) {cmd}")
		}
		n = limboAtoi(s)
	}
	p, err := sys.Pipe()
	if err != nil {
		c.fail("bad pipe", "pipe: cannot make pipe: "+sys.Errstr())
	}
	runasync(c, append([]*Listnode{argv[2]}, c.get("*")...),
		&Redirlist{[]Redirword{{p[1], "", Redir{oRDWR, n, -1}}}})
	c.fds.m[p[0]] = p[0]
	return []*Listnode{wordnode("/fd/" + strconv.Itoa(p[0]))}
}
