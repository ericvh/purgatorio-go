// Command win runs a program in an acme window, after Inferno's
// /acme/dis/win (appl/acme/acme/bin/src/win.b).
//
//	win [command [arg ...]]
//
// win makes a window through /mnt/acme/new, adds Send and Delete to its
// tag and names it dir/-sysname (or after the command). The command,
// sh by default, runs in a process group of its own with /dev/cons, and
// its standard input, output and error, on a pipe. What it writes goes
// into the body at the output point, which then moves past it. Text
// typed after the output point is the program's input: each line goes
// to it when its newline is typed. Button 2 on text that is not one of
// acme's commands sends that text to the program, adding a newline if
// it has none; acme's commands and button 3 go back to acme. When the
// window is deleted the program's group is killed.
//
// acme(4)'s event file cannot say when a read would not block, so a
// second copy of win (win -E) copies the events into a pipe; win waits
// on that pipe and the program's output.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const eventsize = 256

type event struct {
	c1, c2 byte
	q0, q1 int
	flag   int
	nr     int
	b      []byte
	r      []rune
}

var (
	pid       int // the program's
	parentpid int
	killing   bool

	ctlfd, eventfd, addrfd, datafd int
	fd0                            int // win's end of the pipe
	evfd                           int // events, from win -E

	// The output point and the typing after it, which is not yet sent.
	qp         int
	typing     []byte
	ntyper     int
	ntypebreak int

	blank = &event{c1: 'M', c2: 'X', nr: 1, b: []byte(" "), r: []rune(" ")}
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-E" {
		copier()
		return
	}
	argv := os.Args
	sys.Pctl(sys.FORKNS|sys.NEWPGRP, nil)
	parentpid, _ = sys.Pctl(0, nil)
	program := argv[1:]
	name := ""
	if len(program) == 0 {
		program = []string{"sh"}
		name = sysname()
	}
	if name == "" {
		prog := program[0]
		name = prog[strings.LastIndexByte(prog, '/')+1:]
		if len(argv) > 2 {
			for _, a := range argv[2:] {
				if len(name)+1+len(a) >= 16 {
					break
				}
				name += "_" + a
			}
		}
	}
	// The pipe the program talks on, as /dev/cons.
	if err := sys.Bind("#|", "/chan", sys.MAFTER); err != nil {
		fatal("pipe")
	}
	var err error
	ctlfd, err = sys.Open("/mnt/acme/new/ctl", sys.ORDWR)
	buf := make([]byte, 12)
	if err != nil {
		fatal("ctl")
	}
	if n, _ := syscall.Read(ctlfd, buf); n != 12 {
		fatal("ctl")
	}
	id, _ := strconv.Atoi(strings.TrimSpace(string(buf)))
	if fd, err := sys.Create("/env/acmewin", sys.OWRITE, 0o666); err == nil {
		syscall.Write(fd, []byte(strconv.Itoa(id)))
		syscall.Close(fd)
	}
	dir := fmt.Sprintf("/mnt/acme/%d/", id)
	if fd, err := sys.Open(dir+"tag", sys.OWRITE); err == nil {
		syscall.Write(fd, []byte(" Send Delete"))
		syscall.Close(fd)
	}
	eventfd, err = sys.Open(dir+"event", sys.ORDWR)
	if err == nil {
		addrfd, err = sys.Open(dir+"addr", sys.ORDWR)
	}
	if err == nil {
		datafd, err = sys.Open(dir+"data", sys.ORDWR)
	}
	if err != nil {
		fatal("data files")
	}
	run(program)
	fd0, err = sys.Open("/chan/data", sys.ORDWR)
	if err != nil {
		fatal("/chan/data")
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "/"
	}
	wd = strings.TrimSuffix(wd, "/")
	syscall.Write(ctlfd, []byte(fmt.Sprintf("name %s/-%s\n", wd, name)))
	syscall.Write(ctlfd, []byte(fmt.Sprintf("dumpdir %s/\n", wd)))
	syscall.Write(ctlfd, []byte(fmt.Sprintf("dump %s\n", strings.Join(argv, " "))))

	p, err := sys.Pipe()
	if err != nil {
		fatal("pipe")
	}
	if _, err := sys.Spawn("/bin/win.wasm", []string{"win", "-E"}, sys.NEWFD,
		[][2]int{{0, eventfd}, {1, p[1]}, {2, 2}}); err != nil {
		fatal("event copier")
	}
	syscall.Close(p[1])
	evfd = p[0]
	loop()
}

// copier is win -E: the event file on fd 0 into the pipe on fd 1.
func copier() {
	b := make([]byte, 8192)
	for {
		n, err := syscall.Read(0, b)
		if err != nil || n <= 0 {
			return
		}
		if _, err := syscall.Write(1, b[:n]); err != nil {
			return
		}
	}
}

// run starts the program in a group of its own, with the pipe's other
// end as its /dev/cons and its standard files (win.b's run).
func run(argv []string) {
	if err := sys.Bind("/chan/data1", "/dev/cons", sys.MREPL); err != nil {
		fmt.Fprintf(os.Stderr, "can't bind /dev/cons: %s\n", sys.Errstr())
		os.Exit(1)
	}
	c0, err0 := sys.Open("/dev/cons", sys.OREAD)
	c1, err1 := sys.Open("/dev/cons", sys.OWRITE)
	if err0 != nil || err1 != nil {
		fmt.Fprintf(os.Stderr, "can't open /dev/cons: %s\n", sys.Errstr())
		os.Exit(1)
	}
	file := argv[0]
	if !strings.HasSuffix(file, ".wasm") {
		file += ".wasm"
	}
	fds := [][2]int{{0, c0}, {1, c1}, {2, c1}}
	var err error
	pid, err = sys.Spawn(file, argv, sys.NEWFD|sys.NEWPGRP, fds)
	if err != nil && !strings.HasPrefix(file, "/") && !strings.HasPrefix(file, "./") {
		pid, err = sys.Spawn("/bin/"+file, argv, sys.NEWFD|sys.NEWPGRP, fds)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", argv[0], sys.Errstr())
		pid = 0
	}
	syscall.Close(c0)
	syscall.Close(c1)
}

func sysname() string {
	b, err := os.ReadFile("/dev/sysname")
	if err != nil {
		return ""
	}
	return string(b)
}

func postnote(pid int, note string) {
	fd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/ctl", sys.OWRITE)
	if err != nil {
		return
	}
	syscall.Write(fd, []byte(note+"grp"))
	syscall.Close(fd)
}

// fatal reports s, kills the program's group and win's own, and exits
// (win.b's error).
func fatal(s string) {
	if s != "" {
		fmt.Fprintf(os.Stderr, "win: %s: %s\n", s, sys.Errstr())
	}
	if killing {
		return
	}
	killing = true
	if pid != 0 {
		postnote(pid, "kill")
	}
	postnote(parentpid, "kill")
	sys.Exits("")
}

// loop waits for events and the program's output.
func loop() {
	ops := [][2]int{{sys.WaitRead, evfd}, {sys.WaitRead, fd0}}
	for {
		if nbuf > 0 {
			stdin()
			continue
		}
		i, err := sys.Wait(ops, -1)
		if err != nil {
			fatal("wait")
		}
		switch i {
		case 0:
			stdin()
		case 1:
			stdout()
		}
	}
}

var (
	ebuf [8192]byte
	bufp int
	nbuf int
)

func getec() byte {
	if nbuf == 0 {
		n, err := syscall.Read(evfd, ebuf[:])
		if err != nil || n <= 0 {
			fatal("")
		}
		nbuf, bufp = n, 0
	}
	nbuf--
	bufp++
	return ebuf[bufp-1]
}

func geten() int {
	n := 0
	c := getec()
	for ; '0' <= c && c <= '9'; c = getec() {
		n = n*10 + int(c-'0')
	}
	if c != ' ' {
		fatal("event number syntax")
	}
	return n
}

func gete(e *event) {
	e.c1 = getec()
	e.c2 = getec()
	e.q0 = geten()
	e.q1 = geten()
	e.flag = geten()
	e.nr = geten()
	if e.nr > eventsize {
		fatal("event string too long")
	}
	e.b, e.r = e.b[:0], e.r[:0]
	for i := 0; i < e.nr; i++ {
		var rb [utf8.UTFMax]byte
		n := 0
		for {
			rb[n] = getec()
			n++
			if utf8.FullRune(rb[:n]) || n == utf8.UTFMax {
				break
			}
		}
		r, _ := utf8.DecodeRune(rb[:n])
		e.b = append(e.b, rb[:n]...)
		e.r = append(e.r, r)
	}
	if getec() != '\n' {
		fatal("event syntax 2")
	}
}

// stdin handles one event (win.b's stdinx).
func stdin() {
	var e, e2, e3, e4 event
	gete(&e)
	switch e.c1 {
	case 'E', 'F':
		// Writes to the body, ours or others': they do not affect us.
	case 'K', 'M':
		switch e.c2 {
		case 'R':
			addtype(' ', ntyper, e.b, e.nr)
			sendtype(true)
		case 'I':
			if e.q0 < qp {
				qp += e.q1 - e.q0
			} else if e.q0 <= qp+ntyper {
				typex(&e)
			}
		case 'D':
			qp -= deletetype(&e)
		case 'x', 'X':
			if e.flag&2 != 0 {
				gete(&e2)
			}
			if e.flag&8 != 0 {
				gete(&e3)
				gete(&e4)
			}
			if e.flag&1 != 0 || (e.c2 == 'x' && e.nr == 0 && e2.nr == 0) {
				// Send it straight back.
				fmt.Fprintf(fdw(eventfd), "%c%c%d %d\n", e.c1, e.c2, e.q0, e.q1)
				break
			}
			if e.q0 == e.q1 && e.flag&2 != 0 {
				e2.flag = e.flag
				e = e2
			}
			if e.flag&8 != 0 {
				if e.q1 != e.q0 {
					send(&e, false)
					send(blank, false)
				}
				send(&e3, true)
			} else if e.q1 != e.q0 {
				send(&e, true)
			}
		case 'l', 'L':
			// Just send it back.
			if e.flag&2 != 0 {
				gete(&e2)
			}
			fmt.Fprintf(fdw(eventfd), "%c%c%d %d\n", e.c1, e.c2, e.q0, e.q1)
		case 'd', 'i':
		default:
			fmt.Printf("unknown message %c%c\n", e.c1, e.c2)
		}
	default:
		fmt.Printf("unknown message %c%c\n", e.c1, e.c2)
	}
}

type fdw int

func (f fdw) Write(b []byte) (int, error) { return syscall.Write(int(f), b) }

var (
	obuf  [8192 + utf8.UTFMax]byte
	npart int
)

// stdout puts what the program wrote into the body at the output point
// (win.b's stdoutx).
func stdout() {
	n, err := syscall.Read(fd0, obuf[npart:npart+8192])
	if err != nil || n < 0 {
		fatal("")
	}
	if n == 0 {
		// The program and everything it started have gone.
		syscall.Write(ctlfd, []byte("delete\n"))
		fatal("")
	}
	// Squash NULs.
	b := obuf[npart : npart+n]
	t := 0
	for _, c := range b {
		if c != 0 {
			b[t] = c
			t++
		}
	}
	n = npart + t
	// Keep a final partial rune.
	k := n
	if n > 0 {
		j := n - 1
		for j > 0 && j > n-utf8.UTFMax && !utf8.RuneStart(obuf[j]) {
			j--
		}
		if !utf8.FullRune(obuf[j:n]) {
			k = j
		}
	}
	if k > 0 {
		s := fmt.Sprintf("#%d", qp)
		if _, err := syscall.Write(addrfd, []byte(s)); err != nil {
			fatal("stdout writing address")
		}
		if w, err := syscall.Write(datafd, obuf[:k]); err != nil || w != k {
			fatal("stdout writing body")
		}
		qp += utf8.RuneCount(obuf[:k])
	}
	npart = copy(obuf[:], obuf[k:n])
}

// deletetype takes a deletion out of the typing; it returns how far the
// output point moves back.
func deletetype(e *event) int {
	q0, q1 := e.q0, e.q1
	if q1 <= qp {
		return e.q1 - e.q0
	}
	if q0 >= qp+ntyper {
		return 0
	}
	deltap := 0
	if q0 < qp {
		deltap = qp - q0
		q0 = 0
	} else {
		q0 -= qp
	}
	if q1 > qp+ntyper {
		q1 = ntyper
	} else {
		q1 -= qp
	}
	deltype(q0, q1)
	return deltap
}

// runeoff returns the byte offset of rune p in the typing.
func runeoff(p int) (int, bool) {
	b := 0
	for i := 0; i < p; i++ {
		if b >= len(typing) {
			return b, false
		}
		_, w := utf8.DecodeRune(typing[b:])
		b += w
	}
	return b, true
}

// addtype adds b, nr runes, to the typing at rune p0.
func addtype(c byte, p0 int, b []byte, nr int) {
	for i := 0; i < len(b); {
		r, w := utf8.DecodeRune(b[i:])
		i += w
		if r == 0x7F && c == 'K' {
			if pid != 0 {
				postnote(pid, "kill")
			}
			// Toss all typing.
			qp += ntyper + nr
			ntypebreak = 0
			typing = typing[:0]
			ntyper = 0
			return
		}
		if r == '\n' || r == 0x04 {
			ntypebreak++
		}
	}
	if p0 == ntyper {
		typing = append(typing, b...)
	} else {
		b0, ok := runeoff(p0)
		if !ok {
			fatal("typing: findrune")
		}
		t := make([]byte, 0, len(typing)+len(b))
		t = append(append(append(t, typing[:b0]...), b...), typing[b0:]...)
		typing = t
	}
	ntyper += nr
}

// sendtype sends each finished line of the typing to the program.
func sendtype(raw bool) {
	for ntypebreak > 0 {
		brkc := false
		for i := 0; i < len(typing); i++ {
			if typing[i] == '\n' || typing[i] == 0x04 {
				n := i
				if typing[i] == '\n' {
					n++
				}
				i++
				if w, err := syscall.Write(fd0, typing[:n]); err != nil || w != n {
					fatal("sending to program")
				}
				nr := utf8.RuneCount(typing[:i])
				if !raw {
					qp += nr
				}
				ntyper -= nr
				typing = append(typing[:0], typing[i:]...)
				i = -1
				ntypebreak--
				brkc = true
			}
		}
		if !brkc {
			fmt.Printf("no breakchar\n")
			ntypebreak = 0
		}
	}
}

// deltype deletes runes p0 to p1 of the typing.
func deltype(p0, p1 int) {
	b0, ok := runeoff(p0)
	if !ok {
		fatal("deltype 1")
	}
	b1 := b0
	for p := p0; p < p1; p++ {
		if b1 >= len(typing) {
			fatal("deltype 2")
		}
		r, w := utf8.DecodeRune(typing[b1:])
		b1 += w
		if r == '\n' || r == 0x04 {
			ntypebreak--
		}
	}
	typing = append(typing[:b0], typing[b1:]...)
	ntyper -= p1 - p0
}

// readbody reads up to the runes m to q1 of the body.
func readbody(m, q1 int) ([]byte, int) {
	buf := make([]byte, 128)
	syscall.Write(addrfd, []byte(fmt.Sprintf("#%d", m)))
	n, _ := syscall.Read(datafd, buf)
	if n < 0 {
		n = 0
	}
	nr := utf8.RuneCount(buf[:n])
	for m+nr > q1 && n > 0 {
		n--
		for n > 0 && !utf8.RuneStart(buf[n]) {
			n--
		}
		nr--
	}
	return buf[:n], nr
}

// typex takes text inserted in the typing area into the typing.
func typex(e *event) {
	if e.nr > 0 {
		addtype(e.c1, e.q0-qp, e.b, e.nr)
	} else {
		for m := e.q0; m < e.q1; {
			b, nr := readbody(m, e.q1)
			if len(b) == 0 {
				break
			}
			addtype(e.c1, m-qp, b, nr)
			m += nr
		}
	}
	sendtype(false)
}

// send puts the text of e on the end of the typing, and so to the
// program, with a newline after it if donl and it has none.
func send(e *event, donl bool) {
	end := qp + ntyper
	syscall.Write(addrfd, []byte(fmt.Sprintf("#%d", end)))
	var lastc rune
	if e.nr > 0 {
		syscall.Write(datafd, e.b)
		addtype(e.c1, ntyper, e.b, e.nr)
		lastc = e.r[e.nr-1]
	} else {
		for m := e.q0; m < e.q1; {
			b, nr := readbody(m, e.q1)
			if len(b) == 0 {
				break
			}
			syscall.Write(addrfd, []byte(fmt.Sprintf("#%d", end)))
			syscall.Write(datafd, b)
			addtype(e.c1, ntyper, b, nr)
			lastc = rune(b[len(b)-1])
			m += nr
			end += nr
		}
	}
	if donl && lastc != '\n' {
		syscall.Write(datafd, []byte("\n"))
		addtype(e.c1, ntyper, []byte("\n"), 1)
	}
	syscall.Write(ctlfd, []byte("dot=addr"))
	sendtype(false)
}
