package main

// The verbs that run shell commands (exec, pipe, query, run), and void.

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const shbin = "/bin/sh.wasm"

// shvar is a variable a command is given.
type shvar struct {
	name string
	val  []string
}

// shquote quotes s for sh.
func shquote(s string) string {
	need := s == ""
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{', '}', '(', ')', '`', '&', ';', '=', '>', '<', '#', '|', '*', '[', '?', '$', '^', ' ', '\t', '\n', '\r', '"':
			need = true
		case '\'':
			b.WriteByte('\'')
			need = true
		}
		b.WriteByte(s[i])
	}
	if need {
		return "'" + b.String() + "'"
	}
	return b.String()
}

// shscript is sh input that sets vars and runs the braced block cmd.
func shscript(cmd string, vars []shvar) string {
	var b strings.Builder
	for _, v := range vars {
		b.WriteString(shquote(v.name) + "=(")
		for i, s := range v.val {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(shquote(s))
		}
		b.WriteString(");")
	}
	b.WriteString(cmd)
	return b.String()
}

// The wait file, and statuses read for progs not yet waited for.
var (
	waitmu  sync.Mutex
	waitfd  = -1
	waitst  = map[int]string{}
	waiterr string
)

func openwait() {
	if waitfd >= 0 {
		return
	}
	pid, _ := sys.Pctl(0, nil)
	fd, err := sys.Open("#p/"+strconv.Itoa(pid)+"/wait", sys.OREAD)
	if err != nil {
		waiterr = err.Error()
		return
	}
	waitfd = fd
}

// startsh starts sh running script with fds 0, 1 and 2 and those in
// extra (child, parent), and returns its pid.
func startsh(script string, in int, extra ...[2]int) (int, error) {
	waitmu.Lock()
	openwait()
	waitmu.Unlock()
	m := append([][2]int{{0, in}, {1, 1}, {2, 2}}, extra...)
	return sys.Spawn(shbin, []string{"sh", "-c", script}, sys.NEWFD, m)
}

// waitpid waits for pid and returns its status.
func waitpid(pid int) string {
	waitmu.Lock()
	defer waitmu.Unlock()
	if waitfd < 0 {
		return waiterr
	}
	buf := make([]byte, 512)
	for {
		if s, ok := waitst[pid]; ok {
			delete(waitst, pid)
			return s
		}
		n, err := syscall.Read(waitfd, buf)
		if err != nil || n <= 0 {
			return "wait failed"
		}
		msg := string(buf[:n])
		status := ""
		if i := strings.Index(msg, "\":"); i >= 0 {
			status = msg[i+2:]
		}
		who, _ := strconv.Atoi(strings.Fields(msg + " x")[0])
		waitst[who] = status
	}
}

// runsh runs a command and returns its status.
func runsh(cmd string, vars []shvar, stdin int) string {
	pid, err := startsh(shscript(cmd, vars), stdin)
	if err != nil {
		return err.Error()
	}
	return waitpid(pid)
}

func statvars(d *sys.Dir, pflag int) []shvar {
	v := []shvar{
		{"mode", []string{modes(d.Mode)}},
		{"uid", []string{d.UID}},
		{"mtime", []string{strconv.FormatUint(uint64(d.Mtime), 10)}},
		{"length", []string{strconv.FormatUint(d.Length, 10)}},
	}
	if pflag > 1 {
		v = append(v,
			shvar{"name", []string{d.Name}},
			shvar{"gid", []string{d.GID}},
			shvar{"muid", []string{d.MUID}},
			shvar{"qid", []string{fmt.Sprintf("16r%x", d.Qid.Path), strconv.FormatUint(uint64(d.Qid.Vers), 10)}},
			shvar{"atime", []string{strconv.FormatUint(uint64(d.Atime), 10)}},
			shvar{"dtype", []string{string(rune(d.Type))}},
			shvar{"dev", []string{strconv.FormatUint(uint64(d.Dev), 10)}})
	}
	return v
}

func pflags(opts []option) int {
	pflag := 0
	for _, o := range opts {
		switch o.opt {
		case 'p':
			pflag = 1
		case 'P':
			pflag = 2
		}
	}
	return pflag
}

func execrun(r *Report, opts []option, args []value) value {
	n := 1
	pflag := 0
	tcmd := ""
	for _, o := range opts {
		switch o.opt {
		case 'n':
			n, _ = strconv.Atoi(vs(o.args[0]))
			if n <= 0 {
				fmt.Fprintf(os.Stderr, "fs: exec: invalid argument to -n\n")
				return nil
			}
		case 't':
			tcmd = vc(o.args[0])
		case 'p':
			pflag = 1
		case 'P':
			pflag = 2
		}
	}
	if pflag != 0 && n > 1 {
		fmt.Fprintf(os.Stderr, "fs: exec: cannot specify -p with -n %d\n", n)
		return nil
	}
	cmd := vc(args[0])
	c := vt(args[1])
	sync := make(chan int)
	errorc := r.start("exec")
	go func() {
		if <-sync == 0 {
			c.sync <- 0
			errorc <- ""
			return
		}
		c.sync <- 1
		var fl []string
		for d := <-c.c; d.dir != nil; d = <-c.c {
			fl = append(fl, d.path)
			if len(fl) >= n {
				vars := []shvar{{"file", fl}}
				if pflag != 0 {
					vars = append(vars, statvars(d.dir, pflag)...)
				}
				fl = nil
				runsh(cmd, vars, 0)
			}
		}
		if len(fl) > 0 {
			runsh(cmd, []shvar{{"file", fl}}, 0)
		}
		if tcmd != "" {
			runsh(tcmd, []shvar{{"file", nil}}, 0)
		}
		errorc <- ""
	}()
	return vV{sync}
}

func piperun(r *Report, opts []option, args []value) value {
	oneflag := false
	pflag := pflags(opts)
	for _, o := range opts {
		if o.opt == '1' {
			oneflag = true
		}
	}
	if pflag != 0 && oneflag {
		fmt.Fprintf(os.Stderr, "fs: exec: cannot specify -p with -1\n")
		return nil
	}
	cmd := vc(args[0])
	c := vx(args[1])
	sync := make(chan int)
	errorc := r.start("exec")
	proc(func() {
		if <-sync == 0 {
			(<-c).reply <- Quit
			quit(errorc)
		}
		fd, pid := -1, -1
		if oneflag {
			if fd, pid = popen(cmd, nil); fd < 0 {
				(<-c).reply <- Quit
				quit(errorc)
			}
		}
		var names []string
		name := ""
		indent := 0
		for {
			m := <-c
			d := m.d
			if d.dir == nil {
				m.reply <- Next
				indent--
				if indent == 0 {
					break
				}
				name, names = names[len(names)-1], names[:len(names)-1]
				continue
			}
			if d.dir.Mode&sys.DMDIR != 0 {
				m.reply <- Down
				names = append(names, name)
				if indent > 0 && name != "" && name[len(name)-1] != '/' {
					name += "/"
				}
				name += d.dir.Name
				indent++
				continue
			}
			if !oneflag {
				p := name
				if p != "" && p[len(p)-1] != '/' {
					p += "/"
				}
				vars := []shvar{{"file", []string{p + d.dir.Name}}}
				if pflag != 0 {
					vars = append(vars, statvars(d.dir, pflag)...)
				}
				fd, pid = popen(cmd, vars)
			}
			if fd < 0 {
				m.reply <- Next
				continue
			}
			m.reply <- Down
			for {
				m := <-c
				m.reply <- Next
				if m.d.data == nil {
					break
				}
				if writefd(fd, m.d.data) != nil {
					if oneflag {
						(<-c).reply <- Quit
						quit(errorc)
					}
					(<-c).reply <- Skip
					break
				}
			}
			if !oneflag {
				syscall.Close(fd)
				fd = -1
				waitpid(pid)
			}
		}
		if oneflag {
			syscall.Close(fd)
			waitpid(pid)
		}
		quit(errorc)
	})
	return vV{sync}
}

// popen starts a command reading from a pipe and returns the pipe's
// other end and the command's pid.
func popen(cmd string, vars []shvar) (int, int) {
	p, err := sys.Pipe()
	if err != nil {
		return -1, -1
	}
	pid, err := startsh(shscript(cmd, vars), p[0])
	syscall.Close(p[0])
	if err != nil {
		syscall.Close(p[1])
		return -1, -1
	}
	return p[1], pid
}

func queryrun(_ *Report, opts []option, args []value) value {
	pflag := pflags(opts)
	cmd := vc(args[0])
	c := make(gatechan)
	go func() {
		for q := <-c; q.reply != nil; q = <-c {
			vars := []shvar{{"file", []string{q.e.path}}}
			if pflag != 0 {
				vars = append(vars, statvars(q.e.dir, pflag)...)
			}
			q.reply <- runsh(cmd, vars, 0) == ""
		}
	}()
	return vP{c}
}

// runrun runs a command and gives the value of its $s. The command
// runs in a new sh, which gives $s back on fd 5 (3 and 4 are WASI's).
func runrun(_ *Report, _ []option, args []value) value {
	c := vc(args[0])
	p, err := sys.Pipe()
	if err != nil {
		return nil
	}
	pid, err := startsh("s=();"+c+";echo $#s >>/fd/5;echo -n $s >>/fd/5", 0, [2]int{5, p[1]})
	syscall.Close(p[1])
	if err != nil {
		syscall.Close(p[0])
		return nil
	}
	b, _ := io.ReadAll(fdreader(p[0]))
	syscall.Close(p[0])
	st := waitpid(pid)
	out := string(b)
	i := strings.IndexByte(out, '\n')
	if i < 0 {
		e := st
		if strings.HasPrefix(e, "fail:") {
			e = e[5:]
		}
		fmt.Fprintf(os.Stderr, "fs: run: exception %s raised in %s\n", quote.Quote(e), c)
		return nil
	}
	n, _ := strconv.Atoi(out[:i])
	if n != 1 {
		fmt.Fprintf(os.Stderr, "fs: run: $s has %d members; exactly one is required\n", n)
		return nil
	}
	return vS{out[i+1:]}
}

func voidrun(_ *Report, _ []option, args []value) value {
	return vV{vv(args[0])}
}
