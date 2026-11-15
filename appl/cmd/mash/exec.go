package main

import (
	"io"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Exceptions, as panics.
type (
	// mashRaise is a raised exception, such as "fail: mash error".
	mashRaise string
	// mashExit is the exit builtin's exception.
	mashExit struct{}
	// threadExit ends the current thread, as Limbo's exit.
	threadExit struct{}
)

const (
	failpfx = "fail:"
	epipe   = "write on closed pipe"
	devnull = "/dev/null"
	mashbin = "/bin/mash.wasm"
)

func isfail(s string) bool { return strings.HasPrefix(s, failpfx) }

// exits raises "fail: mash s".
func exits(s string) { panic(mashRaise("fail: mash " + s)) }

func errstr() string { return sys.Errstr() }

// nonexistent reports whether e says a file does not exist.
func nonexistent(e string) bool {
	return strings.HasSuffix(e, "does not exist") || strings.HasSuffix(e, "directory entry not found")
}

// fdreader reads a raw fd.
type fdreader int

func (f fdreader) Read(b []byte) (int, error) {
	n, err := syscall.Read(int(f), b)
	if err != nil {
		return 0, sys.Error(sys.Errstr())
	}
	if n == 0 && len(b) > 0 {
		return 0, io.EOF
	}
	return n, nil
}

func writefd(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := syscall.Write(fd, b)
		if err != nil || n <= 0 {
			return sys.Error(sys.Errstr())
		}
		b = b[n:]
	}
	return nil
}

// fprint writes s to fd, ignoring errors.
func fprint(fd int, s string) { _ = writefd(fd, []byte(s)) }

func closefd(fd int) {
	if fd >= 0 {
		_ = syscall.Close(fd)
	}
}

func closefds(l []int) {
	for _, fd := range l {
		closefd(fd)
	}
}

// genfds are the fds of the <{} and >{} generators of the commands
// being evaluated; each command started gets them all.
var genfds []int

func closegen(mark int) {
	if mark < len(genfds) {
		closefds(genfds[mark:])
		genfds = genfds[:mark]
	}
}

func (e *Env) diag(s string) string { return where(e) + s }

func (e *Env) usage(s string) { e.report("usage: " + s) }

// report prints a diagnostic; under -e it ends mash.
func (e *Env) report(s string) {
	fprint(e.stderr, e.diag(s)+"\n")
	if e.flags&ERaise != 0 {
		exits("error")
	}
}

// couldnot reports a system error, which ends mash.
func (e *Env) couldnot(what, who string) {
	fprint(e.stderr, "could not "+what+" "+who+": "+errstr()+"\n")
	exits("system error")
}

// stdout is the fd a command's standard output goes to.
func (e *Env) stdout() int {
	if e.out >= 0 {
		return e.out
	}
	return 1
}

func (e *Env) stdin() int {
	if e.in >= 0 {
		return e.in
	}
	return 0
}

// output writes s to standard output. A write on a closed pipe is an
// exception, as in Limbo.
func (e *Env) output(s string) {
	if s == "" {
		return
	}
	if err := writefd(e.stdout(), []byte(s)); err != nil && err.Error() == epipe {
		panic(mashRaise(epipe))
	}
}

func (e *Env) devnull() int {
	fd, err := sys.Open(devnull, sys.OREAD)
	if err != nil {
		e.couldnot("open", devnull)
	}
	return fd
}

func (e *Env) pipe() []int {
	p, err := sys.Pipe()
	if err != nil {
		e.report("pipe failed: " + errstr())
		return nil
	}
	return []int{p[0], p[1]}
}

// The wait file, opened when a command is first waited for.
var waitfd = -1

func (e *Env) openwait() {
	if waitfd >= 0 {
		return
	}
	pid, _ := sys.Pctl(0, nil)
	w := "#p/" + strconv.Itoa(pid) + "/wait"
	fd, err := sys.Open(w, sys.OREAD)
	if err != nil {
		e.couldnot("open", w)
	}
	waitfd = fd
}

// waiter reads wait messages until pid's, printing those of progs that
// died other than by exiting or failing, and returns pid's status.
func (e *Env) waiter(pid int) string {
	buf := make([]byte, 512)
	for {
		n, err := syscall.Read(waitfd, buf)
		if err != nil || n <= 0 {
			if sys.Errstr() == "interrupted" {
				panic(threadExit{})
			}
			e.report("read wait: " + sys.Errstr() + "\n")
			return ""
		}
		msg := string(buf[:n])
		status := ""
		if i := strings.Index(msg, "\":"); i >= 0 {
			status = msg[i+2:]
		}
		if status != "" && !isfail(status) {
			fprint(e.stderr, msg+"\n")
		}
		who, _ := strconv.Atoi(strings.Fields(msg + " x")[0])
		if who != 0 && who == pid {
			return status
		}
	}
}

// wasm adds .wasm to a command's name if it is missing.
func wasm(s string) string {
	if !strings.HasSuffix(s, ".wasm") {
		return s + ".wasm"
	}
	return s
}

func absolute(s string) bool { return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") }

// exec runs a builtin, or starts a command, and maybe waits for it, as
// mkprog and waitfor in Inferno's mash: under -e the failure of a
// command waited for is raised, and at top level exit ends mash.
func exec(a []string, e *Env, in, out int, wait bool) {
	if in < 0 {
		in = e.in
	}
	if out < 0 {
		out = e.out
	}
	if b := e.builtin(a[0]); b != nil {
		e.runbuiltin(b, a, in, out, wait)
		return
	}
	if wait {
		e.openwait()
	}
	file := wasm(a[0])
	pid, err := e.spawn(file, a, in, out)
	if err != nil {
		es := errstr()
		if nonexistent(es) && !absolute(file) {
			pid, err = e.spawn("/bin/"+file, a, in, out)
			if err != nil {
				es = errstr()
			}
		}
		if err != nil {
			fprint(e.stderr, file+": "+es+"\n")
			return
		}
	}
	if !wait {
		return
	}
	status := e.waiter(pid)
	if isfail(status) && e.flags&ERaise != 0 {
		panic(mashRaise(status))
	}
}

func orfd(fd, def int) int {
	if fd < 0 {
		return def
	}
	return fd
}

// spawn starts path with standard input, output and error, and the
// generators' fds.
func (e *Env) spawn(path string, argv []string, in, out int) (int, error) {
	m := [][2]int{{0, orfd(in, 0)}, {1, orfd(out, 1)}, {2, orfd(e.stderr, 2)}}
	for _, fd := range genfds {
		m = append(m, [2]int{fd, fd})
	}
	return sys.Spawn(path, argv, sys.NEWFD, m)
}

// catch runs f and returns what it panicked with, if it was one of
// mash's exceptions.
func catch(f func()) (r any) {
	defer func() {
		if x := recover(); x != nil {
			switch x.(type) {
			case mashRaise, mashExit, threadExit:
				r = x
			default:
				panic(x)
			}
		}
	}()
	f()
	return nil
}

// runbuiltin runs a builtin as Inferno's mash runs one in a thread of
// its own: an exception it raises ends it, a failure being raised again
// under -e when it was waited for, and exit ending mash when waited for
// at top level.
func (e *Env) runbuiltin(b *Module, a []string, in, out int, wait bool) {
	n := e.copy()
	n.in = in
	n.out = out
	switch x := catch(func() { b.cmd(n, a) }).(type) {
	case mashRaise:
		s := string(x)
		if isfail(s) || s == epipe {
			if wait && e.flags&ERaise != 0 {
				if !isfail(s) {
					s = failpfx + s
				}
				panic(mashRaise(s))
			}
			return
		}
		fprint(e.stderr, a[0]+": "+s+"\n")
	case mashExit:
		if wait && e.flags&ETop != 0 {
			exitmash()
		}
	}
}

// exitmash ends mash, stopping the progs history started.
func exitmash() {
	stophelpers()
	panic(threadExit{})
}

// redirect opens files for redirection.
func redirect(e *Env, f [Rcount]string, in, out int) (int, int, []int, bool) {
	var owned []int
	bad := false
	if f[Rinout] != "" {
		s := f[Rinout]
		fd, err := sys.Open(s, sys.ORDWR)
		if err != nil {
			fprint(e.stderr, s+": "+err.Error()+"\n")
			bad = true
		} else {
			owned = append(owned, fd)
		}
		in, out = fd, fd
	} else if f[Rin] != "" {
		s := f[Rin]
		fd, err := sys.Open(s, sys.OREAD)
		if err != nil {
			fprint(e.stderr, s+": "+err.Error()+"\n")
			bad = true
		} else {
			owned = append(owned, fd)
		}
		in = fd
	}
	if f[Rout] != "" || f[Rappend] != "" {
		var s string
		fd := -1
		if f[Rappend] != "" {
			s = f[Rappend]
			if x, err := sys.Open(s, sys.OWRITE); err == nil {
				fd = x
				_, _ = sys.Seek(fd, 0, 2)
			}
		} else {
			s = f[Rout]
		}
		if fd < 0 {
			x, err := sys.Create(s, sys.OWRITE, 0o666)
			if err != nil {
				fprint(e.stderr, s+": "+err.Error()+"\n")
				bad = true
			} else {
				fd = x
			}
		}
		if fd >= 0 {
			owned = append(owned, fd)
		}
		out = fd
	}
	if bad {
		closefds(owned)
		return -1, -1, nil, false
	}
	return in, out, owned, true
}

// async runs c in the background with standard input from /dev/null. A
// command is started; anything else runs in a new mash given this one's
// variables, functions and loaded modules, where Inferno's mash runs a
// thread that shares them.
func (e *Env) async(c *Cmd) {
	e = e.clone()
	null := e.devnull()
	defer closefd(null)
	e.in = null
	if c != nil && c.op == Csimple {
		mark := len(genfds)
		defer closegen(mark)
		w := pass1(e, c.words)
		if w == nil {
			return
		}
		s := pass2(e, w)
		if s == nil {
			return
		}
		if e.fn(s[0]) == nil && e.builtin(s[0]) == nil {
			if e.flags&EEcho != 0 {
				echo(e, s)
			}
			in, out, owned, ok := mkredirs(e, c.redirs)
			if !ok {
				return
			}
			defer closefds(owned)
			exec(s, e, in, out, false)
			return
		}
	}
	e.submash(c)
}

// submash starts a mash running c with e's state.
func (e *Env) submash(c *Cmd) {
	src := e.preamble() + fulltext(c)
	args := []string{"mash"}
	if e.flags&EEcho != 0 {
		args = append(args, "-x")
	}
	if e.flags&ERaise != 0 {
		args = append(args, "-e")
	}
	args = append(args, "-c", src)
	if _, err := e.spawn(mashbin, args, e.in, e.out); err != nil {
		fprint(e.stderr, "mash: "+errstr()+"\n")
	}
}

// preamble is mash source that recreates e's modules, variables and
// functions.
func (e *Env) preamble() string {
	var b strings.Builder
	for _, m := range loaded {
		if m.name != "builtins" {
			b.WriteString("load " + quote(m.name) + ";")
		}
	}
	for _, s := range e.global.all() {
		if s.isvar {
			b.WriteString(prone(s.name, "=", s))
		}
		if s.fn != nil {
			b.WriteString("fn " + quote(s.name) + " { " + fulltext(s.fn) + " };")
		}
	}
	if e.local != nil {
		for _, s := range e.local.all() {
			if s.isvar {
				b.WriteString(prone(s.name, ":=", s))
			}
		}
	}
	return b.String()
}
