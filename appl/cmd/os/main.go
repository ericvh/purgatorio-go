// Command os runs a host command through #C (Inferno os(1)), copying the
// standard streams to and from it and turning its exit status into
// "fail:host: ...".
package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// copyName is argv[0] of the internal copy progs. Limbo spawns copy
// threads in the same prog; a Go guest blocks as a whole in a read, so
// each copy is a prog of its own.
const copyName = "os-copy"

func main() {
	if len(os.Args) == 1 && os.Args[0] == copyName {
		copyFd(os.Stdin, os.Stdout)
		return
	}
	a := arg.New(os.Args)
	a.SetUsage("os [-d dir] [-m mount] [-n] [-N nice] [-b] command [arg...]")
	nice, nicearg, workdir, mntpoint, foreground := false, "", "", "", true
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'd':
			workdir = a.EArg()
		case 'm':
			mntpoint = a.EArg()
		case 'n':
			nice = true
		case 'N':
			nice, nicearg = true, " "+quote(a.EArg())
		case 'b':
			foreground = false
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	sys.Pctl(sys.FORKNS, nil)
	sys.Bind("#p", "/prog", sys.MREPL)
	if mntpoint == "" {
		mntpoint = "/cmd"
		if _, err := sys.Stat(mntpoint + "/clone"); err != nil {
			if err := sys.Bind("#C", "/", sys.MBEFORE); err != nil {
				fail("bind #C /: " + sys.Errstr())
			}
		}
	}
	cfd, err := os.OpenFile(mntpoint+"/clone", os.O_RDWR, 0)
	if err != nil {
		fail("cannot open /cmd/clone: " + sys.Errstr())
	}
	buf := make([]byte, 32)
	n, _ := cfd.Read(buf)
	if n <= 0 {
		fail("cannot read /cmd/clone: " + sys.Errstr())
	}
	dir := mntpoint + "/" + string(buf[:n])
	wfd, werr := os.Open(dir + "/wait")
	if nice {
		if _, err := cfd.WriteString("nice" + nicearg); err != nil {
			os.Stderr.WriteString("os: warning: can't set nice priority: " + sys.Errstr() + "\n")
		}
	}
	if workdir != "" {
		if _, err := cfd.WriteString("dir " + workdir); err != nil {
			fail("cannot set cwd " + quote(workdir) + ": " + sys.Errstr())
		}
	}
	if foreground {
		if _, err := cfd.WriteString("killonclose"); err != nil {
			os.Stderr.WriteString("os: warning: cannot write killonclose: " + sys.Errstr() + "\n")
		}
	}
	q := make([]string, len(args))
	for i, s := range args {
		q[i] = quote(s)
	}
	if _, err := cfd.WriteString("exec " + strings.Join(q, " ")); err != nil {
		fail("cannot exec: " + sys.Errstr())
	}
	if foreground {
		tocmd, err := sys.Open(dir+"/data", sys.OWRITE)
		if err != nil {
			fail("canot open " + dir + "/data for writing: " + sys.Errstr())
		}
		fromcmd, err := os.Open(dir + "/data")
		if err != nil {
			fail("cannot open " + dir + "/data for reading: " + sys.Errstr())
		}
		errcmd, err := sys.Open(dir+"/stderr", sys.OREAD)
		if err != nil {
			fail("cannot open " + dir + "/stderr for reading: " + sys.Errstr())
		}
		mypid, _ := sys.Pctl(0, nil)
		cwait, _ := os.Open("/prog/" + strconv.Itoa(mypid) + "/wait")
		self := "/bin/os.wasm"
		pid, _ := sys.Spawn(self, []string{copyName}, sys.NEWFD, [][2]int{{0, 0}, {1, tocmd}, {2, 2}})
		sys.Close(tocmd)
		epid, _ := sys.Spawn(self, []string{copyName}, sys.NEWFD, [][2]int{{0, errcmd}, {1, 2}, {2, 2}})
		sys.Close(errcmd)
		copyFd(fromcmd, os.Stdout)
		fromcmd.Close()
		kill(pid)
		// Let the stderr copy reach the end of the command's stderr
		// rather than killing it, so nothing written there is lost.
		if cwait != nil && epid > 0 {
			want := strconv.Itoa(epid) + " "
			msg := make([]byte, 256)
			for {
				n, err := cwait.Read(msg)
				if err != nil || n <= 0 || strings.HasPrefix(string(msg[:n]), want) {
					break
				}
			}
		} else {
			kill(epid)
		}
	}
	if werr == nil {
		status := make([]byte, 1024)
		n, err := wfd.Read(status)
		if err != nil && n == 0 {
			fail("wait error: " + sys.Errstr())
		}
		s := string(status[:n])
		if s != "" {
			flds := unquoted(s)
			if len(flds) < 5 {
				fail("wait error: odd status: " + quote(s))
			}
			if flds[4] != "" {
				sys.Exits("fail:host: " + flds[4])
			}
		}
	}
	_ = cfd
}

func copyFd(f, t *os.File) {
	buf := make([]byte, 8192)
	for {
		r, _ := f.Read(buf)
		if r <= 0 {
			return
		}
		if w, _ := t.Write(buf[:r]); w != r {
			return
		}
	}
}

func kill(pid int) {
	if pid <= 0 {
		return
	}
	if f, err := os.OpenFile("#p/"+strconv.Itoa(pid)+"/ctl", os.O_WRONLY, 0); err == nil {
		f.WriteString("kill")
		f.Close()
	}
}

func fail(msg string) {
	os.Stderr.WriteString("os: " + msg + "\n")
	sys.Exits("fail:" + msg)
}

// quote is Inferno's %q.
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// unquoted splits s as str->unquoted does.
func unquoted(s string) []string {
	var out []string
	var cur strings.Builder
	in, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			if in && i+1 < len(s) && s[i+1] == '\'' {
				cur.WriteByte('\'')
				i++
			} else {
				in = !in
			}
			have = true
		case !in && (c == ' ' || c == '\t' || c == '\n'):
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}
