package main

// Values, the conventions of the streams between verbs, and error
// reports: Inferno's Fslib (appl/lib/fslib.b, module/fslib.m).
//
// An fs value is a stream of fsdata on a channel. After each item the
// receiver replies how to go on: next, down (into a directory or a
// file's data), skip (the rest of a directory or file) or quit. The
// first item is the root directory, whose name is its path; a nil item
// ends a directory's or a file's contents.

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Replies.
const (
	Next = iota
	Down
	Skip
	Quit
)

type fsdata struct {
	dir  *sys.Dir
	data []byte
}

type fsmsg struct {
	d     fsdata
	reply chan int
}

type fschan chan fsmsg

// entry is a file of an entries stream: its directory entry, path and
// depth.
type entry struct {
	dir   *sys.Dir
	path  string
	depth int
}

type entrychan struct {
	sync chan int
	c    chan entry
}

// A gate query with a nil reply channel ends the gate.
type gatequery struct {
	e     entry
	reply chan bool
}

type gatechan chan gatequery

// A comparison with a nil reply channel ends the selector. The reply
// says which of the two entries to take: 1 for the first, 2 for the
// second.
type cmpquery struct {
	d0, d1 *sys.Dir
	reply  chan int
}

type cmpchan chan cmpquery

// Values.
type value interface{ typec() byte }

type (
	vX struct{ i fschan }
	vT struct{ i entrychan }
	vP struct{ i gatechan }
	vC struct{ i string } // a shell command, its braced block
	vS struct{ i string }
	vV struct{ i chan int }
	vM struct{ i cmpchan }
)

func (vX) typec() byte { return 'x' }
func (vT) typec() byte { return 't' }
func (vP) typec() byte { return 'p' }
func (vC) typec() byte { return 'c' }
func (vS) typec() byte { return 's' }
func (vV) typec() byte { return 'v' }
func (vM) typec() byte { return 'm' }

// typeerr is raised when a value is not of the type a verb expects.
type typeerr struct{}

func typeerror(tc byte, v value) {
	fmt.Fprintf(os.Stderr, "fs: bad type conversion, expected %s, was actually %s\n", type2s(tc), type2s(v.typec()))
	panic(typeerr{})
}

func vx(v value) fschan {
	if x, ok := v.(vX); ok {
		return x.i
	}
	typeerror('x', v)
	return nil
}

func vt(v value) entrychan {
	if x, ok := v.(vT); ok {
		return x.i
	}
	typeerror('t', v)
	return entrychan{}
}

func vp(v value) gatechan {
	if x, ok := v.(vP); ok {
		return x.i
	}
	typeerror('p', v)
	return nil
}

func vc(v value) string {
	if x, ok := v.(vC); ok {
		return x.i
	}
	typeerror('c', v)
	return ""
}

func vs(v value) string {
	if x, ok := v.(vS); ok {
		return x.i
	}
	typeerror('s', v)
	return ""
}

func vv(v value) chan int {
	if x, ok := v.(vV); ok {
		return x.i
	}
	typeerror('v', v)
	return nil
}

func vm(v value) cmpchan {
	if x, ok := v.(vM); ok {
		return x.i
	}
	typeerror('m', v)
	return nil
}

// discard tells the process behind a value that it is not wanted.
func discard(v value) {
	switch x := v.(type) {
	case vX:
		(<-x.i).reply <- Quit
	case vP:
		x.i <- gatequery{}
	case vM:
		x.i <- cmpquery{}
	case vV:
		x.i <- 0
	case vT:
		x.i.sync <- 0
	}
}

func discardall(opts []option, vl []value) {
	for _, o := range opts {
		vl = append(vl, o.args...)
	}
	for _, v := range vl {
		discard(v)
	}
}

// option is a verb's option and its arguments.
type option struct {
	opt  byte
	args []value
}

func nulldir() *sys.Dir {
	d := sys.NullDir()
	return &d
}

// sendnulldir sends an empty root directory.
func sendnulldir(c fschan) int {
	reply := make(chan int)
	c <- fsmsg{fsdata{nulldir(), nil}, reply}
	r := <-reply
	if r == Down {
		c <- fsmsg{fsdata{}, reply}
		if <-reply != Quit {
			return Quit
		}
		return Next
	}
	return r
}

// copyfs copies the contents (not the entry itself) of a directory from
// src to dst.
func copyfs(src, dst fschan) int {
	indent := 1
	myreply := make(chan int)
	for {
		m := <-src
		dst <- fsmsg{m.d, myreply}
		r := <-myreply
		m.reply <- r
		switch r {
		case Quit:
			return Quit
		case Next:
			if m.d.dir == nil && m.d.data == nil {
				indent--
				if indent == 0 {
					return Next
				}
			}
		case Skip:
			indent--
			if indent == 0 {
				return Next
			}
		case Down:
			if m.d.dir != nil || m.d.data != nil {
				indent++
			}
		}
	}
}

// Report collects the error reports of the verbs' processes; they are
// held until enable, and printed until every process that started
// reporting has quit.
type Report struct {
	mu  sync.Mutex
	n   int
	all chan string
}

type reportquit struct{}

func newReport() *Report { return &Report{all: make(chan string)} }

// start returns the channel a process named name reports on. It sends
// "" when it quits.
func (r *Report) start(name string) chan string {
	if r == nil {
		return nil
	}
	c := make(chan string)
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
	go func() {
		for s := range c {
			if s == "" {
				r.all <- ""
				return
			}
			r.all <- name + ": " + s
		}
	}()
	return c
}

// wait prints the reports until every reporter has quit.
func (r *Report) wait() {
	for {
		r.mu.Lock()
		n := r.n
		r.mu.Unlock()
		if n == 0 {
			return
		}
		s := <-r.all
		if s == "" {
			r.mu.Lock()
			r.n--
			r.mu.Unlock()
			continue
		}
		fmt.Fprintf(os.Stderr, "fs: %s\n", s)
	}
}

// quitproc ends a process, telling its reporter.
type quitproc struct{}

func quit(errorc chan string) {
	if errorc != nil {
		errorc <- ""
	}
	panic(quitproc{})
}

// proc runs f as a process: quit ends it.
func proc(f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(quitproc); !ok {
					panic(r)
				}
			}
		}()
		f()
	}()
}

func report(errorc chan string, err string) {
	if errorc != nil {
		errorc <- err
	}
}

func type2s(c byte) string {
	switch c {
	case 'a':
		return "any"
	case 'x':
		return "fs"
	case 's':
		return "string"
	case 'v':
		return "void"
	case 'p':
		return "gate"
	case 'c':
		return "command"
	case 't':
		return "entries"
	case 'm':
		return "selector"
	}
	return fmt.Sprintf("unknowntype('%c')", c)
}

// typecompat reports whether a module of type t1 suits a caller that
// expects t0.
func typecompat(t0, t1 string) bool {
	rt0, at0, ot0 := splittype(t0)
	rt1, at1, ot1 := splittype(t1)
	if (rt0 != rt1 && rt0 != 'a') || at0 != at1 {
		return false
	}
	for i := 1; i < len(ot0); i++ {
		j := i
		for ; j < len(ot0); j++ {
			if ot0[j] == '-' {
				break
			}
		}
		t, ok := opttypes(ot0[i], ot1)
		if !ok || ot0[i:j] != t {
			return false
		}
		i = j + 1
	}
	return true
}

// splittype splits a type into result type, argument types and option
// types.
func splittype(t string) (byte, string, string) {
	if t == "" {
		return 0, "", ""
	}
	i := 1
	for ; i < len(t); i++ {
		if t[i] == '-' {
			break
		}
	}
	return t[0], t[1:i], t[i:]
}

// opttypes returns the argument types of option opt.
func opttypes(opt byte, opts string) (string, bool) {
	for i := 1; i < len(opts); i++ {
		if opts[i] == opt && opts[i-1] == '-' {
			j := i + 1
			for ; j < len(opts); j++ {
				if opts[j] == '-' {
					break
				}
			}
			return opts[i+1 : j], true
		}
	}
	return "", false
}

// cmdusage is the usage of verb s of type t.
func cmdusage(s, t string) string {
	if s == "" {
		return ""
	}
	oi := strings.IndexByte(t, '-')
	if oi < 0 {
		oi = len(t)
	}
	if oi < len(t) {
		var single, multi string
		for i := oi; i < len(t)-1; {
			j := i + 1
			for ; j < len(t); j++ {
				if t[j] == '-' {
					break
				}
			}
			optargs := t[i+2 : j]
			if optargs == "" {
				single += string(t[i+1])
			} else {
				multi += fmt.Sprintf(" [-%c", t[i+1])
				for k := 0; k < len(optargs); k++ {
					multi += " " + type2s(optargs[k])
				}
				multi += "]"
			}
			i = j
		}
		if single != "" {
			s += " [-" + single + "]"
		}
		s += multi
	}
	multi := false
	if oi > 2 && t[oi-1] == '*' {
		multi = true
		oi -= 2
	}
	k := 1
	for ; k < oi; k++ {
		s += " " + type2s(t[k])
	}
	if multi {
		s += " [" + type2s(t[k]) + "...]"
	}
	return s + " -> " + type2s(t[0])
}
