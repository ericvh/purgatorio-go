package main

import (
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// The built-in commands (acme's exec.b).
type excmd struct {
	name string
	fn   func(et, t, argt *Text, arg string)
	mark bool // marks the file for undo
}

var exectab []excmd

func init() {
	exectab = []excmd{
		{"Cut", func(et, _, _ *Text, _ string) { cut(et, et, true, true) }, true},
		{"Del", func(et, _, _ *Text, _ string) { del(et, false) }, false},
		{"Delcol", func(et, _, _ *Text, _ string) { delcol(et) }, false},
		{"Delete", func(et, _, _ *Text, _ string) { del(et, true) }, false},
		{"Dump", func(et, _, _ *Text, _ string) { warning(nil, "Dump: not implemented\n") }, false},
		{"Exit", func(et, _, _ *Text, _ string) { exit() }, false},
		{"Get", func(et, _, _ *Text, arg string) { get(et, arg, true) }, false},
		{"Kill", func(et, _, _ *Text, arg string) { kill(arg) }, false},
		{"Load", func(et, _, _ *Text, _ string) { warning(nil, "Load: not implemented\n") }, false},
		{"Look", func(et, t, argt *Text, arg string) { lookcmd(et, t, argt, arg) }, false},
		{"New", func(et, t, argt *Text, arg string) { newcmd(et, t, argt, arg) }, false},
		{"Newcol", func(et, _, _ *Text, _ string) { newcol(et) }, false},
		{"Paste", func(et, _, _ *Text, _ string) { paste(et, et, true, true) }, true},
		{"Put", func(et, _, _ *Text, arg string) { put(et, arg) }, false},
		{"Putall", func(et, _, _ *Text, _ string) { putall() }, false},
		{"Redo", func(et, _, _ *Text, _ string) { undo(et, false) }, false},
		{"Snarf", func(et, _, _ *Text, _ string) { cut(et, et, true, false) }, false},
		{"Sort", func(et, _, _ *Text, _ string) { sortcol(et) }, false},
		{"Tab", func(et, _, _ *Text, arg string) { tab(et, arg) }, false},
		{"Undo", func(et, _, _ *Text, _ string) { undo(et, true) }, false},
		{"Zerox", func(et, _, _ *Text, _ string) { zerox(et) }, false},
	}
}

func lookupcmd(s string) *excmd {
	for i := range exectab {
		if exectab[i].name == s {
			return &exectab[i]
		}
	}
	return nil
}

func isblank(c rune) bool { return c == ' ' || c == '\t' || c == '\n' }

// execute runs the text q0 to q1 of t as a command (exec.b's
// execute): a built-in, or a program. With the window's event file
// open and !external, it is sent there instead.
func execute(t *Text, q0, q1 int, external bool, argt *Text) {
	// An empty sweep means the word there, or the selection it is in.
	if q0 == q1 {
		if t.q1 > t.q0 && t.q0 <= q0 && q0 <= t.q1 {
			q0, q1 = t.q0, t.q1
		} else {
			for q1 < t.file.Nc() && !isblank(t.readc(q1)) {
				q1++
			}
			for q0 > 0 && !isblank(t.readc(q0-1)) {
				q0--
			}
		}
		if q0 == q1 {
			return
		}
	}
	r := string(t.file.Read(q0, q1))
	s := strings.TrimSpace(r)
	word, arg := s, ""
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		word, arg = s[:i], strings.TrimSpace(s[i+1:])
	}
	e := lookupcmd(word)
	if !external && t.w != nil && t.w.nopen[qwEvent] > 0 && (t.what == Body || t.what == Tag) {
		c := 'x'
		if t.what == Body {
			c = 'X'
		}
		f := 0
		if e != nil {
			f |= 1
		}
		if argt != nil {
			f |= 8
		}
		t.w.event("M", c, q0, q1, f, []rune(r))
		if argt != nil && argt.q1 > argt.q0 {
			a := argt.file.Read(argt.q0, argt.q1)
			t.w.event("M", c, 0, 0, 0, a)
			t.w.event("M", c, 0, 0, 0, []rune(""))
		}
		return
	}
	if argt != nil && argt.q1 > argt.q0 {
		a := string(argt.file.Read(argt.q0, argt.q1))
		if arg == "" {
			arg = a
		} else {
			arg += " " + a
		}
	}
	if e != nil {
		et := t
		if t.w != nil {
			et = &t.w.body
			if e.mark {
				g.seq++
				et.file.Mark()
			}
		}
		e.fn(et, t, argt, arg)
		if et.w != nil && !et.w.deleted {
			g.retag[et.w] = true
		}
		return
	}
	dir := g.wdir
	if t.w != nil {
		dir = t.w.dir()
	}
	run(dir, s)
}

// cut copies the selection of t to the snarf buffer if dosnarf, and
// deletes it if docut.
func cut(et, t *Text, dosnarf, docut bool) {
	if et.what == Columntag || et.what == Rowtag {
		return
	}
	q0, q1 := et.q0, et.q1
	if q0 == q1 {
		return
	}
	if dosnarf {
		g.snarf = et.file.Read(q0, q1)
		snarfput(string(g.snarf))
	}
	if docut {
		if et.w != nil && et.what == Body {
			et.w.event("M", 'D', q0, q1, 0, nil)
		}
		et.delete(q0, q1, true)
		et.setselect(q0, q0)
		if et.w != nil {
			g.retag[et.w] = true
		}
	}
}

// paste puts the snarf buffer in place of t's selection; with selectall
// it is left selected.
func paste(et, t *Text, selectall, tobody bool) {
	s := snarfget()
	if s != "" {
		g.snarf = []rune(s)
	}
	if len(g.snarf) == 0 || et.what == Columntag || et.what == Rowtag {
		return
	}
	if et.q1 > et.q0 {
		if et.w != nil && et.what == Body {
			et.w.event("M", 'D', et.q0, et.q1, 0, nil)
		}
		et.delete(et.q0, et.q1, true)
	}
	q := et.q0
	et.insert(q, g.snarf, true)
	if et.w != nil && et.what == Body {
		et.w.event("M", 'I', q, q+len(g.snarf), 0, g.snarf)
	}
	if selectall {
		et.setselect(q, q+len(g.snarf))
	} else {
		et.setselect(q+len(g.snarf), q+len(g.snarf))
	}
	if et.what == Body {
		et.show(et.q0, et.q1, true)
	}
	if et.w != nil {
		g.retag[et.w] = true
	}
}

func del(et *Text, flag1 bool) {
	w := et.w
	if w == nil || w.col == nil {
		return
	}
	if flag1 || w.clean(false) {
		w.col.close(w, true)
	}
}

func delcol(et *Text) {
	c := et.col
	if c == nil {
		return
	}
	for _, w := range c.w {
		if w.nopen[qwEvent] > 0 {
			warning(nil, "can't delete column; "+w.body.file.name+" is running an external command\n")
			return
		}
	}
	if !c.clean() {
		return
	}
	g.row.close(c, true)
}

func exit() {
	if g.row.clean() {
		g.exiting = true
	}
}

func newcol(et *Text) {
	c := g.row.add(-1)
	if c != nil {
		c.add(nil, nil, -1)
	}
}

// newcmd makes a new window, or with an argument opens the files named.
func newcmd(et, t, argt *Text, arg string) {
	c := et.col
	if c == nil {
		c = g.row.col[len(g.row.col)-1]
	}
	if arg == "" {
		c.add(nil, nil, -1)
		return
	}
	dir := g.wdir
	if et.w != nil {
		dir = et.w.dir()
	}
	for _, f := range strings.Fields(arg) {
		if !strings.HasPrefix(f, "/") {
			f = path.Join(dir, f)
		}
		openfile(c, f, "")
	}
}

func sortcol(et *Text) {
	if et.col != nil {
		et.col.sort()
	}
}

func tab(et *Text, arg string) {
	w := et.w
	if w == nil {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if arg == "" || err != nil || n <= 0 {
		warning(nil, w.body.file.name+": Tab "+strconv.Itoa(w.body.tabstop)+"\n")
		return
	}
	w.body.tabstop = n
	w.body.fr.maxtab = n * max(1, g.font.Width("0"))
	w.body.dirty()
}

func undo(et *Text, isundo bool) {
	if et.w == nil {
		return
	}
	et.w.undo(isundo)
}

// zerox makes another window on the same file.
func zerox(et *Text) {
	w := et.w
	if w == nil || w.isdir {
		return
	}
	nw := w.col.add(nil, w, -1)
	nw.body.close()
	nw.body.file = w.body.file
	w.body.file.text = append(w.body.file.text, &nw.body)
	nw.isdir = w.isdir
	nw.setname(w.body.file.name)
	nw.body.dirty()
}

// get reads the window's file again (or the file named).
func get(et *Text, arg string, warn bool) {
	w := et.w
	if w == nil {
		return
	}
	name := w.body.file.name
	if arg != "" {
		name = arg
		if !strings.HasPrefix(name, "/") {
			name = path.Join(w.dir(), name)
		}
	}
	if name == "" {
		warning(nil, "no file name\n")
		return
	}
	if warn && w.body.file.Dirty() && !w.isdir && arg == "" {
		if !w.clean(true) {
			return
		}
	}
	loadfile(w, name)
}

// loadfile reads a file or directory into w's body.
func loadfile(w *Window, name string) bool {
	fi, err := os.Stat(name)
	if err != nil {
		warning(nil, "can't open "+name+": "+errstr(err)+"\n")
		return false
	}
	var text string
	if fi.IsDir() {
		w.isdir = true
		ents, err := os.ReadDir(name)
		if err != nil {
			warning(nil, "can't read "+name+": "+errstr(err)+"\n")
			return false
		}
		var ns []string
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			ns = append(ns, n)
		}
		sort.Strings(ns)
		text = strings.Join(ns, "\n")
		if len(ns) > 0 {
			text += "\n"
		}
		if !strings.HasSuffix(name, "/") {
			name += "/"
		}
	} else {
		b, err := os.ReadFile(name)
		if err != nil {
			warning(nil, "can't read "+name+": "+errstr(err)+"\n")
			return false
		}
		w.isdir = false
		text = string(b)
	}
	f := w.body.file
	f.b = []rune(text)
	f.Reset()
	for _, t := range f.text {
		t.q0, t.q1, t.org = 0, 0, 0
		t.dirty()
	}
	w.setname(name)
	return true
}

func errstr(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

// put writes the window's file (or the file named).
func put(et *Text, arg string) {
	w := et.w
	if w == nil || w.isdir {
		return
	}
	name := w.body.file.name
	if arg != "" {
		name = arg
		if !strings.HasPrefix(name, "/") {
			name = path.Join(w.dir(), name)
		}
	}
	if name == "" {
		warning(nil, "no file name\n")
		return
	}
	if err := os.WriteFile(name, []byte(string(w.body.file.b)), 0o664); err != nil {
		warning(nil, "can't write "+name+": "+errstr(err)+"\n")
		return
	}
	if name == w.body.file.name || arg == "" {
		w.body.file.Clean()
	}
	if arg != "" && name != w.body.file.name {
		w.setname(name)
		w.body.file.Clean()
	}
	w.settag()
}

func putall() {
	for _, w := range g.row.windows() {
		if w.body.file.name != "" && !w.isdir && !w.isscratch && w.body.file.Dirty() {
			put(&w.body, "")
		}
	}
}

// A proc is a running command whose output goes to a +Errors window.
type proc struct {
	pid  int
	fd   int
	dir  string
	name string
	buf  []byte
}

// run starts cmd with sh in dir.
func run(dir, cmd string) {
	p, err := sys.Pipe()
	if err != nil {
		warning(&dir, "can't make pipe: "+err.Error()+"\n")
		return
	}
	null, err := sys.Open("/dev/null", sys.OREAD)
	if err != nil {
		null = p[1]
	}
	shcmd := "cd " + shquote(dir) + "; " + cmd
	pid, err := sys.Spawn("/bin/sh.wasm", []string{"sh", "-c", shcmd}, sys.NEWFD|sys.NEWPGRP,
		[][2]int{{0, null}, {1, p[1]}, {2, p[1]}})
	syscall.Close(p[1])
	if null != p[1] {
		syscall.Close(null)
	}
	if err != nil {
		syscall.Close(p[0])
		warning(&dir, "can't run "+cmd+": "+err.Error()+"\n")
		return
	}
	name := cmd
	if i := strings.IndexAny(name, " \t\n"); i >= 0 {
		name = name[:i]
	}
	g.procs = append(g.procs, &proc{pid: pid, fd: p[0], dir: dir, name: name})
}

func shquote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// procoutput reads a command's output into its +Errors window.
func procoutput(fd int) {
	for i, p := range g.procs {
		if p.fd != fd {
			continue
		}
		b := make([]byte, 8192)
		n, err := syscall.Read(fd, b)
		if err != nil || n <= 0 {
			syscall.Close(fd)
			g.procs = append(g.procs[:i], g.procs[i+1:]...)
			if len(p.buf) > 0 {
				warning(&p.dir, string(p.buf))
			}
			return
		}
		p.buf = append(p.buf, b[:n]...)
		// Keep a partial rune for next time.
		k := len(p.buf)
		for k > 0 && k > len(p.buf)-4 && p.buf[k-1]&0xC0 == 0x80 {
			k--
		}
		if k > 0 && p.buf[k-1] >= 0xC0 {
			k--
		}
		if k > 0 {
			warning(&p.dir, string(p.buf[:k]))
			p.buf = p.buf[k:]
		}
		return
	}
}

// kill kills the commands named, or all of them.
func kill(arg string) {
	names := strings.Fields(arg)
	for _, p := range g.procs {
		if len(names) > 0 {
			found := false
			for _, n := range names {
				if n == p.name {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if fd, err := sys.Open("/prog/"+strconv.Itoa(p.pid)+"/ctl", sys.OWRITE); err == nil {
			_, _ = syscall.Write(fd, []byte("killgrp"))
			syscall.Close(fd)
		}
	}
}

// snarfget and snarfput use the snarf device.
func snarfget() string {
	fd, err := sys.Open("/chan/snarf", sys.OREAD)
	if err != nil {
		if fd, err = sys.Open("#^/snarf", sys.OREAD); err != nil {
			return ""
		}
	}
	defer syscall.Close(fd)
	var b []byte
	buf := make([]byte, 8192)
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil || n <= 0 {
			break
		}
		b = append(b, buf[:n]...)
	}
	return string(b)
}

func snarfput(s string) {
	fd, err := sys.Open("/chan/snarf", sys.OWRITE)
	if err != nil {
		if fd, err = sys.Open("#^/snarf", sys.OWRITE); err != nil {
			return
		}
	}
	defer syscall.Close(fd)
	_, _ = syscall.Write(fd, []byte(s))
}
