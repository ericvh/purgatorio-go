// Command ip/ping sends ICMP echo requests to a host and reports the
// replies (Inferno ping(8)): by default 32 messages of 64 bytes, a
// second apart, through /net/icmp. -s sets the message size, -i the
// interval in milliseconds and -n the number of messages; -a shows the
// addresses in each reply, -l only lost messages and -q nothing but the
// summary of losses.
package main

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/feed"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const self = "/bin/ip/ping.wasm"

// Packet types.
const (
	EchoReply   = 0
	EchoRequest = 8
)

const (
	nmsgDefault = 32
	intervalDef = 1000 // ms
)

// Offsets in a message: an IPv4 header, then ICMP.
const (
	Ottl  = 8
	Osrc  = 12
	Odst  = 16
	Otype = 20
	Ocode = 21
	Oseq  = 26
	Odata = 28
)

type icmp struct {
	ttl      int
	src, dst netip.Addr
	ptype    int
	code     int
	seq      int
	munged   int
	time     time.Time
}

type req struct {
	seq     int
	time    time.Time
	rtt     int64 // µs
	ttl     int
	replied bool
}

var (
	debug     int
	quiet     bool
	lostonly  bool
	lostmsgs  int
	rcvdmsgs  int
	sum       int64
	firstseq  int
	addresses bool
)

func main() {
	feed.Serve()
	msglen, interval := 0, 0
	nmsg := nmsgDefault
	a := arg.New(os.Args)
	a.SetUsage("ip/ping [-alq] [-s msgsize] [-i millisecs] [-n #pings] destination")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'l':
			lostonly = true
		case 'd':
			debug++
		case 's':
			msglen, _ = strconv.Atoi(a.EArg())
		case 'i':
			interval, _ = strconv.Atoi(a.EArg())
		case 'n':
			nmsg, _ = strconv.Atoi(a.EArg())
		case 'a':
			addresses = true
		case 'q':
			quiet = true
		default:
			a.Usage()
		}
	}
	if msglen < 32 {
		msglen = 64
	}
	if msglen >= 65*1024 {
		msglen = 65*1024 - 1
	}
	if interval <= 0 {
		interval = intervalDef
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	sys.Pctl(sys.NEWPGRP|sys.FORKFD, nil)

	addr := dial.Netmkaddr(args[0], "icmp", "1")
	c, err := dial.Dial(addr, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ip/ping: can't dial %s: %s\n", addr, sys.Errstr())
		sys.Exits("fail:dial")
	}
	fmt.Printf("sending %d %d byte messages %d ms apart\n", nmsg, msglen, interval)

	rd, err := feed.Start(self, int(c.Dfd.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ip/ping: can't start reader: %s\n", sys.Errstr())
		sys.Exits("fail:reader")
	}
	defer rd.Close()

	firstseq = rand.IntN(65536) - nmsg // -n to ensure we don't exceed 16 bits
	if firstseq < 0 {
		firstseq = 0
	}
	buf := make([]byte, 64*1024+512)
	for i := Odata; i < msglen; i++ {
		buf[i] = byte(i)
	}
	buf[Otype] = EchoRequest
	buf[Ocode] = 0

	var requests []*req
	seq := firstseq
	sent := 0
	start := time.Now()
	var deadline time.Time
	for lostmsgs+rcvdmsgs < nmsg {
		if sent < nmsg && !time.Now().Before(start.Add(time.Duration(sent*interval)*time.Millisecond)) {
			binary.BigEndian.PutUint16(buf[Oseq:], uint16(seq))
			r := &req{seq: seq, time: time.Now()}
			requests = append(requests, r)
			if n, _ := c.Dfd.Write(buf[:msglen]); n < msglen {
				fmt.Fprintf(os.Stderr, "ping: write failed: %s\n", sys.Errstr())
				sent = nmsg
			} else {
				seq++
				sent++
			}
			if sent == nmsg {
				// must be at least one message outstanding; wait for it
				deadline = time.Now().Add(time.Duration((nmsg-lostmsgs-rcvdmsgs)*interval)*time.Millisecond + 5*time.Second)
			}
			continue
		}
		var until time.Time
		if sent < nmsg {
			until = start.Add(time.Duration(sent*interval) * time.Millisecond)
		} else {
			until = deadline
		}
		wait := time.Until(until)
		if wait <= 0 {
			if sent < nmsg {
				continue
			}
			break
		}
		b, t, err := rd.Read(int((wait + time.Millisecond - 1) / time.Millisecond))
		if err != nil {
			fmt.Printf("read: %s\n", err)
			break
		}
		if b == nil {
			continue
		}
		if len(b) < msglen {
			fmt.Printf("bad len %d/%d\n", len(b), msglen)
			continue
		}
		ic := unpack(b)
		for i := Odata; i < msglen; i++ {
			if b[i] != byte(i) {
				ic.munged++
			}
		}
		ic.time = t
		if ic.munged > 0 {
			fmt.Printf("corrupted reply\n")
		}
		if ic.ptype != EchoReply || ic.code != 0 {
			fmt.Printf("bad type/code %d/%d seq %d\n", ic.ptype, ic.code, ic.seq)
			continue
		}
		requests = clean(requests, ic)
	}
	for _, r := range requests {
		if !r.replied {
			lostmsgs++
		}
	}
	if lostmsgs > 0 {
		fmt.Printf("%d out of %d message(s) lost\n", lostmsgs, lostmsgs+rcvdmsgs)
		rd.Close()
		sys.Exits("fail:lost messages")
	}
}

func clean(l []*req, ic *icmp) []*req {
	var left []*req
	for _, r := range l {
		if ic.seq == r.seq {
			r.rtt = ic.time.Sub(r.time).Nanoseconds()
			r.ttl = ic.ttl
			reply(r, ic)
		}
		if ic.time.Sub(r.time) > time.Minute {
			r.rtt = ic.time.Sub(r.time).Nanoseconds()
			r.ttl = ic.ttl
			if !r.replied {
				lost(r, ic)
			}
		} else {
			left = append(left, r)
		}
	}
	return left
}

func reply(r *req, ic *icmp) {
	rcvdmsgs++
	r.rtt /= 1000
	sum += r.rtt
	if !quiet && !lostonly {
		if addresses {
			fmt.Printf("%d: %s->%s rtt %d µs, avg rtt %d µs, ttl = %d\n",
				uint32(r.seq-firstseq), ic.src, ic.dst, r.rtt, sum/int64(rcvdmsgs), r.ttl)
		} else {
			fmt.Printf("%d: rtt %d µs, avg rtt %d µs, ttl = %d\n",
				uint32(r.seq-firstseq), r.rtt, sum/int64(rcvdmsgs), r.ttl)
		}
	}
	r.replied = true
}

func lost(r *req, ic *icmp) {
	avg := int64(0)
	if rcvdmsgs > 0 {
		avg = sum / int64(rcvdmsgs)
	}
	if !quiet {
		if addresses {
			fmt.Printf("lost %d: %s->%s avg rtt %d µs\n", uint32(r.seq-firstseq), ic.src, ic.dst, avg)
		} else {
			fmt.Printf("lost %d: avg rtt %d µs\n", uint32(r.seq-firstseq), avg)
		}
	}
	lostmsgs++
}

func unpack(b []byte) *icmp {
	return &icmp{
		ttl:   int(b[Ottl]),
		src:   netip.AddrFrom4([4]byte(b[Osrc : Osrc+4])),
		dst:   netip.AddrFrom4([4]byte(b[Odst : Odst+4])),
		ptype: int(b[Otype]),
		code:  int(b[Ocode]),
		seq:   int(binary.BigEndian.Uint16(b[Oseq:])),
	}
}
