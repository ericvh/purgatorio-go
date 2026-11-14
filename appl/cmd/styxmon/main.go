// Command styxmon shows the Styx messages between a client on its
// standard input and a server it runs (Inferno styxmon(8)). The command
// serves Styx on its standard input; styxmon passes each message on
// unchanged and prints its text on standard error.
//
// styxmon.b relays each direction in its own thread. A guest is
// single-threaded, so here one loop waits for either side with sys.Wait.
package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

// maxrpc is Styx->MAXRPC, the largest message readmsg accepts by default.
const maxrpc = styx.IOHDRSZ + 8192

func main() {
	a := arg.New(os.Args)
	a.SetUsage("usage: styxmon [-d] cmd [arg...]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd': // showdata: accepted, as in styxmon.b, but unused
		default:
			a.Usage()
		}
	}
	argv := a.Argv()
	if len(argv) == 0 {
		a.Usage()
	}
	p, err := sys.Pipe()
	if err != nil {
		os.Stderr.WriteString("styxmon: cannot make pipe: " + sys.Errstr() + "\n")
		sys.Exits("fail:pipe")
	}
	if _, err := sys.Spawn("/bin/sh.wasm", []string{"sh", "-c", cmdline(argv)}, sys.NEWFD|sys.FORKNS,
		[][2]int{{0, p[0]}, {1, 1}, {2, 2}}); err != nil {
		os.Stderr.WriteString("styxmon: cannot run " + argv[0] + ": " + sys.Errstr() + "\n")
		sys.Exits("fail:exec")
	}
	sys.Close(p[0])
	relay(0, p[1])
}

type side struct {
	in, out int
	what    string
	f       *os.File
	buf     []byte
}

func relay(client, server int) {
	sides := []*side{
		{in: client, out: server, what: "tmsg", f: os.NewFile(uintptr(client), "client")},
		{in: server, out: client, what: "rmsg", f: os.NewFile(uintptr(server), "server")},
	}
	ops := [][2]int{{sys.WaitRead, client}, {sys.WaitRead, server}}
	rbuf := make([]byte, 64*1024)
	for {
		i, err := sys.Wait(ops, -1)
		if err != nil || i < 0 {
			return
		}
		s := sides[i]
		n, rerr := s.f.Read(rbuf)
		if n > 0 {
			s.buf = append(s.buf, rbuf[:n]...)
		}
		if !s.drain(sides[1-i].f) {
			return
		}
		if n <= 0 {
			if len(s.buf) > 0 {
				s.fail("9P message truncated")
			} else if rerr != nil && rerr.Error() != "EOF" {
				s.fail(sys.Errstr())
			} else {
				os.Stderr.WriteString("styxmon: eof from " + s.what + "\n")
			}
			return
		}
	}
}

func (s *side) fail(e string) {
	os.Stderr.WriteString("styxmon: error from " + s.what + ": " + e + "\n")
}

// drain prints and forwards each whole message in s.buf, and reports
// whether the conversation can go on.
func (s *side) drain(to *os.File) bool {
	for len(s.buf) >= 4 {
		ml := styx.Size(s.buf)
		if ml <= 4 {
			s.fail("invalid 9P message size")
			return false
		}
		if ml > maxrpc {
			s.fail("9P message longer than agreed")
			return false
		}
		if uint32(len(s.buf)) < ml {
			break
		}
		d := s.buf[:ml]
		m, err := styx.Unmarshal(d, ml)
		switch {
		case err != nil:
			os.Stderr.WriteString("styxmon: " + s.what + " message error (0/" + strconv.Itoa(len(d)) + ")\n")
		default:
			os.Stderr.WriteString(msgtext(m) + "\n")
		}
		to.Write(d)
		s.buf = s.buf[ml:]
	}
	return true
}

// cmdline makes a shell command of argv: a word in braces is a block
// and goes as it is; other words are quoted where the shell needs it.
func cmdline(argv []string) string {
	w := make([]string, len(argv))
	for i, a := range argv {
		if len(a) >= 2 && a[0] == '{' && a[len(a)-1] == '}' {
			w[i] = a
		} else {
			w[i] = shquote(a)
		}
	}
	return strings.Join(w, " ")
}

func shquote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'`{}()<>|&;$^=#*?[]\"\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
