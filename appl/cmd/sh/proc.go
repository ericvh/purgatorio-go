package main

import (
	"io"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// shExc is a shell exception, such as "fail:usage".
type shExc struct{ name string }

func raise(name string) { panic(shExc{name}) }

// catch runs f and returns the name of the exception it raised, if any.
func catch(f func()) (exc string, raised bool) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(shExc)
			if !ok {
				panic(r)
			}
			exc, raised = e.name, true
		}
	}()
	f()
	return "", false
}

func isfail(e string) bool { return strings.HasPrefix(e, "fail:") }

func failurestatus(e string) string {
	s := strings.TrimLeft(e[5:], " \t")
	if s != "" {
		return s
	}
	return "failed"
}

// shpanic reports an internal error and ends the shell.
func shpanic(s string) {
	writefd(2, "sh panic: "+s+"\n")
	sys.Exits("panic")
}

func writefd(fd int, s string) error {
	b := []byte(s)
	for len(b) > 0 {
		n, err := syscall.Write(fd, b)
		if err != nil {
			return sys.Error(sys.Errstr())
		}
		b = b[n:]
	}
	return nil
}

func closefd(fd int) { _ = syscall.Close(fd) }

// fdreader reads a raw fd without the os package, whose files close
// their fds when collected.
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

// fdtab maps the fds a command sees to sh's own fds.
type fdtab struct {
	m map[int]int
}

// newfdtab is the table a shell starts with: the standard fds and any
// other fd it was started with, such as one a redirection opened around
// the block this shell runs. Inferno's sh shares its fd group with what
// it starts; here each command is given the fds in the table, so a
// shell that did not list its own would lose them.
func newfdtab() *fdtab {
	t := &fdtab{m: map[int]int{0: 0, 1: 1, 2: 2}}
	for _, fd := range inherited {
		t.m[fd] = fd
	}
	return t
}

// inherited is the fds above 2 the shell was started with (main sets it
// before the shell opens any file of its own).
var inherited []int

// openfds lists the prog's open fds above 2 from #d, without the WASI
// preopens and the fd it reads the directory on.
func openfds() []int {
	dfd, err := sys.Open("#d", sys.OREAD)
	if err != nil {
		return nil
	}
	defer closefd(dfd)
	var fds []int
	for {
		d, err := sys.Dirread(dfd)
		if err != nil || len(d) == 0 {
			break
		}
		for i := range d {
			fd, err := strconv.Atoi(d[i].Name)
			if err != nil || fd <= 2 || fd == dfd || fd == abi.PreopenRootFD || fd == abi.PreopenDotFD {
				continue
			}
			fds = append(fds, fd)
		}
	}
	return fds
}

func (t *fdtab) clone() *fdtab {
	n := &fdtab{m: make(map[int]int, len(t.m))}
	for k, v := range t.m {
		n.m[k] = v
	}
	return n
}

func (t *fdtab) get(fd int) (int, bool) { v, ok := t.m[fd]; return v, ok }

// fdmap is the (child, parent) list for sys.Spawn.
func (t *fdtab) fdmap() [][2]int {
	keys := make([]int, 0, len(t.m))
	for k := range t.m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	m := make([][2]int, len(keys))
	for i, k := range keys {
		m[i] = [2]int{k, t.m[k]}
	}
	return m
}

func (c *Context) fdwrite(fd int, s string) error {
	r, ok := c.fds.get(fd)
	if !ok {
		return sys.Error("fd out of range or not open")
	}
	return writefd(r, s)
}

func (c *Context) eprint(s string) { _ = c.fdwrite(2, s) }
func (c *Context) print(s string)  { _ = c.fdwrite(1, s) }

// The wait file.

var waitfd = -1

func openwait() int {
	pid, _ := sys.Pctl(0, nil)
	wf := strconv.Itoa(pid) + "/wait"
	fd, err := sys.Open("#p/"+wf, sys.OREAD)
	if err != nil {
		fd, err = sys.Open("/prog/"+wf, sys.OREAD)
	}
	if err != nil {
		shpanic("cannot open wait file: " + sys.Errstr())
	}
	return fd
}

// job is a prog started by sh, or the status of one that never started.
type job struct {
	pid    int
	done   bool
	status string
	exc    string // exception raised before it could start
}

func waitfor(c *Context, jobs []job) string {
	if len(jobs) == 0 {
		return ""
	}
	status := make([]string, len(jobs))
	got := make([]bool, len(jobs))
	wcount := len(jobs)
	onebad := false
	for i, j := range jobs {
		if j.done {
			status[i], got[i] = j.status, true
			onebad = onebad || j.status != ""
			wcount--
		}
	}
	buf := make([]byte, 512)
	for wcount > 0 {
		n, err := syscall.Read(waitfd, buf)
		if err != nil || n <= 0 {
			e := sys.Errstr()
			if e == "interrupted" {
				// Killed while waiting.
				sys.Exits("")
			}
			shpanic("error on wait read: " + e)
		}
		who, line, s := parsewaitstatus(c, string(buf[:n]))
		if s != "" {
			if isfail(s) {
				s = failurestatus(s)
			} else {
				c.diagnostic(line)
			}
		}
		for i, j := range jobs {
			if !got[i] && !j.done && j.pid == who {
				got[i] = true
				status[i] = s
				onebad = onebad || s != ""
				wcount--
				break
			}
		}
	}
	if !onebad {
		return ""
	}
	return strings.Join(status, "|")
}

func parsewaitstatus(c *Context, status string) (int, string, string) {
	i := strings.IndexByte(status, ' ')
	if i < 0 || i == len(status)-1 || status[i+1] != '"' {
		c.fail("bad wait read", "sh: bad exit status '"+status+"'")
	}
	j := strings.IndexByte(status[i+2:], '"')
	if j < 0 || i+2+j > len(status)-2 || status[i+2+j+1] != ':' {
		c.fail("bad wait read", "sh: bad exit status '"+status+"'")
	}
	j += i + 2
	return limboAtoi(status), status, status[j+2:]
}

// External commands.

// absolute reports whether p is used as given: it starts with /, #, ./
// or ../.
func absolute(p string) bool {
	if len(p) < 2 {
		return false
	}
	if p[0] == '/' || p[0] == '#' {
		return true
	}
	if len(p) < 3 || p[0] != '.' {
		return false
	}
	return p[1] == '/' || (p[1] == '.' && p[2] == '/')
}

func nonexistent(e string) bool {
	return strings.HasSuffix(e, "does not exist") || strings.HasSuffix(e, "directory entry not found")
}

const modsuffix = ".wasm"

func (c *Context) pathlist(progname string) []string {
	if absolute(progname) {
		return []string{""}
	}
	if pl := c.get("path"); pl != nil {
		return words(pl)
	}
	return []string{"/bin", "."}
}

// startexternal starts the command args names, with c's fds, and
// returns its job. A command that cannot be found is reported and given
// the error as its status, as Inferno's runexternal does.
func startexternal(c *Context, args []*Listnode) job {
	progname := word(args[0])
	argv := words(args)
	ismod := strings.HasSuffix(progname, modsuffix)
	err := ""
	for _, dir := range c.pathlist(progname) {
		path := progname
		if dir != "" {
			path = dir + "/" + progname
		}
		npath := path
		if !ismod {
			npath += modsuffix
		}
		export(c.env.localenv)
		pid, e := sys.Spawn(npath, argv, sys.NEWFD, c.fds.fdmap())
		if e == nil {
			return job{pid: pid}
		}
		err = sys.Errstr()
		if nonexistent(err) && !ismod {
			// Try a module named as given, then a #! script.
			if fd, e := sys.Open(path, sys.OREAD); e == nil {
				d, e := sys.Fstat(fd)
				if e == nil && d.Mode&sys.DMDIR == 0 {
					hdr := make([]byte, 1024)
					n, _ := syscall.Read(fd, hdr)
					closefd(fd)
					if n < 0 {
						n = 0
					}
					hdr = hdr[:n]
					if strings.HasPrefix(string(hdr), "\x00asm") {
						pid, e := sys.Spawn(path, argv, sys.NEWFD, c.fds.fdmap())
						if e == nil {
							return job{pid: pid}
						}
						err = sys.Errstr()
						break
					}
					if d.Mode&0o111 != 0 {
						return hashpling(c, hdr, path, args[1:])
					}
				} else {
					closefd(fd)
				}
			}
			err = sys.Errstr()
		}
		if !nonexistent(err) {
			break
		}
	}
	c.diagnostic(progname + ": " + err)
	return job{done: true, status: err}
}

func hashpling(c *Context, hdr []byte, path string, argv []*Listnode) job {
	i := 0
	for i < len(hdr) && hdr[i] != '\n' {
		i++
	}
	if i == len(hdr) || i < 3 || hdr[0] != '#' || hdr[1] != '!' {
		c.diagnostic("bad script header on " + path)
		return job{done: true, status: "bad header"}
	}
	args := tokenize(string(hdr[2:i]), " \t")
	if len(args) == 0 {
		c.diagnostic("empty header on " + path)
		return job{done: true, status: "bad header"}
	}
	nargs := stringlist(args)
	nargs = append(nargs, wordnode(path))
	nargs = append(nargs, argv...)
	return startexternal(c, nargs)
}

func runexternal(c *Context, args []*Listnode) string {
	j := startexternal(c, args)
	return waitfor(c, []job{j})
}

// isexternal reports whether c.run would look for args as an external
// command.
func isexternal(c *Context, args []*Listnode) bool {
	if len(args) == 0 || args[0].cmd != nil || args[0].word == "" || args[0].word[0] == '{' {
		return false
	}
	_, mods := c.env.builtins.find(args[0].word)
	return mods == nil
}

// runasync starts argv in a new prog with redirs applied to a copy of
// c's fds, as sh.y's runasync does in a new process. An external
// command is started directly; anything else runs in a new sh given the
// context's modules, $0, $* and exported variables.
func runasync(c *Context, argv []*Listnode, redirs *Redirlist) job {
	nc := &Context{env: c.env, fds: c.fds.clone()}
	var owned []int
	defer func() {
		for _, fd := range owned {
			closefd(fd)
		}
	}()
	if e, raised := catch(func() { owned = doredirs(nc, redirs) }); raised {
		if !isfail(e) {
			raise(e)
		}
		return job{done: true, status: failurestatus(e), exc: e}
	}
	if len(argv) == 0 || (argv[0].cmd == nil && argv[0].word == "") {
		return job{done: true}
	}
	if isexternal(nc, argv) {
		if nc.options()&optEXECPRINT != 0 {
			nc.eprint(quoted(argv, false) + "\n")
		}
		return startexternal(nc, argv)
	}
	return startsh(nc, argv)
}

func startsh(c *Context, argv []*Listnode) job {
	var b strings.Builder
	stdloaded := false
	for i := len(c.env.bmods) - 1; i >= 0; i-- {
		b.WriteString("load " + quote(c.env.bmods[i].name, false) + ";")
		if c.env.bmods[i].mod == stdmod {
			stdloaded = true
		}
	}
	b.WriteString("'0'=(" + quoted(c.get("0"), false) + ");")
	b.WriteString("'*'=(" + quoted(c.get("*"), false) + ");")
	args := []string{"sh", "-n"}
	if c.options()&optEXECPRINT != 0 {
		if stdloaded {
			b.WriteString("flag x +;")
		} else {
			args = append(args, "-x")
		}
	}
	if c.options()&optERROREXIT != 0 {
		args = append(args, "-e")
	}
	b.WriteString(quoted(argv, false))
	args = append(args, "-c", b.String())
	export(c.env.localenv)
	pid, err := sys.Spawn("/bin/sh"+modsuffix, args, sys.NEWFD, c.fds.fdmap())
	if err != nil {
		e := sys.Errstr()
		c.diagnostic("cannot start sh: " + e)
		return job{done: true, status: e}
	}
	return job{pid: pid}
}

// tokenize splits s at runs of characters in delim, as Sys->tokenize.
func tokenize(s, delim string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(delim, r) })
}
