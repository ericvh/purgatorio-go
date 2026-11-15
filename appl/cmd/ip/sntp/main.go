// Command ip/sntp sets the clock from a time server (Inferno sntp(8)),
// by RFC 1361's simple network time protocol: it asks server (default
// $ntp) up to four times, three seconds apart, and writes the time it
// gets to #r/rtc and /dev/time. With -i it only asks; -d reports the
// reply.
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/feed"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	self       = "/bin/ip/sntp.wasm"
	retries    = 4
	delay      = 3 * 1000 // milliseconds
	sntplen    = 4 + 3*4 + 4*8
	version    = 1 // accepted by version 2 and version 3 servers
	clientMode = 3
	epoch      = 86400 * (365*70 + 17) // seconds between 1 Jan 1900 and 1 Jan 1970
	microsec   = 1000000
)

type sntp struct {
	li, vn, mode     int
	stratum          int
	poll, precision  int
	rootdelay        uint32
	dispersion       uint32
	clockid          string
	reftime, orgtime uint64
	rcvtime, xmttime uint64
}

var (
	debug  int
	server = "$ntp"
	stderr = os.Stderr
	rd     *feed.Reader
)

func main() {
	feed.Serve()
	a := arg.New(os.Args)
	a.SetUsage("sntp [-d] [server]")
	doset := true
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd':
			debug++
		case 'i':
			doset = false
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) > 1 {
		a.Usage()
	}
	if len(args) > 0 {
		server = args[0]
	}
	sys.Pctl(sys.NEWPGRP|sys.FORKFD, nil)
	conn, err := dial.Dial(dial.Netmkaddr(server, "udp", "ntp"), "")
	if err != nil {
		fmt.Fprintf(stderr, "sntp: can't dial %s: %s\n", server, sys.Errstr())
		sys.Exits("fail:dial")
	}
	rd, err = feed.Start(self, int(conn.Dfd.Fd()))
	if err != nil {
		fmt.Fprintf(stderr, "sntp: can't start reader: %s\n", sys.Errstr())
		sys.Exits("fail:reader")
	}
	for i := 0; i < retries; i++ {
		req := &sntp{vn: version, mode: clientMode, poll: 6}
		req.orgtime = uint64(now()+epoch) << 32
		b := req.pack()
		if n, _ := conn.Dfd.Write(b); n != len(b) {
			fmt.Fprintf(stderr, "sntp: UDP write failed: %s\n", sys.Errstr())
			continue
		}
		reply := readreply(delay)
		if reply == nil {
			continue
		}
		if debug > 0 {
			fmt.Fprintf(stderr, "LI = %d, version = %d, mode = %d\n", reply.li, reply.vn, reply.mode)
			if reply.stratum == 1 {
				fmt.Fprintf(stderr, "stratum = 1 (%s), ", reply.clockid)
			} else {
				fmt.Fprintf(stderr, "stratum = %d, ", reply.stratum)
			}
			fmt.Fprintf(stderr, "poll = %d, prec = %d\n", reply.poll, reply.precision)
			fmt.Fprintf(stderr, "rootdelay = %d, dispersion = %d\n", reply.rootdelay, reply.dispersion)
		}
		if reply.vn == 0 || reply.vn > 3 {
			continue // unsupported version, ignored
		}
		if reply.mode >= 6 || reply.mode == clientMode {
			continue
		}
		t := int64(reply.xmttime>>32) - epoch
		if t <= 1120000000 {
			continue
		}
		if reply.li == 3 || reply.stratum == 0 { // unsynchronised
			stderr.WriteString("sntp: time server not synchronised to reference time\n")
		}
		if debug > 0 {
			fmt.Printf("%d\n", t)
		}
		if doset {
			settime("#r/rtc", t)
			settime("/dev/time", t*microsec)
		}
		quit("")
	}
	fmt.Fprintf(stderr, "sntp: no response from server %s\n", server)
	quit("timeout")
}

// readreply waits up to ms milliseconds for a valid reply; nil on a
// timeout.
func readreply(ms int) *sntp {
	for {
		b, _, err := rd.Read(ms)
		if err != nil {
			if debug > 0 {
				fmt.Fprintf(stderr, "sntp: UDP read failed: %s\n", err)
			}
			quit("read error")
		}
		if b == nil {
			return nil
		}
		if r := unpack(b); r != nil {
			return r
		}
		// ignore bad replies
		if debug > 0 {
			fmt.Fprintf(stderr, "sntp: invalid reply (len %d)\n", len(b))
		}
	}
}

func quit(s string) {
	rd.Close()
	if s != "" {
		sys.Exits("fail:" + s)
	}
	sys.Exits("")
}

// now is the time in seconds, from #r/rtc when it has been set.
func now() int64 {
	if n := rdn("#r/rtc"); n > 300 { // ie, possibly set
		return n
	}
	n := rdn("/dev/time")
	if n <= 0 {
		return 0
	}
	return n / microsec
}

func rdn(f string) int64 {
	b, err := os.ReadFile(f)
	if err != nil {
		return -1
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func settime(f string, t int64) {
	if fd, err := sys.Open(f, sys.OWRITE); err == nil {
		file := os.NewFile(uintptr(fd), f)
		fmt.Fprintf(file, "%d", t)
		file.Close()
	}
}

func unpack(a []byte) *sntp {
	if len(a) < sntplen {
		return nil
	}
	s := &sntp{}
	mode := int(a[0])
	s.li = mode >> 6
	s.vn = mode >> 3 & 7
	s.mode = mode & 7
	s.stratum = int(a[1])
	s.poll = int(int8(a[2]))
	s.precision = int(int8(a[3]))
	s.rootdelay = binary.BigEndian.Uint32(a[4:])
	s.dispersion = binary.BigEndian.Uint32(a[8:])
	if s.stratum <= 1 {
		i := 12
		for ; i < 16 && a[i] != 0; i++ {
		}
		s.clockid = string(a[12:i])
	} else {
		s.clockid = fmt.Sprintf("%d.%d.%d.%d", a[12], a[13], a[14], a[15])
	}
	s.reftime = binary.BigEndian.Uint64(a[16:])
	s.orgtime = binary.BigEndian.Uint64(a[24:])
	s.rcvtime = binary.BigEndian.Uint64(a[32:])
	s.xmttime = binary.BigEndian.Uint64(a[40:])
	return s
}

func (s *sntp) pack() []byte {
	a := make([]byte, sntplen)
	a[0] = byte(s.li<<6 | s.vn<<3 | s.mode)
	a[1] = byte(s.stratum)
	a[2] = byte(s.poll)
	a[3] = byte(s.precision)
	binary.BigEndian.PutUint32(a[4:], s.rootdelay)
	binary.BigEndian.PutUint32(a[8:], s.dispersion)
	binary.BigEndian.PutUint64(a[16:], s.reftime)
	binary.BigEndian.PutUint64(a[24:], s.orgtime)
	binary.BigEndian.PutUint64(a[32:], s.rcvtime)
	binary.BigEndian.PutUint64(a[40:], s.xmttime)
	return a
}
