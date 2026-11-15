// Command ip/tftpd is a TFTP server (Inferno tftpd(8)): it serves the
// files under homedir (default /services/tftpd) on udp port 69, or that
// given by -p, as user none. Each transfer runs in a prog of its own,
// on a conversation dialled back to the client. With -r, names outside
// homedir, starting with #, or with .. are refused.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/feed"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	self       = "/bin/ip/tftpd.wasm"
	xferArg    = "-#xfer"
	udphdrsize = 52

	tftpRead  = 1
	tftpWrite = 2
	tftpData  = 3
	tftpAck   = 4
	tftpError = 5

	segsize = 512
)

var (
	dir        = "/services/tftpd"
	netdir     = "/net"
	dbg        int
	restricted bool
	port       = 69
	stderr     = os.Stderr
)

func main() {
	feed.Serve()
	if len(os.Args) == 6 && os.Args[1] == xferArg {
		dbg, _ = strconv.Atoi(os.Args[2])
		transfer(os.Args[3], os.Args[4], os.Args[5])
		return
	}
	sys.Pctl(sys.NEWPGRP|sys.FORKFD|sys.FORKNS, nil)
	a := arg.New(os.Args)
	a.SetUsage("tftpd [-dr] [-p port] [-h homedir] [-x network-dir]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd':
			dbg++
		case 'h':
			dir = a.EArg()
		case 'r':
			restricted = true
		case 'p':
			port, _ = strconv.Atoi(a.EArg())
		case 'x':
			netdir = a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) > 0 {
		netdir = args[0]
		args = args[1:]
	}
	if len(args) > 0 {
		a.Usage()
	}
	if err := sys.Chdir(dir); err != nil {
		fatal("can't chdir to " + dir)
	}
	mainthing()
}

func debugf(format string, args ...any) {
	if dbg > 0 {
		fmt.Fprintf(stderr, "tftpd: %d: %s\n", os.Getpid(), fmt.Sprintf(format, args...))
	}
}

func mainthing() {
	req := openlisten()
	setuser()
	bigbuf := make([]byte, 32768)
	for {
		dlen, err := req.Read(bigbuf)
		if err != nil && dlen <= 0 {
			fatal("listen")
		}
		if dlen < udphdrsize+2 {
			continue
		}
		h := bigbuf[:udphdrsize]
		ra := netip.AddrFrom16([16]byte(h[0:16])).Unmap()
		rport := binary.BigEndian.Uint16(h[48:])
		raddr := fmt.Sprintf("%s/udp!%s!%d", netdir, ra, rport)
		debugf("raddr=%s", raddr)
		op := int(binary.BigEndian.Uint16(bigbuf[udphdrsize:]))
		mbuf := bigbuf[udphdrsize+2 : dlen]
		switch op {
		case tftpRead, tftpWrite:
		case tftpError:
			debugf("tftp error")
			continue
		default:
			if cx := dialback(raddr); cx != nil {
				nak(cx, 4, "Illegal TFTP operation")
				cx.Close()
			}
			continue
		}
		path, rest, ok1 := strings.Cut(string(mbuf), "\x00")
		mode, _, ok2 := strings.Cut(rest, "\x00")
		debugf("path = %s, mode = %s", path, mode)
		if !ok1 || !ok2 {
			if cx := dialback(raddr); cx != nil {
				nak(cx, 0, "bad tftpmode")
				cx.Close()
			}
			continue
		}
		if restricted && dodgy(path) {
			if cx := dialback(raddr); cx != nil {
				nak(cx, 4, "Permission denied")
				cx.Close()
			}
			continue
		}
		rw := "r"
		if op == tftpWrite {
			rw = "w"
		}
		_, err = sys.Spawn(self, []string{"tftpd", xferArg, strconv.Itoa(dbg), rw + raddr, path, mode}, sys.NEWFD, [][2]int{{1, 1}, {2, 2}})
		if err != nil {
			fmt.Fprintf(stderr, "tftpd: can't start transfer: %s\n", sys.Errstr())
		}
	}
}

func dialback(raddr string) *os.File {
	cx, err := dial.Dial(raddr, "")
	if err != nil {
		fatal("dialing " + raddr)
	}
	return cx.Dfd
}

// transfer runs one transfer in its own prog: how is r or w followed by
// the client's address.
func transfer(how, name, mode string) {
	sys.Pctl(sys.NEWPGRP, nil)
	cx := dialback(how[1:])
	if how[0] == 'r' {
		sendfile(cx, name, mode)
	} else {
		recvfile(cx, name, mode)
	}
}

func dodgy(path string) bool {
	n, nd := len(path), len(dir)
	if n == 0 || path[0] == '#' ||
		path[0] == '/' && (n < nd+1 || path[:nd] != dir || path[nd] != '/') {
		return true
	}
	for _, f := range strings.Split(path, "/") {
		if f == ".." {
			return true
		}
	}
	return false
}

func sendfile(net *os.File, name, mode string) {
	debugf("sendfile: name=%s mode=%s", name, mode)
	if name == "" {
		nak(net, 0, "not in our database")
		return
	}
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		debugf("open failed: %s", name)
		nak(net, 0, sys.Errstr())
		return
	}
	file := os.NewFile(uintptr(fd), name)
	rd, err := feed.Start(self, int(net.Fd()))
	if err != nil {
		fatal("can't start reader")
	}
	defer rd.Close()
	block := 0
	buf := make([]byte, 4+segsize)
mainloop:
	for {
		block++
		buf[0], buf[1], buf[2], buf[3] = 0, tftpData, byte(block>>8), byte(block)
		n, err := io.ReadFull(file, buf[4:])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			nak(net, 0, sys.Errstr())
			break
		}
		debugf("write of %d to block %d", n, block)
		retries := 15
		if !sendblock(net, buf[:4+n]) {
			return
		}
		for rxl := 0; rxl < 10; rxl++ {
			ack, _, err := rd.Read(1000)
			if err != nil {
				return
			}
			if ack == nil { // timeout: send it again
				if retries--; retries < 0 {
					debugf("too many retries")
					return
				}
				if !sendblock(net, buf[:4+n]) {
					return
				}
				rxl--
				continue
			}
			if len(ack) < 4 {
				continue
			}
			op := int(ack[0])<<8 | int(ack[1])
			if op == tftpError {
				break mainloop
			}
			ackblock := int(ack[2])<<8 | int(ack[3])
			debugf("got ack: block=%d ackblock=%d", block, ackblock)
			if ackblock == block&0xffff {
				break
			}
			if ackblock == 0xffff {
				block--
				break
			}
		}
		if n < segsize {
			break
		}
	}
}

func sendblock(net *os.File, b []byte) bool {
	ret, err := net.Write(b)
	if err != nil {
		fatal("tftp: network write error")
	}
	return ret == len(b)
}

func recvfile(net *os.File, name, mode string) {
	debugf("recvfile: name=%s mode=%s", name, mode)
	fd, err := sys.Create(name, sys.OWRITE, 0o666)
	if err != nil {
		nak(net, 0, sys.Errstr())
		return
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	rd, err := feed.Start(self, int(net.Fd()))
	if err != nil {
		fatal("can't start reader")
	}
	defer rd.Close()
	block := 0
	ack(net, block)
	block++
	for {
		debugf("reading block %d", block)
		buf, _, err := rd.Read(15000)
		if err != nil || buf == nil || len(buf) < 4 {
			break
		}
		op := int(buf[0])<<8 | int(buf[1])
		if op == tftpError {
			break
		}
		n := len(buf) - 4
		inblock := int(buf[2])<<8 | int(buf[3])
		if op == tftpData {
			if inblock == block&0xffff {
				if _, err := file.Write(buf[4:]); err != nil {
					nak(net, 0, sys.Errstr())
					break
				}
				block++
			}
			ack(net, inblock)
			if n < segsize && inblock == (block-1)&0xffff {
				break
			}
		}
	}
}

func ack(fd *os.File, block int) {
	if _, err := fd.Write([]byte{0, tftpAck, byte(block >> 8), byte(block)}); err != nil {
		fatal("write ack")
	}
}

func nak(fd *os.File, code int, msg string) {
	fmt.Printf("nak: %s\n", msg)
	buf := append([]byte{0, tftpError, 0, byte(code)}, msg...)
	if _, err := fd.Write(append(buf, 0)); err != nil {
		fatal("write nak")
	}
}

func fatal(msg string) {
	fmt.Fprintf(stderr, "tftpd: %s: %s\n", msg, sys.Errstr())
	sys.Exits("fail:error")
}

func openlisten() *os.File {
	name := netdir + "/udp!*!" + strconv.Itoa(port)
	c, err := dial.Announce(name)
	if err != nil {
		fatal("can't announce " + name)
	}
	if _, err := c.Cfd.Write([]byte("headers")); err != nil {
		fatal("can't set header mode")
	}
	fd, err := sys.Open(c.Dir+"/data", sys.ORDWR)
	if err != nil {
		fatal("open udp data")
	}
	return os.NewFile(uintptr(fd), "data")
}

func setuser() {
	if fd, err := sys.Open("/dev/user", sys.OWRITE); err == nil {
		f := os.NewFile(uintptr(fd), "user")
		f.Write([]byte("none"))
		f.Close()
	}
}
