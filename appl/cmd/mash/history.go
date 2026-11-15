package main

// The history module serves /chan/history, the commands typed at the
// console, which a write adds to, and /chan/mash, to which a write
// gives the console a command to run. Inferno's mash serves them from
// threads of its own; here two helper progs (this command, started as
// mash --history and mash --serve) serve them, as this mash cannot
// answer requests while it waits for a command. The --serve helper
// passes what is written on a pipe that the console loop reads along
// with the console.

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const chandir = "/chan"

var (
	histstarted bool
	histfd      = -1
	mashfd      = -1
	helperpids  []int
)

func init() {
	modules["history"] = &Module{name: "history", init: initHistory, cmd: func(*Env, []string) {}}
}

func initHistory(e *Env, m *Module) {
	if histstarted {
		return
	}
	histstarted = true
	if starthelper(e, "--history") < 0 {
		return
	}
	if fd, err := sys.Open(chandir+"/history", sys.OWRITE); err == nil {
		histfd = fd
	}
	mashfd = starthelper(e, "--serve")
}

// starthelper starts a helper and returns the read end of the pipe on
// its standard output, after the byte it writes when it is serving.
func starthelper(e *Env, flag string) int {
	p, err := sys.Pipe()
	if err != nil {
		e.couldnot("pipe", "")
	}
	null := e.devnull()
	pid, err := sys.Spawn(mashbin, []string{"mash", flag}, sys.NEWFD, [][2]int{{0, null}, {1, p[1]}, {2, orfd(e.stderr, 2)}})
	closefd(null)
	closefd(p[1])
	if err != nil {
		closefd(p[0])
		e.report("history: " + errstr())
		return -1
	}
	helperpids = append(helperpids, pid)
	b := make([]byte, 1)
	if n, _ := syscall.Read(p[0], b); n != 1 {
		closefd(p[0])
		return -1
	}
	return p[0]
}

// addhist adds a line typed at the console to the history.
func addhist(s string) {
	if histfd >= 0 {
		fprint(histfd, s)
	}
}

// stophelpers kills the helpers.
func stophelpers() {
	for _, pid := range helperpids {
		if fd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/ctl", sys.OWRITE); err == nil {
			fprint(fd, "kill")
			closefd(fd)
		}
	}
	helperpids = nil
}

// helper runs this command as a helper, if arg asks for one.
func helper(arg string) bool {
	switch arg {
	case "--history":
		c := servefile("history")
		servehist(c)
	case "--serve":
		c := servefile("mash")
		servemash(c)
	default:
		return false
	}
	return true
}

var srv string
var nsrv int

// servefile makes /chan/n, binding #s on /chan if need be.
func servefile(n string) *sys.FileIO {
	d, err := sys.Stat(chandir)
	if err != nil {
		hfail("stat", chandir)
	}
	if d.Type != 's' {
		if _, err := sys.Bind("#s", chandir, sys.MBEFORE); err != nil {
			hfail("bind", chandir)
		}
	}
	pid, _ := sys.Pctl(0, nil)
	srv = "mash." + strconv.Itoa(pid)
	retry := false
	var c *sys.FileIO
	var s string
	for {
		if retry {
			s = srv + "." + strconv.Itoa(nsrv)
			nsrv++
		} else {
			s = n
		}
		c, err = sys.File2chan(chandir, s)
		s = chandir + "/" + s
		if err != nil {
			if retry || errstr() != "file exists" {
				hfail("file2chan", s)
			}
			retry = true
			continue
		}
		break
	}
	if retry {
		if _, err := sys.Bind(s, chandir+"/"+n, sys.MREPL); err != nil {
			hfail("bind", chandir+"/"+n)
		}
	}
	fprint(1, "R")
	return c
}

func hfail(what, who string) {
	os.Stderr.WriteString("could not " + what + " " + who + ": " + errstr() + "\n")
	sys.Exits("fail: mash system error")
}

// hcmd is a command in the history: its offset and text.
type hcmd struct {
	seek int
	text []byte
}

type hreader struct {
	fid, offset, hint int
}

var (
	history = make([]*hcmd, 0, 256)
	lhist   = 256
	hseek   int
	readers []*hreader
)

func addhcmd(b []byte) {
	if len(history) == lhist {
		n := 3 * len(history) / 4
		history = append(history[:0], history[len(history)-n:]...)
	}
	history = append(history, &hcmd{hseek, b})
	hseek += len(b)
}

func getfid(fid int, del bool) *hreader {
	for i, r := range readers {
		if r.fid == fid {
			if del {
				readers = append(readers[:i], readers[i+1:]...)
				return nil
			}
			return r
		}
	}
	o := 0
	if len(history) > 0 {
		o = history[0].seek
	}
	r := &hreader{fid: fid, offset: o}
	readers = append([]*hreader{r}, readers...)
	return r
}

func readhist(off, fid int) ([]byte, string) {
	r := getfid(fid, false)
	off += r.offset
	nhist := len(history)
	if nhist == 0 || off >= hseek {
		return nil, ""
	}
	i := r.hint
	if i >= nhist {
		i = nhist - 1
	}
	s := history[i].seek
	if off == s {
		r.hint = i + 1
		return history[i].text, ""
	}
	if off > s {
		for {
			i++
			if i == nhist {
				break
			}
			s = history[i].seek
			if off < s {
				break
			}
		}
		i--
	} else {
		for {
			i--
			if i < 0 {
				return nil, "data truncated"
			}
			s = history[i].seek
			if off >= s {
				break
			}
		}
	}
	r.hint = i + 1
	b := history[i].text
	s = history[i].seek
	if off != s {
		b = b[off-s:]
	}
	return b, ""
}

func servehist(c *sys.FileIO) {
	for {
		req, err := c.Next()
		if err != nil {
			return
		}
		switch m := req.(type) {
		case *sys.ReadReq:
			if m.Closed() {
				getfid(m.Fid, true)
				continue
			}
			b, e := readhist(int(m.Off), m.Fid)
			if len(b) > m.Count {
				b = b[:m.Count]
			}
			m.Reply(b, e)
		case *sys.WriteReq:
			if m.Closed() {
				continue
			}
			m.Reply(len(m.Data), "")
			in := bufio.NewReader(strings.NewReader(string(m.Data)))
			for {
				s, err := in.ReadString('\n')
				if s != "" {
					addhcmd([]byte(s))
				}
				if err != nil {
					break
				}
			}
		}
	}
}

func servemash(c *sys.FileIO) {
	for {
		req, err := c.Next()
		if err != nil {
			return
		}
		switch m := req.(type) {
		case *sys.ReadReq:
			m.Reply(nil, "not supported")
		case *sys.WriteReq:
			if m.Closed() {
				continue
			}
			m.Reply(len(m.Data), "")
			if len(m.Data) > 0 {
				_ = writefd(1, m.Data)
			}
		}
	}
}
