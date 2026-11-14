// Command telnet talks the Telnet protocol to a remote machine (Inferno
// telnet(1)): it copies standard input to the connection and the
// connection, less the protocol's negotiations, to standard output.
//
// Inferno's telnet runs a thread for each direction. A guest is single
// threaded and #I cannot say whether a read would block, so a second
// prog (telnet.wasm with a hidden argument) copies the connection to a
// pipe, and telnet waits for the pipe or standard input to be readable.
// The first side to end finishes the session.
package main

import (
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Telnet commands.
const (
	cSe          = 240
	cAreYouThere = 246
	cSb          = 250
	cWill        = 251
	cWont        = 252
	cDo          = 253
	cDont        = 254
	cIac         = 255
)

type option struct {
	name          string
	code          int
	noway         bool
	remote, local bool
}

// Indexes into opts that the code refers to.
const (
	optEcho = 1
	optTerm = 6
)

var opts = []option{
	{name: "binary", code: 0},
	{name: "echo", code: 1},
	{name: "suppress go ahead", code: 3},
	{name: "status", code: 5, noway: true},
	{name: "timing", code: 6, noway: true},
	{name: "det", code: 20, noway: true},
	{name: "terminal", code: 24},
	{name: "end of record", code: 25, noway: true},
	{name: "uid", code: 26, noway: true},
	{name: "outmark", code: 27, noway: true},
	{name: "ttyloc", code: 28, noway: true},
	{name: "3270 mode", code: 29, noway: true},
	{name: "pad x.3", code: 30, noway: true},
	{name: "window size", code: 31, noway: true},
	{name: "speed", code: 32, noway: true},
	{name: "flow control", code: 33, noway: true},
	{name: "line mode", code: 34, noway: true},
	{name: "X display loc", code: 35, noway: true},
	{name: "Extended", code: 255, noway: true},
}

const (
	bufsize = 2048
	copyarg = "-#copy" // runs the prog that copies the connection
)

var (
	net    *os.File // the connection, for writing
	netin  *os.File // the pipe the connection is copied to
	stderr = os.Stderr
	stdout = os.Stdout
	ccfd   *os.File
)

// The connection's input, and the output it is copied to.
var (
	inbuf  = make([]byte, bufsize)
	inptr  int
	nbyte  int
	outbuf = make([]byte, 0, bufsize)
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == copyarg {
		sys.Stream(0, 1, bufsize)
		return
	}
	if len(os.Args) < 2 {
		stderr.WriteString("usage: telnet host [port]\n")
		sys.Exits("fail:usage")
	}
	host := os.Args[1]
	port := "23"
	if len(os.Args) > 2 {
		port = os.Args[2]
	}
	dest := dial.Netmkaddr(host, "tcp", port)
	c, err := dial.Dial(dest, "")
	if err != nil {
		stderr.WriteString("telnet: can't dial " + dest + ": " + err.Error() + "\n")
		sys.Exits("fail:dial")
	}
	net = c.Dfd
	stderr.WriteString("telnet: connected to " + host + "\n")
	p, err := sys.Pipe()
	if err != nil {
		stderr.WriteString("telnet: can't make pipe: " + err.Error() + "\n")
		sys.Exits("fail:pipe")
	}
	pid, err := sys.Spawn("/bin/telnet.wasm", []string{"telnet", copyarg}, sys.NEWFD,
		[][2]int{{0, int(net.Fd())}, {1, p[1]}, {2, 2}})
	if err != nil {
		stderr.WriteString("telnet: can't start reader: " + err.Error() + "\n")
		sys.Exits("fail:spawn")
	}
	sys.Close(p[1])
	netin = os.NewFile(uintptr(p[0]), "net")
	raw(true)
	nfd := p[0]
	ub := make([]byte, 1024)
	for {
		i, err := sys.Wait([][2]int{{sys.WaitRead, nfd}, {sys.WaitRead, 0}}, -1)
		if err != nil {
			break
		}
		if i == 0 {
			if !fromnet() {
				stderr.WriteString("telnet: remote host closed connection\n")
				break
			}
			continue
		}
		n, _ := os.Stdin.Read(ub)
		if n <= 0 {
			stderr.WriteString("telnet: error reading stdin: " + sys.Errstr() + "\n")
			break
		}
		if !opts[optEcho].remote {
			stdout.Write(ub[:n])
		}
		net.Write(ub[:n])
	}
	kill(pid)
	raw(false)
}

func kill(pid int) {
	fd, err := sys.Open("/prog/"+strconv.Itoa(pid)+"/ctl", sys.OWRITE)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(fd), "ctl")
	f.WriteString("kill")
	f.Close()
}

// fromnet serves what the connection has to give; false when it has
// closed.
func fromnet() bool {
	for {
		c := getc()
		switch c {
		case -1:
			flushout()
			return false
		case cIac:
			c = getc()
			if c != cIac {
				flushout()
				if control(c) < 0 {
					return false
				}
			} else {
				putc(c)
			}
		default:
			putc(c)
		}
		if nbyte == 0 {
			flushout()
			return true
		}
	}
}

func getc() int {
	if nbyte == 0 {
		flushout()
		n, _ := netin.Read(inbuf)
		if n <= 0 {
			return -1
		}
		nbyte, inptr = n, 0
	}
	nbyte--
	inptr++
	return int(inbuf[inptr-1])
}

func putc(c int) {
	outbuf = append(outbuf, byte(c))
	if len(outbuf) == bufsize {
		flushout()
	}
}

func flushout() {
	if len(outbuf) > 0 {
		stdout.Write(outbuf)
		outbuf = outbuf[:0]
	}
}

func control(c int) int {
	switch c {
	case cAreYouThere:
		net.Write([]byte("Inferno telnet\r\n"))
	case cSb:
		return sub()
	case cWill:
		return will()
	case cWont:
		return wont()
	case cDo:
		return doit()
	case cDont:
		return dont()
	case cSe:
		stderr.WriteString("telnet: SE without an SB\n")
	case -1:
		return -1
	}
	return 0
}

func lookup(code int) int {
	for i, o := range opts {
		if o.code == code {
			return i
		}
	}
	return -1
}

func sub() int {
	var subneg []int
	for {
		c := getc()
		if c == cIac {
			c = getc()
			if c == cSe {
				break
			}
			subneg = append(subneg, cIac)
		}
		if c < 0 {
			return -1
		}
		subneg = append(subneg, c)
	}
	if len(subneg) == 0 {
		return 0
	}
	if lookup(subneg[0]) == optTerm {
		sbsend(opts[optTerm].code, []byte("network"))
	}
	return 0
}

func sbsend(code int, data []byte) {
	b := append([]byte{cIac, cSb, byte(code), 0}, data...)
	net.Write(append(b, cIac, cSe))
}

func will() int {
	c := getc()
	if c < 0 {
		return -1
	}
	i := lookup(c)
	if i < 0 {
		return send3(cIac, cDont, c)
	}
	rv := 0
	if opts[i].noway {
		send3(cIac, cDont, c)
	} else if !opts[i].remote {
		rv |= send3(cIac, cDo, c)
	}
	opts[i].remote = true
	return rv
}

func wont() int {
	c := getc()
	if c < 0 {
		return -1
	}
	i := lookup(c)
	if i < 0 {
		return 0
	}
	rv := 0
	if opts[i].remote {
		rv |= send3(cIac, cDont, c)
	}
	opts[i].remote = false
	return rv
}

func doit() int {
	c := getc()
	if c < 0 {
		return -1
	}
	i := lookup(c)
	if i < 0 || opts[i].noway {
		send3(cIac, cWont, c)
		return 0
	}
	rv := 0
	if !opts[i].local {
		rv |= send3(cIac, cWill, c)
	}
	opts[i].local = true
	return rv
}

func dont() int {
	c := getc()
	if c < 0 {
		return -1
	}
	i := lookup(c)
	if i < 0 || opts[i].noway {
		return 0
	}
	rv := 0
	if opts[i].local {
		rv |= send3(cIac, cWont, c)
	}
	opts[i].local = false
	return rv
}

func send3(c0, c1, c2 int) int {
	if n, _ := net.Write([]byte{byte(c0), byte(c1), byte(c2)}); n != 3 {
		return -1
	}
	return 0
}

func raw(on bool) {
	if ccfd == nil {
		fd, err := sys.Open("/dev/consctl", sys.OWRITE)
		if err != nil {
			stderr.WriteString("telnet: cannot open /dev/consctl: " + err.Error() + "\n")
			return
		}
		ccfd = os.NewFile(uintptr(fd), "/dev/consctl")
	}
	if on {
		ccfd.Write([]byte("rawon"))
	} else {
		ccfd.Write([]byte("rawoff"))
	}
}
